//go:build integration

package integration

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The manager-driven tests here prove what a direct Reconcile call can't:
// that the Service and EndpointSlice watches, the InternalIP predicate,
// and the constant-key event mapping actually wake the reconciler for the
// events they are meant to, without any Node event.

// managerCtx is a per-test context the manager and its polls share.
func managerCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func TestManager_ManagedEndpointSliceDeleted_RecreatedWithoutNodeEvent(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "n1-"+id, "10.60.0.1", true, withExtraLabels(cpLabel))
	original := waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})
	svc := getService(t, ctx, id)

	var nodeBefore corev1.Node
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "n1-" + id}, &nodeBefore); err != nil {
		t.Fatalf("getting node: %v", err)
	}

	if err := k8sClient.Delete(ctx, original); err != nil {
		t.Fatalf("deleting managed slice: %v", err)
	}

	es := waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return es.UID != original.UID && len(es.Endpoints) == 1
	})
	if ref := ownerRefTo(t, es.OwnerReferences, svc); ref.UID != svc.UID {
		t.Errorf("expected the recreated slice owned by the Service (UID %s), got %+v", svc.UID, es.OwnerReferences)
	}

	// Nothing touched the Node: the EndpointSlice watch alone drove this.
	var nodeAfter corev1.Node
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "n1-" + id}, &nodeAfter); err != nil {
		t.Fatalf("getting node: %v", err)
	}
	if nodeAfter.ResourceVersion != nodeBefore.ResourceVersion {
		t.Errorf("expected the Node unchanged during slice recovery: rv %s -> %s", nodeBefore.ResourceVersion, nodeAfter.ResourceVersion)
	}
}

func TestManager_ManagedEndpointSliceEdited_RepairedWithoutNodeEvent(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "n1-"+id, "10.60.1.1", true, withExtraLabels(cpLabel))
	es := waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), es) })

	// A hand edit pointing the slice at the wrong address.
	tampered := es.DeepCopy()
	tampered.Endpoints[0].Addresses = []string{"10.60.1.99"}
	if err := k8sClient.Update(ctx, tampered); err != nil {
		t.Fatalf("tampering with managed slice: %v", err)
	}

	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1 && es.Endpoints[0].Addresses[0] == "10.60.1.1"
	})
}

func TestManager_ManagedServiceDeleted_RecreatedAndSliceReowned(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "n1-"+id, "10.60.2.1", true, withExtraLabels(cpLabel))
	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})
	original := getService(t, ctx, id)

	// envtest runs no garbage collector, so the slice outlives its owner
	// with a dangling ownerRef -- the reconciler must re-point it.
	if err := k8sClient.Delete(ctx, original); err != nil {
		t.Fatalf("deleting managed Service: %v", err)
	}

	var recreated corev1.Service
	waitFor(t, ctx, "Service "+id+" recreated", func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: id}, &recreated)
		if isNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return recreated.UID != original.UID, nil
	})
	if recreated.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("expected the recreated Service headless, got %q", recreated.Spec.ClusterIP)
	}

	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		for _, ref := range es.OwnerReferences {
			if ref.UID == recreated.UID {
				return true
			}
		}
		return false
	})
}

func TestManager_NodeInternalIPChanged_SliceUpdated(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "n1-"+id, "10.60.3.1", true, withExtraLabels(cpLabel))
	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1 && es.Endpoints[0].Addresses[0] == "10.60.3.1"
	})

	// Readiness, labels, schedulability all unchanged: only the address.
	setNodeInternalIP(t, ctx, "n1-"+id, "10.60.3.2")

	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1 && es.Endpoints[0].Addresses[0] == "10.60.3.2"
	})
}

func TestManager_LastNodeDeleted_SliceEmptied(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "n1-"+id, "10.60.4.1", true, withExtraLabels(cpLabel))
	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})

	deleteNode(t, ctx, "n1-"+id)

	waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 0
	})
}

func TestManager_LastIPv6NodeDeleted_IPv6SlicePruned(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "v4-"+id, "10.60.5.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "v6-"+id, "2001:db8::60:5:1", true, withExtraLabels(cpLabel))
	waitForSlice(t, ctx, testNamespace, id+"-metrics-ipv6", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})

	deleteNode(t, ctx, "v6-"+id)

	waitFor(t, ctx, "-ipv6 slice pruned", func() (bool, error) {
		err := getEndpointSliceErr(ctx, id+"-metrics-ipv6")
		if isNotFound(err) {
			return true, nil
		}
		return false, err
	})
	v4 := getEndpointSlice(t, ctx, id+"-metrics")
	if len(v4.Endpoints) != 1 || v4.Endpoints[0].Addresses[0] != "10.60.5.1" {
		t.Fatalf("expected the IPv4 slice intact, got %+v", v4.Endpoints)
	}
}

func TestManager_StartupWithSeveralNodes_Converges(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}

	// Nodes exist before the manager starts: the initial informer sync
	// must produce one converged slice, not one per node event.
	createNode(t, ctx, "n1-"+id, "10.60.6.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "n2-"+id, "10.60.6.2", false, withExtraLabels(cpLabel))
	createNode(t, ctx, "n3-"+id, "10.60.6.3", true, withExtraLabels(cpLabel), withCordon())

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	startManager(t, ctx, adminConfig, cfg)

	es := waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 3
	})
	ready := 0
	for _, ep := range es.Endpoints {
		if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
			ready++
		}
	}
	if ready != 1 {
		t.Errorf("expected exactly 1 ready endpoint (n2 NotReady, n3 cordoned), got %d: %+v", ready, es.Endpoints)
	}

	var eps corev1.Endpoints //nolint:staticcheck
	waitFor(t, ctx, "legacy Endpoints converged", func() (bool, error) {
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: id}, &eps)
		if isNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return len(eps.Subsets) == 1 && len(eps.Subsets[0].Addresses) == 1 && len(eps.Subsets[0].NotReadyAddresses) == 2, nil
	})
}

func TestManager_ForeignEndpointSliceChanges_DoNotDisturbManagedSlice(t *testing.T) {
	ctx := managerCtx(t)
	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	cfg := svcConfig(id, cpLabel)
	startManager(t, ctx, adminConfig, cfg)

	createNode(t, ctx, "n1-"+id, "10.60.7.1", true, withExtraLabels(cpLabel))
	es := waitForSlice(t, ctx, testNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), es) })

	foreign := &discoveryv1.EndpointSlice{
		Name: id + "-foreign", Namespace: testNamespace,
		Labels: map[string]string{
			discoveryv1.LabelServiceName: id,
			discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.60.7.50"}}},
	}
	if err := k8sClient.Create(ctx, foreign); err != nil {
		t.Fatalf("creating foreign slice: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), foreign) })
	foreign.Endpoints[0].Addresses = []string{"10.60.7.51"}
	if err := k8sClient.Update(ctx, foreign); err != nil {
		t.Fatalf("updating foreign slice: %v", err)
	}
	if err := k8sClient.Delete(ctx, foreign); err != nil {
		t.Fatalf("deleting foreign slice: %v", err)
	}

	// Whatever those events did or didn't trigger, the managed slice is
	// unchanged and the foreign one was never adopted or recreated.
	waitFor(t, ctx, "foreign slice stays gone", func() (bool, error) {
		return isNotFound(getEndpointSliceErr(ctx, id+"-foreign")), nil
	})
	after := getEndpointSlice(t, ctx, id+"-metrics")
	if after.ResourceVersion != es.ResourceVersion {
		t.Errorf("expected the managed slice untouched by foreign slice events: rv %s -> %s", es.ResourceVersion, after.ResourceVersion)
	}
}
