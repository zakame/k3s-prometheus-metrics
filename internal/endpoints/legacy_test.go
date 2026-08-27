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
	cfg := testConfig()
	nodes := []corev1.Node{node("no-ip", "", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	if got != nil {
		t.Fatalf("expected nil when no node has a usable InternalIP, got %#v", got)
	}
}

func TestBuildEndpoints_ReadyNode_InAddresses(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
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
	cfg := testConfig()
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionFalse))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.Addresses) != 0 || len(subset.NotReadyAddresses) != 1 {
		t.Fatalf("expected 0 ready, 1 not-ready, got ready=%d notReady=%d",
			len(subset.Addresses), len(subset.NotReadyAddresses))
	}
}

func TestBuildEndpoints_CordonedNode_InNotReadyAddresses(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue), cordoned())}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.NotReadyAddresses) != 1 {
		t.Fatalf("cordoned node must land in NotReadyAddresses (v1 Endpoints has no per-address condition), got ready=%d notReady=%d",
			len(subset.Addresses), len(subset.NotReadyAddresses))
	}
}

func TestBuildEndpoints_MixedReadyAndNotReady_CorrectSplit(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("ready1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("ready2", "10.0.0.2", withReadyCondition(corev1.ConditionTrue)),
		node("notready1", "10.0.0.3", withReadyCondition(corev1.ConditionFalse)),
	}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if len(subset.Addresses) != 2 {
		t.Errorf("expected 2 ready addresses, got %d", len(subset.Addresses))
	}
	if len(subset.NotReadyAddresses) != 1 {
		t.Errorf("expected 1 not-ready address, got %d", len(subset.NotReadyAddresses))
	}
}

func TestBuildEndpoints_AllNotReady_ReadyAddressesIsNil(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionFalse))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if subset.Addresses != nil {
		t.Errorf("expected nil (not empty) Addresses when all nodes are not-ready, got %#v", subset.Addresses)
	}
}

func TestBuildEndpoints_AllReady_NotReadyAddressesIsNil(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	subset := got[0].Subsets[0]
	if subset.NotReadyAddresses != nil {
		t.Errorf("expected nil (not empty) NotReadyAddresses when all nodes are ready, got %#v", subset.NotReadyAddresses)
	}
}

func TestBuildEndpoints_MultipleServices_PortsAndLabelsPerService(t *testing.T) {
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolUDP, AppProtocol: "https"},
	)
	cfg.Namespace = "custom-ns"
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	if len(got) != 2 {
		t.Fatalf("expected 2 Endpoints objects, got %d", len(got))
	}

	byName := map[string]corev1.Endpoints{} //nolint:staticcheck
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

func TestBuildEndpoints_DifferentNodeSetsPerService_NoCrossContamination(t *testing.T) {
	cpNode := node("cp", "10.0.1.1", withReadyCondition(corev1.ConditionTrue))
	agentNode := node("agent", "10.0.1.2", withReadyCondition(corev1.ConditionTrue))

	cfg := testConfig(
		config.Service{Name: "kube-scheduler", Port: 10259, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
		config.Service{Name: "kube-proxy", Port: 10249, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
	)
	nodesByService := map[string][]corev1.Node{
		"kube-scheduler": {cpNode},
		"kube-proxy":     {cpNode, agentNode},
	}

	got := endpoints.BuildEndpoints(nodesByService, cfg) //nolint:staticcheck
	byName := map[string]corev1.Endpoints{}              //nolint:staticcheck
	for _, e := range got {
		byName[e.Name] = e
	}

	sched, ok := byName["kube-scheduler"]
	if !ok {
		t.Fatal("missing kube-scheduler Endpoints object")
	}
	if len(sched.Subsets[0].Addresses) != 1 || *sched.Subsets[0].Addresses[0].NodeName != "cp" {
		t.Fatalf("expected kube-scheduler Endpoints to contain only the cp node, got %+v", sched.Subsets[0].Addresses)
	}

	proxy, ok := byName["kube-proxy"]
	if !ok {
		t.Fatal("missing kube-proxy Endpoints object")
	}
	if len(proxy.Subsets[0].Addresses) != 2 {
		t.Fatalf("expected kube-proxy Endpoints to contain both nodes, got %d: %+v",
			len(proxy.Subsets[0].Addresses), proxy.Subsets[0].Addresses)
	}
}

func TestBuildEndpoints_ServiceWithNoQualifyingNodes_OnlyThatServiceSkipped(t *testing.T) {
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
	)
	nodesByService := map[string][]corev1.Node{
		"svc-a": {node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))},
		// svc-b: absent -- e.g. its selector currently matches zero nodes.
	}

	got := endpoints.BuildEndpoints(nodesByService, cfg) //nolint:staticcheck
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 Endpoints object (svc-b has no qualifying nodes), got %d: %+v", len(got), got)
	}
	if got[0].Name != "svc-a" {
		t.Fatalf("expected the surviving object to be svc-a, got %q", got[0].Name)
	}
}

func TestBuildEndpoints_PortsMatchDefaultServices(t *testing.T) {
	cfg := testConfig(config.DefaultServices...)
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck

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
	cfg := testConfig(config.Service{Name: "custom", PortName: "custom-port-name", Port: 4242, Protocol: corev1.ProtocolTCP, AppProtocol: "http"})
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck
	if got[0].Subsets[0].Ports[0].Name != "custom-port-name" {
		t.Fatalf("expected port name to come from Service.PortName, got %q", got[0].Subsets[0].Ports[0].Name)
	}
}

func TestBuildEndpoints_AppProtocolPointersNotAliasedAcrossServices(t *testing.T) {
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
	)
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpoints(nodesFor(cfg, nodes), cfg) //nolint:staticcheck

	byName := map[string]corev1.Endpoints{} //nolint:staticcheck
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
	cfg := testConfig(config.DefaultServices...)
	nodes := []corev1.Node{
		node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("n2", "10.0.0.2", withReadyCondition(corev1.ConditionFalse)),
	}
	nbs := nodesFor(cfg, nodes)

	first := endpoints.BuildEndpoints(nbs, cfg)  //nolint:staticcheck
	second := endpoints.BuildEndpoints(nbs, cfg) //nolint:staticcheck

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
	nbs := nodesFor(cfg, nodes)

	slices := endpoints.BuildEndpointSlices(nbs, cfg)
	legacy := endpoints.BuildEndpoints(nbs, cfg) //nolint:staticcheck

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
