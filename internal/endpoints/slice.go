// Package endpoints builds the Service, EndpointSlice, and legacy
// Endpoints objects this controller manages, as pure functions with no
// client or API calls.
package endpoints

import (
	"net"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

const (
	// ManagedByValue marks EndpointSlice/Endpoints objects owned by this
	// controller, distinct from Kubernetes' own EndpointSlice mirroring
	// controller (which uses "endpointslice-controller.k8s.io") so neither
	// adopts or overwrites the other's objects.
	ManagedByValue = "k3s-prometheus-metrics"

	sliceNameSuffix = "-metrics"
)

// BuildEndpointSlices turns nodes into EndpointSlice objects in
// cfg.Namespace: one per (service, address family) pair, since an
// EndpointSlice's AddressType is fixed and can't mix IPv4/IPv6. Nodes
// without a usable InternalIP are skipped. Returns nil if none qualify.
func BuildEndpointSlices(nodes []corev1.Node, cfg config.Config) []discoveryv1.EndpointSlice {
	byFamily := endpointsFromNodes(nodes)
	if len(byFamily) == 0 {
		return nil
	}

	families := make([]discoveryv1.AddressType, 0, len(byFamily))
	for family := range byFamily {
		families = append(families, family)
	}
	sort.Slice(families, func(i, j int) bool { return families[i] < families[j] })

	slices := make([]discoveryv1.EndpointSlice, 0, len(cfg.Services)*len(families))
	for _, svc := range cfg.Services {
		port := svc.Port
		protocol := svc.Protocol
		appProtocol := svc.AppProtocol
		portName := svc.PortName

		for _, family := range families {
			name := svc.Name + sliceNameSuffix
			if family == discoveryv1.AddressTypeIPv6 {
				name += "-ipv6"
			}

			slices = append(slices, discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: cfg.Namespace,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: svc.Name,
						discoveryv1.LabelManagedBy:   ManagedByValue,
					},
				},
				AddressType: family,
				Endpoints:   byFamily[family],
				Ports: []discoveryv1.EndpointPort{{
					Name:        &portName,
					Port:        &port,
					Protocol:    &protocol,
					AppProtocol: &appProtocol,
				}},
			})
		}
	}
	return slices
}

// endpointsFromNodes groups nodes' endpoints by their InternalIP's address
// family, since discovery.k8s.io/v1 requires every address in an
// EndpointSlice to match its single declared AddressType.
func endpointsFromNodes(nodes []corev1.Node) map[discoveryv1.AddressType][]discoveryv1.Endpoint {
	byFamily := map[discoveryv1.AddressType][]discoveryv1.Endpoint{}
	for i := range nodes {
		node := &nodes[i]
		addr, ok := internalIP(node)
		if !ok {
			continue
		}

		ready := isReady(node)
		nodeName := node.Name
		family := addressFamily(addr)
		byFamily[family] = append(byFamily[family], discoveryv1.Endpoint{
			Addresses: []string{addr},
			Conditions: discoveryv1.EndpointConditions{
				Ready:   &ready,
				Serving: &ready,
			},
			NodeName: &nodeName,
		})
	}
	return byFamily
}

func internalIP(node *corev1.Node) (string, bool) {
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address, true
		}
	}
	return "", false
}

// addressFamily reports the discovery.k8s.io/v1 AddressType matching addr's
// actual IP family, rather than assuming IPv4.
func addressFamily(addr string) discoveryv1.AddressType {
	if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
		return discoveryv1.AddressTypeIPv6
	}
	return discoveryv1.AddressTypeIPv4
}

// isReady reports whether node should be marked Ready/Serving. Cordoned or
// NotReady nodes stay in the EndpointSlice with Ready=false rather than
// being dropped, so Prometheus shows them as down instead of missing.
func isReady(node *corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
