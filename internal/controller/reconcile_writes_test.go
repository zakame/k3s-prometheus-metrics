package controller

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// TestReconcile_APIWriteCount_ScalesWithServicesNotNodes guards against a
// per-node write regression (e.g. patching individual endpoints) that would
// still pass every correctness test here while making Reconcile O(nodes)
// against the API server.
func TestReconcile_APIWriteCount_ScalesWithServicesNotNodes(t *testing.T) {
	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: map[string]string{"role": "control-plane"},
		Services:     config.DefaultServices,
	}

	reconcileAndCountWrites := func(t *testing.T, nodeCount int) int {
		t.Helper()

		objs := make([]client.Object, 0, nodeCount)
		for i := 0; i < nodeCount; i++ {
			objs = append(objs, &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   fmt.Sprintf("n%d", i),
					Labels: map[string]string{"role": "control-plane"},
				},
				Status: corev1.NodeStatus{
					Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)}},
					Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				},
			})
		}

		var writes int
		c := fake.NewClientBuilder().
			WithScheme(testScheme(t)).
			WithObjects(objs...).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					writes++
					return cl.Create(ctx, obj, opts...)
				},
				Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					writes++
					return cl.Update(ctx, obj, opts...)
				},
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					writes++
					return cl.Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()

		r := &NodeReconciler{Client: c, Config: cfg}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		return writes
	}

	small := reconcileAndCountWrites(t, 3)
	large := reconcileAndCountWrites(t, 300)

	if small != large {
		t.Fatalf("expected identical write-call count independent of node count, got 3 nodes=%d writes, 300 nodes=%d writes", small, large)
	}

	// 1 Service create + 1 EndpointSlice create per service (IPv4-only nodes).
	wantWrites := len(config.DefaultServices) * 2
	if small != wantWrites {
		t.Fatalf("expected %d writes (1 Service + 1 EndpointSlice create per service), got %d", wantWrites, small)
	}
}
