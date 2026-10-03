package endpoints

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// BuildServices returns one selector-less Service per cfg.Services entry,
// for the reconciler to own EndpointSlice/Endpoints against.
func BuildServices(cfg config.Config) []corev1.Service {
	svcs := make([]corev1.Service, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		appProtocol := svc.AppProtocol
		svcs = append(svcs, corev1.Service{
			Name:      svc.Name,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       svc.Name,
				"k8s-app":                      svc.Name,
				"app.kubernetes.io/managed-by": ManagedByValue,
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: corev1.ClusterIPNone,
				Ports: []corev1.ServicePort{{
					Name: svc.PortName,
					Port: svc.Port,
					// Set explicitly: the API server defaults it to Port,
					// and leaving it unset would make every reconcile see
					// a difference and rewrite the Service.
					TargetPort:  intstr.FromInt32(svc.Port),
					Protocol:    svc.Protocol,
					AppProtocol: &appProtocol,
				}},
			},
		})
	}
	return svcs
}
