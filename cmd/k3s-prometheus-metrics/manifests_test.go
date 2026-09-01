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

func labeledNode(name string, labels map[string]string, addr string, ready bool) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Node{
		Name:   name,
		Labels: labels,
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: addr}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
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

// TestGenerateManifests_NotReadyNodeKeptWithReadyFalse guards that the
// one-shot path preserves internal/endpoints.BuildEndpointSlices's
// guarantee: a cordoned/NotReady node stays in the EndpointSlice with
// ready=false rather than being silently dropped from the generated YAML.
func TestGenerateManifests_NotReadyNodeKeptWithReadyFalse(t *testing.T) {
	labels := map[string]string{"node-role.kubernetes.io/control-plane": "true"}
	readyNode := labeledNode("cp-ready", labels, "10.0.0.1", true)
	notReadyNode := labeledNode("cp-not-ready", labels, "10.0.0.2", false)
	c := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(readyNode, notReadyNode).Build()

	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: labels,
		Services:     config.DefaultServices,
	}

	var buf bytes.Buffer
	if err := generateManifests(context.Background(), &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"10.0.0.1", "10.0.0.2", "ready: false"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// TestGenerateManifests_CustomServiceNodeSelectorOverridesConfigDefault
// guards that a per-Service NodeSelector (config.Service.NodeSelector),
// not just the shared config.Config.NodeSelector, is honored end-to-end
// through ListNodesByService and into the rendered output.
func TestGenerateManifests_CustomServiceNodeSelectorOverridesConfigDefault(t *testing.T) {
	edgeNode := labeledNode("edge-1", map[string]string{"tier": "edge"}, "10.0.0.9", true)
	cpNode := labeledNode("cp-1", map[string]string{"node-role.kubernetes.io/control-plane": "true"}, "10.0.0.1", true)
	c := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(edgeNode, cpNode).Build()

	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: map[string]string{"node-role.kubernetes.io/control-plane": "true"},
		Services: []config.Service{
			{Name: "custom-svc", NodeSelector: map[string]string{"tier": "edge"}, PortName: "http-metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		},
	}

	var buf bytes.Buffer
	if err := generateManifests(context.Background(), &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "10.0.0.9") {
		t.Errorf("expected custom-svc's edge node address in output, got:\n%s", out)
	}
	if strings.Contains(out, "10.0.0.1") {
		t.Errorf("expected control-plane node NOT matched by custom-svc's own selector, got:\n%s", out)
	}
}

// TestGenerateManifests_DualStackNodesProduceIPv4AndIPv6EndpointSlices
// exercises the split-by-address-family path (internal/endpoints splits
// dual-stack nodes into separate EndpointSlices per AddressType) all the
// way through generateManifests and manifest.Render's ordering, not just
// unit-tested in isolation.
func TestGenerateManifests_DualStackNodesProduceIPv4AndIPv6EndpointSlices(t *testing.T) {
	labels := map[string]string{"node-role.kubernetes.io/control-plane": "true"}
	ipv4Node := labeledNode("cp-v4", labels, "10.0.0.1", true)
	ipv6Node := labeledNode("cp-v6", labels, "fd00::1", true)
	c := fake.NewClientBuilder().WithScheme(runtimeScheme).WithObjects(ipv4Node, ipv6Node).Build()

	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: labels,
		Services:     config.DefaultServices,
	}

	var buf bytes.Buffer
	if err := generateManifests(context.Background(), &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	out := buf.String()
	ipv4Idx := strings.Index(out, "name: kube-scheduler-metrics\n")
	ipv6Idx := strings.Index(out, "name: kube-scheduler-metrics-ipv6\n")
	if ipv4Idx < 0 || ipv6Idx < 0 {
		t.Fatalf("expected both kube-scheduler-metrics and kube-scheduler-metrics-ipv6 EndpointSlices, got:\n%s", out)
	}
	if ipv4Idx > ipv6Idx {
		t.Errorf("expected IPv4 EndpointSlice to sort before its -ipv6 sibling, got:\n%s", out)
	}
	for _, want := range []string{"10.0.0.1", "fd00::1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}
