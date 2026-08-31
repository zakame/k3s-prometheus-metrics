package endpoints

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// BuildEndpoints turns nodesByService (see BuildEndpointSlices) into one
// legacy v1 Endpoints object per service, for Kubernetes clusters older
// than 1.33. Not-ready nodes go in NotReadyAddresses, which most legacy
// consumers don't scrape by default.
func BuildEndpoints(nodesByService map[string][]corev1.Node, cfg config.Config) []corev1.Endpoints { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	var all []corev1.Endpoints //nolint:staticcheck
	for _, svc := range cfg.Services {
		ready, notReady := splitByReadiness(nodesByService[svc.Name])
		if len(ready) == 0 && len(notReady) == 0 {
			continue
		}

		all = append(all, corev1.Endpoints{ //nolint:staticcheck
			Name:      svc.Name,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName:   svc.Name,
				"app.kubernetes.io/managed-by": ManagedByValue,
				// Without this, kube-controller-manager's
				// EndpointSliceMirroring controller duplicates this
				// into a second, conflicting EndpointSlice.
				discoveryv1.LabelSkipMirror: "true",
			},
			Subsets: []corev1.EndpointSubset{{ //nolint:staticcheck
				Addresses:         ready,
				NotReadyAddresses: notReady,
				Ports: []corev1.EndpointPort{{
					Name:        svc.PortName,
					Port:        svc.Port,
					Protocol:    svc.Protocol,
					AppProtocol: &svc.AppProtocol,
				}},
			}},
		})
	}
	return all
}

// splitByReadiness splits nodes into ready/not-ready v1 EndpointAddresses,
// per the same isReady rule EndpointSlice uses.
func splitByReadiness(nodes []corev1.Node) (ready, notReady []corev1.EndpointAddress) {
	for i := range nodes {
		node := &nodes[i]
		addr, ok := internalIP(node)
		if !ok {
			continue
		}

		nodeName := node.Name
		ea := corev1.EndpointAddress{IP: addr, NodeName: &nodeName}
		if isReady(node) {
			ready = append(ready, ea)
		} else {
			notReady = append(notReady, ea)
		}
	}
	return ready, notReady
}
