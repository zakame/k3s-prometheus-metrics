// Package controller contains the Node reconciler that drives
// EndpointSlice (and, optionally, legacy Endpoints) objects to reflect the
// current set of control-plane nodes.
package controller

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
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
// discovery.k8s.io/endpointslices and (when --write-legacy-endpoints is
// set) on "" /endpoints -- both namespace-scoped to Config.Namespace, not
// cluster-wide like the nodes rule above. These are intentionally NOT
// +kubebuilder:rbac markers: controller-gen's RBAC generator always emits
// a single ClusterRole, which would grant those namespaced verbs
// cluster-wide (every namespace) rather than just Config.Namespace. See
// deploy/standard/role-endpoints.yaml (hand-maintained, matching this
// comment) for the actual least-privilege Role.

// NodeReconciler watches cluster Nodes and drives EndpointSlice (and,
// optionally, legacy Endpoints) objects in Config.Namespace to reflect
// current control-plane node state.
type NodeReconciler struct {
	client.Client
	Config config.Config

	// LegacyClient, if set, is used to write legacy v1 Endpoints objects
	// instead of Client. It exists so callers can scope a WarningHandler
	// that suppresses the v1 Endpoints deprecation Warning header
	// Kubernetes 1.33+ API servers attach to every Endpoints read/write --
	// see cmd/k3s-prometheus-metrics. If nil, Client is used.
	LegacyClient client.Client
}

// Reconcile implements reconcile.Reconciler. It ignores the incoming
// request's identity and always recomputes desired state from the full set
// of matching nodes, since every service's EndpointSlice/Endpoints must
// reflect the same node set.
func (r *NodeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabels(r.Config.NodeSelector)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing nodes: %w", err)
	}

	slices := endpoints.BuildEndpointSlices(nodeList.Items, r.Config)
	if err := r.applyEndpointSlices(ctx, slices); err != nil {
		return ctrl.Result{}, err
	}

	slicesCount := len(slices)
	epsCount := 0
	if r.Config.WriteLegacyEndpoints {
		eps := endpoints.BuildEndpoints(nodeList.Items, r.Config) //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
		if err := r.applyLegacyEndpoints(ctx, eps); err != nil {
			return ctrl.Result{}, err
		}
		epsCount = len(eps)
	}

	logger.V(1).Info("reconciled control-plane endpoints",
		"nodes", len(nodeList.Items), "endpointSlices", slicesCount, "legacyEndpoints", epsCount)
	return ctrl.Result{}, nil
}

func (r *NodeReconciler) applyEndpointSlices(ctx context.Context, want []discoveryv1.EndpointSlice) error {
	for i := range want {
		desired := &want[i]
		got := &discoveryv1.EndpointSlice{}
		got.Name = desired.Name
		got.Namespace = desired.Namespace

		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, got, func() error {
			got.Labels = desired.Labels
			got.AddressType = desired.AddressType
			got.Endpoints = desired.Endpoints
			got.Ports = desired.Ports
			return nil
		}); err != nil {
			return fmt.Errorf("applying endpointslice %s/%s: %w", desired.Namespace, desired.Name, err)
		}
	}
	return nil
}

func (r *NodeReconciler) applyLegacyEndpoints(ctx context.Context, want []corev1.Endpoints) error { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	c := r.Client
	if r.LegacyClient != nil {
		c = r.LegacyClient
	}

	for i := range want {
		desired := &want[i]
		got := &corev1.Endpoints{} //nolint:staticcheck
		got.Name = desired.Name
		got.Namespace = desired.Namespace

		if _, err := controllerutil.CreateOrUpdate(ctx, c, got, func() error {
			got.Labels = desired.Labels
			got.Subsets = desired.Subsets
			return nil
		}); err != nil {
			return fmt.Errorf("applying endpoints %s/%s: %w", desired.Namespace, desired.Name, err)
		}
	}
	return nil
}

// SetupWithManager wires the reconciler into mgr, watching Node objects.
// Update events are filtered to only the field changes that affect
// endpoint output (see nodeChangedPredicate) -- without this, every node's
// periodic heartbeat status update (every 10-40s per node by default)
// would trigger a full reconcile, regardless of whether it changes
// anything this controller cares about.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(nodeChangedPredicate)).
		Named("node").
		Complete(r)
}

// nodeChangedPredicate lets Create and Delete events through unfiltered,
// but only lets an Update event through when the node's schedulability,
// Ready condition, or labels changed -- the inputs to isReady() in
// internal/endpoints, plus labels because Reconcile lists nodes via
// client.MatchingLabels(Config.NodeSelector): relabeling a node to add or
// remove the control-plane role must trigger a reconcile even though
// neither Unschedulable nor Ready changed. Any other field change (e.g.
// heartbeat timestamps, resource capacity) is ignored.
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
