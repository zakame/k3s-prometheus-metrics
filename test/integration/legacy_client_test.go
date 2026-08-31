//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// writeCounter tallies Create/Update calls per object kind, letting a test
// tell which of two clients a given write actually went through -- both
// still hit the same real envtest API server, so the object alone can't
// say who wrote it.
type writeCounter struct {
	mu              sync.Mutex
	endpointsWrites int
	serviceWrites   int
}

func (w *writeCounter) record(obj client.Object) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch obj.(type) {
	case *corev1.Endpoints: //nolint:staticcheck
		w.endpointsWrites++
	case *corev1.Service:
		w.serviceWrites++
	}
}

// trackedClient wraps base, tallying its Create/Update calls in counter
// while delegating every call (including these) to base, which still
// reaches the real API server underneath.
type trackedClient struct {
	client.Client
	counter *writeCounter
}

func (t trackedClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	t.counter.record(obj)
	return t.Client.Create(ctx, obj, opts...)
}

func (t trackedClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	t.counter.record(obj)
	return t.Client.Update(ctx, obj, opts...)
}

// TestReconcile_LegacyClient_RoutesEndpointsWritesAwayFromPrimaryClient
// proves applyLegacyEndpoints's LegacyClient branch (node_controller.go)
// actually takes effect end-to-end: once LegacyClient is set, every legacy
// Endpoints write goes through it exclusively, never through the primary
// Client -- the scenario LegacyClient exists for (scoping a WarningHandler
// that suppresses the v1 Endpoints deprecation warning, see main.go)
// without that leaking onto the Service/EndpointSlice writes Reconcile
// also makes.
func TestReconcile_LegacyClient_RoutesEndpointsWritesAwayFromPrimaryClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.30.0.1", true, withExtraLabels(cpLabel))

	primaryCounter := &writeCounter{}
	legacyCounter := &writeCounter{}
	primary := trackedClient{Client: k8sClient, counter: primaryCounter}
	legacy := trackedClient{Client: k8sClient, counter: legacyCounter}

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcileWithClients(t, ctx, cfg, primary, legacy)

	if legacyCounter.endpointsWrites == 0 {
		t.Fatal("expected the legacy Endpoints write to go through LegacyClient, got 0 writes on it")
	}
	if primaryCounter.endpointsWrites != 0 {
		t.Fatalf("expected zero Endpoints writes on the primary Client once LegacyClient is set, got %d", primaryCounter.endpointsWrites)
	}

	// Sanity: LegacyClient only diverts the Endpoints write -- Services
	// still go through the primary Client, not LegacyClient.
	if primaryCounter.serviceWrites == 0 {
		t.Fatal("expected the Service write to still go through the primary Client")
	}
	if legacyCounter.serviceWrites != 0 {
		t.Fatalf("expected zero Service writes on LegacyClient, got %d", legacyCounter.serviceWrites)
	}

	// And prove the write actually landed, not just that some client's
	// Create was invoked.
	eps := getLegacyEndpoints(t, ctx, id)
	if len(eps.Subsets) != 1 || len(eps.Subsets[0].Addresses) != 1 {
		t.Fatalf("expected 1 subset with 1 ready address via LegacyClient, got %+v", eps.Subsets)
	}
}
