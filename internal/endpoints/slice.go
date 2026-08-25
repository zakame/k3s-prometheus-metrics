// Package endpoints contains pure functions that turn a set of
// control-plane Nodes into discovery.k8s.io/v1 EndpointSlice objects (and,
// optionally, legacy v1 Endpoints -- see legacy.go). These functions take
// no client and make no API calls, so they are unit-testable without a
// fake or envtest client.
package endpoints

import (
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

// BuildEndpointSlices turns nodes into one discovery.k8s.io/v1
// EndpointSlice per service in cfg.Services, in cfg.Namespace. Nodes
// without a usable InternalIP address are skipped entirely. Returns nil if
// no node yields a usable address.
func BuildEndpointSlices(nodes []corev1.Node, cfg config.Config) []discoveryv1.EndpointSlice {
	eps := endpointsFromNodes(nodes)
	if len(eps) == 0 {
		return nil
	}

	slices := make([]discoveryv1.EndpointSlice, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		port := svc.Port
		protocol := svc.Protocol
		appProtocol := svc.AppProtocol
		portName := svc.PortName

		slices = append(slices, discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svc.Name + sliceNameSuffix,
				Namespace: cfg.Namespace,
				Labels: map[string]string{
					discoveryv1.LabelServiceName: svc.Name,
					discoveryv1.LabelManagedBy:   ManagedByValue,
				},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints:   eps,
			Ports: []discoveryv1.EndpointPort{{
				Name:        &portName,
				Port:        &port,
				Protocol:    &protocol,
				AppProtocol: &appProtocol,
			}},
		})
	}
	return slices
}

func endpointsFromNodes(nodes []corev1.Node) []discoveryv1.Endpoint {
	eps := make([]discoveryv1.Endpoint, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		addr, ok := internalIP(node)
		if !ok {
			continue
		}

		ready := isReady(node)
		nodeName := node.Name
		eps = append(eps, discoveryv1.Endpoint{
			Addresses: []string{addr},
			Conditions: discoveryv1.EndpointConditions{
				Ready:   &ready,
				Serving: &ready,
			},
			NodeName: &nodeName,
		})
	}
	return eps
}

func internalIP(node *corev1.Node) (string, bool) {
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address, true
		}
	}
	return "", false
}

// isReady reports whether node should be marked Ready/Serving in its
// endpoint conditions. Cordoned (Unschedulable) or NotReady nodes stay in
// the EndpointSlice -- consistent with how Kubernetes' own EndpointSlice
// controller handles transiently-unready pods -- but with Ready=false, so
// Prometheus service discovery reflects them as down rather than the
// target silently disappearing.
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
