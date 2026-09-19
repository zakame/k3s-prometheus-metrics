//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

// A pre-existing Service with an allocated ClusterIP at a managed name
// can't be adopted (spec.clusterIP is immutable). These tests prove that
// collision costs only that one Service its endpoints, not the others'.

// threeServiceConfig is svcConfig's shape widened to three services named
// <id>-a, <id>-b, <id>-c on distinct ports.
func threeServiceConfig(id string, selector map[string]string) config.Config {
	return config.Config{
		Namespace:    testNamespace,
		NodeSelector: selector,
		Services: []config.Service{
			{Name: id + "-a", PortName: "metrics", Port: 9001, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			{Name: id + "-b", PortName: "metrics", Port: 9002, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			{Name: id + "-c", PortName: "metrics", Port: 9003, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		},
	}
}

// createCollidingService creates a Service at name with an allocated (so
// immutable, non-headless) ClusterIP, a selector, and foreign labels: the
// shape of a user's unrelated Service that happens to share a managed name.
func createCollidingService(t *testing.T, ctx context.Context, name string) *corev1.Service {
	t.Helper()
	pre := &corev1.Service{
		Name: name, Namespace: testNamespace,
		Labels: map[string]string{"owner": "someone-else"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "unrelated"},
			Ports:    []corev1.ServicePort{{Name: "web", Port: 8080, Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatalf("creating colliding Service %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &corev1.Service{Name: name, Namespace: testNamespace})
	})
	if pre.Spec.ClusterIP == "" || pre.Spec.ClusterIP == corev1.ClusterIPNone {
		t.Fatalf("setup: expected an allocated ClusterIP, got %q", pre.Spec.ClusterIP)
	}
	return pre
}

func reconcileErr(ctx context.Context, cfg config.Config) error {
	r := &controller.NodeReconciler{Client: k8sClient, Config: cfg}
	_, err := r.Reconcile(ctx, ctrl.Request{})
	return err
}

func listSlicesFor(t *testing.T, ctx context.Context, svcName string) []discoveryv1.EndpointSlice {
	t.Helper()
	var list discoveryv1.EndpointSliceList
	if err := k8sClient.List(ctx, &list, client.InNamespace(testNamespace), client.MatchingLabels{discoveryv1.LabelServiceName: svcName}); err != nil {
		t.Fatalf("listing EndpointSlices for %s: %v", svcName, err)
	}
	return list.Items
}

// assertConverged checks svcName is the headless, managed Service this
// controller builds, with exactly one EndpointSlice carrying nodeIP and a
// controller ownerRef back to it.
func assertConverged(t *testing.T, ctx context.Context, svcName, nodeIP string) {
	t.Helper()
	svc := getService(t, ctx, svcName)
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("%s: expected headless, got ClusterIP %q", svcName, svc.Spec.ClusterIP)
	}
	if svc.Spec.Selector != nil {
		t.Errorf("%s: expected no selector, got %v", svcName, svc.Spec.Selector)
	}
	if svc.Labels["app.kubernetes.io/managed-by"] != endpoints.ManagedByValue {
		t.Errorf("%s: expected managed-by %q, got %q", svcName, endpoints.ManagedByValue, svc.Labels["app.kubernetes.io/managed-by"])
	}

	slices := listSlicesFor(t, ctx, svcName)
	if len(slices) != 1 {
		t.Fatalf("%s: expected exactly one EndpointSlice, got %d", svcName, len(slices))
	}
	es := getEndpointSlice(t, ctx, slices[0].Name) // registers cleanup
	if len(es.Endpoints) != 1 || len(es.Endpoints[0].Addresses) != 1 || es.Endpoints[0].Addresses[0] != nodeIP {
		t.Errorf("%s: expected the node IP %s as the sole endpoint, got %+v", es.Name, nodeIP, es.Endpoints)
	}
	ref := ownerRefTo(t, es.OwnerReferences, svc)
	if ref.UID != svc.UID || ref.Controller == nil || !*ref.Controller {
		t.Errorf("%s: expected a controller ownerRef to %s (UID %s), got %+v", es.Name, svcName, svc.UID, ref)
	}
}

func assertCollidingServiceUntouched(t *testing.T, ctx context.Context, pre *corev1.Service) {
	t.Helper()
	var now corev1.Service
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pre), &now); err != nil {
		t.Fatalf("getting colliding Service: %v", err)
	}
	if now.ResourceVersion != pre.ResourceVersion {
		t.Errorf("colliding Service was written to: ResourceVersion %s -> %s", pre.ResourceVersion, now.ResourceVersion)
	}
	if !equality.Semantic.DeepEqual(now.Labels, pre.Labels) {
		t.Errorf("colliding Service labels changed: %v -> %v", pre.Labels, now.Labels)
	}
	if !equality.Semantic.DeepEqual(now.Spec.Selector, pre.Spec.Selector) {
		t.Errorf("colliding Service selector changed: %v -> %v", pre.Spec.Selector, now.Spec.Selector)
	}
	if !equality.Semantic.DeepEqual(now.Spec.Ports, pre.Spec.Ports) {
		t.Errorf("colliding Service ports changed: %+v -> %+v", pre.Spec.Ports, now.Spec.Ports)
	}
	if now.Spec.ClusterIP != pre.Spec.ClusterIP {
		t.Errorf("colliding Service ClusterIP changed: %s -> %s", pre.Spec.ClusterIP, now.Spec.ClusterIP)
	}
	if len(listSlicesFor(t, ctx, pre.Name)) != 0 {
		t.Errorf("expected no EndpointSlice labelled %s=%s", discoveryv1.LabelServiceName, pre.Name)
	}
}

func TestReconcile_CollidingService_OthersStillConverge(t *testing.T) {
	for _, tc := range []struct {
		name string
		idx  int
	}{
		{"first", 0},
		{"last", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
			defer cancel()

			id := testID(t)
			cpLabel := map[string]string{"role-" + id: "control-plane"}
			nodeIP := fmt.Sprintf("10.41.0.%d", tc.idx+1)
			createNode(t, ctx, "n1-"+id, nodeIP, true, withExtraLabels(cpLabel))

			cfg := threeServiceConfig(id, cpLabel)
			colliding := cfg.Services[tc.idx].Name
			pre := createCollidingService(t, ctx, colliding)

			err := reconcileErr(ctx, cfg)
			if err == nil {
				t.Fatal("expected Reconcile to report the collision")
			}
			if !apierrors.IsInvalid(err) {
				t.Errorf("expected an Invalid API error through the join, got %v", err)
			}
			if !strings.Contains(err.Error(), testNamespace+"/"+colliding) {
				t.Errorf("expected the error to name the colliding Service, got %q", err.Error())
			}
			for _, svc := range cfg.Services {
				if svc.Name != colliding && strings.Contains(err.Error(), svc.Name) {
					t.Errorf("expected the error not to mention converged Service %s, got %q", svc.Name, err.Error())
				}
			}

			for _, svc := range cfg.Services {
				if svc.Name != colliding {
					assertConverged(t, ctx, svc.Name, nodeIP)
				}
			}
			assertCollidingServiceUntouched(t, ctx, pre)
		})
	}
}

func TestReconcile_CollidingService_RepeatedReconcileIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.41.1.1", true, withExtraLabels(cpLabel))

	cfg := threeServiceConfig(id, cpLabel)
	colliding := cfg.Services[1].Name
	pre := createCollidingService(t, ctx, colliding)

	first := reconcileErr(ctx, cfg)
	if first == nil {
		t.Fatal("expected the first Reconcile to report the collision")
	}
	rvs := map[string]string{}
	for _, svc := range cfg.Services {
		if svc.Name == colliding {
			continue
		}
		rvs[svc.Name] = getService(t, ctx, svc.Name).ResourceVersion
		slices := listSlicesFor(t, ctx, svc.Name)
		if len(slices) != 1 {
			t.Fatalf("%s: expected one slice after the first reconcile, got %d", svc.Name, len(slices))
		}
		rvs[slices[0].Name] = getEndpointSlice(t, ctx, slices[0].Name).ResourceVersion
	}

	for i := 2; i <= 3; i++ {
		err := reconcileErr(ctx, cfg)
		if err == nil {
			t.Fatalf("reconcile %d: expected the collision to be reported again", i)
		}
		if err.Error() != first.Error() {
			t.Errorf("reconcile %d: expected the same error as the first, got %q want %q", i, err.Error(), first.Error())
		}
	}

	for _, svc := range cfg.Services {
		if svc.Name == colliding {
			continue
		}
		if rv := getService(t, ctx, svc.Name).ResourceVersion; rv != rvs[svc.Name] {
			t.Errorf("%s: expected no further Service writes, ResourceVersion %s -> %s", svc.Name, rvs[svc.Name], rv)
		}
		slices := listSlicesFor(t, ctx, svc.Name)
		if len(slices) != 1 {
			t.Errorf("%s: expected still exactly one slice, got %d", svc.Name, len(slices))
			continue
		}
		if rv := slices[0].ResourceVersion; rv != rvs[slices[0].Name] {
			t.Errorf("%s: expected no further slice writes, ResourceVersion %s -> %s", slices[0].Name, rvs[slices[0].Name], rv)
		}
	}
	assertCollidingServiceUntouched(t, ctx, pre)

	var svcs corev1.ServiceList
	if err := k8sClient.List(ctx, &svcs, client.InNamespace(testNamespace)); err != nil {
		t.Fatalf("listing Services: %v", err)
	}
	var mine int
	for _, s := range svcs.Items {
		if strings.HasPrefix(s.Name, id+"-") {
			mine++
		}
	}
	if mine != 3 {
		t.Errorf("expected exactly 3 Services named %s-*, (2 managed + 1 colliding), got %d", id, mine)
	}
}

func TestReconcile_CollidingService_LegacyEndpointsFollowTheSameRule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	const nodeIP = "10.41.2.1"
	createNode(t, ctx, "n1-"+id, nodeIP, true, withExtraLabels(cpLabel))

	cfg := threeServiceConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	colliding := cfg.Services[0].Name
	pre := createCollidingService(t, ctx, colliding)

	if err := reconcileErr(ctx, cfg); err == nil || !apierrors.IsInvalid(err) {
		t.Fatalf("expected an Invalid collision error, got %v", err)
	}

	var eps corev1.Endpoints //nolint:staticcheck
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: colliding}, &eps); !isNotFound(err) {
		t.Errorf("expected no legacy Endpoints for the colliding Service, got err=%v", err)
	}
	for _, svc := range cfg.Services[1:] {
		svcObj := getService(t, ctx, svc.Name)
		got := getLegacyEndpoints(t, ctx, svc.Name)
		if len(got.Subsets) != 1 || len(got.Subsets[0].Addresses) != 1 || got.Subsets[0].Addresses[0].IP != nodeIP {
			t.Errorf("%s: expected the node IP as the sole ready address, got %+v", svc.Name, got.Subsets)
		}
		if ref := ownerRefTo(t, got.OwnerReferences, svcObj); ref.UID != svcObj.UID {
			t.Errorf("%s: expected legacy Endpoints ownerRef UID %s, got %s", svc.Name, svcObj.UID, ref.UID)
		}
		assertConverged(t, ctx, svc.Name, nodeIP)
	}
	assertCollidingServiceUntouched(t, ctx, pre)
}
