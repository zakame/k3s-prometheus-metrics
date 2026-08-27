//go:build e2e

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// TestControlPlaneServicesEndToEnd verifies the whole system converges on a
// real k3d cluster: for every config.DefaultServices entry, the
// selector-less Service and its EndpointSlice exist with the configured
// port, and the cluster's single node -- both the control-plane and the
// only kube-proxy target on a default k3d cluster -- appears as a ready
// endpoint in each.
func TestControlPlaneServicesEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	node := theNode(t, ctx)
	nodeIP := nodeInternalIP(t, node)

	for _, svc := range config.DefaultServices {
		t.Run(svc.Name, func(t *testing.T) {
			gotSvc := eventuallyService(t, ctx, svc.Name)
			if len(gotSvc.Spec.Selector) != 0 {
				t.Errorf("expected selector-less Service, got selector %+v", gotSvc.Spec.Selector)
			}
			if len(gotSvc.Spec.Ports) != 1 {
				t.Fatalf("expected 1 Service port, got %+v", gotSvc.Spec.Ports)
			}
			assertServicePort(t, gotSvc.Spec.Ports[0], svc)

			es := eventuallyEndpointSlice(t, ctx, svc.Name+"-metrics")
			if es.Labels[discoveryv1.LabelManagedBy] != endpoints.ManagedByValue {
				t.Errorf("expected managed-by label %q, got %q", endpoints.ManagedByValue, es.Labels[discoveryv1.LabelManagedBy])
			}
			if len(es.Ports) != 1 {
				t.Fatalf("expected 1 EndpointSlice port, got %+v", es.Ports)
			}
			assertEndpointSlicePort(t, es.Ports[0], svc)

			if len(es.Endpoints) != 1 {
				t.Fatalf("expected 1 endpoint for the cluster's single node, got %d: %+v", len(es.Endpoints), es.Endpoints)
			}
			ep := es.Endpoints[0]
			if len(ep.Addresses) != 1 || ep.Addresses[0] != nodeIP {
				t.Errorf("expected endpoint address %q, got %+v", nodeIP, ep.Addresses)
			}
			if ep.NodeName == nil || *ep.NodeName != node.Name {
				t.Errorf("expected endpoint NodeName %q, got %v", node.Name, ep.NodeName)
			}
			if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
				t.Errorf("expected endpoint to be Ready")
			}
		})
	}
}

func assertServicePort(t *testing.T, port corev1.ServicePort, svc config.Service) {
	t.Helper()
	if port.Name != svc.PortName {
		t.Errorf("Service port name: got %q, want %q", port.Name, svc.PortName)
	}
	if port.Port != svc.Port {
		t.Errorf("Service port: got %d, want %d", port.Port, svc.Port)
	}
	if port.Protocol != svc.Protocol {
		t.Errorf("Service protocol: got %q, want %q", port.Protocol, svc.Protocol)
	}
	if port.AppProtocol == nil || *port.AppProtocol != svc.AppProtocol {
		t.Errorf("Service appProtocol: got %v, want %q", port.AppProtocol, svc.AppProtocol)
	}
}

func assertEndpointSlicePort(t *testing.T, port discoveryv1.EndpointPort, svc config.Service) {
	t.Helper()
	if port.Name == nil || *port.Name != svc.PortName {
		t.Errorf("EndpointSlice port name: got %v, want %q", port.Name, svc.PortName)
	}
	if port.Port == nil || *port.Port != svc.Port {
		t.Errorf("EndpointSlice port: got %v, want %d", port.Port, svc.Port)
	}
	if port.Protocol == nil || *port.Protocol != svc.Protocol {
		t.Errorf("EndpointSlice protocol: got %v, want %q", port.Protocol, svc.Protocol)
	}
	if port.AppProtocol == nil || *port.AppProtocol != svc.AppProtocol {
		t.Errorf("EndpointSlice appProtocol: got %v, want %q", port.AppProtocol, svc.AppProtocol)
	}
}
