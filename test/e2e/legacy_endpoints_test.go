//go:build e2e

package e2e

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// TestLegacyEndpointsEndToEnd verifies the --write-legacy-endpoints path on
// a real k3d cluster: for every config.DefaultServices entry, a legacy v1
// Endpoints object exists alongside the Service/EndpointSlice pair
// TestControlPlaneServicesEndToEnd already covers, with the configured port
// and the cluster's single node as a ready address.
//
// Only deploy/e2e-legacy's overlay sets --write-legacy-endpoints, so this
// test is gated on E2E_LEGACY_ENDPOINTS=true, which the CI job running that
// overlay sets before invoking `go test -tags e2e`. The default e2e job
// (deploy/e2e, no flag) skips this test harmlessly.
func TestLegacyEndpointsEndToEnd(t *testing.T) {
	if os.Getenv("E2E_LEGACY_ENDPOINTS") != "true" {
		t.Skip("skipping: E2E_LEGACY_ENDPOINTS != true (only set by the CI job running deploy/e2e-legacy with --write-legacy-endpoints)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	node := theNode(t, ctx)
	nodeIP := nodeInternalIP(t, node)

	for _, svc := range config.DefaultServices {
		t.Run(svc.Name, func(t *testing.T) {
			ep := eventuallyEndpoints(t, ctx, svc.Name)

			if ep.Labels[discoveryv1.LabelServiceName] != svc.Name {
				t.Errorf("expected %s label %q, got %q", discoveryv1.LabelServiceName, svc.Name, ep.Labels[discoveryv1.LabelServiceName])
			}
			if ep.Labels[discoveryv1.LabelSkipMirror] != "true" {
				t.Errorf("expected %s=true, got %q", discoveryv1.LabelSkipMirror, ep.Labels[discoveryv1.LabelSkipMirror])
			}
			if ep.Labels["app.kubernetes.io/managed-by"] != endpoints.ManagedByValue {
				t.Errorf("expected managed-by label %q, got %q", endpoints.ManagedByValue, ep.Labels["app.kubernetes.io/managed-by"])
			}

			if len(ep.Subsets) != 1 { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
				t.Fatalf("expected 1 Subset, got %+v", ep.Subsets)
			}
			subset := ep.Subsets[0] //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
			if len(subset.Ports) != 1 {
				t.Fatalf("expected 1 Endpoints port, got %+v", subset.Ports)
			}
			assertEndpointsPort(t, subset.Ports[0], svc)

			if len(subset.Addresses) != 1 { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
				t.Fatalf("expected 1 ready address for the cluster's single node, got %d: %+v", len(subset.Addresses), subset.Addresses)
			}
			addr := subset.Addresses[0] //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
			if addr.IP != nodeIP {
				t.Errorf("expected address IP %q, got %q", nodeIP, addr.IP)
			}
			if addr.NodeName == nil || *addr.NodeName != node.Name {
				t.Errorf("expected address NodeName %q, got %v", node.Name, addr.NodeName)
			}
		})
	}
}

func assertEndpointsPort(t *testing.T, port corev1.EndpointPort, svc config.Service) {
	t.Helper()
	if port.Name != svc.PortName {
		t.Errorf("Endpoints port name: got %q, want %q", port.Name, svc.PortName)
	}
	if port.Port != svc.Port {
		t.Errorf("Endpoints port: got %d, want %d", port.Port, svc.Port)
	}
	if port.Protocol != svc.Protocol {
		t.Errorf("Endpoints protocol: got %q, want %q", port.Protocol, svc.Protocol)
	}
	if port.AppProtocol == nil || *port.AppProtocol != svc.AppProtocol {
		t.Errorf("Endpoints appProtocol: got %v, want %q", port.AppProtocol, svc.AppProtocol)
	}
}
