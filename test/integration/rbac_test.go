//go:build integration

package integration

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
)

// --- loading the actual shipped manifests ---------------------------------
//
// These tests parse deploy/standard's real RBAC YAML from disk rather than
// re-declaring the rules in Go, so a Surgeon edit to the shipped manifest is
// exercised here automatically instead of silently diverging from a copy.

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// loadManifest parses every YAML document in relPath (relative to the repo
// root) into the matching typed object, skipping any Kind this test doesn't
// need (Deployment, Service, etc).
func loadManifest(t *testing.T, relPath string) []client.Object {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(t), relPath))
	if err != nil {
		t.Fatalf("opening manifest %s: %v", relPath, err)
	}
	defer f.Close()

	reader := k8syaml.NewYAMLReader(bufio.NewReader(f))
	var objs []client.Object
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading manifest %s: %v", relPath, err)
		}
		if len(strings.TrimSpace(string(doc))) == 0 {
			continue
		}

		var meta metav1.TypeMeta
		if err := k8syaml.Unmarshal(doc, &meta); err != nil {
			t.Fatalf("parsing manifest %s: %v", relPath, err)
		}

		var obj client.Object
		switch meta.Kind {
		case "Namespace":
			obj = &corev1.Namespace{}
		case "ServiceAccount":
			obj = &corev1.ServiceAccount{}
		case "ClusterRole":
			obj = &rbacv1.ClusterRole{}
		case "ClusterRoleBinding":
			obj = &rbacv1.ClusterRoleBinding{}
		case "Role":
			obj = &rbacv1.Role{}
		case "RoleBinding":
			obj = &rbacv1.RoleBinding{}
		default:
			continue
		}
		if err := k8syaml.Unmarshal(doc, obj); err != nil {
			t.Fatalf("decoding %s from %s: %v", meta.Kind, relPath, err)
		}
		objs = append(objs, obj)
	}
	return objs
}

func applyManifest(t *testing.T, ctx context.Context, relPath string) {
	t.Helper()
	for _, obj := range loadManifest(t, relPath) {
		if err := k8sClient.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating %s/%s from %s: %v", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), relPath, err)
		}
	}
}

func ensureNamespace(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating namespace %s: %v", name, err)
	}
}

// impersonatedClient builds a client.Client authenticated as userName (with
// the given groups), reusing the envtest admin connection's transport --
// the admin identity is in system:masters, which bypasses RBAC entirely
// (including for the "impersonate" verb), so this doesn't require its own
// RBAC grant.
func impersonatedClient(t *testing.T, userName string, groups ...string) client.Client {
	t.Helper()
	cfg := rest.CopyConfig(adminConfig)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: userName, Groups: groups}
	c, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("building impersonated client for %s: %v", userName, err)
	}
	return c
}

func saUser(namespace, name string) (userName string, groups []string) {
	return "system:serviceaccount:" + namespace + ":" + name,
		[]string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"}
}

// --- the shipped manifest, as-is ------------------------------------------

func applyShippedRBAC(t *testing.T, ctx context.Context) {
	t.Helper()
	ensureNamespace(t, ctx, "kube-system") // pre-exists on a real cluster; envtest may not bootstrap it
	applyManifest(t, ctx, "deploy/standard/namespace.yaml")
	applyManifest(t, ctx, "deploy/standard/serviceaccount.yaml")
	applyManifest(t, ctx, "deploy/standard/role.yaml")
	applyManifest(t, ctx, "deploy/standard/rolebinding.yaml")
	applyManifest(t, ctx, "deploy/standard/role-endpoints.yaml")
	applyManifest(t, ctx, "deploy/standard/rolebinding-endpoints.yaml")
	applyManifest(t, ctx, "deploy/standard/role-leader-election.yaml")
	applyManifest(t, ctx, "deploy/standard/rolebinding-leader-election.yaml")
}

func TestRBAC_ShippedManifest_ReconcileSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	applyShippedRBAC(t, ctx)

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.20.0.1", true, withExtraLabels(cpLabel))

	userName, groups := saUser("monitoring", "k3s-prometheus-metrics")
	restricted := impersonatedClient(t, userName, groups...)

	r := &controller.NodeReconciler{
		Client: restricted,
		Config: config.Config{
			Namespace:            "kube-system",
			NodeSelector:         cpLabel,
			WriteLegacyEndpoints: true, // exercises the endpoints verbs too
			Services: []config.Service{
				{Name: id, PortName: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			},
		},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatalf("expected reconcile to succeed under the shipped RBAC manifest, got: %v", err)
	}

	// Prove it actually did the work, not just that no error occurred.
	// cfg.Namespace above is "kube-system" (matching role-endpoints.yaml),
	// not the "default" namespace the other integration tests use.
	es := getEndpointSliceIn(t, ctx, "kube-system", id+"-metrics")
	if len(es.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(es.Endpoints))
	}
	eps := getLegacyEndpointsIn(t, ctx, "kube-system", id)
	if len(eps.Subsets[0].Addresses) != 1 {
		t.Fatalf("expected 1 ready legacy address, got %d", len(eps.Subsets[0].Addresses))
	}
}

func TestRBAC_ServiceAccountWithNoBindings_ReconcileFailsForbidden(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	applyShippedRBAC(t, ctx) // must exist so we know the failure isn't "role doesn't exist"

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.21.0.1", true, withExtraLabels(cpLabel))

	userName, groups := saUser("monitoring", "no-permissions-"+id)
	restricted := impersonatedClient(t, userName, groups...)

	r := &controller.NodeReconciler{
		Client: restricted,
		Config: config.Config{
			Namespace:    "kube-system",
			NodeSelector: cpLabel,
			Services: []config.Service{
				{Name: id, PortName: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			},
		},
	}
	_, err := r.Reconcile(ctx, ctrl.Request{})
	if err == nil {
		t.Fatal("expected reconcile to fail for a ServiceAccount with no RoleBindings at all")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected a Forbidden error, got: %v", err)
	}
}

// TestRBAC_MissingListOnNodes_ReconcileFailsForbidden proves the "list"
// verb on nodes in role.yaml is load-bearing: with everything else granted
// as shipped but that one verb dropped, reconcile must fail specifically on
// listing nodes, not silently succeed some other way.
func TestRBAC_MissingListOnNodes_ReconcileFailsForbidden(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	applyShippedRBAC(t, ctx)

	id := testID(t)
	saName := "sa-no-list-" + id
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: "monitoring"}}
	if err := k8sClient.Create(ctx, sa); err != nil {
		t.Fatalf("creating service account: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), sa) })

	// Same as role.yaml's node rule, minus "list".
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "nodes-no-list-" + id},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "watch"}},
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("creating cluster role: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), cr) })
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "nodes-no-list-" + id},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: cr.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: "monitoring"}},
	}
	if err := k8sClient.Create(ctx, crb); err != nil {
		t.Fatalf("creating cluster role binding: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), crb) })
	// Still grant the namespaced endpoints/endpointslices role so the only
	// broken permission is nodes:list.
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "endpoints-for-" + id, Namespace: "kube-system"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "k3s-prometheus-metrics-endpoints"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: "monitoring"}},
	}
	if err := k8sClient.Create(ctx, rb); err != nil {
		t.Fatalf("creating role binding: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), rb) })

	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.22.0.1", true, withExtraLabels(cpLabel))

	userName, groups := saUser("monitoring", saName)
	restricted := impersonatedClient(t, userName, groups...)

	r := &controller.NodeReconciler{
		Client: restricted,
		Config: config.Config{
			Namespace:    "kube-system",
			NodeSelector: cpLabel,
			Services: []config.Service{
				{Name: id, PortName: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			},
		},
	}
	_, err := r.Reconcile(ctx, ctrl.Request{})
	if err == nil {
		t.Fatal("expected reconcile to fail once nodes:list is removed, even though get/watch remain")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected a Forbidden error, got: %v", err)
	}
}

// --- events RBAC (leader-election recorder) --------------------------------
//
// Reconcile() never touches events; only the leader-election recorder does
// (Create then Patch, see role-leader-election.yaml), so it needs its own
// tests.

// newProbeEvent returns a minimal Event naming the shipped leader-election
// Lease as its InvolvedObject.
func newProbeEvent(name, namespace string) *corev1.Event {
	now := metav1.Now()
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Lease",
			Namespace: namespace,
			Name:      "k3s-prometheus-metrics.zakame.github.io",
		},
		Reason:         "LeaderElection",
		Message:        "test became leader",
		Type:           corev1.EventTypeNormal,
		Source:         corev1.EventSource{Component: "test"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
}

func TestRBAC_ShippedManifest_CanRecordLeaderElectionEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	applyShippedRBAC(t, ctx)

	id := testID(t)
	userName, groups := saUser("monitoring", "k3s-prometheus-metrics")
	restricted := impersonatedClient(t, userName, groups...)

	ev := newProbeEvent("probe-"+id, "monitoring")
	if err := restricted.Create(ctx, ev); err != nil {
		t.Fatalf("expected shipped RBAC to allow creating an Event in the leader-election namespace, got: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), ev)
	})

	// Patch, not Update: role-leader-election.yaml only grants "patch".
	original := ev.DeepCopy()
	ev.Count = 2
	ev.LastTimestamp = metav1.Now()
	if err := restricted.Patch(ctx, ev, client.MergeFrom(original)); err != nil {
		t.Fatalf("expected shipped RBAC to allow patching an Event in the leader-election namespace, got: %v", err)
	}
}

// TestRBAC_MissingEventsOnLeaderElection_RecordEventFailsForbidden proves
// the events rule in role-leader-election.yaml is load-bearing: leases
// access alone isn't enough to record an Event.
func TestRBAC_MissingEventsOnLeaderElection_RecordEventFailsForbidden(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	applyShippedRBAC(t, ctx)

	id := testID(t)
	saName := "sa-no-events-" + id
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: "monitoring"}}
	if err := k8sClient.Create(ctx, sa); err != nil {
		t.Fatalf("creating service account: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), sa) })

	// Same as role-leader-election.yaml's leases rule, minus events.
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "leader-election-no-events-" + id, Namespace: "monitoring"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("creating role: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), role) })
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "leader-election-no-events-" + id, Namespace: "monitoring"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: "monitoring"}},
	}
	if err := k8sClient.Create(ctx, rb); err != nil {
		t.Fatalf("creating role binding: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), rb) })

	userName, groups := saUser("monitoring", saName)
	restricted := impersonatedClient(t, userName, groups...)

	ev := newProbeEvent("probe-"+id, "monitoring")
	err := restricted.Create(ctx, ev)
	if err == nil {
		t.Fatal("expected creating an Event to fail once the events rule is removed, even though leases access remains")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected a Forbidden error, got: %v", err)
	}
}
