package endpoints_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

func TestBuildServices_OneServicePerConfigEntry(t *testing.T) {
	cfg := testConfig(
		config.Service{Name: "kube-scheduler", PortName: "https-metrics", Port: 10259, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
		config.Service{Name: "kube-proxy", PortName: "http-metrics", Port: 10249, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
	)

	got := endpoints.BuildServices(cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 services, got %d: %+v", len(got), got)
	}
}

func TestBuildServices_SelectorLessAndHeadless(t *testing.T) {
	cfg := testConfig(config.Service{Name: "kube-scheduler", PortName: "https-metrics", Port: 10259, Protocol: corev1.ProtocolTCP, AppProtocol: "https"})

	got := endpoints.BuildServices(cfg)
	svc := got[0]

	if svc.Spec.Selector != nil {
		t.Errorf("expected a selector-less Service (Prometheus targets come from the controller-managed EndpointSlice/Endpoints, not pod selection), got selector %v", svc.Spec.Selector)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("expected ClusterIP %q (headless), got %q", corev1.ClusterIPNone, svc.Spec.ClusterIP)
	}
}

func TestBuildServices_NameNamespacePortsMatchConfig(t *testing.T) {
	cfg := testConfig(config.Service{Name: "kube-controller-manager", PortName: "https-metrics", Port: 10257, Protocol: corev1.ProtocolTCP, AppProtocol: "https"})
	cfg.Namespace = "kube-system"

	got := endpoints.BuildServices(cfg)
	svc := got[0]

	if svc.Name != "kube-controller-manager" {
		t.Errorf("expected Service name %q, got %q", "kube-controller-manager", svc.Name)
	}
	if svc.Namespace != "kube-system" {
		t.Errorf("expected Service namespace %q, got %q", "kube-system", svc.Namespace)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("expected exactly 1 port, got %d: %+v", len(svc.Spec.Ports), svc.Spec.Ports)
	}
	port := svc.Spec.Ports[0]
	if port.Name != "https-metrics" || port.Port != 10257 || port.Protocol != corev1.ProtocolTCP {
		t.Errorf("expected port {https-metrics 10257 TCP}, got %+v", port)
	}
}

func TestBuildServices_LabelsIdentifyComponentAndOwner(t *testing.T) {
	cfg := testConfig(config.Service{Name: "kube-proxy", PortName: "http-metrics", Port: 10249, Protocol: corev1.ProtocolTCP, AppProtocol: "http"})

	got := endpoints.BuildServices(cfg)
	svc := got[0]

	if svc.Labels["app.kubernetes.io/name"] != "kube-proxy" {
		t.Errorf("expected app.kubernetes.io/name=kube-proxy, got %q", svc.Labels["app.kubernetes.io/name"])
	}
	if svc.Labels["k8s-app"] != "kube-proxy" {
		t.Errorf("expected k8s-app=kube-proxy, got %q", svc.Labels["k8s-app"])
	}
	if svc.Labels["app.kubernetes.io/managed-by"] != endpoints.ManagedByValue {
		t.Errorf("expected app.kubernetes.io/managed-by=%q, got %q", endpoints.ManagedByValue, svc.Labels["app.kubernetes.io/managed-by"])
	}
}

func TestBuildServices_AppProtocolMatchesConfig(t *testing.T) {
	cfg := testConfig(config.Service{Name: "kube-scheduler", PortName: "https-metrics", Port: 10259, Protocol: corev1.ProtocolTCP, AppProtocol: "https"})

	got := endpoints.BuildServices(cfg)
	port := got[0].Spec.Ports[0]
	if port.AppProtocol == nil || *port.AppProtocol != "https" {
		t.Errorf("expected AppProtocol %q, got %v", "https", port.AppProtocol)
	}
}

func TestBuildServices_EmptyConfigServices_ReturnsEmpty(t *testing.T) {
	cfg := config.Config{Namespace: "kube-system", Services: nil}

	got := endpoints.BuildServices(cfg)
	if len(got) != 0 {
		t.Fatalf("expected no services for an empty config.Services, got %d: %+v", len(got), got)
	}
}
