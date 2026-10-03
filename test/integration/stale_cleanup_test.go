//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// Stale state must not outlive the node change that made it stale: a
// slice is emptied (never deleted) when its last node leaves, the -ipv6
// slice is pruned when the last IPv6 node leaves, and a changed InternalIP
// lands on the next reconcile. All against the real API server, which
// also validates the empty EndpointSlice and subset-less Endpoints shapes.

func TestReconcile_LastMatchingNodeDeleted_SliceEmptiedNotStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.0.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)
	before := getEndpointSlice(t, ctx, id+"-metrics")
	if len(before.Endpoints) != 1 {
		t.Fatalf("setup: expected 1 endpoint, got %+v", before.Endpoints)
	}

	deleteNode(t, ctx, "n1-"+id)
	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if len(es.Endpoints) != 0 {
		t.Fatalf("expected the slice emptied after its last node left, still advertising %+v", es.Endpoints)
	}
	if es.UID != before.UID {
		t.Errorf("expected the slice updated in place (same UID), got %s -> %s", before.UID, es.UID)
	}
	if es.ResourceVersion == before.ResourceVersion {
		t.Error("expected a real write to the slice")
	}

	eps := getLegacyEndpoints(t, ctx, id)
	if len(eps.Subsets) != 0 {
		t.Errorf("expected legacy Endpoints subsets cleared, got %+v", eps.Subsets)
	}

	// The API server hands back nil for both `endpoints: []` and empty
	// subsets; the empty desired state must still compare equal to that,
	// or every resync would rewrite (and, via the watch, re-trigger).
	counter := &allWritesCounter{}
	reconcileWithClients(t, ctx, cfg, countingClient{Client: k8sClient, counter: counter}, nil)
	if n := counter.total(); n != 0 {
		t.Fatalf("expected zero writes reconciling an already-empty slice and Endpoints, got %d: %v", n, counter.writes)
	}
}

func TestReconcile_LastIPv6NodeRemoved_IPv6SlicePrunedIPv4Intact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "v4-"+id, "10.50.1.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "v6-"+id, "2001:db8::50:1:1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	_ = getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6") // registers cleanup should prune fail
	v4Before := getEndpointSlice(t, ctx, id+"-metrics")

	deleteNode(t, ctx, "v6-"+id)
	reconcile(t, ctx, cfg)

	if err := getEndpointSliceErr(ctx, id+"-metrics-ipv6"); !isNotFound(err) {
		t.Fatalf("expected the -ipv6 slice pruned once the last IPv6 node left, got err=%v", err)
	}
	v4 := getEndpointSlice(t, ctx, id+"-metrics")
	if len(v4.Endpoints) != 1 || v4.Endpoints[0].Addresses[0] != "10.50.1.1" {
		t.Fatalf("expected the IPv4 slice intact, got %+v", v4.Endpoints)
	}
	if v4.ResourceVersion != v4Before.ResourceVersion {
		t.Errorf("expected no write to the unchanged IPv4 slice: rv %s -> %s", v4Before.ResourceVersion, v4.ResourceVersion)
	}
}

func TestReconcile_IPv6NodeReturns_IPv6SliceRecreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "v4-"+id, "10.50.2.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "v6-"+id, "2001:db8::50:2:1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	deleteNode(t, ctx, "v6-"+id)
	reconcile(t, ctx, cfg)
	if err := getEndpointSliceErr(ctx, id+"-metrics-ipv6"); !isNotFound(err) {
		t.Fatalf("setup: expected the -ipv6 slice pruned, got err=%v", err)
	}

	createNode(t, ctx, "v6b-"+id, "2001:db8::50:2:2", true, withExtraLabels(cpLabel))
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	v6 := getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6")
	if len(v6.Endpoints) != 1 || v6.Endpoints[0].Addresses[0] != "2001:db8::50:2:2" {
		t.Fatalf("expected the -ipv6 slice recreated for the new node, got %+v", v6.Endpoints)
	}
	if ref := ownerRefTo(t, v6.OwnerReferences, svc); ref.UID != svc.UID {
		t.Errorf("expected the recreated -ipv6 slice owned by the Service, got %+v", v6.OwnerReferences)
	}
}

func TestReconcile_NodeInternalIPChanged_SliceCarriesNewIP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.3.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	setNodeInternalIP(t, ctx, "n1-"+id, "10.50.3.2")
	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if len(es.Endpoints) != 1 || es.Endpoints[0].Addresses[0] != "10.50.3.2" {
		t.Fatalf("expected the slice to carry the new InternalIP, got %+v", es.Endpoints)
	}
	eps := getLegacyEndpoints(t, ctx, id)
	if len(eps.Subsets) != 1 || len(eps.Subsets[0].Addresses) != 1 || eps.Subsets[0].Addresses[0].IP != "10.50.3.2" {
		t.Fatalf("expected legacy Endpoints to carry the new InternalIP, got %+v", eps.Subsets)
	}
}

func TestReconcile_NodeMovesIPv4ToIPv6_SlicesSwapFamilies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.4.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	setNodeInternalIP(t, ctx, "n1-"+id, "2001:db8::50:4:1")
	reconcile(t, ctx, cfg)

	v4 := getEndpointSlice(t, ctx, id+"-metrics")
	if len(v4.Endpoints) != 0 {
		t.Errorf("expected the IPv4 slice emptied (not deleted) once the node became IPv6-only, got %+v", v4.Endpoints)
	}
	v6 := getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6")
	if len(v6.Endpoints) != 1 || v6.Endpoints[0].Addresses[0] != "2001:db8::50:4:1" {
		t.Errorf("expected the -ipv6 slice created with the node's new address, got %+v", v6.Endpoints)
	}
}

func TestReconcile_ManagedSliceDeletedByUser_RecreatedWithServiceUID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.5.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	svc := getService(t, ctx, id)
	original := getEndpointSlice(t, ctx, id+"-metrics")

	if err := k8sClient.Delete(ctx, original); err != nil {
		t.Fatalf("deleting managed slice: %v", err)
	}
	if err := getEndpointSliceErr(ctx, id+"-metrics"); !isNotFound(err) {
		t.Fatalf("setup: expected the slice gone, got err=%v", err)
	}

	reconcile(t, ctx, cfg)

	es := getEndpointSlice(t, ctx, id+"-metrics")
	if es.UID == original.UID {
		t.Fatal("expected a freshly created slice, got the original UID back")
	}
	if len(es.Endpoints) != 1 || es.Endpoints[0].Addresses[0] != "10.50.5.1" {
		t.Errorf("expected the recreated slice to carry the node, got %+v", es.Endpoints)
	}
	ref := ownerRefTo(t, es.OwnerReferences, svc)
	if ref.UID != svc.UID || ref.Controller == nil || !*ref.Controller {
		t.Errorf("expected a controller ownerRef to the live Service (UID %s), got %+v", svc.UID, es.OwnerReferences)
	}
}

func TestReconcile_ForeignSliceWithServiceNameLabel_NeverDeleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.6.1", true, withExtraLabels(cpLabel))

	// Named like a slice we might have produced, labelled for our Service,
	// but managed by someone else: exactly what a stale-prune must skip.
	foreign := &discoveryv1.EndpointSlice{
		Name:      id + "-metrics-ipv6",
		Namespace: testNamespace,
		Labels: map[string]string{
			discoveryv1.LabelServiceName: id,
			discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
		},
		AddressType: discoveryv1.AddressTypeIPv6,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"2001:db8::50:6:1"}}},
	}
	if err := k8sClient.Create(ctx, foreign); err != nil {
		t.Fatalf("creating foreign slice: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), foreign) })
	unlabelled := &discoveryv1.EndpointSlice{
		Name:        id + "-metrics-stale",
		Namespace:   testNamespace,
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.50.6.99"}}},
	}
	if err := k8sClient.Create(ctx, unlabelled); err != nil {
		t.Fatalf("creating unlabelled slice: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), unlabelled) })

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	reconcile(t, ctx, cfg) // and again, in case prune only misfires on a second pass

	_ = getEndpointSlice(t, ctx, id+"-metrics")
	for _, pre := range []*discoveryv1.EndpointSlice{foreign, unlabelled} {
		var now discoveryv1.EndpointSlice
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pre), &now); err != nil {
			t.Fatalf("%s: expected the foreign slice to survive, got %v", pre.Name, err)
		}
		if now.ResourceVersion != pre.ResourceVersion {
			t.Errorf("%s: foreign slice was written to: rv %s -> %s", pre.Name, pre.ResourceVersion, now.ResourceVersion)
		}
	}
}

func TestReconcile_ManagedSliceForUnconfiguredService_Pruned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.7.1", true, withExtraLabels(cpLabel))

	// Left behind by a previous config that still listed <id>-old.
	leftover := &discoveryv1.EndpointSlice{
		Name:      id + "-old-metrics",
		Namespace: testNamespace,
		Labels: map[string]string{
			discoveryv1.LabelServiceName: id + "-old",
			discoveryv1.LabelManagedBy:   endpoints.ManagedByValue,
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.50.7.1"}}},
	}
	if err := k8sClient.Create(ctx, leftover); err != nil {
		t.Fatalf("creating leftover slice: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), leftover) })

	reconcile(t, ctx, svcConfig(id, cpLabel))

	if err := getEndpointSliceErr(ctx, id+"-old-metrics"); !isNotFound(err) {
		t.Fatalf("expected the leftover managed slice pruned, got err=%v", err)
	}
	_ = getEndpointSlice(t, ctx, id+"-metrics")
}

// --- echo reconcile -----------------------------------------------------

// allWritesCounter tallies every Create/Update/Patch/Delete through the
// client, by kind, so a converged reconcile can be shown to write nothing.
// ResourceVersion checks alone can't: the API server short-circuits a
// semantically no-op Update without bumping ResourceVersion, hiding a
// write that would still re-trigger the reconciler through its own
// Service/EndpointSlice watch.
type allWritesCounter struct {
	mu     sync.Mutex
	writes map[string]int
}

func (w *allWritesCounter) record(verb string, obj client.Object) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writes == nil {
		w.writes = map[string]int{}
	}
	kind := "other"
	switch obj.(type) {
	case *corev1.Service:
		kind = "service"
	case *discoveryv1.EndpointSlice:
		kind = "endpointslice"
	case *corev1.Endpoints: //nolint:staticcheck
		kind = "endpoints"
	}
	w.writes[verb+" "+kind]++
}

func (w *allWritesCounter) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.writes {
		n += c
	}
	return n
}

type countingClient struct {
	client.Client
	counter *allWritesCounter
}

func (c countingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.counter.record("create", obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c countingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.counter.record("update", obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c countingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.counter.record("patch", obj)
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c countingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.counter.record("delete", obj)
	return c.Client.Delete(ctx, obj, opts...)
}

// TestReconcile_Converged_EchoReconcileMakesNoWrites guards the explicit
// Service targetPort (the API server defaults it to port, so an unset
// value looks like a diff every reconcile) and prune's no-op path: once
// converged, a second reconcile over dual-stack nodes with legacy
// Endpoints on must perform zero writes of any kind.
func TestReconcile_Converged_EchoReconcileMakesNoWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "v4-"+id, "10.50.8.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "v6-"+id, "2001:db8::50:8:1", false, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true

	counter := &allWritesCounter{}
	spy := countingClient{Client: k8sClient, counter: counter}
	reconcileWithClients(t, ctx, cfg, spy, nil)
	if counter.total() == 0 {
		t.Fatal("setup: expected the first reconcile to write")
	}
	_ = getService(t, ctx, id)
	_ = getEndpointSlice(t, ctx, id+"-metrics")
	_ = getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6")
	_ = getLegacyEndpoints(t, ctx, id)

	counter.mu.Lock()
	counter.writes = map[string]int{}
	counter.mu.Unlock()
	reconcileWithClients(t, ctx, cfg, spy, nil)

	if n := counter.total(); n != 0 {
		t.Fatalf("expected zero writes on the echo reconcile, got %d: %v", n, counter.writes)
	}
}

// TestReconcile_AdoptedServiceWithDefaultedTargetPort_NoEchoWrite covers
// the pre-existing Service case: a headless Service created without a
// targetPort has it defaulted by the API server, and adopting it must not
// keep rewriting it either.
func TestReconcile_AdoptedServiceWithDefaultedTargetPort_NoEchoWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.9.1", true, withExtraLabels(cpLabel))

	pre := &corev1.Service{
		Name: id, Namespace: testNamespace,
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Ports:     []corev1.ServicePort{{Name: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatalf("creating pre-existing Service: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), pre) })

	cfg := svcConfig(id, cpLabel)
	counter := &allWritesCounter{}
	spy := countingClient{Client: k8sClient, counter: counter}
	reconcileWithClients(t, ctx, cfg, spy, nil) // adopts, labels, sets appProtocol
	_ = getEndpointSlice(t, ctx, id+"-metrics")

	counter.mu.Lock()
	counter.writes = map[string]int{}
	counter.mu.Unlock()
	reconcileWithClients(t, ctx, cfg, spy, nil)

	if n := counter.writes["update service"]; n != 0 {
		t.Fatalf("expected no Service rewrite on the echo reconcile, got %d: %v", n, counter.writes)
	}
	var svc corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: id}, &svc); err != nil {
		t.Fatalf("getting adopted Service: %v", err)
	}
	if svc.Spec.Ports[0].TargetPort.IntVal != 9999 {
		t.Errorf("expected targetPort to equal port after adoption, got %v", svc.Spec.Ports[0].TargetPort)
	}
}

// TestReconcile_CollidingService_PreExistingManagedSliceNotPruned pins
// prune's key to everything the builder produced, not just the slices
// written this round: a Service that can't be applied (an unadoptable
// ClusterIP squatter) keeps whatever managed slice already exists at its
// name rather than losing it to a transient failure.
func TestReconcile_CollidingService_PreExistingManagedSliceNotPruned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.50.10.1", true, withExtraLabels(cpLabel))

	cfg := threeServiceConfig(id, cpLabel)
	reconcile(t, ctx, cfg) // all three converge, including <id>-b's slice
	bBefore := getEndpointSlice(t, ctx, id+"-b-metrics")

	// Now <id>-b's Service is replaced by an unadoptable squatter.
	if err := k8sClient.Delete(ctx, &corev1.Service{Name: id + "-b", Namespace: testNamespace}); err != nil {
		t.Fatalf("deleting managed Service: %v", err)
	}
	createCollidingService(t, ctx, id+"-b")

	if err := reconcileErr(ctx, cfg); err == nil {
		t.Fatal("expected the colliding Service to surface an error")
	}

	bAfter := getEndpointSlice(t, ctx, id+"-b-metrics")
	if bAfter.UID != bBefore.UID {
		t.Fatalf("expected <id>-b's existing managed slice kept (same UID), got %s -> %s", bBefore.UID, bAfter.UID)
	}
	if bAfter.ResourceVersion != bBefore.ResourceVersion {
		t.Errorf("expected <id>-b's slice left untouched (not rewritten against the squatter): rv %s -> %s", bBefore.ResourceVersion, bAfter.ResourceVersion)
	}
	for _, ok := range []string{id + "-a", id + "-c"} {
		if es := getEndpointSlice(t, ctx, ok+"-metrics"); len(es.Endpoints) != 1 {
			t.Errorf("%s: expected the other services' slices intact, got %+v", ok, es.Endpoints)
		}
	}
}

// replaceOnDelete swaps the named slice for a fresh same-named one (new
// UID) just before the first Delete of it reaches the API server, as a
// racing writer would between prune's cached List and its Delete.
type replaceOnDelete struct {
	client.Client
	t        *testing.T
	name     string
	replaced *discoveryv1.EndpointSlice
}

func (c *replaceOnDelete) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if obj.GetName() == c.name && c.replaced == nil {
		es, ok := obj.(*discoveryv1.EndpointSlice)
		if !ok {
			c.t.Fatalf("expected an EndpointSlice Delete for %s, got %T", c.name, obj)
		}
		if err := c.Client.Delete(ctx, es); err != nil {
			c.t.Fatalf("race: deleting %s: %v", c.name, err)
		}
		fresh := &discoveryv1.EndpointSlice{
			Name: es.Name, Namespace: es.Namespace, Labels: es.Labels,
			AddressType: es.AddressType, Endpoints: es.Endpoints,
		}
		if err := c.Client.Create(ctx, fresh); err != nil {
			c.t.Fatalf("race: recreating %s: %v", c.name, err)
		}
		c.replaced = fresh
	}
	return c.Client.Delete(ctx, obj, opts...)
}

// TestReconcile_PruneRacesRecreate_PreconditionProtectsNewSlice proves the
// UID precondition on prune's Delete against the real API server: the
// replacement survives that reconcile (which reports the Conflict), and
// the next reconcile, seeing it stale under its own UID, prunes it.
func TestReconcile_PruneRacesRecreate_PreconditionProtectsNewSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "v4-"+id, "10.50.11.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "v6-"+id, "2001:db8::50:11:1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	stale := getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6")
	deleteNode(t, ctx, "v6-"+id)

	racer := &replaceOnDelete{Client: k8sClient, t: t, name: id + "-metrics-ipv6"}
	r := &controller.NodeReconciler{Client: racer, Config: cfg}
	_, err := r.Reconcile(ctx, ctrl.Request{})
	if racer.replaced == nil {
		t.Fatal("setup: expected prune to attempt the Delete")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected the preconditioned Delete to fail with Conflict, got %v", err)
	}

	var now discoveryv1.EndpointSlice
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(racer.replaced), &now); err != nil {
		t.Fatalf("expected the replacement slice to survive the stale Delete: %v", err)
	}
	if now.UID == stale.UID || now.UID != racer.replaced.UID {
		t.Fatalf("expected the replacement (UID %s) in place, got UID %s", racer.replaced.UID, now.UID)
	}

	reconcile(t, ctx, cfg) // fresh List sees the replacement's own UID
	if err := getEndpointSliceErr(ctx, id+"-metrics-ipv6"); !isNotFound(err) {
		t.Fatalf("expected the still-stale replacement pruned on the next reconcile, got err=%v", err)
	}
}
