//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func sliceAddresses(t *testing.T, ctx context.Context, name string) []string {
	t.Helper()
	var addrs []string
	for _, ep := range getEndpointSliceIn(t, ctx, testNamespace, name).Endpoints {
		addrs = append(addrs, ep.Addresses...)
	}
	return addrs
}

func legacyAddresses(t *testing.T, ctx context.Context, name string) []string {
	t.Helper()
	var addrs []string
	for _, s := range getLegacyEndpoints(t, ctx, name).Subsets {
		for _, a := range slices.Concat(s.Addresses, s.NotReadyAddresses) {
			addrs = append(addrs, a.IP)
		}
	}
	return addrs
}

func TestReconcile_DualStackNodes_EachInPrimaryFamilySliceOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "a-"+id, "10.60.0.1", true, withExtraLabels(cpLabel))
	setNodeInternalIPs(t, ctx, "a-"+id, "10.60.0.1", "2001:db8::60:1")
	createNode(t, ctx, "b-"+id, "2001:db8::60:2", true, withExtraLabels(cpLabel))
	setNodeInternalIPs(t, ctx, "b-"+id, "2001:db8::60:2", "10.60.0.2")

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	if got := sliceAddresses(t, ctx, id+"-metrics"); !slices.Equal(got, []string{"10.60.0.1"}) {
		t.Errorf("IPv4 slice: expected only the IPv4-primary node, got %v", got)
	}
	if got := sliceAddresses(t, ctx, id+"-metrics-ipv6"); !slices.Equal(got, []string{"2001:db8::60:2"}) {
		t.Errorf("IPv6 slice: expected only the IPv6-primary node, got %v", got)
	}
	if got := legacyAddresses(t, ctx, id); !slices.Equal(got, []string{"10.60.0.1", "2001:db8::60:2"}) {
		t.Errorf("legacy Endpoints: expected each node once at its primary InternalIP, got %v", got)
	}
}

func TestReconcile_NodeGainsReordersAndDropsIPv6_SlicesFollowPrimary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	name := "n1-" + id
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, name, "10.60.1.1", true, withExtraLabels(cpLabel))
	cfg := svcConfig(id, cpLabel)
	reconcile(t, ctx, cfg)

	setNodeInternalIPs(t, ctx, name, "10.60.1.1", "2001:db8::60:11")
	reconcile(t, ctx, cfg)
	if err := getEndpointSliceErr(ctx, id+"-metrics-ipv6"); !isNotFound(err) {
		t.Fatalf("expected no -ipv6 slice for a node whose secondary InternalIP is IPv6, got err=%v", err)
	}

	setNodeInternalIPs(t, ctx, name, "2001:db8::60:11", "10.60.1.1")
	reconcile(t, ctx, cfg)
	if got := sliceAddresses(t, ctx, id+"-metrics"); len(got) != 0 {
		t.Errorf("expected the IPv4 slice emptied once IPv6 became primary, got %v", got)
	}
	if got := sliceAddresses(t, ctx, id+"-metrics-ipv6"); !slices.Equal(got, []string{"2001:db8::60:11"}) {
		t.Errorf("expected the -ipv6 slice to carry the new primary, got %v", got)
	}

	setNodeInternalIPs(t, ctx, name, "10.60.1.1")
	reconcile(t, ctx, cfg)
	if err := getEndpointSliceErr(ctx, id+"-metrics-ipv6"); !isNotFound(err) {
		t.Fatalf("expected the -ipv6 slice pruned once the node went IPv4-only, got err=%v", err)
	}
	if got := sliceAddresses(t, ctx, id+"-metrics"); !slices.Equal(got, []string{"10.60.1.1"}) {
		t.Errorf("expected the node back in the IPv4 slice, got %v", got)
	}
}

func TestReconcile_UnparseableInternalIP_NodeUsesNextValidOneAndOthersConverge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "a-"+id, "10.60.2.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "b-"+id, "not-an-ip", true, withExtraLabels(cpLabel))
	setNodeInternalIPs(t, ctx, "b-"+id, "not-an-ip", "010.60.2.2", "10.60.2.2")
	createNode(t, ctx, "c-"+id, "not-an-ip", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	if got := sliceAddresses(t, ctx, id+"-metrics"); !slices.Equal(got, []string{"10.60.2.1", "10.60.2.2"}) {
		t.Errorf("expected the healthy node and b's first valid InternalIP, got %v", got)
	}
	if got := legacyAddresses(t, ctx, id); !slices.Equal(got, []string{"10.60.2.1", "10.60.2.2"}) {
		t.Errorf("legacy Endpoints: expected the healthy node and b's first valid InternalIP, got %v", got)
	}
}

// reversedNodeList stands in for the cache's arbitrary List order.
type reversedNodeList struct{ client.Client }

func (c reversedNodeList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.Client.List(ctx, list, opts...); err != nil {
		return err
	}
	if nl, ok := list.(*corev1.NodeList); ok {
		slices.Reverse(nl.Items)
	}
	return nil
}

// Uses the real API server so any write-time normalisation shows up as an
// echo write.
func TestReconcile_NodeListOrderChanged_EchoReconcileMakesNoWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	// Name order disagrees with both string and numeric IP order.
	createNode(t, ctx, "c-"+id, "10.60.3.9", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "a-"+id, "10.60.3.10", false, withExtraLabels(cpLabel))
	createNode(t, ctx, "d-"+id, "10.60.3.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "e-"+id, "2001:db8::60:9", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "b-"+id, "2001:db8::60:10", false, withExtraLabels(cpLabel))
	setNodeInternalIPs(t, ctx, "b-"+id, "2001:db8::60:10", "10.60.3.2")

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true

	counter := &allWritesCounter{}
	reconcileWithClients(t, ctx, cfg, countingClient{Client: k8sClient, counter: counter}, nil)
	if counter.total() == 0 {
		t.Fatal("setup: expected the first reconcile to write")
	}
	_ = getService(t, ctx, id)
	_ = getEndpointSlice(t, ctx, id+"-metrics")
	_ = getEndpointSliceIn(t, ctx, testNamespace, id+"-metrics-ipv6")
	_ = getLegacyEndpoints(t, ctx, id)

	counter.mu.Lock()
	counter.writes = map[string]int{}
	counter.mu.Unlock()
	reconcileWithClients(t, ctx, cfg, countingClient{Client: reversedNodeList{k8sClient}, counter: counter}, nil)

	if n := counter.total(); n != 0 {
		t.Fatalf("expected zero writes when only the node List order changed, got %d: %v", n, counter.writes)
	}
}

func TestReconcile_NonCanonicalInternalIPs_AcceptedByAPIServerAndStable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "a-"+id, "::ffff:10.60.4.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "b-"+id, "2001:DB8::60:4", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "c-"+id, "10.60.4.3", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	counter := &allWritesCounter{}
	spy := countingClient{Client: k8sClient, counter: counter}
	reconcileWithClients(t, ctx, cfg, spy, nil)

	if got := sliceAddresses(t, ctx, id+"-metrics"); !slices.Equal(got, []string{"10.60.4.1", "10.60.4.3"}) {
		t.Errorf("IPv4 slice: expected the unmapped and plain IPv4 nodes, got %v", got)
	}
	if got := sliceAddresses(t, ctx, id+"-metrics-ipv6"); !slices.Equal(got, []string{"2001:db8::60:4"}) {
		t.Errorf("IPv6 slice: expected the lowercased address, got %v", got)
	}
	if got := legacyAddresses(t, ctx, id); !slices.Equal(got, []string{"10.60.4.1", "2001:db8::60:4", "10.60.4.3"}) {
		t.Errorf("legacy Endpoints: expected canonical addresses in node-name order, got %v", got)
	}
	_ = getService(t, ctx, id)

	counter.mu.Lock()
	counter.writes = map[string]int{}
	counter.mu.Unlock()
	reconcileWithClients(t, ctx, cfg, spy, nil)
	if n := counter.total(); n != 0 {
		t.Fatalf("expected zero writes on the echo reconcile, got %d: %v", n, counter.writes)
	}
}

func TestReconcile_SpecialInternalIPs_SkippedAndOthersConverge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	id := testID(t)
	cpLabel := map[string]string{"role-" + id: "control-plane"}
	createNode(t, ctx, "a-"+id, "10.60.5.1", true, withExtraLabels(cpLabel))
	createNode(t, ctx, "b-"+id, "127.0.0.1", true, withExtraLabels(cpLabel))
	setNodeInternalIPs(t, ctx, "b-"+id, "127.0.0.1", "169.254.1.1", "::ffff:0.0.0.0", "fe80::1", "ff02::1", "10.60.5.2")
	createNode(t, ctx, "c-"+id, "::1", true, withExtraLabels(cpLabel))

	cfg := svcConfig(id, cpLabel)
	cfg.WriteLegacyEndpoints = true
	reconcile(t, ctx, cfg)

	if got := sliceAddresses(t, ctx, id+"-metrics"); !slices.Equal(got, []string{"10.60.5.1", "10.60.5.2"}) {
		t.Errorf("expected the healthy node and b's first usable InternalIP, got %v", got)
	}
	if got := legacyAddresses(t, ctx, id); !slices.Equal(got, []string{"10.60.5.1", "10.60.5.2"}) {
		t.Errorf("legacy Endpoints: expected the healthy node and b's first usable InternalIP, got %v", got)
	}
}
