package endpoints

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// BuildEndpoints turns nodes into one legacy v1 Endpoints object per
// service, for Kubernetes clusters older than 1.33. v1 Endpoints has no
// per-address Ready condition, so not-ready nodes go in NotReadyAddresses
// instead, which most legacy consumers don't scrape by default.
func BuildEndpoints(nodes []corev1.Node, cfg config.Config) []corev1.Endpoints { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	var ready, notReady []corev1.EndpointAddress
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
	if len(ready) == 0 && len(notReady) == 0 {
		return nil
	}

	all := make([]corev1.Endpoints, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		all = append(all, corev1.Endpoints{
			ObjectMeta: metav1.ObjectMeta{
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
			},
			Subsets: []corev1.EndpointSubset{{
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
