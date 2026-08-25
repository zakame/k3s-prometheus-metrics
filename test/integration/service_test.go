//go:build integration

package integration

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
)

func TestReconcile_CreatesSelectorLessService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.0.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	if svc.Spec.Selector != nil {
		t.Errorf("expected a selector-less Service, got selector %v", svc.Spec.Selector)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("expected headless ClusterIP %q, got %q", corev1.ClusterIPNone, svc.Spec.ClusterIP)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 9999 || svc.Spec.Ports[0].Name != "metrics" {
		t.Errorf("expected port {metrics 9999}, got %+v", svc.Spec.Ports)
	}
}

// TestReconcile_ServiceCreatedEvenWithNoMatchingNodes proves the Service
// itself doesn't depend on any node currently matching the selector: a
// ServiceMonitor targets the Service, which must exist independently of
// whether any control-plane node happens to be present right now.
func TestReconcile_ServiceCreatedEvenWithNoMatchingNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "worker-"+id, "10.40.1.1", true) // no matching label

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	_ = getService(t, ctx, id) // must exist

	if err := getEndpointSliceErr(ctx, id+"-metrics"); !isNotFound(err) {
		t.Fatalf("expected no EndpointSlice (no matching nodes), got err=%v", err)
	}
}

// TestReconcile_ReconcilesExistingHeadlessService covers the migration path
// from a previous version's static deploy/standard/control-plane-services.yaml:
// a headless Service with stale ports must be patched to the desired state,
// not left stale or duplicated.
// TestReconcile_AppProtocolConsistentAcrossServiceAndEndpointObjects proves
// the Service's port AppProtocol (added after an earlier gap: BuildServices
// wasn't setting it at all) matches what EndpointSlice/Endpoints already
// carry for the same config.Service entry -- a scraper relying on either
// object shouldn't see conflicting scheme hints for the same target.
func TestReconcile_AppProtocolConsistentAcrossServiceAndEndpointObjects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.11.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel) // AppProtocol: "http", see svcConfig
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	es := getEndpointSlice(t, ctx, id+"-metrics")
	eps := getLegacyEndpoints(t, ctx, id)

	svcAppProto := svc.Spec.Ports[0].AppProtocol
	if svcAppProto == nil || *svcAppProto != "http" {
		t.Fatalf("expected Service AppProtocol %q, got %v", "http", svcAppProto)
	}
	if es.Ports[0].AppProtocol == nil || *es.Ports[0].AppProtocol != *svcAppProto {
		t.Errorf("expected EndpointSlice AppProtocol to match Service's %q, got %v", *svcAppProto, es.Ports[0].AppProtocol)
	}
	if eps.Subsets[0].Ports[0].AppProtocol == nil || *eps.Subsets[0].Ports[0].AppProtocol != *svcAppProto {
		t.Errorf("expected legacy Endpoints AppProtocol to match Service's %q, got %v", *svcAppProto, eps.Subsets[0].Ports[0].AppProtocol)
	}
}

// TestReconcile_AdoptsPreExistingServiceByName_UnlikeEndpointSlice is the
// deliberate mirror-image of TestReconcile_DoesNotAdoptForeignEndpointSlice:
// EndpointSlices are never adopted by name alone (they carry a distinct
// managed-by label precisely to avoid fighting Kubernetes' own mirroring
// controller), but Services ARE meant to be adopted by name -- the upstream
// kubeadm convention already uses these exact names
// (kube-scheduler/kube-controller-manager/kube-proxy in kube-system), and
// the migration path off the old static control-plane-services.yaml
// depends on the controller taking over a same-named Service rather than
// erroring out or creating a duplicate.
func TestReconcile_AdoptsPreExistingServiceByName_UnlikeEndpointSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.12.1", true, withExtraLabels(cpLabel))

	// No app.kubernetes.io/managed-by label -- simulates a Service this
	// controller never created (e.g. the old static manifest, or a
	// kubeadm-flavored cluster's pre-existing Service).
	pre := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id,
			Namespace: testNamespace,
			Labels:    map[string]string{"app.kubernetes.io/name": id, "k8s-app": id},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Ports:     []corev1.ServicePort{{Name: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatalf("creating pre-existing foreign-looking Service: %v", err)
	}
	preUID := pre.UID

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	if svc.UID != preUID {
		t.Fatalf("expected the pre-existing Service to be adopted in place (same UID), got a new object: %s -> %s", preUID, svc.UID)
	}
	if svc.Labels["app.kubernetes.io/managed-by"] != endpoints.ManagedByValue {
		t.Errorf("expected the adopted Service to gain the managed-by label, got %q", svc.Labels["app.kubernetes.io/managed-by"])
	}

	// And it now backs a real ownerReference, proving adoption is complete,
	// not just a label patch.
	es := getEndpointSlice(t, ctx, id+"-metrics")
	if ref := ownerRefTo(t, es.OwnerReferences, svc); ref.UID != svc.UID {
		t.Errorf("expected EndpointSlice ownerRef to the adopted Service, got UID %s want %s", ref.UID, svc.UID)
	}
}

func TestReconcile_ReconcilesExistingHeadlessService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.2.1", true, withExtraLabels(cpLabel))

	pre := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Ports:     []corev1.ServicePort{{Name: "stale", Port: 1111, Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatalf("creating pre-existing Service: %v", err)
	}
	preUID := pre.UID

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	if svc.UID != preUID {
		t.Fatalf("expected the same Service object to be reconciled in place (same UID), got a new one: %s -> %s", preUID, svc.UID)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Name != "metrics" || svc.Spec.Ports[0].Port != 9999 {
		t.Fatalf("expected stale port {stale 1111} to be reconciled to {metrics 9999}, got %+v", svc.Spec.Ports)
	}
}

// TestReconcile_AdoptingForeignService_ClearsStaleSelector guards against a
// regression of a real gap found during test-writing: applyServices'
// CreateOrUpdate mutate function initially only assigned Labels/Ports/
// ClusterIP, never clearing a pre-existing Spec.Selector -- so a Service
// that already existed at the desired name with a Selector set (e.g.
// hand-created before the controller ever ran) would keep that stale
// Selector forever, silently breaking the "selector-less,
// EndpointSlice-driven" invariant BuildServices otherwise guarantees for a
// freshly-created Service. Now fixed (node_controller.go's applyServices
// sets got.Spec.Selector = desired.Spec.Selector).
func TestReconcile_AdoptingForeignService_ClearsStaleSelector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.3.1", true, withExtraLabels(cpLabel))

	pre := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  map[string]string{"app": "something-unrelated"},
			Ports:     []corev1.ServicePort{{Name: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatalf("creating pre-existing Service with a Selector: %v", err)
	}

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	if svc.Spec.Selector != nil {
		t.Fatalf("expected the stale Selector to be cleared to nil, got %v", svc.Spec.Selector)
	}
}

// TestReconcile_AdoptingNonHeadlessForeignService_ErrorsRatherThanSilentlyDrifting
// covers a Service that pre-exists with an allocated ClusterIP (not
// headless) at the desired name -- e.g. a user's own unrelated Service
// object collides with the name the operator chose. spec.clusterIP is
// immutable on the API server once set, so applyServices' unconditional
// `got.Spec.ClusterIP = corev1.ClusterIPNone` must fail loudly here, not
// leave the object in a half-updated state.
func TestReconcile_AdoptingNonHeadlessForeignService_ErrorsRatherThanSilentlyDrifting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.4.1", true, withExtraLabels(cpLabel))

	pre := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: "metrics", Port: 9999, Protocol: corev1.ProtocolTCP}},
			// ClusterIP left unset: API server allocates a real cluster IP,
			// making Spec.ClusterIP immutable and unequal to "None".
		},
	}
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatalf("creating pre-existing non-headless Service: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: testNamespace},
		})
	})
	if pre.Spec.ClusterIP == "" || pre.Spec.ClusterIP == corev1.ClusterIPNone {
		t.Fatalf("setup: expected the API server to allocate a real ClusterIP, got %q", pre.Spec.ClusterIP)
	}

	r := &controller.NodeReconciler{Client: k8sClient, Config: svcConfig(id, cpLabel)}
	_, err := r.Reconcile(ctx, ctrl.Request{})
	if err == nil {
		t.Fatal("expected Reconcile to fail rather than attempt an invalid (immutable-field) update to a foreign non-headless Service")
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected an Invalid API error surfacing the immutable ClusterIP conflict, got: %v", err)
	}
}

func ownerRefTo(t *testing.T, refs []metav1.OwnerReference, svc *corev1.Service) metav1.OwnerReference {
	t.Helper()
	for _, ref := range refs {
		if ref.Kind == "Service" && ref.Name == svc.Name {
			return ref
		}
	}
	t.Fatalf("no OwnerReference to Service %q found in %+v", svc.Name, refs)
	return metav1.OwnerReference{}
}

// TestReconcile_EndpointSliceOwnerReference_PointsToService also proves the
// UID is correct on the very first reconcile -- not just after a second
// pass -- since applyServices fully completes its CreateOrUpdate
// round-trip (populating .UID from the API server) before
// BuildEndpointSlices/ownEndpointSlices run.
func TestReconcile_EndpointSliceOwnerReference_PointsToService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.5.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg) // single, first reconcile

	svc := getService(t, ctx, id)
	if svc.UID == "" {
		t.Fatal("setup: expected the API server to have assigned a UID to the Service")
	}
	es := getEndpointSlice(t, ctx, id+"-metrics")

	ref := ownerRefTo(t, es.OwnerReferences, svc)
	if ref.APIVersion != "v1" {
		t.Errorf("expected APIVersion %q, got %q", "v1", ref.APIVersion)
	}
	if ref.UID != svc.UID {
		t.Errorf("expected OwnerReference UID %q (the live Service's UID), got %q", svc.UID, ref.UID)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Errorf("expected Controller=true, got %v", ref.Controller)
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Errorf("expected BlockOwnerDeletion=true, got %v", ref.BlockOwnerDeletion)
	}
}

func TestReconcile_LegacyEndpointsOwnerReference_PointsToService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.6.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	eps := getLegacyEndpoints(t, ctx, id)

	ref := ownerRefTo(t, eps.OwnerReferences, svc)
	if ref.UID != svc.UID {
		t.Errorf("expected legacy Endpoints OwnerReference UID %q, got %q", svc.UID, ref.UID)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Errorf("expected Controller=true on legacy Endpoints ownerRef, got %v", ref.Controller)
	}
}

// TestReconcile_ServiceRecreatedExternally_OwnerReferenceUIDUpdated proves a
// stale UID left over from a deleted-and-recreated Service doesn't linger
// in the EndpointSlice's ownerReferences after the next reconcile -- since
// envtest runs no garbage-collector controller, cascading deletion itself
// can't be observed here, only that the UID is kept in sync.
func TestReconcile_ServiceRecreatedExternally_OwnerReferenceUIDUpdated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.7.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	oldUID := getService(t, ctx, id).UID

	if err := k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: testNamespace}}); err != nil {
		t.Fatalf("deleting Service: %v", err)
	}

	reconcile(t, ctx, cfg)

	newSvc := getService(t, ctx, id)
	if newSvc.UID == oldUID {
		t.Fatal("expected the recreated Service to get a new UID from the API server")
	}

	es := getEndpointSlice(t, ctx, id+"-metrics")
	ref := ownerRefTo(t, es.OwnerReferences, newSvc)
	if ref.UID == oldUID {
		t.Fatal("EndpointSlice ownerReference still points at the deleted Service's stale UID")
	}
	if ref.UID != newSvc.UID {
		t.Fatalf("expected ownerReference UID to track the recreated Service, got %q, want %q", ref.UID, newSvc.UID)
	}
}

// TestReconcile_MultipleServices_EachEndpointSliceOwnedByOwnService guards
// against a class of bug where all EndpointSlices end up pointing at one
// Service (e.g. an off-by-one, or a `svcs` map keyed wrong) instead of each
// at its own -- svcConfig only ever configures a single service, so every
// other test in this file couldn't have caught this.
func TestReconcile_MultipleServices_EachEndpointSliceOwnedByOwnService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.8.1", true, withExtraLabels(cpLabel))

	svcA, svcB := id+"-a", id+"-b"
	cfg := config.Config{
		Namespace:    testNamespace,
		NodeSelector: cpLabel,
		Services: []config.Service{
			{Name: svcA, PortName: "metrics", Port: 9001, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
			{Name: svcB, PortName: "metrics", Port: 9002, Protocol: corev1.ProtocolTCP, AppProtocol: "http"},
		},
	}
	reconcile(t, ctx, cfg)

	svcAObj := getService(t, ctx, svcA)
	svcBObj := getService(t, ctx, svcB)
	if svcAObj.UID == svcBObj.UID {
		t.Fatal("setup: expected distinct Services with distinct UIDs")
	}

	esA := getEndpointSlice(t, ctx, svcA+"-metrics")
	esB := getEndpointSlice(t, ctx, svcB+"-metrics")

	refA := ownerRefTo(t, esA.OwnerReferences, svcAObj)
	if refA.UID != svcAObj.UID {
		t.Errorf("EndpointSlice %s: expected ownerRef to svcA (%s), got UID %s", esA.Name, svcAObj.UID, refA.UID)
	}
	refB := ownerRefTo(t, esB.OwnerReferences, svcBObj)
	if refB.UID != svcBObj.UID {
		t.Errorf("EndpointSlice %s: expected ownerRef to svcB (%s), got UID %s", esB.Name, svcBObj.UID, refB.UID)
	}
}

// TestReconcile_Idempotent_ServiceNoAPIWriteOnUnchangedState is the Service
// counterpart to TestReconcile_Idempotent_NoAPIWriteOnUnchangedState: the
// new applyServices/ownEndpointSlices logic must not cause spurious writes
// (or UID churn) on a reconcile that changes nothing.
func TestReconcile_Idempotent_ServiceNoAPIWriteOnUnchangedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "n1-"+id, "10.40.9.1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)
	svc1 := getService(t, ctx, id)
	es1 := getEndpointSlice(t, ctx, id+"-metrics")

	reconcile(t, ctx, cfg) // identical state
	svc2 := getService(t, ctx, id)
	es2 := getEndpointSlice(t, ctx, id+"-metrics")

	if svc1.ResourceVersion != svc2.ResourceVersion {
		t.Errorf("expected no API write to Service (stable ResourceVersion): %s -> %s", svc1.ResourceVersion, svc2.ResourceVersion)
	}
	if es1.ResourceVersion != es2.ResourceVersion {
		t.Errorf("expected no API write to EndpointSlice on unchanged state (stable ResourceVersion): %s -> %s", es1.ResourceVersion, es2.ResourceVersion)
	}
}

// TestReconcile_DualStackNodes_BothSlicesOwnedBySameService proves the
// per-address-family split in BuildEndpointSlices doesn't break
// ownership: both the IPv4 and the "-ipv6" slice for one service must
// carry an ownerReference to the same single Service.
func TestReconcile_DualStackNodes_BothSlicesOwnedBySameService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "v4-"+id, "10.40.10.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "v6-"+id, "2001:db8::40:10:1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	svc := getService(t, ctx, id)
	v4 := getEndpointSlice(t, ctx, id+"-metrics")
	v6 := getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6")

	if v4.AddressType != discoveryv1.AddressTypeIPv4 {
		t.Fatalf("setup: expected the base slice to be IPv4, got %v", v4.AddressType)
	}
	if v6.AddressType != discoveryv1.AddressTypeIPv6 {
		t.Fatalf("setup: expected the -ipv6 slice to be IPv6, got %v", v6.AddressType)
	}

	if ref := ownerRefTo(t, v4.OwnerReferences, svc); ref.UID != svc.UID {
		t.Errorf("IPv4 slice: expected ownerRef UID %s, got %s", svc.UID, ref.UID)
	}
	if ref := ownerRefTo(t, v6.OwnerReferences, svc); ref.UID != svc.UID {
		t.Errorf("IPv6 slice: expected ownerRef UID %s, got %s", svc.UID, ref.UID)
	}
}
