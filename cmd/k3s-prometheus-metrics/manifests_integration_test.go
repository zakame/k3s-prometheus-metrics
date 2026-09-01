//go:build integration

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// Uses a real envtest apiserver so ListNodesByService's label selector
// (via generateManifests) is proven against real API server filtering,
// not just the fake client used in manifests_test.go.
func TestGenerateManifests_AgainstRealAPIServer(t *testing.T) {
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

	c, err := client.New(restCfg, client.Options{Scheme: runtimeScheme})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}

	ctx := context.Background()
	node := &corev1.Node{
		Name:   "cp-1",
		Labels: map[string]string{"node-role.kubernetes.io/control-plane": "true"},
	}
	if err := c.Create(ctx, node); err != nil {
		t.Fatalf("creating Node: %v", err)
	}
	node.Status = corev1.NodeStatus{
		Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.40.9.1"}},
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
	}
	if err := c.Status().Update(ctx, node); err != nil {
		t.Fatalf("updating Node status: %v", err)
	}

	cfg := config.Config{
		Namespace:    "kube-system",
		NodeSelector: map[string]string{"node-role.kubernetes.io/control-plane": "true"},
		Services:     config.DefaultServices,
	}

	var buf bytes.Buffer
	if err := generateManifests(ctx, &buf, c, cfg); err != nil {
		t.Fatalf("generateManifests: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"kind: Service", "name: kube-scheduler", "kind: EndpointSlice", "10.40.9.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}
