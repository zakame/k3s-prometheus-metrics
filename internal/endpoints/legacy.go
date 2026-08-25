package endpoints

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// BuildEndpoints turns nodes into one legacy v1 Endpoints object per
// service in cfg.Services, in cfg.Namespace, for Kubernetes clusters older
// than 1.33 that may not yet universally consume EndpointSlices. Only
// nodes considered ready (see isReady) are included: unlike EndpointSlice,
// v1 Endpoints has no per-address Ready condition, so a not-ready node can
// only be represented via NotReadyAddresses, which most legacy consumers
// (including older Prometheus relabeling configs) do not scrape by
// default -- keeping ready and not-ready nodes in the same subset split
// mirrors that expectation.
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
					"kubernetes.io/service-name":   svc.Name,
					"app.kubernetes.io/managed-by": ManagedByValue,
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
