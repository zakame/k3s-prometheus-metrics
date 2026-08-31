//go:build integration

package main

import (
	"context"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// fakeManager implements only the two ctrl.Manager methods
// newLegacyEndpointsClient calls; every other method panics via the nil
// embedded interface.
type fakeManager struct {
	ctrl.Manager
	cfg    *rest.Config
	scheme *runtime.Scheme
}

func (f *fakeManager) GetConfig() *rest.Config    { return f.cfg }
func (f *fakeManager) GetScheme() *runtime.Scheme { return f.scheme }

// spyWarningHandler records every warning message it's handed.
type spyWarningHandler struct {
	mu    sync.Mutex
	calls []string
}

func (s *spyWarningHandler) HandleWarningHeaderWithContext(_ context.Context, _ int, _ string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, message)
}

func (s *spyWarningHandler) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// Uses a real envtest apiserver, not a hand-built HTTP response, so the
// warning being suppressed is the one Kubernetes actually sends.
func TestNewLegacyEndpointsClient_SuppressesDeprecationWarning(t *testing.T) {
	testEnv := &envtest.Environment{}
	restCfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("starting envtest environment: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stopping envtest environment: %v", err)
		}
	})

	runtimeScheme := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(runtimeScheme))

	spy := &spyWarningHandler{}
	cfg := rest.CopyConfig(restCfg)
	cfg.WarningHandlerWithContext = spy

	mgr := &fakeManager{cfg: cfg, scheme: runtimeScheme}

	c, err := newLegacyEndpointsClient(mgr)
	if err != nil {
		t.Fatalf("newLegacyEndpointsClient: %v", err)
	}

	ctx := context.Background()
	eps := &corev1.Endpoints{} //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	eps.Name = "warning-probe"
	eps.Namespace = "default"
	eps.Subsets = []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}}} //nolint:staticcheck
	if err := c.Create(ctx, eps); err != nil {
		t.Fatalf("creating Endpoints via the legacy client: %v", err)
	}

	if got := spy.callCount(); got != 0 {
		t.Fatalf("expected 0 warnings observed through the original *rest.Config's handler (proving newLegacyEndpointsClient overrode it), got %d: %#v", got, spy.calls)
	}

	// Control: confirm the server really sends this warning, so the zero
	// count above means "suppressed" rather than "never sent".
	controlSpy := &spyWarningHandler{}
	controlCfg := rest.CopyConfig(restCfg)
	controlCfg.WarningHandlerWithContext = controlSpy
	controlClient, err := client.New(controlCfg, client.Options{Scheme: runtimeScheme})
	if err != nil {
		t.Fatalf("building control client: %v", err)
	}
	controlEps := &corev1.Endpoints{} //nolint:staticcheck
	controlEps.Name = "warning-control"
	controlEps.Namespace = "default"
	if err := controlClient.Create(ctx, controlEps); err != nil {
		t.Fatalf("creating control Endpoints: %v", err)
	}
	if got := controlSpy.callCount(); got == 0 {
		t.Fatal("expected the control client (no WarningHandlerWithContext override) to observe the v1 Endpoints deprecation warning -- if this fails, the API server stopped sending it and the suppression assertion above is meaningless")
	}
}
