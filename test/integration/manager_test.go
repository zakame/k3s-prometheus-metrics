//go:build integration

package integration

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
)

// rbacNamespace is where role-endpoints.yaml scopes EndpointSlice/Endpoints
// access -- kube-system, matching the shipped default --namespace.
const rbacNamespace = "kube-system"

// startManager runs a real manager with NodeReconciler wired in through
// SetupWithManager (so the real watches, predicates, and event mapping
// drive Reconcile), returning once its cache has synced. The Cache option
// mirrors cmd/k3s-prometheus-metrics's production wiring: Service and
// EndpointSlice watches scoped to cfg.Namespace.
//
// Its cleanup stops the manager and then sweeps cfg's managed objects,
// since a reconcile racing the other cleanups could otherwise recreate
// them after they were deleted.
func startManager(t *testing.T, ctx context.Context, restCfg *rest.Config, cfg config.Config) ctrl.Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 testScheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Controller names are validated process-wide, not per-manager;
		// avoids collisions with other manager tests in this binary.
		// Test-only, production wants the collision protection.
		Controller: ctrlconfig.Controller{SkipNameValidation: new(true)},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Service{}:            {Namespaces: map[string]cache.Config{cfg.Namespace: {}}},
				&discoveryv1.EndpointSlice{}: {Namespaces: map[string]cache.Config{cfg.Namespace: {}}},
			},
		},
	})
	if err != nil {
		cancel()
		t.Fatalf("creating manager: %v", err)
	}

	r := &controller.NodeReconciler{Client: mgr.GetClient(), Config: cfg}
	if err := r.SetupWithManager(mgr); err != nil {
		cancel()
		t.Fatalf("SetupWithManager: %v", err)
	}

	mgrDone := make(chan error, 1)
	go func() { mgrDone <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-mgrDone
		for _, svc := range cfg.Services {
			_ = k8sClient.Delete(context.Background(), &discoveryv1.EndpointSlice{Name: svc.Name + "-metrics", Namespace: cfg.Namespace})
			_ = k8sClient.Delete(context.Background(), &discoveryv1.EndpointSlice{Name: svc.Name + "-metrics-ipv6", Namespace: cfg.Namespace})
			_ = k8sClient.Delete(context.Background(), &corev1.Endpoints{Name: svc.Name, Namespace: cfg.Namespace}) //nolint:staticcheck
			_ = k8sClient.Delete(context.Background(), &corev1.Service{Name: svc.Name, Namespace: cfg.Namespace})
		}
	})

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("manager cache never synced")
	}
	return mgr
}

// waitForSlice polls until the named EndpointSlice exists and ok accepts
// it, returning the accepted object.
func waitForSlice(t *testing.T, ctx context.Context, namespace, name string, ok func(*discoveryv1.EndpointSlice) bool) *discoveryv1.EndpointSlice {
	t.Helper()
	var got discoveryv1.EndpointSlice
	waitFor(t, ctx, "EndpointSlice "+namespace+"/"+name, func() (bool, error) {
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &got)
		if isNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return ok(&got), nil
	})
	return &got
}

// TestManager_ImpersonatedShippedRBAC_DrivesEndpointSliceViaWatch starts a
// real manager, authenticated as the impersonated shipped ServiceAccount
// under the real deploy/standard RBAC, with the real k3s control-plane
// label. An admin-privileged manager test can't see an RBAC-scope
// mismatch between the cache's default cluster-wide watch and a
// namespace-scoped Role -- exactly the bug that reached a real cluster.
func TestManager_ImpersonatedShippedRBAC_DrivesEndpointSliceViaWatch(t *testing.T) {
	ctx := t.Context()

	applyShippedRBAC(t, ctx)

	id := testID(t)
	cpLabel := map[string]string{config.ControlPlaneNodeSelector: "true"}

	restCfg := rest.CopyConfig(adminConfig)
	userName, groups := saUser("monitoring", "k3s-prometheus-metrics")
	restCfg.Impersonate = rest.ImpersonationConfig{UserName: userName, Groups: groups}

	startManager(t, ctx, restCfg, config.Config{
		Namespace:    rbacNamespace,
		NodeSelector: cpLabel,
		Services: []config.Service{
			{Name: id, PortName: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		},
	})

	createNode(t, ctx, "cp-"+id, "10.31.0.1", true, withExtraLabels(cpLabel))

	es := waitForSlice(t, ctx, rbacNamespace, id+"-metrics", func(es *discoveryv1.EndpointSlice) bool {
		return len(es.Endpoints) == 1
	})
	if *es.Endpoints[0].NodeName != "cp-"+id {
		t.Fatalf("expected endpoint for cp-%s, got %s", id, *es.Endpoints[0].NodeName)
	}
}
