package manifest

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestRender_MultiDocOrderedByKindThenName(t *testing.T) {
	svcs := []corev1.Service{
		{Name: "kube-proxy"},
		{Name: "kube-scheduler"},
	}
	slices := []discoveryv1.EndpointSlice{
		{Name: "kube-scheduler-metrics"},
	}

	out, err := Render(svcs, slices, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	docs := strings.Split(strings.TrimSpace(string(out)), "---\n")
	if len(docs) != 3 {
		t.Fatalf("expected 3 documents, got %d:\n%s", len(docs), out)
	}

	wantOrder := []string{"kube-proxy", "kube-scheduler", "kube-scheduler-metrics"}
	for i, want := range wantOrder {
		if !strings.Contains(docs[i], "name: "+want) {
			t.Errorf("doc %d: expected name %q, got:\n%s", i, want, docs[i])
		}
	}
}

func TestRender_StampsTypeMeta(t *testing.T) {
	svcs := []corev1.Service{{Name: "kube-proxy"}}
	slices := []discoveryv1.EndpointSlice{{Name: "kube-proxy-metrics"}}
	eps := []corev1.Endpoints{{Name: "kube-proxy"}} //nolint:staticcheck // SA1019: intentional legacy support

	out, err := Render(svcs, slices, eps)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, doc := range strings.Split(strings.TrimSpace(string(out)), "---\n") {
		var tm metav1.TypeMeta
		if err := yaml.Unmarshal([]byte(doc), &tm); err != nil {
			t.Fatalf("unmarshaling doc: %v\n%s", err, doc)
		}
		if tm.APIVersion == "" || tm.Kind == "" {
			t.Errorf("doc missing apiVersion/kind:\n%s", doc)
		}
	}
}

func TestRender_EmptyInputsProduceNoOutput(t *testing.T) {
	out, err := Render(nil, nil, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty output for empty inputs, got:\n%s", out)
	}
}
