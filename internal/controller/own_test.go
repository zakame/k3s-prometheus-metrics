package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// ownEndpointSlices and ownEndpoints are unexported, so these are
// white-box (same package) -- see list_nodes_test.go's comment for why
// that's preferable to an envtest round-trip here.

// conflictingOwnerRef looks like a controller reference to some object
// other than the Service ownEndpointSlices/ownEndpoints is about to set,
// which is what makes controllerutil.SetControllerReference refuse to
// overwrite it.
func conflictingOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Name:       "someone-else",
		UID:        types.UID("other-uid"),
		Controller: new(true),
	}
}

func TestOwnEndpointSlices_ConflictingControllerOwner_ReturnsError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &NodeReconciler{Client: c, Config: config.Config{Namespace: "kube-system"}}

	svc := corev1.Service{Name: "kube-scheduler", Namespace: "kube-system", UID: "svc-uid"}
	svcs := map[string]corev1.Service{"kube-scheduler": svc}

	slice := discoveryv1.EndpointSlice{
		Name:            "kube-scheduler-metrics",
		Namespace:       "kube-system",
		Labels:          map[string]string{discoveryv1.LabelServiceName: "kube-scheduler"},
		OwnerReferences: []metav1.OwnerReference{conflictingOwnerRef()},
	}

	if err := r.ownEndpointSlices([]discoveryv1.EndpointSlice{slice}, svcs); err == nil {
		t.Fatal("expected an error when the EndpointSlice already has a conflicting controller owner reference")
	}
}

func TestOwnEndpointSlices_NoMatchingService_LeavesUnowned(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &NodeReconciler{Client: c, Config: config.Config{Namespace: "kube-system"}}

	slice := discoveryv1.EndpointSlice{
		Name:      "orphan-metrics",
		Namespace: "kube-system",
		Labels:    map[string]string{discoveryv1.LabelServiceName: "no-such-service"},
	}

	slices := []discoveryv1.EndpointSlice{slice}
	if err := r.ownEndpointSlices(slices, map[string]corev1.Service{}); err != nil {
		t.Fatalf("ownEndpointSlices: %v", err)
	}
	if len(slices[0].OwnerReferences) != 0 {
		t.Fatalf("expected no owner reference set when no Service matches, got %+v", slices[0].OwnerReferences)
	}
}

func TestOwnEndpoints_ConflictingControllerOwner_ReturnsError(t *testing.T) { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &NodeReconciler{Client: c, Config: config.Config{Namespace: "kube-system"}}

	svc := corev1.Service{Name: "kube-scheduler", Namespace: "kube-system", UID: "svc-uid"}
	svcs := map[string]corev1.Service{"kube-scheduler": svc}

	eps := corev1.Endpoints{ //nolint:staticcheck
		Name:            "kube-scheduler",
		Namespace:       "kube-system",
		Labels:          map[string]string{discoveryv1.LabelServiceName: "kube-scheduler"},
		OwnerReferences: []metav1.OwnerReference{conflictingOwnerRef()},
	}

	if err := r.ownEndpoints([]corev1.Endpoints{eps}, svcs); err == nil { //nolint:staticcheck
		t.Fatal("expected an error when the Endpoints object already has a conflicting controller owner reference")
	}
}

func TestOwnEndpoints_NoMatchingService_LeavesUnowned(t *testing.T) { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &NodeReconciler{Client: c, Config: config.Config{Namespace: "kube-system"}}

	eps := corev1.Endpoints{ //nolint:staticcheck
		Name:      "orphan",
		Namespace: "kube-system",
		Labels:    map[string]string{discoveryv1.LabelServiceName: "no-such-service"},
	}

	epsList := []corev1.Endpoints{eps} //nolint:staticcheck
	if err := r.ownEndpoints(epsList, map[string]corev1.Service{}); err != nil {
		t.Fatalf("ownEndpoints: %v", err)
	}
	if len(epsList[0].OwnerReferences) != 0 {
		t.Fatalf("expected no owner reference set when no Service matches, got %+v", epsList[0].OwnerReferences)
	}
}
