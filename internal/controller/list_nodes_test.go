package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// listNodesByService is unexported, so these are white-box (same package).

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(discoveryv1.AddToScheme(s))
	return s
}

func labeledNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestListNodesByService_DedupesBySelector_OneListCallPerDistinctSelector(t *testing.T) {
	cpNode := labeledNode("cp", map[string]string{"role": "control-plane"})
	agentNode := labeledNode("agent", nil)

	var nodeListCalls int
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(cpNode, agentNode).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.NodeList); ok {
					nodeListCalls++
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	r := &NodeReconciler{Client: c, Config: config.Config{
		NodeSelector: map[string]string{"role": "control-plane"},
		Services:     config.DefaultServices,
	}}

	got, err := r.listNodesByService(context.Background())
	if err != nil {
		t.Fatalf("listNodesByService: %v", err)
	}

	if nodeListCalls != 2 {
		t.Fatalf("expected exactly 2 Node List calls (1 shared control-plane selector + 1 kube-proxy all-nodes selector), got %d", nodeListCalls)
	}

	for _, svc := range []string{"kube-scheduler", "kube-controller-manager"} {
		nodes := got[svc]
		if len(nodes) != 1 || nodes[0].Name != "cp" {
			t.Fatalf("%s: expected only the control-plane node, got %+v", svc, nodes)
		}
	}

	proxyNodes := got["kube-proxy"]
	if len(proxyNodes) != 2 {
		t.Fatalf("kube-proxy: expected both nodes (empty selector matches all nodes), got %d: %+v", len(proxyNodes), proxyNodes)
	}
}

func TestListNodesByService_CustomSelectorIsolatedFromSharedAndEmpty(t *testing.T) {
	cpNode := labeledNode("cp", map[string]string{"role": "control-plane"})
	agentNode := labeledNode("agent", nil)
	edgeNode := labeledNode("edge", map[string]string{"tier": "edge"})

	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(cpNode, agentNode, edgeNode).
		Build()

	r := &NodeReconciler{Client: c, Config: config.Config{
		NodeSelector: map[string]string{"role": "control-plane"},
		Services: []config.Service{
			{Name: "kube-scheduler"},                                              // nil -> inherits control-plane selector
			{Name: "kube-proxy", NodeSelector: map[string]string{}},               // empty -> all nodes
			{Name: "custom-svc", NodeSelector: map[string]string{"tier": "edge"}}, // distinct, non-empty
		},
	}}

	got, err := r.listNodesByService(context.Background())
	if err != nil {
		t.Fatalf("listNodesByService: %v", err)
	}

	if nodes := got["kube-scheduler"]; len(nodes) != 1 || nodes[0].Name != "cp" {
		t.Fatalf("kube-scheduler: expected only the control-plane node, got %+v", nodes)
	}
	if nodes := got["kube-proxy"]; len(nodes) != 3 {
		t.Fatalf("kube-proxy: expected all 3 nodes, got %d: %+v", len(nodes), nodes)
	}
	if nodes := got["custom-svc"]; len(nodes) != 1 || nodes[0].Name != "edge" {
		t.Fatalf("custom-svc: expected only the edge node, got %+v", nodes)
	}
}
