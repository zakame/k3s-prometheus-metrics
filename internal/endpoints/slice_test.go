package endpoints_test

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// --- test fixtures -----------------------------------------------------

func testConfig(services ...config.Service) config.Config {
	if len(services) == 0 {
		services = []config.Service{
			{Name: "test-svc", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		}
	}
	return config.Config{Namespace: "kube-system", Services: services}
}

// nodesFor assigns the same nodes to every service in cfg, for tests that
// don't care about per-service scoping (see config.Service.NodeSelector).
func nodesFor(cfg config.Config, nodes []corev1.Node) map[string][]corev1.Node {
	m := make(map[string][]corev1.Node, len(cfg.Services))
	for _, svc := range cfg.Services {
		m[svc.Name] = nodes
	}
	return m
}

type nodeOpt func(*corev1.Node)

func node(name, internalIP string, opts ...nodeOpt) corev1.Node {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if internalIP != "" {
		n.Status.Addresses = append(n.Status.Addresses,
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: internalIP})
	}
	for _, o := range opts {
		o(&n)
	}
	return n
}

func withExternalIP(ip string) nodeOpt {
	return func(n *corev1.Node) {
		n.Status.Addresses = append(n.Status.Addresses,
			corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: ip})
	}
}

func withInternalIP(ip string) nodeOpt {
	return func(n *corev1.Node) {
		n.Status.Addresses = append(n.Status.Addresses,
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip})
	}
}

func withReadyCondition(status corev1.ConditionStatus) nodeOpt {
	return func(n *corev1.Node) {
		n.Status.Conditions = append(n.Status.Conditions,
			corev1.NodeCondition{Type: corev1.NodeReady, Status: status})
	}
}

func cordoned() nodeOpt {
	return func(n *corev1.Node) { n.Spec.Unschedulable = true }
}

// --- helpers to inspect results -----------------------------------------

func endpointByNode(t *testing.T, eps []discoveryv1.Endpoint, nodeName string) discoveryv1.Endpoint {
	t.Helper()
	for _, ep := range eps {
		if ep.NodeName != nil && *ep.NodeName == nodeName {
			return ep
		}
	}
	t.Fatalf("no endpoint found for node %q in %+v", nodeName, eps)
	return discoveryv1.Endpoint{}
}

func boolPtrVal(t *testing.T, p *bool) bool {
	t.Helper()
	if p == nil {
		t.Fatal("expected non-nil bool pointer")
	}
	return *p
}

// --- BuildEndpointSlices: node selection / skipping ----------------------

func TestBuildEndpointSlices_NoNodes_ReturnsNil(t *testing.T) {
	got := endpoints.BuildEndpointSlices(nil, testConfig())
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestBuildEndpointSlices_NodeWithoutInternalIP_Skipped(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("no-ip", "", withReadyCondition(corev1.ConditionTrue)),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if got != nil {
		t.Fatalf("expected nil when no node has a usable InternalIP, got %#v", got)
	}
}

func TestBuildEndpointSlices_NodeWithOnlyExternalIP_Skipped(t *testing.T) {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "external-only"}}
	withExternalIP("203.0.113.1")(&n)
	withReadyCondition(corev1.ConditionTrue)(&n)

	cfg := testConfig()
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, []corev1.Node{n}), cfg)
	if got != nil {
		t.Fatalf("expected nil, node has no InternalIP address, got %#v", got)
	}
}

func TestBuildEndpointSlices_MixOfUsableAndUnusableNodes_OnlyUsableIncluded(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("has-ip", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("no-ip", "", withReadyCondition(corev1.ConditionTrue)),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 slice, got %d", len(got))
	}
	if len(got[0].Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint (unusable node skipped), got %d: %+v", len(got[0].Endpoints), got[0].Endpoints)
	}
	if *got[0].Endpoints[0].NodeName != "has-ip" {
		t.Fatalf("expected surviving endpoint to be has-ip, got %q", *got[0].Endpoints[0].NodeName)
	}
}

// --- BuildEndpointSlices: readiness classification ------------------------

func TestBuildEndpointSlices_ReadyNode_MarkedReadyAndServing(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	ep := endpointByNode(t, got[0].Endpoints, "n1")
	if !boolPtrVal(t, ep.Conditions.Ready) || !boolPtrVal(t, ep.Conditions.Serving) {
		t.Fatalf("expected Ready=true, Serving=true, got Ready=%v Serving=%v",
			ep.Conditions.Ready, ep.Conditions.Serving)
	}
	if ep.Addresses[0] != "10.0.0.1" {
		t.Fatalf("expected address 10.0.0.1, got %v", ep.Addresses)
	}
}

func TestBuildEndpointSlices_CordonedNode_StaysButNotReady(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("cordoned", "10.0.0.2", withReadyCondition(corev1.ConditionTrue), cordoned()),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got[0].Endpoints) != 1 {
		t.Fatalf("cordoned node must stay in the endpoint list, got %d endpoints", len(got[0].Endpoints))
	}
	ep := endpointByNode(t, got[0].Endpoints, "cordoned")
	if boolPtrVal(t, ep.Conditions.Ready) || boolPtrVal(t, ep.Conditions.Serving) {
		t.Fatalf("cordoned node must be Ready=false, Serving=false, got Ready=%v Serving=%v",
			*ep.Conditions.Ready, *ep.Conditions.Serving)
	}
}

func TestBuildEndpointSlices_NotReadyCondition_StaysButNotReady(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("notready", "10.0.0.3", withReadyCondition(corev1.ConditionFalse)),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	ep := endpointByNode(t, got[0].Endpoints, "notready")
	if boolPtrVal(t, ep.Conditions.Ready) {
		t.Fatal("expected Ready=false for a node reporting NodeReady=False")
	}
}

func TestBuildEndpointSlices_UnknownReadyCondition_TreatedNotReady(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("unknown", "10.0.0.4", withReadyCondition(corev1.ConditionUnknown)),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	ep := endpointByNode(t, got[0].Endpoints, "unknown")
	if boolPtrVal(t, ep.Conditions.Ready) {
		t.Fatal("expected Ready=false when NodeReady condition is Unknown (only True counts as ready)")
	}
}

func TestBuildEndpointSlices_MissingReadyCondition_TreatedNotReady(t *testing.T) {
	cfg := testConfig()
	// No NodeReady condition set at all (e.g. brand-new node still joining).
	nodes := []corev1.Node{node("bare", "10.0.0.5")}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got[0].Endpoints) != 1 {
		t.Fatalf("node missing NodeReady condition must still be included, got %d endpoints", len(got[0].Endpoints))
	}
	ep := endpointByNode(t, got[0].Endpoints, "bare")
	if boolPtrVal(t, ep.Conditions.Ready) {
		t.Fatal("expected Ready=false when NodeReady condition is entirely absent")
	}
}

func TestBuildEndpointSlices_CordonedAndNotReady_StillIncludedOnce(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("both", "10.0.0.6", withReadyCondition(corev1.ConditionFalse), cordoned()),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got[0].Endpoints) != 1 {
		t.Fatalf("expected exactly 1 endpoint for a node that is both cordoned and not-ready, got %d",
			len(got[0].Endpoints))
	}
}

// --- BuildEndpointSlices: multi-node / multi-service shape ----------------

func TestBuildEndpointSlices_MultipleNodes_SingleSlicePerServiceWithAllEndpoints(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("n2", "10.0.0.2", withReadyCondition(corev1.ConditionTrue)),
		node("n3", "10.0.0.3", withReadyCondition(corev1.ConditionFalse)),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 slice for 1 configured service, got %d", len(got))
	}
	if len(got[0].Endpoints) != 3 {
		t.Fatalf("expected all 3 nodes folded into one slice's endpoint list, got %d", len(got[0].Endpoints))
	}
}

func TestBuildEndpointSlices_MultipleServices_OneSlicePerService(t *testing.T) {
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
	)
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 slices for 2 configured services, got %d", len(got))
	}
	names := map[string]bool{got[0].Name: true, got[1].Name: true}
	if !names["svc-a-metrics"] || !names["svc-b-metrics"] {
		t.Fatalf("expected slice names svc-a-metrics and svc-b-metrics, got %v", names)
	}
	for _, s := range got {
		if len(s.Endpoints) != 1 {
			t.Fatalf("slice %s: expected 1 endpoint, got %d", s.Name, len(s.Endpoints))
		}
	}
}

func TestBuildEndpointSlices_DifferentNodeSetsPerService_NoCrossContamination(t *testing.T) {
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

	got := endpoints.BuildEndpointSlices(nodesByService, cfg)
	byName := map[string]discoveryv1.EndpointSlice{}
	for _, s := range got {
		byName[s.Name] = s
	}

	sched, ok := byName["kube-scheduler-metrics"]
	if !ok {
		t.Fatal("missing kube-scheduler-metrics slice")
	}
	if len(sched.Endpoints) != 1 || *sched.Endpoints[0].NodeName != "cp" {
		t.Fatalf("expected kube-scheduler-metrics to contain only the cp node, got %+v", sched.Endpoints)
	}

	proxy, ok := byName["kube-proxy-metrics"]
	if !ok {
		t.Fatal("missing kube-proxy-metrics slice")
	}
	if len(proxy.Endpoints) != 2 {
		t.Fatalf("expected kube-proxy-metrics to contain both nodes, got %d: %+v", len(proxy.Endpoints), proxy.Endpoints)
	}
}

// One service having zero qualifying nodes must not suppress the others'
// slices (all-or-nothing -> per-service skip).
func TestBuildEndpointSlices_ServiceWithNoQualifyingNodes_OnlyThatServiceSkipped(t *testing.T) {
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
	)
	nodesByService := map[string][]corev1.Node{
		"svc-a": {node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))},
		// svc-b: absent -- e.g. its selector currently matches zero nodes.
	}

	got := endpoints.BuildEndpointSlices(nodesByService, cfg)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 slice (svc-b has no qualifying nodes), got %d: %+v", len(got), got)
	}
	if got[0].Name != "svc-a-metrics" {
		t.Fatalf("expected the surviving slice to be svc-a-metrics, got %q", got[0].Name)
	}
}

func TestBuildEndpointSlices_PortsMatchDefaultServices(t *testing.T) {
	cfg := testConfig(config.DefaultServices...)
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)

	want := map[string]struct {
		port        int32
		protocol    corev1.Protocol
		appProtocol string
		portName    string
	}{
		"kube-scheduler-metrics":          {10259, corev1.ProtocolTCP, "https", "https-metrics"},
		"kube-controller-manager-metrics": {10257, corev1.ProtocolTCP, "https", "https-metrics"},
		"kube-proxy-metrics":              {10249, corev1.ProtocolTCP, "http", "http-metrics"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d slices, got %d", len(want), len(got))
	}
	for _, s := range got {
		w, ok := want[s.Name]
		if !ok {
			t.Fatalf("unexpected slice name %q", s.Name)
		}
		if len(s.Ports) != 1 {
			t.Fatalf("%s: expected exactly 1 port, got %d", s.Name, len(s.Ports))
		}
		p := s.Ports[0]
		if p.Port == nil || *p.Port != w.port {
			t.Errorf("%s: expected port %d, got %v", s.Name, w.port, p.Port)
		}
		if p.Protocol == nil || *p.Protocol != w.protocol {
			t.Errorf("%s: expected protocol %v, got %v", s.Name, w.protocol, p.Protocol)
		}
		if p.AppProtocol == nil || *p.AppProtocol != w.appProtocol {
			t.Errorf("%s: expected appProtocol %v, got %v", s.Name, w.appProtocol, p.AppProtocol)
		}
		// PortName must match kube-prometheus's stock ServiceMonitor port
		// selectors exactly (see config.Service.PortName doc) -- a silent
		// rename here breaks scraping without any error anywhere.
		if p.Name == nil || *p.Name != w.portName {
			gotName := "<nil>"
			if p.Name != nil {
				gotName = *p.Name
			}
			t.Errorf("%s: expected port name %q, got %q", s.Name, w.portName, gotName)
		}
	}
}

// Guards against a "fix" that sets kube-proxy's NodeSelector back to nil,
// which would silently drop every agent node from its EndpointSlice.
func TestDefaultServices_KubeProxyUsesAllNodesSelector(t *testing.T) {
	want := map[string]bool{
		"kube-scheduler":          false, // nil -> inherits Config.NodeSelector
		"kube-controller-manager": false,
		"kube-proxy":              true, // non-nil (empty) -> all nodes
	}
	if len(config.DefaultServices) != len(want) {
		t.Fatalf("expected %d default services, got %d", len(want), len(config.DefaultServices))
	}
	for _, svc := range config.DefaultServices {
		wantOverride, ok := want[svc.Name]
		if !ok {
			t.Fatalf("unexpected default service %q", svc.Name)
		}
		gotOverride := svc.NodeSelector != nil
		if gotOverride != wantOverride {
			t.Errorf("%s: expected NodeSelector override=%v, got %v (value=%#v)", svc.Name, wantOverride, gotOverride, svc.NodeSelector)
		}
		if svc.Name == "kube-proxy" && len(svc.NodeSelector) != 0 {
			t.Errorf("kube-proxy: expected an empty NodeSelector (matches all nodes), got %#v", svc.NodeSelector)
		}
	}
}

func TestBuildEndpointSlices_PortNamePropagatedFromService(t *testing.T) {
	cfg := testConfig(config.Service{Name: "custom", PortName: "custom-port-name", Port: 4242, Protocol: corev1.ProtocolTCP, AppProtocol: "http"})
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	p := got[0].Ports[0]
	if p.Name == nil || *p.Name != "custom-port-name" {
		t.Fatalf("expected port name to come from Service.PortName, got %v", p.Name)
	}
}

func TestBuildEndpointSlices_LabelsAndNamespace(t *testing.T) {
	cfg := testConfig(config.Service{Name: "kube-scheduler", Port: 10259, Protocol: corev1.ProtocolTCP, AppProtocol: "https"})
	cfg.Namespace = "custom-ns"
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}

	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	s := got[0]
	if s.Namespace != "custom-ns" {
		t.Errorf("expected namespace custom-ns, got %q", s.Namespace)
	}
	if s.Labels[discoveryv1.LabelServiceName] != "kube-scheduler" {
		t.Errorf("expected %s label = kube-scheduler, got %q", discoveryv1.LabelServiceName, s.Labels[discoveryv1.LabelServiceName])
	}
	if s.Labels[discoveryv1.LabelManagedBy] != endpoints.ManagedByValue {
		t.Errorf("expected %s label = %s, got %q", discoveryv1.LabelManagedBy, endpoints.ManagedByValue, s.Labels[discoveryv1.LabelManagedBy])
	}
	if s.AddressType != discoveryv1.AddressTypeIPv4 {
		t.Errorf("expected AddressType IPv4, got %v", s.AddressType)
	}
}

// --- Edge cases around IP address selection -------------------------------

func TestBuildEndpointSlices_MultipleInternalIPs_FirstOneWins(t *testing.T) {
	cfg := testConfig()
	// Malformed/unusual but not impossible: more than one InternalIP address.
	// Document current first-match behavior so a silent change is caught.
	n := node("dual", "10.0.0.100", withInternalIP("10.0.0.200"), withReadyCondition(corev1.ConditionTrue))
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, []corev1.Node{n}), cfg)
	ep := endpointByNode(t, got[0].Endpoints, "dual")
	if ep.Addresses[0] != "10.0.0.100" {
		t.Errorf("expected first InternalIP (10.0.0.100) to win, got %v", ep.Addresses)
	}
}

// --- Dual-stack: nodes are grouped into per-address-family slices ---------
// (discovery.k8s.io/v1 requires every address in an EndpointSlice to match
// its single declared AddressType, so a mixed IPv4/IPv6 node set must split
// into separate slices rather than mislabeling one family as the other.)

func TestBuildEndpointSlices_IPv6OnlyNode_ProducesIPv6Slice(t *testing.T) {
	cfg := testConfig()
	n := node("ipv6-node", "2001:db8::1", withReadyCondition(corev1.ConditionTrue))
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, []corev1.Node{n}), cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 slice for an IPv6-only node set, got %d", len(got))
	}
	if got[0].AddressType != discoveryv1.AddressTypeIPv6 {
		t.Fatalf("expected AddressType IPv6, got %v", got[0].AddressType)
	}
	if got[0].Name != "test-svc-metrics-ipv6" {
		t.Fatalf("expected IPv6 slice name to carry an -ipv6 suffix, got %q", got[0].Name)
	}
	if got[0].Endpoints[0].Addresses[0] != "2001:db8::1" {
		t.Fatalf("expected the IPv6 address to be preserved, got %v", got[0].Endpoints[0].Addresses)
	}
}

func TestBuildEndpointSlices_MixedIPv4AndIPv6Nodes_SplitIntoTwoSlicesPerService(t *testing.T) {
	cfg := testConfig()
	nodes := []corev1.Node{
		node("v4", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("v6", "2001:db8::1", withReadyCondition(corev1.ConditionTrue)),
	}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 slices (one per address family) for 1 service, got %d", len(got))
	}

	byFamily := map[discoveryv1.AddressType]discoveryv1.EndpointSlice{}
	for _, s := range got {
		byFamily[s.AddressType] = s
	}

	v4, ok := byFamily[discoveryv1.AddressTypeIPv4]
	if !ok {
		t.Fatal("missing IPv4 slice")
	}
	if len(v4.Endpoints) != 1 || *v4.Endpoints[0].NodeName != "v4" {
		t.Fatalf("expected IPv4 slice to contain only the v4 node, got %+v", v4.Endpoints)
	}
	if v4.Name != "test-svc-metrics" {
		t.Errorf("expected IPv4 slice to keep the unsuffixed name, got %q", v4.Name)
	}

	v6, ok := byFamily[discoveryv1.AddressTypeIPv6]
	if !ok {
		t.Fatal("missing IPv6 slice")
	}
	if len(v6.Endpoints) != 1 || *v6.Endpoints[0].NodeName != "v6" {
		t.Fatalf("expected IPv6 slice to contain only the v6 node, got %+v", v6.Endpoints)
	}
	if v6.Name != "test-svc-metrics-ipv6" {
		t.Errorf("expected IPv6 slice name to carry the -ipv6 suffix, got %q", v6.Name)
	}
}

func TestBuildEndpointSlices_MultipleServicesWithMixedFamilies_OneSlicePerServicePerFamily(t *testing.T) {
	nodes := []corev1.Node{
		node("v4", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("v6", "2001:db8::1", withReadyCondition(corev1.ConditionTrue)),
	}
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
	)
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)
	if len(got) != 4 { // 2 services x 2 families
		t.Fatalf("expected 4 slices (2 services x 2 families), got %d", len(got))
	}
}

func TestBuildEndpointSlices_UnparseableInternalIP_DefaultsToIPv4(t *testing.T) {
	cfg := testConfig()
	// Malformed but not empty -- internalIP() only checks presence, not
	// validity, so addressFamily must degrade gracefully rather than panic.
	n := node("garbage-ip", "not-an-ip-address", withReadyCondition(corev1.ConditionTrue))
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, []corev1.Node{n}), cfg)
	if len(got) != 1 || got[0].AddressType != discoveryv1.AddressTypeIPv4 {
		t.Fatalf("expected a single IPv4-defaulted slice for an unparseable address, got %+v", got)
	}
}

// --- Determinism / idempotency --------------------------------------------

func TestBuildEndpointSlices_Idempotent(t *testing.T) {
	cfg := testConfig(config.DefaultServices...)
	nodes := []corev1.Node{
		node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue)),
		node("n2", "10.0.0.2", withReadyCondition(corev1.ConditionFalse)),
	}
	nbs := nodesFor(cfg, nodes)

	first := endpoints.BuildEndpointSlices(nbs, cfg)
	second := endpoints.BuildEndpointSlices(nbs, cfg)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected identical output for identical input (required for CreateOrUpdate to no-op):\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

func TestBuildEndpointSlices_PortPointersNotAliasedAcrossServices(t *testing.T) {
	cfg := testConfig(
		config.Service{Name: "svc-a", Port: 1111, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		config.Service{Name: "svc-b", Port: 2222, Protocol: corev1.ProtocolUDP, AppProtocol: "https"},
	)
	nodes := []corev1.Node{node("n1", "10.0.0.1", withReadyCondition(corev1.ConditionTrue))}
	got := endpoints.BuildEndpointSlices(nodesFor(cfg, nodes), cfg)

	byName := map[string]discoveryv1.EndpointSlice{}
	for _, s := range got {
		byName[s.Name] = s
	}
	a := byName["svc-a-metrics"].Ports[0]
	b := byName["svc-b-metrics"].Ports[0]
	if *a.Port != 1111 || *b.Port != 2222 {
		t.Fatalf("service ports bled into each other: a=%d b=%d (loop-variable aliasing regression)", *a.Port, *b.Port)
	}
	if *a.Protocol != corev1.ProtocolTCP || *b.Protocol != corev1.ProtocolUDP {
		t.Fatalf("service protocols bled into each other: a=%v b=%v", *a.Protocol, *b.Protocol)
	}
	if *a.AppProtocol != "http" || *b.AppProtocol != "https" {
		t.Fatalf("service appProtocols bled into each other: a=%v b=%v", *a.AppProtocol, *b.AppProtocol)
	}
}
