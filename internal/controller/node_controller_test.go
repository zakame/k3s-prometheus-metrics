package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// nodeChangedPredicate gates which watch events actually trigger a
// reconcile. These are white-box tests (same package) because the
// predicate is unexported and only otherwise reachable by wiring up a full
// manager -- exercising it directly here is cheaper and more precise than
// an envtest round-trip for what is pure decision logic.

func baseNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"a": "1"}},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func TestNodeChangedPredicate_CreateAlwaysLetsThrough(t *testing.T) {
	if !nodeChangedPredicate.Create(event.CreateEvent{Object: baseNode("n1")}) {
		t.Fatal("expected Create events to always trigger reconcile")
	}
}

func TestNodeChangedPredicate_DeleteAlwaysLetsThrough(t *testing.T) {
	if !nodeChangedPredicate.Delete(event.DeleteEvent{Object: baseNode("n1")}) {
		t.Fatal("expected Delete events to always trigger reconcile")
	}
}

func TestNodeChangedPredicate_GenericAlwaysLetsThrough(t *testing.T) {
	if !nodeChangedPredicate.Generic(event.GenericEvent{Object: baseNode("n1")}) {
		t.Fatal("expected Generic events to always trigger reconcile")
	}
}

func TestNodeChangedPredicate_UpdateFiltersIrrelevantChanges(t *testing.T) {
	old := baseNode("n1")
	newNode := baseNode("n1")
	newNode.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: {}}
	newNode.ResourceVersion = "999"

	if nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newNode}) {
		t.Fatal("expected an update touching only capacity/resourceVersion to be filtered out")
	}
}

func TestNodeChangedPredicate_UpdateLetsThroughOnUnschedulableChange(t *testing.T) {
	old := baseNode("n1")
	newNode := baseNode("n1")
	newNode.Spec.Unschedulable = true

	if !nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newNode}) {
		t.Fatal("expected a cordon/uncordon change to trigger reconcile")
	}
}

func TestNodeChangedPredicate_UpdateLetsThroughOnReadyConditionChange(t *testing.T) {
	old := baseNode("n1")
	newNode := baseNode("n1")
	newNode.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}

	if !nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newNode}) {
		t.Fatal("expected a NodeReady status change to trigger reconcile")
	}
}

func TestNodeChangedPredicate_UpdateLetsThroughWhenReadyConditionAppears(t *testing.T) {
	old := baseNode("n1")
	old.Status.Conditions = nil // no NodeReady condition at all yet
	newNode := baseNode("n1")

	if !nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newNode}) {
		t.Fatal("expected the NodeReady condition first appearing to trigger reconcile")
	}
}

func TestNodeChangedPredicate_UpdateIgnoresReadyHeartbeatOnlyChange(t *testing.T) {
	old := baseNode("n1")
	newNode := baseNode("n1")
	// Same Status (True), only the heartbeat timestamp advances -- this is
	// exactly the noisy periodic update the predicate exists to filter.
	newNode.Status.Conditions[0].LastHeartbeatTime = metav1.Now()

	if nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newNode}) {
		t.Fatal("expected a heartbeat-only update (Ready status unchanged) to be filtered out")
	}
}

func TestNodeChangedPredicate_UpdateLetsThroughOnLabelChange(t *testing.T) {
	old := baseNode("n1")
	newNode := baseNode("n1")
	newNode.Labels = map[string]string{"a": "1", "node-role.kubernetes.io/control-plane": ""}

	if !nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newNode}) {
		t.Fatal("expected a label change to trigger reconcile (Reconcile lists nodes by label selector)")
	}
}

func TestNodeChangedPredicate_UpdateWithWrongObjectType_LetsThrough(t *testing.T) {
	// Defensive: if the watch ever delivered a non-Node object, fail open
	// (reconcile) rather than silently dropping a change we can't inspect.
	old := &corev1.Pod{}
	newPod := &corev1.Pod{}
	if !nodeChangedPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: newPod}) {
		t.Fatal("expected a non-Node object pair to fail open (let the update through)")
	}
}
