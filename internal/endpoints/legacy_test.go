package endpoints_test

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

func addrByIP(t *testing.T, addrs []corev1.EndpointAddress, ip string) corev1.EndpointAddress {
	t.Helper()
	for _, a := range addrs {
		if a.IP == ip {
			return a
		}
	}
	t.Fatalf("no address %q found in %+v", ip, addrs)
	return corev1.EndpointAddress{}
}

func TestBuildEndpoints_NoNodes_ReturnsNil(t *testing.T) {
	got := endpoints.BuildEndpoints(nil, testConfig()) //nolint:staticcheck
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestBuildEndpoints_NodeWithoutInternalIP_Skipped(t *testing.T) {
	nodes := []corev1.Node{node("no-ip", "", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	if got != nil {
		t.Fatalf("expected nil when no node has a usable InternalIP, got %#v", got)
	}
}

func TestBuildEndpoints_ReadyNode_InAddresses(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.Addresses) != 1 || len(subset.NotReadyAddresses) != 0 {
		t.Fatalf("expected 1 ready address, 0 not-ready, got ready=%d notReady=%d",
			len(subset.Addresses), len(subset.NotReadyAddresses))
	}
	a := addrByIP(t, subset.Addresses, "10.0.0.1")
	if a.NodeName == nil || *a.NodeName != "n1" {
		t.Errorf("expected NodeName n1, got %v", a.NodeName)
	}
}

func TestBuildEndpoints_NotReadyNode_InNotReadyAddresses(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionFalse))}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.Addresses) != 0 || len(subset.NotReadyAddresses) != 1 {
		t.Fatalf("expected 0 ready, 1 not-ready, got ready=%d notReady=%d",
			len(subset.Addresses), len(subset.NotReadyAddresses))
	}
}

func TestBuildEndpoints_CordonedNode_InNotReadyAddresses(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue), cordoned())}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.NotReadyAddresses) != 1 {
		t.Fatalf("cordoned node must land in NotReadyAddresses (v1 Endpoints has no per-address condition), got ready=%d notReady=%d",
			len(subset.Addresses), len(subset.NotReadyAddresses))
	}
}

func TestBuildEndpoints_MixedReadyAndNotReady_CorrectSplit(t *testing.T) {
	nodes := []corev1.Node{
		node("ready1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("ready2", "10.0.0.2", withReadyCondition(corev1.ConditionTrue)),
		node("notready1", "10.0.0.3", withReadyCondition(corev1.ConditionFalse)),
	}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.Addresses) != 2 {
		t.Errorf("expected 2 ready addresses, got %d", len(subset.Addresses))
	}
	if len(subset.NotReadyAddresses) != 1 {
		t.Errorf("expected 1 not-ready address, got %d", len(subset.NotReadyAddresses))
	}
}

func TestBuildEndpoints_AllNotReady_ReadyAddressesIsNil(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionFalse))}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if subset.Addresses != nil {
		t.Errorf("expected nil (not empty) Addresses when all nodes are not-ready, got %#v", subset.Addresses)
	}
}

func TestBuildEndpoints_AllReady_NotReadyAddressesIsNil(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodes, testConfig()) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if subset.NotReadyAddresses != nil {
		t.Errorf("expected nil (not empty) NotReadyAddresses when all nodes are ready, got %#v", subset.NotReadyAddresses)
	}
}

func TestBuildEndpoints_MultipleServices_PortsAndLabelsPerService(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolUDP, AppProtocol: "https"},
	)
	cfg.Namespace = "custom-ns"
	got := endpoints.BuildEndpoints(nodes, cfg) //nolint:staticcheck
	if len(got) != 2 {
		t.Fatalf("expected 2 Endpoints objects, got %d", len(got))
	}

	byName := map[string]corev1.Endpoints{}
	for _, e := range got {
		byName[e.Name] = e
	}

	a, ok := byName["svc-a"]
	if !ok {
		t.Fatal("missing svc-a Endpoints object")
	}
	if a.Namespace != "custom-ns" {
		t.Errorf("expected namespace custom-ns, got %q", a.Namespace)
	}
	if a.Labels["kubernetes.io/service-name"] != "svc-a" {
		t.Errorf("expected service-name label svc-a, got %q", a.Labels["kubernetes.io/service-name"])
	}
	if a.Labels["app.kubernetes.io/managed-by"] != endpoints.ManagedByValue {
		t.Errorf("expected managed-by label %s, got %q", endpoints.ManagedByValue, a.Labels["app.kubernetes.io/managed-by"])
	}
	// Without this, kube-controller-manager's EndpointSliceMirroring
	// controller (bundled by k3s) would auto-mirror this Endpoints object
	// into a second EndpointSlice, duplicating/conflicting with the one
	// BuildEndpointSlices already produces for the same service.
	if a.Labels[discoveryv1.LabelSkipMirror] != "true" {
		t.Errorf("expected %s=true to suppress EndpointSlice auto-mirroring, got %q",
			discoveryv1.LabelSkipMirror, a.Labels[discoveryv1.LabelSkipMirror])
	}
	pa := a.Subsets[0].Ports[0]
	if pa.Port != 1111 || pa.Protocol != corev1.ProtocolTCP || pa.AppProtocol == nil || *pa.AppProtocol != "http" {
		t.Errorf("svc-a port mismatch: %+v", pa)
	}

	b := byName["svc-b"]
	pb := b.Subsets[0].Ports[0]
	if pb.Port != 2222 || pb.Protocol != corev1.ProtocolUDP || pb.AppProtocol == nil || *pb.AppProtocol != "https" {
		t.Errorf("svc-b port mismatch: %+v", pb)
	}
}

func TestBuildEndpoints_PortsMatchDefaultServices(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodes, testConfig(config.DefaultServices...)) //nolint:staticcheck

	want := map[string]struct {
		port        int32
		protocol    corev1.Protocol
		appProtocol string
		portName    string
	}{
		"kube-scheduler":          {10259, corev1.ProtocolTCP, "https", "https-metrics"},
		"kube-controller-manager": {10257, corev1.ProtocolTCP, "https", "https-metrics"},
		"kube-proxy":              {10249, corev1.ProtocolTCP, "http", "http-metrics"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d Endpoints objects, got %d", len(want), len(got))
	}
	for _, e := range got {
		w, ok := want[e.Name]
		if !ok {
			t.Fatalf("unexpected Endpoints name %q", e.Name)
		}
		p := e.Subsets[0].Ports[0]
		if p.Port != w.port {
			t.Errorf("%s: expected port %d, got %d", e.Name, w.port, p.Port)
		}
		if p.Protocol != w.protocol {
			t.Errorf("%s: expected protocol %v, got %v", e.Name, w.protocol, p.Protocol)
		}
		if p.AppProtocol == nil || *p.AppProtocol != w.appProtocol {
			t.Errorf("%s: expected appProtocol %v, got %v", e.Name, w.appProtocol, p.AppProtocol)
		}
		if p.Name != w.portName {
			t.Errorf("%s: expected port name %q, got %q", e.Name, w.portName, p.Name)
		}
	}
}

func TestBuildEndpoints_PortNamePropagatedFromService(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	cfg := testConfig(config.Service{Name: "custom", PortName: "custom-port-name", Port: 4242, Protocol: corev1.ProtocolTCP, AppProtocol: "http"})
	got := endpoints.BuildEndpoints(nodes, cfg) //nolint:staticcheck
	if got[0].Subsets[0].Ports[0].Name != "custom-port-name" {
		t.Fatalf("expected port name to come from Service.PortName, got %q", got[0].Subsets[0].Ports[0].Name)
	}
}

func TestBuildEndpoints_AppProtocolPointersNotAliasedAcrossServices(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
	)
	got := endpoints.BuildEndpoints(nodes, cfg) //nolint:staticcheck

	byName := map[string]corev1.Endpoints{}
	for _, e := range got {
		byName[e.Name] = e
	}
	pa := byName["svc-a"].Subsets[0].Ports[0].AppProtocol
	pb := byName["svc-b"].Subsets[0].Ports[0].AppProtocol
	if pa == nil || pb == nil || *pa != "http" || *pb != "https" {
		t.Fatalf("appProtocols bled into each other across services: a=%v b=%v", pa, pb)
	}
}

func TestBuildEndpoints_Idempotent(t *testing.T) {
	nodes := []corev1.Node{
		node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("n2", "10.0.0.2", withReadyCondition(corev1.ConditionFalse)),
	}
	cfg := testConfig(config.DefaultServices...)

	first := endpoints.BuildEndpoints(nodes, cfg)  //nolint:staticcheck
	second := endpoints.BuildEndpoints(nodes, cfg) //nolint:staticcheck

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected identical output for identical input (required for CreateOrUpdate to no-op):\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

// TestReadyClassification_ConsistentBetweenSliceAndLegacy guards against the
// two builders' readiness logic drifting apart: a node considered Ready in
// the EndpointSlice output must be the same set of nodes landing in
// Addresses (not NotReadyAddresses) for the legacy Endpoints output.
func TestReadyClassification_ConsistentBetweenSliceAndLegacy(t *testing.T) {
	nodes := []corev1.Node{
		node("ready", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("cordoned", "10.0.0.2", withReadyCondition(corev1.ConditionTrue), cordoned()),
		node("notready", "10.0.0.3", withReadyCondition(corev1.ConditionFalse)),
		node("unknown", "10.0.0.4", withReadyCondition(corev1.ConditionUnknown)),
		node("bare", "10.0.0.5"),
	}
	cfg := testConfig()

	slices := endpoints.BuildEndpointSlices(nodes, cfg)
	legacy := endpoints.BuildEndpoints(nodes, cfg) //nolint:staticcheck

	sliceReady := map[string]bool{}
	for _, ep := range slices[0].Endpoints {
		sliceReady[*ep.NodeName] = *ep.Conditions.Ready
	}

	legacyReady := map[string]bool{}
	for _, a := range legacy[0].Subsets[0].Addresses {
		legacyReady[*a.NodeName] = true
	}
	for _, a := range legacy[0].Subsets[0].NotReadyAddresses {
		legacyReady[*a.NodeName] = false
	}

	if !reflect.DeepEqual(sliceReady, legacyReady) {
		t.Fatalf("readiness classification diverged between builders:\nslice:  %#v\nlegacy: %#v", sliceReady, legacyReady)
	}
}
