package controller

import (
	"context"
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
		for i := range nodeCount {
			objs = append(objs, &corev1.Node{
				Name:   fmt.Sprintf("n%d", i),
				Labels: map[string]string{"role": "control-plane"},
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

func TestReconcile_NodeListOrderChange_NoWrites(t *testing.T) {
	cfg := config.Config{
		Namespace:            "kube-system",
		NodeSelector:         map[string]string{"role": "control-plane"},
		Services:             config.DefaultServices,
		WriteLegacyEndpoints: true,
	}

	var objs []client.Object
	for i, ip := range []string{"10.0.0.9", "10.0.0.10", "2001:db8::9", "2001:db8::10", "10.0.0.1"} {
		ready := corev1.ConditionTrue
		if i%2 == 1 {
			ready = corev1.ConditionFalse
		}
		objs = append(objs, &corev1.Node{
			Name:   fmt.Sprintf("n%d", i),
			Labels: map[string]string{"role": "control-plane"},
			Status: corev1.NodeStatus{
				Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
			},
		})
	}

	reverse := false
	var writes []string
	record := func(verb string, obj client.Object) {
		writes = append(writes, fmt.Sprintf("%s %T %s", verb, obj, obj.GetName()))
	}
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if err := cl.List(ctx, list, opts...); err != nil {
					return err
				}
				if nl, ok := list.(*corev1.NodeList); ok && reverse {
					slices.Reverse(nl.Items)
				}
				return nil
			},
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				record("create", obj)
				return cl.Create(ctx, obj, opts...)
			},
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				record("update", obj)
				return cl.Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				record("patch", obj)
				return cl.Patch(ctx, obj, patch, opts...)
			},
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				record("delete", obj)
				return cl.Delete(ctx, obj, opts...)
			},
		}).
		Build()

	r := &NodeReconciler{Client: c, Config: cfg}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(writes) == 0 {
		t.Fatal("setup: expected the first reconcile to write")
	}

	writes = nil
	reverse = true
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("expected no writes when only the node List order changed, got %v", writes)
	}
}
