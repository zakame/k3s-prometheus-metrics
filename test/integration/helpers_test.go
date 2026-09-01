//go:build integration

package integration

import (
	"context"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
)

const testNamespace = "default"

var nonDNSLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// testID turns a *testing.T name into a short, unique, DNS-1123-safe token
// used to namespace Node/Service names so parallel test cases sharing one
// envtest API server never collide.
func testID(t *testing.T) string {
	t.Helper()
	s := strings.ToLower(t.Name())
	s = nonDNSLabel.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[len(s)-40:]
	}
	return strings.Trim(s, "-")
}

type nodeOpt func(*corev1.Node)

func withCordon() nodeOpt {
	return func(n *corev1.Node) { n.Spec.Unschedulable = true }
}

func withExtraLabels(labels map[string]string) nodeOpt {
	return func(n *corev1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		maps.Copy(n.Labels, labels)
	}
}

// createNode creates a Node with the given InternalIP and readiness, then
// persists Status separately since Node has a status subresource on a real
// API server (a plain Update never sticks Status changes).
func createNode(t *testing.T, ctx context.Context, name, internalIP string, ready bool, opts ...nodeOpt) *corev1.Node {
	t.Helper()

	n := &corev1.Node{Name: name}
	for _, o := range opts {
		o(n)
	}
	if err := k8sClient.Create(ctx, n); err != nil {
		t.Fatalf("creating node %s: %v", name, err)
	}

	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: internalIP}}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: readyStatus}}
	if err := k8sClient.Status().Update(ctx, n); err != nil {
		t.Fatalf("setting status on node %s: %v", name, err)
	}

	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &corev1.Node{Name: name})
	})
	return n
}

func setNodeReady(t *testing.T, ctx context.Context, name string, ready bool) {
	t.Helper()
	var n corev1.Node
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &n); err != nil {
		t.Fatalf("getting node %s: %v", name, err)
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}
	if err := k8sClient.Status().Update(ctx, &n); err != nil {
		t.Fatalf("updating readiness on node %s: %v", name, err)
	}
}

func deleteNode(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if err := k8sClient.Delete(ctx, &corev1.Node{Name: name}); err != nil {
		t.Fatalf("deleting node %s: %v", name, err)
	}
}

// reconcile invokes NodeReconciler.Reconcile directly (rather than driving
// it through a running manager's watch loop) so assertions run immediately
// after a known API mutation, with no eventual-consistency polling needed.
// This still exercises the real List/CreateOrUpdate calls against a live
// API server -- only the watch-triggering plumbing is bypassed.
func reconcile(t *testing.T, ctx context.Context, cfg config.Config) {
	t.Helper()
	reconcileWithClients(t, ctx, cfg, k8sClient, nil)
}

func reconcileWithLegacyClient(t *testing.T, ctx context.Context, cfg config.Config, legacy client.Client) {
	t.Helper()
	reconcileWithClients(t, ctx, cfg, k8sClient, legacy)
}

// reconcileWithClients is reconcile/reconcileWithLegacyClient's common,
// most general form -- letting a test substitute the primary Client too
// (e.g. to spy on its writes with interceptor.NewClient), which the other
// two intentionally don't expose since they always drive the shared
// package-level k8sClient.
func reconcileWithClients(t *testing.T, ctx context.Context, cfg config.Config, primary, legacy client.Client) {
	t.Helper()
	r := &controller.NodeReconciler{Client: primary, Config: cfg, LegacyClient: legacy}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func getEndpointSliceIn(t *testing.T, ctx context.Context, namespace, name string) *discoveryv1.EndpointSlice {
	t.Helper()
	var es discoveryv1.EndpointSlice
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &es); err != nil {
		t.Fatalf("getting EndpointSlice %s/%s: %v", namespace, name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &discoveryv1.EndpointSlice{
			Name: name, Namespace: namespace,
		})
	})
	return &es
}

func getEndpointSlice(t *testing.T, ctx context.Context, name string) *discoveryv1.EndpointSlice {
	t.Helper()
	return getEndpointSliceIn(t, ctx, testNamespace, name)
}

func getEndpointSliceErrIn(ctx context.Context, namespace, name string) error {
	var es discoveryv1.EndpointSlice
	return k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &es)
}

func getEndpointSliceErr(ctx context.Context, name string) error {
	return getEndpointSliceErrIn(ctx, testNamespace, name)
}

func getServiceIn(t *testing.T, ctx context.Context, namespace, name string) *corev1.Service {
	t.Helper()
	var svc corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &svc); err != nil {
		t.Fatalf("getting Service %s/%s: %v", namespace, name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &corev1.Service{
			Name: name, Namespace: namespace,
		})
	})
	return &svc
}

func getService(t *testing.T, ctx context.Context, name string) *corev1.Service {
	t.Helper()
	return getServiceIn(t, ctx, testNamespace, name)
}

func getServiceErrIn(ctx context.Context, namespace, name string) error {
	var svc corev1.Service
	return k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &svc)
}

func getLegacyEndpointsIn(t *testing.T, ctx context.Context, namespace, name string) *corev1.Endpoints { //nolint:staticcheck
	t.Helper()
	var eps corev1.Endpoints //nolint:staticcheck
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &eps); err != nil {
		t.Fatalf("getting Endpoints %s/%s: %v", namespace, name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &corev1.Endpoints{ //nolint:staticcheck
			Name: name, Namespace: namespace,
		})
	})
	return &eps
}

func getLegacyEndpoints(t *testing.T, ctx context.Context, name string) *corev1.Endpoints { //nolint:staticcheck
	t.Helper()
	return getLegacyEndpointsIn(t, ctx, testNamespace, name)
}

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }

const reconcileTimeout = 10 * time.Second
