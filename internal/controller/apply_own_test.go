package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// applyAll and ownAll are unexported package-level generics that the five
// method wrappers (applyServices, ownEndpointSlices, ownEndpoints,
// applyEndpointSlices, applyLegacyEndpoints) delegate to. These tests
// exercise the generics directly rather than through a wrapper -- see
// own_test.go's comment for why white-box beats an envtest round-trip for
// this kind of logic.

func TestApplyAll_EmptyWant_ReturnsEmptyNonNilResult(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	applied, err := applyAll(context.Background(), c, []corev1.Service{}, "service", func(_, _ *corev1.Service) {})
	if err != nil {
		t.Fatalf("applyAll: %v", err)
	}
	if applied == nil {
		t.Fatal("expected a non-nil empty slice for an empty want, got nil")
	}
	if len(applied) != 0 {
		t.Fatalf("expected zero applied objects, got %d", len(applied))
	}
}

func TestApplyAll_ReturnsAppliedObjectsInOrderWithServerPopulatedFields(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	want := []corev1.Service{
		{Name: "b-service", Namespace: "kube-system"},
		{Name: "a-service", Namespace: "kube-system"},
	}

	applied, err := applyAll(context.Background(), c, want, "service", func(_, _ *corev1.Service) {})
	if err != nil {
		t.Fatalf("applyAll: %v", err)
	}
	if len(applied) != 2 || applied[0].Name != "b-service" || applied[1].Name != "a-service" {
		t.Fatalf("expected applied objects in the same order as want, got %+v", applied)
	}
	for i, svc := range applied {
		if svc.ResourceVersion == "" {
			t.Fatalf("applied[%d] %s: expected a server-populated ResourceVersion, got none", i, svc.Name)
		}
	}
}

func TestApplyAll_SecondCallUpdatesExistingObjectRatherThanDuplicating(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	mutateLabel := func(value string) func(got, desired *corev1.Service) {
		return func(got, _ *corev1.Service) { got.Labels = map[string]string{"v": value} }
	}

	want := []corev1.Service{{Name: "kube-scheduler", Namespace: "kube-system"}}
	first, err := applyAll(context.Background(), c, want, "service", mutateLabel("first"))
	if err != nil {
		t.Fatalf("applyAll (create): %v", err)
	}

	second, err := applyAll(context.Background(), c, want, "service", mutateLabel("second"))
	if err != nil {
		t.Fatalf("applyAll (update): %v", err)
	}

	if second[0].Labels["v"] != "second" {
		t.Fatalf("expected the second call's mutate to update the existing object, got labels %+v", second[0].Labels)
	}
	if second[0].ResourceVersion == first[0].ResourceVersion {
		t.Fatalf("expected ResourceVersion to change on update, both calls returned %q", first[0].ResourceVersion)
	}

	var list corev1.ServiceList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly one Service after two applyAll calls for the same name, got %d", len(list.Items))
	}
}

func TestApplyAll_CreateOrUpdateErrorIsWrappedWithKindNamespaceName(t *testing.T) {
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
				return boom
			},
		}).
		Build()

	want := []corev1.Service{{Name: "kube-scheduler", Namespace: "kube-system"}}
	_, err := applyAll(context.Background(), c, want, "widget", func(_, _ *corev1.Service) {})
	if err == nil {
		t.Fatal("expected an error when the underlying Create fails")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected the wrapped error to satisfy errors.Is against the underlying failure, got %v", err)
	}
	for _, want := range []string{"widget", "kube-system", "kube-scheduler"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error message %q to contain %q", err.Error(), want)
		}
	}
}

func TestApplyAll_WorksWithLegacyEndpointsType(t *testing.T) { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	// applyLegacyEndpoints is the only wrapper never exercised by
	// Reconcile in this package's other tests (none set
	// WriteLegacyEndpoints), so applyAll's corev1.Endpoints
	// instantiation would otherwise go completely untested.
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	want := []corev1.Endpoints{{ //nolint:staticcheck
		Name:      "kube-scheduler",
		Namespace: "kube-system",
	}}
	subsets := []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}}} //nolint:staticcheck

	applied, err := applyAll(context.Background(), c, want, "endpoints", func(got, _ *corev1.Endpoints) { //nolint:staticcheck
		got.Subsets = subsets
	})
	if err != nil {
		t.Fatalf("applyAll: %v", err)
	}
	if len(applied) != 1 || applied[0].Name != "kube-scheduler" {
		t.Fatalf("expected the applied Endpoints to be returned, got %+v", applied)
	}
	if len(applied[0].Subsets) != 1 || applied[0].Subsets[0].Addresses[0].IP != "10.0.0.1" {
		t.Fatalf("expected mutate to have set Subsets, got %+v", applied[0].Subsets)
	}

	var stored corev1.Endpoints //nolint:staticcheck
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: "kube-scheduler"}, &stored); err != nil {
		t.Fatalf("expected the Endpoints object to actually be persisted: %v", err)
	}
}

func TestOwnAll_ConflictingOwner_ErrorMessageIncludesKindAndName(t *testing.T) {
	// Uses a kind string ("widget") that no wrapper actually passes, to
	// prove ownAll's error message is genuinely parameterized by its
	// kind argument rather than hardcoding "endpointslice"/"endpoints".
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	svc := corev1.Service{Name: "kube-scheduler", Namespace: "kube-system", UID: "svc-uid"}
	svcs := map[string]corev1.Service{"kube-scheduler": svc}

	eps := corev1.Endpoints{ //nolint:staticcheck
		Name:            "kube-scheduler-widget",
		Namespace:       "kube-system",
		Labels:          map[string]string{discoveryv1.LabelServiceName: "kube-scheduler"},
		OwnerReferences: []metav1.OwnerReference{conflictingOwnerRef()},
	}

	r := &NodeReconciler{Client: c}
	err := ownAll([]corev1.Endpoints{eps}, svcs, r.Scheme(), "widget") //nolint:staticcheck
	if err == nil {
		t.Fatal("expected an error from a conflicting controller owner reference")
	}
	for _, want := range []string{"widget", "kube-scheduler-widget"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error message %q to contain %q", err.Error(), want)
		}
	}
}
