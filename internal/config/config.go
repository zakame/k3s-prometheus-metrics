// Package config holds the static table of k3s-bundled control-plane
// components this controller exposes, and the runtime configuration
// derived from CLI flags in cmd/k3s-prometheus-metrics.
package config

import corev1 "k8s.io/api/core/v1"

// Service describes a k3s-bundled control-plane component whose metrics
// endpoint this controller exposes to Prometheus.
type Service struct {
	// Name is the EndpointSlice's "kubernetes.io/service-name" label value,
	// and the name of the selector-less Service this controller manages
	// for Prometheus to scrape.
	Name string
	// PortName names the metrics port on the Service, EndpointSlice, and
	// legacy Endpoints. kube-scheduler and kube-controller-manager need
	// "https-metrics" to match kube-prometheus's stock
	// kubernetesControlPlane-serviceMonitorKube{Scheduler,ControllerManager}.yaml
	// ServiceMonitors.
	PortName string
	// Port is the metrics port exposed on each control-plane node's host
	// network, once the cluster admin has bound it to a non-loopback
	// address via k3s server/agent flags -- see
	// https://github.com/k3s-io/k3s/issues/3619.
	Port int32
	// Protocol is the transport protocol for Port.
	Protocol corev1.Protocol
	// AppProtocol hints at HTTPS vs HTTP metrics endpoints to scrapers.
	AppProtocol string
}

// DefaultServices is the k3s-bundled control-plane components this
// controller watches by default. Ports match upstream Kubernetes'
// --secure-port defaults; k3s only changes the default bind address, not
// the port numbers. kube-proxy has no upstream kube-prometheus
// ServiceMonitor, so "http-metrics" is this project's own convention.
var DefaultServices = []Service{
	{Name: "kube-scheduler", PortName: "https-metrics", Port: 10259, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
	{Name: "kube-controller-manager", PortName: "https-metrics", Port: 10257, Protocol: corev1.ProtocolTCP, AppProtocol: "https"},
	{Name: "kube-proxy", PortName: "http-metrics", Port: 10249, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
}

// ControlPlaneNodeSelector is the standard label key for control-plane
// nodes, shared by kubeadm and k3s. The value differs: kubeadm leaves it
// empty, k3s sets it to "true".
const ControlPlaneNodeSelector = "node-role.kubernetes.io/control-plane"

// Config holds the controller's runtime configuration, populated from CLI
// flags in cmd/k3s-prometheus-metrics.
type Config struct {
	// Namespace is where Service, EndpointSlice, and (if enabled) Endpoints
	// objects are created. Defaults to "kube-system" to match where
	// kube-prometheus and kube-prometheus-stack expect control-plane
	// Services to live; independent of the controller Deployment's own
	// namespace.
	Namespace string
	// NodeSelector restricts which nodes are treated as control-plane
	// nodes and included as endpoints.
	NodeSelector map[string]string
	// WriteLegacyEndpoints also creates/updates v1 Endpoints objects
	// alongside EndpointSlices, for Kubernetes clusters older than 1.33.
	WriteLegacyEndpoints bool
	// Services is the set of control-plane components to expose.
	Services []Service
}
