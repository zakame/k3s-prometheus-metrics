package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// Pruning and the empty-slice path: a managed slice the builder no longer
// produces (the -ipv6 one after the last IPv6 node left) is deleted; a
// service whose nodes all left keeps its IPv4 slice, emptied. Everything
// not ours, or not in Config.Namespace, is never touched.

// managedSlice returns an EndpointSlice carrying this controller's
// managed-by label for svc, with one endpoint so a test can tell whether
// it was rewritten.
func managedSlice(namespace, name, svc string, family discoveryv1.AddressType) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		Name:      name,
		Namespace: namespace,
		Labels: map[string]string{
			discoveryv1.LabelServiceName: svc,
			discoveryv1.LabelManagedBy:   endpoints.ManagedByValue,
		},
		AddressType: family,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.1"}}},
	}
}

// writeLog records every Create/Update/Delete the reconciler makes as
// "verb kind namespace/name", so a test can assert which writes happened
// and, just as importantly, which didn't.
type writeLog struct{ entries []string }

func (w *writeLog) add(verb string, obj client.Object) {
	kind := fmt.Sprintf("%T", obj)
	w.entries = append(w.entries, fmt.Sprintf("%s %s %s/%s", verb, kind, obj.GetNamespace(), obj.GetName()))
}

// matching returns the entries for verb; name, if non-empty, narrows to
// that object name.
func (w *writeLog) matching(verb, name string) []string {
	var out []string
	for _, e := range w.entries {
		if !strings.HasPrefix(e, verb+" ") {
			continue
		}
		if name != "" && !strings.HasSuffix(e, "/"+name) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (w *writeLog) funcs(deleteErr func(obj client.Object) error) interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			w.add("create", obj)
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			w.add("update", obj)
			return cl.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			w.add("patch", obj)
			return cl.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			w.add("delete", obj)
			if deleteErr != nil {
				if err := deleteErr(obj); err != nil {
					return err
				}
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}
}

// pruneFixture builds a fake client holding objs with every write logged,
// and the reconciler over it. deleteErr, if set, is consulted on Delete.
func pruneFixture(t *testing.T, cfg config.Config, deleteErr func(client.Object) error, objs ...client.Object) (*NodeReconciler, client.Client, *writeLog) {
	t.Helper()
	log := &writeLog{}
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithInterceptorFuncs(log.funcs(deleteErr)).
		Build()
	return &NodeReconciler{Client: c, Config: cfg}, c, log
}

func getSlice(t *testing.T, c client.Client, namespace, name string) (*discoveryv1.EndpointSlice, error) {
	t.Helper()
	var es discoveryv1.EndpointSlice
	err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &es)
	return &es, err
}

func mustGetSlice(t *testing.T, c client.Client, namespace, name string) *discoveryv1.EndpointSlice {
	t.Helper()
	es, err := getSlice(t, c, namespace, name)
	if err != nil {
		t.Fatalf("expected EndpointSlice %s/%s to exist: %v", namespace, name, err)
	}
	return es
}

func mustNotFindSlice(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	if _, err := getSlice(t, c, namespace, name); !apierrors.IsNotFound(err) {
		t.Fatalf("expected EndpointSlice %s/%s to be gone, got err=%v", namespace, name, err)
	}
}

func TestReconcile_Prune_DeletesStaleIPv6SliceWhenNoIPv6NodesRemain(t *testing.T) {
	stale := managedSlice(isoNamespace, "alpha-metrics-ipv6", "alpha", discoveryv1.AddressTypeIPv6)
	r, c, log := pruneFixture(t, isoConfig(), nil, isoNode("v4", isoNodeIP), stale)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	mustNotFindSlice(t, c, isoNamespace, "alpha-metrics-ipv6")
	if got := log.matching("delete", "alpha-metrics-ipv6"); len(got) != 1 {
		t.Errorf("expected exactly one Delete of the stale -ipv6 slice, got %v", log.entries)
	}
	for _, svc := range isoConfig().Services {
		es := mustGetSlice(t, c, isoNamespace, svc.Name+"-metrics")
		if len(es.Endpoints) != 1 || es.Endpoints[0].Addresses[0] != isoNodeIP {
			t.Errorf("%s: expected the IPv4 slice intact with the node's IP, got %+v", es.Name, es.Endpoints)
		}
	}
}

func TestReconcile_Prune_DualStackToIPv4Only_OnlyIPv6SliceGoes(t *testing.T) {
	r, c, log := pruneFixture(t, isoConfig(), nil, isoNode("v4", isoNodeIP), isoNode("v6", isoNodeIPv6))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	for _, svc := range isoConfig().Services {
		mustGetSlice(t, c, isoNamespace, svc.Name+"-metrics-ipv6")
	}
	if n := len(log.matching("delete", "")); n != 0 {
		t.Fatalf("setup: expected no deletes while both families exist, got %v", log.entries)
	}

	if err := c.Delete(context.Background(), &corev1.Node{Name: "v6"}); err != nil {
		t.Fatalf("deleting v6 node: %v", err)
	}
	log.entries = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	for _, svc := range isoConfig().Services {
		mustNotFindSlice(t, c, isoNamespace, svc.Name+"-metrics-ipv6")
		es := mustGetSlice(t, c, isoNamespace, svc.Name+"-metrics")
		if len(es.Endpoints) != 1 || es.Endpoints[0].Addresses[0] != isoNodeIP {
			t.Errorf("%s: expected the IPv4 slice untouched, got %+v", es.Name, es.Endpoints)
		}
	}
	if n := len(log.matching("delete", "")); n != len(isoConfig().Services) {
		t.Errorf("expected exactly one Delete per service (its -ipv6 slice), got %v", log.entries)
	}
}

func TestReconcile_Prune_LeavesSlicesWithoutOurManagedByLabelAlone(t *testing.T) {
	// Kubernetes' own mirroring controller's slice, labelled for one of our
	// service names, plus an unlabelled slice at a name we might produce.
	foreign := &discoveryv1.EndpointSlice{
		Name: "alpha", Namespace: isoNamespace,
		Labels: map[string]string{
			discoveryv1.LabelServiceName: "alpha",
			discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"198.51.100.1"}}},
	}
	unlabelled := &discoveryv1.EndpointSlice{
		Name: "bravo-metrics-ipv6", Namespace: isoNamespace,
		AddressType: discoveryv1.AddressTypeIPv6,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"2001:db8::51"}}},
	}
	r, c, log := pruneFixture(t, isoConfig(), nil, isoNode("v4", isoNodeIP), foreign, unlabelled)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for _, name := range []string{"alpha", "bravo-metrics-ipv6"} {
		es := mustGetSlice(t, c, isoNamespace, name)
		if len(es.Endpoints) != 1 {
			t.Errorf("%s: expected the foreign slice's endpoints untouched, got %+v", name, es.Endpoints)
		}
		if got := log.matching("delete", name); len(got) != 0 {
			t.Errorf("%s: expected no Delete of a slice we don't manage, got %v", name, got)
		}
		if got := log.matching("update", name); len(got) != 0 {
			t.Errorf("%s: expected no Update of a slice we don't manage, got %v", name, got)
		}
	}
	if es := mustGetSlice(t, c, isoNamespace, "alpha"); es.Labels[discoveryv1.LabelManagedBy] != "endpointslice-controller.k8s.io" {
		t.Errorf("foreign managed-by label was rewritten: %v", es.Labels)
	}
}

func TestReconcile_Prune_LeavesOtherNamespacesAlone(t *testing.T) {
	elsewhere := managedSlice("other", "alpha-metrics-ipv6", "alpha", discoveryv1.AddressTypeIPv6)
	r, c, log := pruneFixture(t, isoConfig(), nil, isoNode("v4", isoNodeIP), elsewhere)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	mustGetSlice(t, c, "other", "alpha-metrics-ipv6")
	if n := len(log.matching("delete", "")); n != 0 {
		t.Errorf("expected no deletes outside %s, got %v", isoNamespace, log.entries)
	}
}

// A managed slice for a service that has since left the config is stale
// in exactly the same way as a -ipv6 slice, so prune removes it too.
func TestReconcile_Prune_RemovesManagedSliceForServiceNoLongerConfigured(t *testing.T) {
	gone := managedSlice(isoNamespace, "delta-metrics", "delta", discoveryv1.AddressTypeIPv4)
	r, c, _ := pruneFixture(t, isoConfig(), nil, isoNode("v4", isoNodeIP), gone)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	mustNotFindSlice(t, c, isoNamespace, "delta-metrics")
}

func TestReconcile_Prune_NotFoundOnDeleteIsNotAnError(t *testing.T) {
	stale := managedSlice(isoNamespace, "alpha-metrics-ipv6", "alpha", discoveryv1.AddressTypeIPv6)
	// Simulates the slice vanishing (GC, a user) between List and Delete.
	raced := func(obj client.Object) error {
		return apierrors.NewNotFound(schema.GroupResource{Group: "discovery.k8s.io", Resource: "endpointslices"}, obj.GetName())
	}
	r, _, log := pruneFixture(t, isoConfig(), raced, isoNode("v4", isoNodeIP), stale)

	res, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("expected NotFound on Delete to be swallowed, got %v", err)
	}
	if res.RequeueAfter != resyncInterval {
		t.Errorf("expected the normal resync requeue after a swallowed NotFound, got %+v", res)
	}
	if got := log.matching("delete", "alpha-metrics-ipv6"); len(got) != 1 {
		t.Errorf("expected the Delete to have been attempted once, got %v", log.entries)
	}
}

func TestReconcile_Prune_DeleteErrorSurfacesButOtherWritesStillHappen(t *testing.T) {
	stale := managedSlice(isoNamespace, "alpha-metrics-ipv6", "alpha", discoveryv1.AddressTypeIPv6)
	boom := errors.New("boom")
	fail := func(obj client.Object) error { return boom }
	r, c, _ := pruneFixture(t, isoConfig(), fail, isoNode("v4", isoNodeIP), stale)

	res, err := r.Reconcile(context.Background(), ctrl.Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the Delete failure surfaced, got %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("expected a zero Result alongside an error, got %+v", res)
	}
	for _, svc := range isoConfig().Services {
		mustGetSlice(t, c, isoNamespace, svc.Name+"-metrics")
	}
	mustGetSlice(t, c, isoNamespace, "alpha-metrics-ipv6") // still there, to be retried
}

// A Service that failed to apply this round keeps whatever slices it has:
// prune is keyed on everything the builder produced, not just the slices
// written against successfully applied Services.
func TestReconcile_Prune_FailedServiceKeepsItsExistingSlices(t *testing.T) {
	boom := errors.New("boom")
	existing := managedSlice(isoNamespace, "bravo-metrics", "bravo", discoveryv1.AddressTypeIPv4)
	log := &writeLog{}
	funcs := log.funcs(nil)
	base := funcs.Create
	funcs.Create = func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if svc, ok := obj.(*corev1.Service); ok && svc.Name == "bravo" {
			return boom
		}
		return base(ctx, cl, obj, opts...)
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(isoNode("v4", isoNodeIP), existing).WithInterceptorFuncs(funcs).Build()
	r := &NodeReconciler{Client: c, Config: isoConfig()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); !errors.Is(err, boom) {
		t.Fatalf("expected the Service failure surfaced, got %v", err)
	}

	es := mustGetSlice(t, c, isoNamespace, "bravo-metrics")
	if len(es.Endpoints) != 1 || es.Endpoints[0].Addresses[0] != "192.0.2.1" {
		t.Errorf("expected bravo's pre-existing slice untouched (not rewritten against an unapplied Service), got %+v", es.Endpoints)
	}
	if got := log.matching("delete", "bravo-metrics"); len(got) != 0 {
		t.Errorf("expected bravo's slice not pruned over a transient Service failure, got %v", got)
	}
	for _, ok := range []string{"alpha", "charlie"} {
		mustGetSlice(t, c, isoNamespace, ok+"-metrics")
	}
}

func TestReconcile_NodesGoToZero_UpdatesSliceToEmptyRatherThanDeleting(t *testing.T) {
	cfg := isoConfig()
	cfg.WriteLegacyEndpoints = true
	r, c, log := pruneFixture(t, cfg, nil, isoNode("v4", isoNodeIP))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if es := mustGetSlice(t, c, isoNamespace, "alpha-metrics"); len(es.Endpoints) != 1 {
		t.Fatalf("setup: expected 1 endpoint before the node leaves, got %+v", es.Endpoints)
	}

	if err := c.Delete(context.Background(), &corev1.Node{Name: "v4"}); err != nil {
		t.Fatalf("deleting node: %v", err)
	}
	log.entries = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	for _, svc := range cfg.Services {
		name := svc.Name + "-metrics"
		es := mustGetSlice(t, c, isoNamespace, name)
		if len(es.Endpoints) != 0 {
			t.Errorf("%s: expected the slice emptied, got %+v", name, es.Endpoints)
		}
		if es.AddressType != discoveryv1.AddressTypeIPv4 || len(es.OwnerReferences) != 1 {
			t.Errorf("%s: expected the emptied slice to keep its address type and owner, got %v / %+v", name, es.AddressType, es.OwnerReferences)
		}
		if got := log.matching("update", name); len(got) != 1 {
			t.Errorf("%s: expected exactly one Update to empty the slice, got %v", name, log.entries)
		}
		if got := log.matching("delete", name); len(got) != 0 {
			t.Errorf("%s: expected no Delete of the IPv4 slice, got %v", name, got)
		}

		var eps corev1.Endpoints //nolint:staticcheck
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: svc.Name}, &eps); err != nil {
			t.Fatalf("expected legacy Endpoints %s kept: %v", svc.Name, err)
		}
		if eps.Subsets != nil {
			t.Errorf("%s: expected legacy Endpoints subsets cleared, got %+v", svc.Name, eps.Subsets)
		}
	}
	if n := len(log.matching("delete", "")); n != 0 {
		t.Errorf("expected no deletes at all when only IPv4 slices exist, got %v", log.entries)
	}
}

func TestReconcile_NoNodesEver_CreatesEmptySlicesAndSubsetlessEndpoints(t *testing.T) {
	cfg := isoConfig()
	cfg.WriteLegacyEndpoints = true
	r, c, _ := pruneFixture(t, cfg, nil)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var list discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &list, client.InNamespace(isoNamespace)); err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, es := range list.Items {
		names = append(names, es.Name)
		if len(es.Endpoints) != 0 {
			t.Errorf("%s: expected empty, got %+v", es.Name, es.Endpoints)
		}
	}
	sort.Strings(names)
	want := []string{"alpha-metrics", "bravo-metrics", "charlie-metrics"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("expected exactly the IPv4 slices %v, got %v", want, names)
	}
}

func TestReconcile_Success_RequeuesAfterResyncInterval(t *testing.T) {
	r, _, _ := pruneFixture(t, isoConfig(), nil, isoNode("v4", isoNodeIP))
	res, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != resyncInterval || res.RequeueAfter <= 0 {
		t.Errorf("expected RequeueAfter=%v on success, got %+v", resyncInterval, res)
	}
}

func TestReconcile_ListNodesFails_ReturnsErrorAndZeroResult(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.NodeList); ok {
				return boom
			}
			return cl.List(ctx, list, opts...)
		},
	}).Build()
	r := &NodeReconciler{Client: c, Config: isoConfig()}

	res, err := r.Reconcile(context.Background(), ctrl.Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the List error surfaced, got %v", err)
	}
	// controller-runtime warns and ignores RequeueAfter when err != nil;
	// the error's rate-limited requeue is the intended path.
	if res != (ctrl.Result{}) {
		t.Errorf("expected a zero Result with an error, got %+v", res)
	}
}

func TestReconcile_ServiceApplyFails_ReturnsZeroResult(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(isoNode("v4", isoNodeIP)).
		WithInterceptorFuncs(failCreates(map[string]error{"alpha": boom})).Build()
	r := &NodeReconciler{Client: c, Config: isoConfig()}

	res, err := r.Reconcile(context.Background(), ctrl.Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the Service failure surfaced, got %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("expected a zero Result with an error, got %+v", res)
	}
}

// With Services and EndpointSlices now watched, a write on an already
// converged reconcile would re-trigger the reconciler through its own
// watch. The echo reconcile must therefore make no writes at all.
func TestReconcile_ConvergedState_EchoReconcileMakesNoWrites(t *testing.T) {
	cfg := isoConfig()
	cfg.WriteLegacyEndpoints = true
	r, _, log := pruneFixture(t, cfg, nil, isoNode("v4", isoNodeIP), isoNode("v6", isoNodeIPv6))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(log.entries) == 0 {
		t.Fatal("setup: expected the first reconcile to create objects")
	}

	log.entries = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(log.entries) != 0 {
		t.Errorf("expected zero writes on the echo reconcile, got %v", log.entries)
	}
}

// The prune List is served from the cache, so the Delete carries the
// listed slice's UID as a precondition: a same-named slice created since
// must not be deleted on stale information.
func TestReconcile_Prune_DeleteCarriesListedUIDPrecondition(t *testing.T) {
	stale := managedSlice(isoNamespace, "alpha-metrics-ipv6", "alpha", discoveryv1.AddressTypeIPv6)
	stale.UID = "stale-uid"

	var seen []types.UID
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(isoNode("v4", isoNodeIP), stale).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				var o client.DeleteOptions
				o.ApplyOptions(opts)
				if o.Preconditions == nil || o.Preconditions.UID == nil {
					t.Errorf("expected a UID precondition on Delete of %s, got %+v", obj.GetName(), o.Preconditions)
				} else {
					seen = append(seen, *o.Preconditions.UID)
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &NodeReconciler{Client: c, Config: isoConfig()}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(seen) != 1 || seen[0] != "stale-uid" {
		t.Fatalf("expected exactly one Delete preconditioned on the listed UID, got %v", seen)
	}
}
