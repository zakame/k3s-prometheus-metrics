package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// Per-object error isolation: one object that can't be applied must not
// take the rest of its batch (or, in Reconcile, the other services'
// endpoints) down with it.

const (
	isoNamespace = "kube-system"
	isoNodeIP    = "10.9.0.1"
	isoNodeIPv6  = "2001:db8::9:1"
)

var isoNodeLabel = map[string]string{"role": "control-plane"}

func isoConfig() config.Config {
	return config.Config{
		Namespace:    isoNamespace,
		NodeSelector: isoNodeLabel,
		Services: []config.Service{
			{Name: "alpha", PortName: "metrics", Port: 9001, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			{Name: "bravo", PortName: "metrics", Port: 9002, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			{Name: "charlie", PortName: "metrics", Port: 9003, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		},
	}
}

func isoServices(names ...string) []corev1.Service {
	svcs := make([]corev1.Service, len(names))
	for i, n := range names {
		svcs[i] = corev1.Service{Name: n, Namespace: isoNamespace}
	}
	return svcs
}

func isoNode(name, ip string) *corev1.Node {
	return &corev1.Node{
		Name:   name,
		Labels: isoNodeLabel,
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

// failCreates injects failures[name] on Create of any object with that
// name; everything else passes through.
func failCreates(failures map[string]error) interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err, ok := failures[obj.GetName()]; ok {
				return err
			}
			return cl.Create(ctx, obj, opts...)
		},
	}
}

// slicesLabelledFor lists the EndpointSlices in the fake client carrying
// the kubernetes.io/service-name label for svc.
func slicesLabelledFor(t *testing.T, c client.Client, svc string) []discoveryv1.EndpointSlice {
	t.Helper()
	var list discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &list, client.InNamespace(isoNamespace), client.MatchingLabels{discoveryv1.LabelServiceName: svc}); err != nil {
		t.Fatalf("listing EndpointSlices for %s: %v", svc, err)
	}
	return list.Items
}

func appliedNames(svcs []corev1.Service) []string {
	names := make([]string, len(svcs))
	for i, s := range svcs {
		names[i] = s.Name
	}
	return names
}

func TestApplyAll_MiddleObjectFails_ReturnsOthersInOrderAndNamesOnlyTheFailure(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(failCreates(map[string]error{"bravo": boom})).
		Build()

	applied, err := applyAll(context.Background(), c, isoServices("alpha", "bravo", "charlie"), "service", func(_, _ *corev1.Service) {})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the joined error to satisfy errors.Is against the injected failure, got %v", err)
	}
	if got := appliedNames(applied); len(got) != 2 || got[0] != "alpha" || got[1] != "charlie" {
		t.Fatalf("expected exactly the successes [alpha charlie] in want order, got %v", got)
	}
	for _, svc := range applied {
		if svc.ResourceVersion == "" {
			t.Errorf("applied %s: expected a server-populated ResourceVersion", svc.Name)
		}
	}

	msg := err.Error()
	if !strings.Contains(msg, isoNamespace+"/bravo") {
		t.Errorf("expected the error to name the failed object, got %q", msg)
	}
	for _, ok := range []string{"alpha", "charlie"} {
		if strings.Contains(msg, ok) {
			t.Errorf("expected the error not to mention successfully applied %s, got %q", ok, msg)
		}
	}

	var stored corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: "bravo"}, &stored); !apierrors.IsNotFound(err) {
		t.Errorf("expected the failed object not to be persisted, got err=%v", err)
	}
	for _, ok := range []string{"alpha", "charlie"} {
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: ok}, &stored); err != nil {
			t.Errorf("expected %s to be persisted despite bravo failing: %v", ok, err)
		}
	}
}

func TestApplyAll_TwoOfThreeFail_ErrorJoinsEachDistinctFailure(t *testing.T) {
	errAlpha := errors.New("alpha boom")
	errCharlie := errors.New("charlie boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(failCreates(map[string]error{"alpha": errAlpha, "charlie": errCharlie})).
		Build()

	applied, err := applyAll(context.Background(), c, isoServices("alpha", "bravo", "charlie"), "service", func(_, _ *corev1.Service) {})
	if err == nil {
		t.Fatal("expected an error when two objects fail")
	}
	for _, want := range []error{errAlpha, errCharlie} {
		if !errors.Is(err, want) {
			t.Errorf("expected errors.Is(err, %v) through the join, got %v", want, err)
		}
	}
	msg := err.Error()
	for _, want := range []string{isoNamespace + "/alpha", isoNamespace + "/charlie"} {
		if strings.Count(msg, want) != 1 {
			t.Errorf("expected %q to appear exactly once in %q", want, msg)
		}
	}
	if strings.Contains(msg, "bravo") {
		t.Errorf("expected the error not to mention the success, got %q", msg)
	}
	if got := appliedNames(applied); len(got) != 1 || got[0] != "bravo" {
		t.Fatalf("expected only [bravo] applied, got %v", got)
	}
}

func TestApplyAll_UpdateFailure_IsIsolatedLikeCreateFailure(t *testing.T) {
	// CreateOrUpdate takes the Update path for a pre-existing object, so a
	// failure there must be isolated too, not just failures on Create.
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(&corev1.Service{Name: "bravo", Namespace: isoNamespace}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if obj.GetName() == "bravo" {
					return boom
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).
		Build()

	// The mutate must change something, or CreateOrUpdate skips Update.
	mutate := func(got, _ *corev1.Service) { got.Labels = map[string]string{"v": "1"} }
	applied, err := applyAll(context.Background(), c, isoServices("alpha", "bravo", "charlie"), "service", mutate)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the Update failure through the join, got %v", err)
	}
	if got := appliedNames(applied); len(got) != 2 || got[0] != "alpha" || got[1] != "charlie" {
		t.Fatalf("expected [alpha charlie] applied, got %v", got)
	}
}

func TestApplyAll_GetFailure_IsIsolatedLikeCreateFailure(t *testing.T) {
	// A non-NotFound Get error is CreateOrUpdate's third failure point.
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == "bravo" {
					return boom
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	applied, err := applyAll(context.Background(), c, isoServices("alpha", "bravo", "charlie"), "service", func(_, _ *corev1.Service) {})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the Get failure through the join, got %v", err)
	}
	if got := appliedNames(applied); len(got) != 2 || got[0] != "alpha" || got[1] != "charlie" {
		t.Fatalf("expected [alpha charlie] applied, got %v", got)
	}
}

func TestApplyAll_AllFail_ReturnsEmptyNonNilAppliedAndNamesEveryFailure(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(failCreates(map[string]error{"alpha": boom, "bravo": boom, "charlie": boom})).
		Build()

	applied, err := applyAll(context.Background(), c, isoServices("alpha", "bravo", "charlie"), "service", func(_, _ *corev1.Service) {})
	if err == nil {
		t.Fatal("expected an error when every object fails")
	}
	if applied == nil || len(applied) != 0 {
		t.Fatalf("expected a non-nil empty applied slice, got %#v", applied)
	}
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if strings.Count(err.Error(), isoNamespace+"/"+name) != 1 {
			t.Errorf("expected %s named exactly once in %q", name, err.Error())
		}
	}
}

func TestOwnedBy_DropsUnappliedServicesPreservingOrder(t *testing.T) {
	svcs := map[string]corev1.Service{"alpha": {}, "charlie": {}}
	slices := []discoveryv1.EndpointSlice{
		{Name: "alpha-metrics", Labels: map[string]string{discoveryv1.LabelServiceName: "alpha"}},
		{Name: "bravo-metrics", Labels: map[string]string{discoveryv1.LabelServiceName: "bravo"}},
		{Name: "bravo-metrics-ipv6", Labels: map[string]string{discoveryv1.LabelServiceName: "bravo"}},
		{Name: "charlie-metrics", Labels: map[string]string{discoveryv1.LabelServiceName: "charlie"}},
		{Name: "unlabelled"},
	}

	kept := ownedBy(slices, svcs)
	if len(kept) != 2 || kept[0].Name != "alpha-metrics" || kept[1].Name != "charlie-metrics" {
		t.Fatalf("expected [alpha-metrics charlie-metrics], got %+v", kept)
	}
}

func TestOwnedBy_NilInput_ReturnsEmpty(t *testing.T) {
	if kept := ownedBy[discoveryv1.EndpointSlice](nil, map[string]corev1.Service{"alpha": {}}); len(kept) != 0 {
		t.Fatalf("expected nothing kept from a nil input, got %+v", kept)
	}
}

// reconcileWithFailingServices runs Reconcile against a fake client where
// creating the named Services fails with boom, returning the client, the
// number of EndpointSlice writes attempted per service, and the error.
func reconcileWithFailingServices(t *testing.T, cfg config.Config, boom error, failing []string, nodes ...client.Object) (client.Client, map[string]int, error) {
	t.Helper()

	failures := map[string]error{}
	for _, name := range failing {
		failures[name] = boom
	}
	sliceWrites := map[string]int{}
	countSlice := func(obj client.Object) {
		if _, ok := obj.(*discoveryv1.EndpointSlice); ok {
			sliceWrites[obj.GetLabels()[discoveryv1.LabelServiceName]]++
		}
	}
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(nodes...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, isSvc := obj.(*corev1.Service); isSvc {
					if err, ok := failures[obj.GetName()]; ok {
						return err
					}
				}
				countSlice(obj)
				return cl.Create(ctx, obj, opts...)
			},
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				countSlice(obj)
				return cl.Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				countSlice(obj)
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	r := &NodeReconciler{Client: c, Config: cfg}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	return c, sliceWrites, err
}

func TestReconcile_OneServiceFailsToApply_OthersStillGetEndpointSlices(t *testing.T) {
	for _, failing := range []string{"alpha", "bravo", "charlie"} {
		t.Run(failing, func(t *testing.T) {
			boom := errors.New("boom")
			c, sliceWrites, err := reconcileWithFailingServices(t, isoConfig(), boom, []string{failing}, isoNode("n1", isoNodeIP))
			if !errors.Is(err, boom) {
				t.Fatalf("expected Reconcile to surface the Service failure, got %v", err)
			}
			if n := strings.Count(err.Error(), isoNamespace+"/"+failing); n != 1 {
				t.Errorf("expected the failed service named exactly once (no double wrapping), got %d in %q", n, err.Error())
			}

			if got := slicesLabelledFor(t, c, failing); len(got) != 0 {
				t.Errorf("expected no EndpointSlice labelled for the failed service, got %d", len(got))
			}
			if sliceWrites[failing] != 0 {
				t.Errorf("expected zero EndpointSlice write attempts for the failed service, got %d", sliceWrites[failing])
			}

			for _, svc := range isoConfig().Services {
				if svc.Name == failing {
					continue
				}
				var stored corev1.Service
				if err := c.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: svc.Name}, &stored); err != nil {
					t.Fatalf("expected Service %s to exist despite %s failing: %v", svc.Name, failing, err)
				}
				slices := slicesLabelledFor(t, c, svc.Name)
				if len(slices) != 1 {
					t.Fatalf("expected exactly one EndpointSlice for %s, got %d", svc.Name, len(slices))
				}
				es := slices[0]
				if len(es.Endpoints) != 1 || es.Endpoints[0].Addresses[0] != isoNodeIP {
					t.Errorf("%s: expected the node's IP as the sole endpoint, got %+v", es.Name, es.Endpoints)
				}
				if len(es.OwnerReferences) != 1 || es.OwnerReferences[0].UID != stored.UID || es.OwnerReferences[0].Controller == nil || !*es.OwnerReferences[0].Controller {
					t.Errorf("%s: expected a controller ownerRef to Service %s (UID %s), got %+v", es.Name, svc.Name, stored.UID, es.OwnerReferences)
				}
			}
		})
	}
}

func TestReconcile_DualStackNodes_FailedServiceLosesBothAddressFamilies(t *testing.T) {
	boom := errors.New("boom")
	c, sliceWrites, err := reconcileWithFailingServices(t, isoConfig(), boom, []string{"bravo"},
		isoNode("v4", isoNodeIP), isoNode("v6", isoNodeIPv6))
	if !errors.Is(err, boom) {
		t.Fatalf("expected the Service failure surfaced, got %v", err)
	}
	if got := slicesLabelledFor(t, c, "bravo"); len(got) != 0 {
		t.Errorf("expected neither the IPv4 nor the -ipv6 slice for bravo, got %d", len(got))
	}
	if sliceWrites["bravo"] != 0 {
		t.Errorf("expected zero EndpointSlice writes for bravo, got %d", sliceWrites["bravo"])
	}
	for _, ok := range []string{"alpha", "charlie"} {
		if got := slicesLabelledFor(t, c, ok); len(got) != 2 {
			t.Errorf("expected both address-family slices for %s, got %d", ok, len(got))
		}
	}
}

func TestReconcile_AllServicesFail_ReturnsErrorAndWritesNoEndpointSlices(t *testing.T) {
	boom := errors.New("boom")
	c, sliceWrites, err := reconcileWithFailingServices(t, isoConfig(), boom, []string{"alpha", "bravo", "charlie"}, isoNode("n1", isoNodeIP))
	if !errors.Is(err, boom) {
		t.Fatalf("expected an error when every Service fails, got %v", err)
	}
	var list discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &list, client.InNamespace(isoNamespace)); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 0 || len(sliceWrites) != 0 {
		t.Fatalf("expected no EndpointSlices at all, got %d stored and writes %v", len(list.Items), sliceWrites)
	}
}

func TestReconcile_OneServiceFails_LegacyEndpointsFollowThroughLegacyClient(t *testing.T) {
	boom := errors.New("boom")
	base := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(isoNode("n1", isoNodeIP)).
		WithInterceptorFuncs(failCreates(map[string]error{"bravo": boom})).
		Build()

	// LegacyClient shares base's store but tallies what reaches it, so the
	// test can tell filtered writes never even left Reconcile.
	legacyWrites := map[string]int{}
	legacy := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			legacyWrites[obj.GetName()]++
			return cl.Create(ctx, obj, opts...)
		},
	})

	cfg := isoConfig()
	cfg.WriteLegacyEndpoints = true
	r := &NodeReconciler{Client: base, Config: cfg, LegacyClient: legacy}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the Service failure surfaced, got %v", err)
	}

	var eps corev1.Endpoints //nolint:staticcheck
	if err := base.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: "bravo"}, &eps); !apierrors.IsNotFound(err) {
		t.Errorf("expected no legacy Endpoints for the failed service, got err=%v", err)
	}
	if legacyWrites["bravo"] != 0 {
		t.Errorf("expected zero legacy writes for bravo, got %d", legacyWrites["bravo"])
	}
	for _, ok := range []string{"alpha", "charlie"} {
		if err := base.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: ok}, &eps); err != nil {
			t.Fatalf("expected legacy Endpoints for %s: %v", ok, err)
		}
		if len(eps.Subsets) != 1 || len(eps.Subsets[0].Addresses) != 1 || eps.Subsets[0].Addresses[0].IP != isoNodeIP {
			t.Errorf("%s: expected the node's IP as the sole ready address, got %+v", ok, eps.Subsets)
		}
		if len(eps.OwnerReferences) != 1 || eps.OwnerReferences[0].Name != ok {
			t.Errorf("%s: expected a single ownerRef to its own Service, got %+v", ok, eps.OwnerReferences)
		}
		if legacyWrites[ok] != 1 {
			t.Errorf("expected exactly one legacy write for %s, got %d", ok, legacyWrites[ok])
		}
	}
}

func TestReconcile_OneEndpointSliceFails_OtherSlicesAndLegacyEndpointsStillWritten(t *testing.T) {
	// The failure is downstream of the Services this time, so bravo's
	// Service and legacy Endpoints must still land; only its slice is lost.
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(isoNode("n1", isoNodeIP)).
		WithInterceptorFuncs(failCreates(map[string]error{"bravo-metrics": boom})).
		Build()

	cfg := isoConfig()
	cfg.WriteLegacyEndpoints = true
	r := &NodeReconciler{Client: c, Config: cfg}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the EndpointSlice failure surfaced, got %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "endpointslice "+isoNamespace+"/bravo-metrics") {
		t.Errorf("expected the error to name the failed slice by kind and name, got %q", msg)
	}

	if got := slicesLabelledFor(t, c, "bravo"); len(got) != 0 {
		t.Errorf("expected no slice for bravo, got %d", len(got))
	}
	for _, ok := range []string{"alpha", "charlie"} {
		if got := slicesLabelledFor(t, c, ok); len(got) != 1 {
			t.Errorf("expected one slice for %s, got %d", ok, len(got))
		}
	}
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		var svc corev1.Service
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: name}, &svc); err != nil {
			t.Errorf("expected Service %s: %v", name, err)
		}
		var eps corev1.Endpoints //nolint:staticcheck
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: isoNamespace, Name: name}, &eps); err != nil {
			t.Errorf("expected legacy Endpoints %s despite the slice failure: %v", name, err)
		}
	}
}

func TestReconcile_ServiceAndSliceFailures_BothSurfaceInOneError(t *testing.T) {
	// Legs are joined, not short-circuited: a Service failure in one leg
	// and a slice failure in another both reach the caller.
	svcBoom := errors.New("service boom")
	sliceBoom := apierrors.NewInvalid(schema.GroupKind{Group: "discovery.k8s.io", Kind: "EndpointSlice"}, "charlie-metrics", nil)
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(isoNode("n1", isoNodeIP)).
		WithInterceptorFuncs(failCreates(map[string]error{"alpha": svcBoom, "charlie-metrics": sliceBoom})).
		Build()

	r := &NodeReconciler{Client: c, Config: isoConfig()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	if !errors.Is(err, svcBoom) {
		t.Errorf("expected the Service failure through the join, got %v", err)
	}
	if !apierrors.IsInvalid(err) {
		t.Errorf("expected apierrors.IsInvalid to see the slice failure through the join, got %v", err)
	}
	if got := slicesLabelledFor(t, c, "bravo"); len(got) != 1 {
		t.Errorf("expected bravo, untouched by either failure, to converge; got %d slices", len(got))
	}
	if got := fmt.Sprint(err); strings.Count(got, isoNamespace+"/alpha") != 1 || strings.Count(got, isoNamespace+"/charlie-metrics") != 1 {
		t.Errorf("expected each failure named exactly once, got %q", got)
	}
}
