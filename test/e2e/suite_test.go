// Package e2e exercises the running controller Deployment against a real
// k3d cluster -- no envtest, no in-process Reconcile() calls like
// test/integration. Assertions poll live cluster state that the
// controller's own manager loop reconciles asynchronously. Requires
// deploy/e2e already applied and a kubeconfig pointed at the target
// cluster; run with:
//
//	go test -tags e2e ./test/e2e/...
//
//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	controllerNamespace = "monitoring"
	controllerName      = "k3s-prometheus-metrics"
	// serviceNamespace is internal/config.Config's default --namespace,
	// which deploy/e2e's Deployment args don't override. It's distinct
	// from controllerNamespace, where the controller Pod itself runs.
	serviceNamespace = "kube-system"

	pollInterval = 2 * time.Second
	pollTimeout  = 2 * time.Minute
)

var k8sClient client.Client

func TestMain(m *testing.M) {
	restCfg := ctrl.GetConfigOrDie()

	testScheme := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(testScheme))
	utilruntime.Must(discoveryv1.AddToScheme(testScheme))

	var err error
	k8sClient, err = client.New(restCfg, client.Options{Scheme: testScheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating client:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()
	if err := waitForControllerReady(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "controller Deployment never became ready:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// waitForControllerReady polls the controller Deployment until it reports
// at least one AvailableReplica, so a crash-looping controller fails here
// with a clear message instead of a confusing timeout waiting on
// EndpointSlices that will never appear.
func waitForControllerReady(ctx context.Context) error {
	var dep appsv1.Deployment
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: controllerNamespace, Name: controllerName}, &dep); err != nil {
			return false, nil //nolint:nilerr // Deployment may not exist yet; keep polling
		}
		return dep.Status.AvailableReplicas >= 1, nil
	})
	if err != nil {
		return fmt.Errorf("Deployment %s/%s status: %+v: %w", controllerNamespace, controllerName, dep.Status, err)
	}
	return nil
}
