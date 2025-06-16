package deviceplugins

import (
	"context"
	"testing"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUpdateAllocationStatusSetsCondition(t *testing.T) {
	alloc := &instav1.AllocationClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: "das-operator"},
	}
	client := fakeclient.NewSimpleClientset(alloc)

	updated, err := UpdateAllocationStatus(context.Background(), client, alloc, instav1.AllocationClaimStatusInUse)
	if err != nil {
		t.Fatalf("UpdateAllocationStatus returned error: %v", err)
	}
	if updated.Status.State != instav1.AllocationClaimStatusInUse {
		t.Fatalf("state not updated, got %s", updated.Status.State)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, "State")
	if cond == nil {
		t.Fatalf("expected State condition to be set")
	}
	if cond.Status != metav1.ConditionTrue || cond.Reason != string(instav1.AllocationClaimStatusInUse) {
		t.Fatalf("unexpected condition %+v", cond)
	}
}

func TestEmulatedDiscoverySetsReadyCondition(t *testing.T) {
	nodeName := "node-test"
	client := fakeclient.NewSimpleClientset()
	d := &EmulatedMigGpuDiscoverer{
		ctx:         context.Background(),
		nodeName:    nodeName,
		instaClient: client.OpenShiftOperatorV1alpha1().NodeAccelerators(instasliceNamespace),
	}
	inst, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	cond := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatalf("expected Ready condition to be set")
	}
	if cond.Status != metav1.ConditionTrue || cond.Reason != "GPUsAccessible" {
		t.Fatalf("unexpected condition %+v", cond)
	}
}

func TestAllocationStatusConditionOrder(t *testing.T) {
	alloc := &instav1.AllocationClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: "das-operator"},
	}
	client := fakeclient.NewSimpleClientset(alloc)

	updated, err := UpdateAllocationStatus(context.Background(), client, alloc, instav1.AllocationClaimStatusCreated)
	if err != nil {
		t.Fatalf("update to created returned error: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, "State")
	if cond == nil || cond.Reason != string(instav1.AllocationClaimStatusCreated) {
		t.Fatalf("unexpected condition after created: %+v", cond)
	}

	updated, err = UpdateAllocationStatus(context.Background(), client, updated, instav1.AllocationClaimStatusProcessing)
	if err != nil {
		t.Fatalf("update to processing returned error: %v", err)
	}
	cond = meta.FindStatusCondition(updated.Status.Conditions, "State")
	if cond == nil || cond.Reason != string(instav1.AllocationClaimStatusProcessing) {
		t.Fatalf("unexpected condition after processing: %+v", cond)
	}

	updated, err = UpdateAllocationStatus(context.Background(), client, updated, instav1.AllocationClaimStatusInUse)
	if err != nil {
		t.Fatalf("update to inUse returned error: %v", err)
	}
	cond = meta.FindStatusCondition(updated.Status.Conditions, "State")
	if cond == nil || cond.Reason != string(instav1.AllocationClaimStatusInUse) {
		t.Fatalf("unexpected condition after inUse: %+v", cond)
	}
}
