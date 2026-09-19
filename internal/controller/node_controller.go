// Package controller contains the Node reconciler that drives Service,
// EndpointSlice, and (optionally) legacy Endpoints objects to reflect
// each watched service's own qualifying node set.
package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//
// The reconciler also needs create;get;list;patch;update;watch on
// "" /services, "" /endpoints (when --write-legacy-endpoints is set), and
// discovery.k8s.io/endpointslices, all namespace-scoped to Config.Namespace
// rather than cluster-wide like the nodes rule above. Not
// +kubebuilder:rbac markers: controller-gen only emits a single
// cluster-wide ClusterRole, which can't express that scoping.

// NodeReconciler watches cluster Nodes and drives Service, EndpointSlice,
// and (optionally) legacy Endpoints objects in Config.Namespace to reflect
// current control-plane node state.
type NodeReconciler struct {
	client.Client
	Config config.Config

	// LegacyClient, if set, writes legacy v1 Endpoints instead of Client --
	// lets callers scope a WarningHandler suppressing the v1 Endpoints
	// deprecation warning on Kubernetes 1.33+. If nil, Client is used.
	LegacyClient client.Client
}

// Reconcile implements reconcile.Reconciler, ignoring the incoming
// request's identity and always recomputing desired state.
func (r *NodeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nodesByService, err := ListNodesByService(ctx, r.Client, r.Config)
	if err != nil {
		return ctrl.Result{}, err
	}
	for name, nodes := range nodesByService {
		logger.V(1).Info("discovered nodes", "service", name, "names", nodeNames(nodes))
	}

	// A Service that can't be applied (e.g. a pre-existing one with an
	// allocated ClusterIP, which is immutable) is skipped along with its
	// endpoints so the others still converge. Its error is still returned,
	// so the reconcile is requeued and counted as failed. Never join a
	// reconcile.TerminalError here: errors.Is finds it through the join
	// and would suppress the requeue for every service.
	svcs, svcErr := r.applyServices(ctx)

	slices := ownedBy(endpoints.BuildEndpointSlices(nodesByService, r.Config), svcs)
	if err := r.ownEndpointSlices(slices, svcs); err != nil {
		return ctrl.Result{}, errors.Join(svcErr, err)
	}
	sliceErr := r.applyEndpointSlices(ctx, slices)

	epsCount := 0
	var epsErr error
	if r.Config.WriteLegacyEndpoints {
		eps := ownedBy(endpoints.BuildEndpoints(nodesByService, r.Config), svcs) //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
		if err := r.ownEndpoints(eps, svcs); err != nil {
			return ctrl.Result{}, errors.Join(svcErr, sliceErr, err)
		}
		epsErr = r.applyLegacyEndpoints(ctx, eps)
		epsCount = len(eps)
	}

	if err := errors.Join(svcErr, sliceErr, epsErr); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(1).Info("reconciled endpoints", "endpointSlices", len(slices), "legacyEndpoints", epsCount)
	return ctrl.Result{}, nil
}

// ListNodesByService lists nodes once per distinct node selector, then
// returns each service's matching nodes by name. Exported so the
// "manifests" one-shot subcommand can reuse the exact same
// selector-matching logic as the live reconciler.
func ListNodesByService(ctx context.Context, c client.Client, cfg config.Config) (map[string][]corev1.Node, error) {
	byService := make(map[string][]corev1.Node, len(cfg.Services))
	bySelector := map[string][]corev1.Node{}
	for _, svc := range cfg.Services {
		sel := svc.NodeSelector
		if sel == nil {
			sel = cfg.NodeSelector
		}

		key := labels.Set(sel).String()
		nodes, ok := bySelector[key]
		if !ok {
			var nodeList corev1.NodeList
			if err := c.List(ctx, &nodeList, client.MatchingLabels(sel)); err != nil {
				return nil, fmt.Errorf("listing nodes for %s: %w", svc.Name, err)
			}
			nodes = nodeList.Items
			bySelector[key] = nodes
		}
		byService[svc.Name] = nodes
	}
	return byService, nil
}

// applyAll creates/updates each of want via CreateOrUpdate, applying mutate
// to copy the type-specific fields callers care about. A failed object
// doesn't stop the rest: the result holds only the successfully applied
// objects (with server-populated fields such as UID/ResourceVersion), in
// want order, and the error joins one wrapped error per failure. kind
// names the object kind in those errors, since typed clients don't
// populate GroupVersionKind.
func applyAll[T any, PT interface {
	*T
	client.Object
}](ctx context.Context, c client.Client, want []T, kind string, mutate func(got, desired PT)) ([]T, error) {
	applied := make([]T, 0, len(want))
	var errs []error
	for i := range want {
		desired := PT(&want[i])
		got := new(T)
		gotPT := PT(got)
		gotPT.SetName(desired.GetName())
		gotPT.SetNamespace(desired.GetNamespace())

		if _, err := controllerutil.CreateOrUpdate(ctx, c, gotPT, func() error {
			mutate(gotPT, desired)
			return nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("applying %s %s/%s: %w", kind, desired.GetNamespace(), desired.GetName(), err))
			continue
		}
		applied = append(applied, *got)
	}
	return applied, errors.Join(errs...)
}

// ownedBy drops objects whose service-name label has no applied Service,
// so endpoints for a Service this controller couldn't apply are never
// written against whatever object holds that name.
func ownedBy[T any, PT interface {
	*T
	client.Object
}](objs []T, svcs map[string]corev1.Service) []T {
	kept := objs[:0]
	for i := range objs {
		if _, ok := svcs[PT(&objs[i]).GetLabels()[discoveryv1.LabelServiceName]]; ok {
			kept = append(kept, objs[i])
		}
	}
	return kept
}

// ownAll sets a controller OwnerReference from each of objs to its matching
// Service (looked up by the discovery.k8s.io/v1 LabelServiceName label), so
// deleting the Service garbage-collects objs. An object with no matching
// Service is left unowned. kind names the object kind in a wrapped error.
func ownAll[T any, PT interface {
	*T
	client.Object
}](objs []T, svcs map[string]corev1.Service, scheme *runtime.Scheme, kind string) error {
	for i := range objs {
		obj := PT(&objs[i])
		svc, ok := svcs[obj.GetLabels()[discoveryv1.LabelServiceName]]
		if !ok {
			continue
		}
		if err := controllerutil.SetControllerReference(&svc, obj, scheme); err != nil {
			return fmt.Errorf("owning %s %s: %w", kind, obj.GetName(), err)
		}
	}
	return nil
}

// applyServices creates/updates the selector-less Service per
// config.Service, returning the successfully applied ones by name so
// callers can own EndpointSlice/Endpoints against them, plus the joined
// error for any that failed.
func (r *NodeReconciler) applyServices(ctx context.Context) (map[string]corev1.Service, error) {
	want := endpoints.BuildServices(r.Config)
	applied, err := applyAll(ctx, r.Client, want, "service", func(got, desired *corev1.Service) {
		got.Labels = desired.Labels
		got.Spec.Ports = desired.Spec.Ports
		got.Spec.Selector = desired.Spec.Selector
		got.Spec.ClusterIP = corev1.ClusterIPNone
	})
	svcs := make(map[string]corev1.Service, len(applied))
	for _, svc := range applied {
		svcs[svc.Name] = svc
	}
	return svcs, err
}

// ownEndpointSlices sets a controller OwnerReference from each slice to its
// matching Service, so deleting the Service garbage-collects its slices.
func (r *NodeReconciler) ownEndpointSlices(slices []discoveryv1.EndpointSlice, svcs map[string]corev1.Service) error {
	return ownAll(slices, svcs, r.Scheme(), "endpointslice")
}

// ownEndpoints is ownEndpointSlices for the legacy Endpoints path.
func (r *NodeReconciler) ownEndpoints(eps []corev1.Endpoints, svcs map[string]corev1.Service) error { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	return ownAll(eps, svcs, r.Scheme(), "endpoints")
}

func (r *NodeReconciler) applyEndpointSlices(ctx context.Context, want []discoveryv1.EndpointSlice) error {
	_, err := applyAll(ctx, r.Client, want, "endpointslice", func(got, desired *discoveryv1.EndpointSlice) {
		got.Labels = desired.Labels
		got.AddressType = desired.AddressType
		got.Endpoints = desired.Endpoints
		got.Ports = desired.Ports
		got.OwnerReferences = desired.OwnerReferences
	})
	return err
}

func (r *NodeReconciler) applyLegacyEndpoints(ctx context.Context, want []corev1.Endpoints) error { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	c := r.Client
	if r.LegacyClient != nil {
		c = r.LegacyClient
	}

	_, err := applyAll(ctx, c, want, "endpoints", func(got, desired *corev1.Endpoints) { //nolint:staticcheck
		got.Labels = desired.Labels
		got.Subsets = desired.Subsets
		got.OwnerReferences = desired.OwnerReferences
	})
	return err
}

// SetupWithManager wires the reconciler into mgr, watching Node objects.
// Update events are filtered by nodeChangedPredicate to avoid a full
// reconcile on every heartbeat.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(nodeChangedPredicate)).
		Named("node").
		Complete(r)
}

// nodeChangedPredicate lets Create/Delete through unfiltered, but only
// lets an Update through when schedulability, Ready, or labels changed --
// labels matter too since Reconcile lists nodes by NodeSelector, so
// relabeling a node's control-plane role must still trigger a reconcile.
var nodeChangedPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldNode, okOld := e.ObjectOld.(*corev1.Node)
		newNode, okNew := e.ObjectNew.(*corev1.Node)
		if !okOld || !okNew {
			return true
		}
		return oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable ||
			nodeReadyStatus(oldNode) != nodeReadyStatus(newNode) ||
			!reflect.DeepEqual(oldNode.Labels, newNode.Labels)
	},
}

func nodeReadyStatus(node *corev1.Node) corev1.ConditionStatus {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status
		}
	}
	return corev1.ConditionUnknown
}

func nodeNames(nodes []corev1.Node) []string {
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	return names
}
