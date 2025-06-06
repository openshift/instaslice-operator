package gpu

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	"github.com/openshift/instaslice-operator/test/utils"
)

func newTestPod(uid, profile string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Name:      "pod-" + uid,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "c1",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/mig-" + profile): resource.MustParse("1"),
						},
					},
				},
			},
		},
	}
}

func TestPreBindAllocatesGPU(t *testing.T) {
	ctx := context.Background()
	inst := utils.GenerateFakeCapacity("node1")
	client := fakeclient.NewSimpleClientset(inst)

	p := &Plugin{instaClient: client, namespace: inst.Namespace}
	pod := newTestPod("p1", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st != nil && !st.IsSuccess() {
		t.Fatalf("unexpected status: %v", st)
	}

	updated, err := client.OpenShiftOperatorV1alpha1().Instaslices(inst.Namespace).Get(ctx, "node1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get instaslice: %v", err)
	}

	if updated.Spec.PodAllocationRequests == nil || len(*updated.Spec.PodAllocationRequests) != 1 {
		t.Fatalf("expected one allocation request, got %v", updated.Spec.PodAllocationRequests)
	}
	if _, ok := (*updated.Spec.PodAllocationRequests)[pod.UID]; !ok {
		t.Fatalf("allocation request for pod not found")
	}
	if updated.Status.PodAllocationResults == nil || len(updated.Status.PodAllocationResults) != 1 {
		t.Fatalf("expected one allocation result, got %v", updated.Status.PodAllocationResults)
	}
	res := updated.Status.PodAllocationResults[string(pod.UID)]
	if res.GPUUUID == "" {
		t.Fatalf("allocation result not populated")
	}
}

func TestPreBindUnschedulable(t *testing.T) {
	ctx := context.Background()
	inst := utils.GenerateFakeCapacity("node1")
	inst.Status.PodAllocationResults["existing"] = instav1.AllocationResult{
		MigPlacement: instav1.Placement{Start: 0, Size: 8},
		GPUUUID:      inst.Status.NodeResources.NodeGPUs[0].GPUUUID,
		Nodename:     "node1",
		AllocationStatus: instav1.AllocationStatus{
			AllocationStatusController: string(instav1.AllocationStatusCreated),
		},
		ConfigMapResourceIdentifier: types.UID("dummy"),
	}
	client := fakeclient.NewSimpleClientset(inst)
	p := &Plugin{instaClient: client, namespace: inst.Namespace}
	pod := newTestPod("p2", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st == nil || st.Code() != framework.Unschedulable {
		t.Fatalf("expected unschedulable status, got %v", st)
	}
}

func TestPreBindInstasliceNotFound(t *testing.T) {
	ctx := context.Background()
	client := fakeclient.NewSimpleClientset() // no objects
	p := &Plugin{instaClient: client, namespace: "instaslice-system"}
	pod := newTestPod("p3", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st == nil || st.Code() != framework.Error {
		t.Fatalf("expected error status when instaslice missing, got %v", st)
	}
}
