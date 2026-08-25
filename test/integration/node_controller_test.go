//go:build integration

package integration

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

func svcConfig(id string, selector map[string]string) config.Config {
	return config.Config{
		Namespace:    testNamespace,
		NodeSelector: selector,
		Services: []config.Service{
			{Name: id, PortName: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		},
	}
}

func TestReconcile_CreatesEndpointSliceFromNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.1.0.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "n2-"+id, "10.1.0.2", false, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if len(es.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d: %+v", len(es.Endpoints), es.Endpoints)
	}
	if es.Labels[discoveryv1.LabelManagedBy] != endpoints.ManagedByValue {
		t.Errorf("expected managed-by label %q, got %q", endpoints.ManagedByValue, es.Labels[discoveryv1.LabelManagedBy])
	}
}

func TestReconcile_UpdatesEndpointSliceWhenNodeReadinessChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.2.0.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if !*es.Endpoints[0].Conditions.Ready {
		t.Fatal("expected node to start out Ready")
	}
	rvBefore := es.ResourceVersion

	setNodeReady(t, ctx, "n1-"+id, false)
	reconcile(t, ctx, cfg)

	es = getEndpointSlice(t, ctx, id+"-metrics")
	if *es.Endpoints[0].Conditions.Ready {
		t.Fatal("expected EndpointSlice to reflect the node going NotReady")
	}
	if es.ResourceVersion == rvBefore {
		t.Fatal("expected a real update (new ResourceVersion) once the underlying node state changed")
	}
}

func TestReconcile_RemovesDeletedNodeFromEndpointSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.3.0.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "n2-"+id, "10.3.0.2", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	if es := getEndpointSlice(t, ctx, id+"-metrics"); len(es.Endpoints) != 2 {
		t.Fatalf("setup: expected 2 endpoints before delete, got %d", len(es.Endpoints))
	}

	deleteNode(t, ctx, "n2-"+id)
	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if len(es.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint after node deletion, got %d: %+v", len(es.Endpoints), es.Endpoints)
	}
	if *es.Endpoints[0].NodeName != "n1-"+id {
		t.Fatalf("expected surviving endpoint to be n1-%s, got %s", id, *es.Endpoints[0].NodeName)
	}
}

func TestReconcile_NodeSelectorExcludesNonMatchingNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "cp-"+id, "10.4.0.1", true, withExtraLabels(cpLabel))
	// Worker node: matches nothing in cfg.NodeSelector, must never appear.
	createNode(t, ctx, "worker-"+id, "10.4.0.99", true)

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if len(es.Endpoints) != 1 {
		t.Fatalf("expected only the labeled control-plane node, got %d endpoints: %+v", len(es.Endpoints), es.Endpoints)
	}
	if *es.Endpoints[0].NodeName != "cp-"+id {
		t.Fatalf("expected cp-%s, got %s", id, *es.Endpoints[0].NodeName)
	}
}

func TestReconcile_NoMatchingNodes_NoEndpointSliceCreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	// A node exists, but none carry the selector label.
	createNode(t, ctx, "worker-"+id, "10.5.0.1", true)

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	if err := getEndpointSliceErr(ctx, id+"-metrics"); !isNotFound(err) {
		t.Fatalf("expected no EndpointSlice to be created, got err=%v", err)
	}
}

func TestReconcile_WriteLegacyEndpointsEnabled_CreatesEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.6.0.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	eps := getLegacyEndpoints(t, ctx, id)
	if len(eps.Subsets) != 1 || len(eps.Subsets[0].Addresses) != 1 {
		t.Fatalf("expected 1 subset with 1 ready address, got %+v", eps.Subsets)
	}
	// The EndpointSlice path must also have run: legacy is additive, not a
	// substitute.
	_ = getEndpointSlice(t, ctx, id+"-metrics")
}

func TestReconcile_WriteLegacyEndpointsDisabled_NoEndpointsCreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.7.0.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel) // WriteLegacyEndpoints defaults to false
	reconcile(t, ctx, cfg)

	var eps corev1.Endpoints //nolint:staticcheck
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: id}, &eps)
	if !isNotFound(err) {
		t.Fatalf("expected no legacy Endpoints object when disabled, got err=%v", err)
	}
}

func TestReconcile_Idempotent_NoAPIWriteOnUnchangedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.8.0.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	rv1 := getEndpointSlice(t, ctx, id+"-metrics").ResourceVersion

	reconcile(t, ctx, cfg) // identical node state
	rv2 := getEndpointSlice(t, ctx, id+"-metrics").ResourceVersion

	if rv1 != rv2 {
		t.Fatalf("expected no API write (stable ResourceVersion) on unchanged state: %s -> %s", rv1, rv2)
	}
}

// TestReconcile_DoesNotAdoptForeignEndpointSlice guards against fighting
// Kubernetes' own EndpointSlice mirroring controller (which manages objects
// labeled endpointslice-controller.k8s.io, typically named after the
// Service they mirror). Our controller must create its own
// distinctly-named, distinctly-labeled object rather than touching one it
// doesn't own.
func TestReconcile_DoesNotAdoptForeignEndpointSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.9.0.1", true, withExtraLabels(cpLabel))

	foreign := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id, // same base name our Service uses, no "-metrics" suffix
			Namespace: testNamespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: id,
				discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
	}
	if err := k8sClient.Create(ctx, foreign); err != nil {
		t.Fatalf("creating foreign EndpointSlice: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), foreign)
	})
	rvBefore := foreign.ResourceVersion

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	// Our object exists, separately.
	_ = getEndpointSlice(t, ctx, id+"-metrics")

	// The foreign object must be untouched.
	var refetched discoveryv1.EndpointSlice
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: id}, &refetched); err != nil {
		t.Fatalf("getting foreign EndpointSlice: %v", err)
	}
	if refetched.ResourceVersion != rvBefore {
		t.Fatalf("foreign EndpointSlice was modified by our reconciler: rv %s -> %s", rvBefore, refetched.ResourceVersion)
	}
	if refetched.Labels[discoveryv1.LabelManagedBy] != "endpointslice-controller.k8s.io" {
		t.Fatalf("foreign EndpointSlice's managed-by label was overwritten: %q", refetched.Labels[discoveryv1.LabelManagedBy])
	}
}
