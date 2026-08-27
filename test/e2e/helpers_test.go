//go:build e2e

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// theNode returns the cluster's single Node. This suite targets a
// single-node k3d cluster (the CI default), where that one node acts as
// both the control-plane and the only kube-proxy target. A differently
// sized cluster fails this fast with an explicit message instead of
// silently asserting the wrong endpoint counts.
func theNode(t *testing.T, ctx context.Context) corev1.Node {
	t.Helper()
	var nodes corev1.NodeList
	if err := k8sClient.List(ctx, &nodes); err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	if len(nodes.Items) != 1 {
		t.Fatalf("this suite assumes a single-node k3d cluster, found %d nodes", len(nodes.Items))
	}
	return nodes.Items[0]
}

func nodeInternalIP(t *testing.T, node corev1.Node) string {
	t.Helper()
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	t.Fatalf("node %s has no InternalIP address", node.Name)
	return ""
}

// eventuallyEndpointSlice polls for name in serviceNamespace, since the
// controller's manager loop reconciles asynchronously rather than
// synchronously like test/integration's direct Reconcile() calls.
func eventuallyEndpointSlice(t *testing.T, ctx context.Context, name string) *discoveryv1.EndpointSlice {
	t.Helper()
	var es discoveryv1.EndpointSlice
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: serviceNamespace, Name: name}, &es)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		t.Fatalf("waiting for EndpointSlice %s/%s: %v", serviceNamespace, name, err)
	}
	return &es
}

func eventuallyService(t *testing.T, ctx context.Context, name string) *corev1.Service {
	t.Helper()
	var svc corev1.Service
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: serviceNamespace, Name: name}, &svc)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		t.Fatalf("waiting for Service %s/%s: %v", serviceNamespace, name, err)
	}
	return &svc
}
