package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// The Service and EndpointSlice watches exist so a deleted or hand-edited
// managed object is repaired without waiting for a Node event. Their
// predicates decide which objects count as "managed"; anything else must
// not wake the reconciler.

// allEventKinds runs p against every event kind for obj, so a predicate
// built with NewPredicateFuncs is checked on Delete too, where it matters
// most (a deleted managed object is the case the watch is for).
func allEventKinds(p predicate.Predicate, obj client.Object) map[string]bool {
	return map[string]bool{
		"create":  p.Create(event.CreateEvent{Object: obj}),
		"update":  p.Update(event.UpdateEvent{ObjectOld: obj, ObjectNew: obj}),
		"delete":  p.Delete(event.DeleteEvent{Object: obj}),
		"generic": p.Generic(event.GenericEvent{Object: obj}),
	}
}

func assertAllEvents(t *testing.T, got map[string]bool, want bool, what string) {
	t.Helper()
	for kind, v := range got {
		if v != want {
			t.Errorf("%s: expected %s event -> %v, got %v", what, kind, want, v)
		}
	}
}

func managedService(namespace, name string) *corev1.Service {
	return &corev1.Service{
		Name: name, Namespace: namespace,
		Labels: map[string]string{"app.kubernetes.io/managed-by": endpoints.ManagedByValue},
	}
}

func TestManagedServicePredicate_LabelledServiceInNamespace_LetsThrough(t *testing.T) {
	r := &NodeReconciler{Config: isoConfig()}
	assertAllEvents(t, allEventKinds(r.managedServicePredicate(), managedService(isoNamespace, "anything")), true, "labelled service")
}

func TestManagedServicePredicate_UnlabelledServiceAtManagedName_LetsThrough(t *testing.T) {
	// A squatter at one of our names: its change or deletion still matters.
	r := &NodeReconciler{Config: isoConfig()}
	squatter := &corev1.Service{Name: "bravo", Namespace: isoNamespace, Labels: map[string]string{"owner": "someone-else"}}
	assertAllEvents(t, allEventKinds(r.managedServicePredicate(), squatter), true, "squatter at a managed name")
}

func TestManagedServicePredicate_UnrelatedServiceInNamespace_Filtered(t *testing.T) {
	r := &NodeReconciler{Config: isoConfig()}
	other := &corev1.Service{Name: "kubernetes", Namespace: isoNamespace}
	assertAllEvents(t, allEventKinds(r.managedServicePredicate(), other), false, "unrelated service")
}

func TestManagedServicePredicate_LabelledServiceInOtherNamespace_Filtered(t *testing.T) {
	r := &NodeReconciler{Config: isoConfig()}
	assertAllEvents(t, allEventKinds(r.managedServicePredicate(), managedService("other", "alpha")), false, "labelled service elsewhere")
}

func TestManagedServicePredicate_ManagedNameInOtherNamespace_Filtered(t *testing.T) {
	r := &NodeReconciler{Config: isoConfig()}
	assertAllEvents(t, allEventKinds(r.managedServicePredicate(), &corev1.Service{Name: "alpha", Namespace: "other"}), false, "managed name elsewhere")
}

func TestManagedServicePredicate_NilLabels_DoesNotPanic(t *testing.T) {
	r := &NodeReconciler{Config: isoConfig()}
	if r.managedServicePredicate().Delete(event.DeleteEvent{Object: &corev1.Service{Name: "x", Namespace: isoNamespace}}) {
		t.Fatal("expected an unlabelled, unmanaged-name service to be filtered")
	}
}

func TestManagedSlicePredicate_OurManagedBy_LetsThrough(t *testing.T) {
	es := managedSlice(isoNamespace, "alpha-metrics", "alpha", discoveryv1.AddressTypeIPv4)
	assertAllEvents(t, allEventKinds(slicePredicate(), es), true, "our slice")
}

func TestManagedSlicePredicate_ForeignManagedBy_Filtered(t *testing.T) {
	es := &discoveryv1.EndpointSlice{
		Name: "alpha-metrics", Namespace: isoNamespace,
		Labels: map[string]string{
			discoveryv1.LabelServiceName: "alpha",
			discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
		},
	}
	assertAllEvents(t, allEventKinds(slicePredicate(), es), false, "foreign slice")
}

func TestManagedSlicePredicate_NoLabels_Filtered(t *testing.T) {
	assertAllEvents(t, allEventKinds(slicePredicate(), &discoveryv1.EndpointSlice{Name: "x", Namespace: isoNamespace}), false, "unlabelled slice")
}

func TestManagedSlicePredicate_ServiceNameLabelAloneIsNotEnough(t *testing.T) {
	es := &discoveryv1.EndpointSlice{
		Name: "alpha-metrics", Namespace: isoNamespace,
		Labels: map[string]string{discoveryv1.LabelServiceName: "alpha"},
	}
	assertAllEvents(t, allEventKinds(slicePredicate(), es), false, "service-name label only")
}

// reconcileKey is what every watch maps to; a controller-runtime workqueue
// treats an empty request as a valid (if odd) key, so pin it non-empty.
// Cluster-scoped: a namespace would suggest the key identifies an object.
func TestReconcileKey_IsNonEmptyAndClusterScoped(t *testing.T) {
	if reconcileKey.Name == "" {
		t.Fatal("expected reconcileKey to carry a name")
	}
	if reconcileKey.Namespace != "" {
		t.Fatalf("expected a cluster-scoped key, got namespace %q", reconcileKey.Namespace)
	}
}

// slicePredicate is managedSlicePredicate for isoConfig's namespace.
func slicePredicate() predicate.Predicate {
	return (&NodeReconciler{Config: isoConfig()}).managedSlicePredicate()
}

func TestManagedSlicePredicate_OurManagedByInOtherNamespace_Filtered(t *testing.T) {
	// Another instance's slice in its own namespace must not wake this one,
	// and prune never lists outside Config.Namespace anyway.
	es := managedSlice("other", "alpha-metrics", "alpha", discoveryv1.AddressTypeIPv4)
	assertAllEvents(t, allEventKinds(slicePredicate(), es), false, "our slice elsewhere")
}
