package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

func labeledReadyNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		Name:   name,
		Labels: labels,
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func TestGenerateManifests_WritesServiceAndEndpointSliceYAML(t *testing.T) {
	cpNode := labeledReadyNode("cp", map[string]string{"node-role.kubernetes.io/control-plane": "true"})
	c := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(cpNode).Build()

	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: map[string]string{"node-role.kubernetes.io/control-plane": "true"},
		Services:     config.DefaultServices,
	}

	var buf bytes.Buffer
	if err := generateManifests(context.Background(), &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"kind: Service", "name: kube-scheduler", "kind: EndpointSlice", "name: kube-scheduler-metrics", "10.0.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestGenerateManifests_WriteLegacyEndpointsIncludesEndpointsKind(t *testing.T) {
	cpNode := labeledReadyNode("cp", map[string]string{"node-role.kubernetes.io/control-plane": "true"})
	c := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(cpNode).Build()

	cfg := config.Config{
		Namespace:            "kube-system",
		NodeSelector:         map[string]string{"node-role.kubernetes.io/control-plane": "true"},
		WriteLegacyEndpoints: true,
		Services:             config.DefaultServices,
	}

	var buf bytes.Buffer
	if err := generateManifests(context.Background(), &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	if !strings.Contains(buf.String(), "kind: Endpoints") {
		t.Errorf("expected legacy Endpoints in output, got:\n%s", buf.String())
	}
}

func TestGenerateManifests_NoMatchingNodesProducesOnlyServices(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(runtimeScheme).Build()

	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: map[string]string{"node-role.kubernetes.io/control-plane": "true"},
		Services:     config.DefaultServices,
	}

	var buf bytes.Buffer
	if err := generateManifests(context.Background(), &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "kind: Service") {
		t.Errorf("expected Service manifests even with no nodes, got:\n%s", out)
	}
	if strings.Contains(out, "kind: EndpointSlice") {
		t.Errorf("expected no EndpointSlice manifests with no matching nodes, got:\n%s", out)
	}
}
