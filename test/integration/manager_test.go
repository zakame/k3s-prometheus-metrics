//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
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

// TestManager_ImpersonatedShippedRBAC_DrivesEndpointSliceViaWatch starts a
// real manager, authenticated as the impersonated shipped ServiceAccount
// under the real deploy/standard RBAC, with the real k3s control-plane
// label. An admin-privileged manager test can't see an RBAC-scope
// mismatch between the cache's default cluster-wide watch and a
// namespace-scoped Role -- exactly the bug that reached a real cluster.
// The Cache option mirrors cmd/k3s-prometheus-metrics's production wiring.
func TestManager_ImpersonatedShippedRBAC_DrivesEndpointSliceViaWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applyShippedRBAC(t, ctx)

	id := testID(t)
	cpLabel := map[string]string{config.ControlPlaneNodeSelector: "true"}

	restCfg := rest.CopyConfig(adminConfig)
	userName, groups := saUser("monitoring", "k3s-prometheus-metrics")
	restCfg.Impersonate = rest.ImpersonationConfig{UserName: userName, Groups: groups}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 testScheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Controller names are validated process-wide, not per-manager;
		// avoids collisions with other manager tests in this binary.
		// Test-only, production wants the collision protection.
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
		// Mirrors cmd/k3s-prometheus-metrics's production Cache scoping.
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&discoveryv1.EndpointSlice{}: {Namespaces: map[string]cache.Config{rbacNamespace: {}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}

	r := &controller.NodeReconciler{
		Client: mgr.GetClient(),
		Config: config.Config{
			Namespace:    rbacNamespace,
			NodeSelector: cpLabel,
			Services: []config.Service{
				{Name: id, PortName: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			},
		},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager: %v", err)
	}

	mgrDone := make(chan error, 1)
	go func() { mgrDone <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-mgrDone
	})

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("manager cache never synced")
	}

	createNode(t, ctx, "cp-"+id, "10.31.0.1", true, withExtraLabels(cpLabel))

	name := id + "-metrics"
	deadline := time.Now().Add(reconcileTimeout)
	for {
		if err := getEndpointSliceErrIn(ctx, rbacNamespace, name); err == nil {
			break
		} else if !isNotFound(err) {
			t.Fatalf("getting EndpointSlice %s/%s: %v", rbacNamespace, name, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the manager-driven watch/reconcile to create EndpointSlice %s/%s under the impersonated shipped ServiceAccount", rbacNamespace, name)
		}
		time.Sleep(50 * time.Millisecond)
	}

	es := getEndpointSliceIn(t, ctx, rbacNamespace, name)
	if len(es.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d: %+v", len(es.Endpoints), es.Endpoints)
	}
	if *es.Endpoints[0].NodeName != "cp-"+id {
		t.Fatalf("expected endpoint for cp-%s, got %s", id, *es.Endpoints[0].NodeName)
	}
}
