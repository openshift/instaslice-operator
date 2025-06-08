package gpu

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	instalisters "github.com/openshift/instaslice-operator/pkg/generated/listers/instasliceoperator/v1alpha1"
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
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	_ = indexer.Add(inst)

	lister := instalisters.NewInstasliceLister(indexer)
	p := &Plugin{instaClient: client, namespace: inst.Namespace, instasliceLister: lister}
	pod := newTestPod("p1", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st != nil && !st.IsSuccess() {
		t.Fatalf("unexpected status: %v", st)
	}

	allocs, err := client.OpenShiftOperatorV1alpha1().Allocations(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list allocations: %v", err)
	}
	if len(allocs.Items) != 1 {
		t.Fatalf("expected one allocation, got %d", len(allocs.Items))
	}
	if allocs.Items[0].Spec.GPUUUID == "" {
		t.Fatalf("allocation not populated")
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
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	_ = indexer.Add(inst)
	lister := instalisters.NewInstasliceLister(indexer)
	p := &Plugin{instaClient: client, namespace: inst.Namespace, instasliceLister: lister}
	pod := newTestPod("p2", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st == nil || st.Code() != framework.Unschedulable {
		t.Fatalf("expected unschedulable status, got %v", st)
	}
	allocs, err := client.OpenShiftOperatorV1alpha1().Allocations(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list allocations: %v", err)
	}
	if len(allocs.Items) != 0 {
		t.Fatalf("expected no allocations, got %d", len(allocs.Items))
	}
}

func TestPreBindInstasliceNotFound(t *testing.T) {
	ctx := context.Background()
	client := fakeclient.NewSimpleClientset() // no objects
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	lister := instalisters.NewInstasliceLister(indexer)
	p := &Plugin{instaClient: client, namespace: "instaslice-system", instasliceLister: lister}
	pod := newTestPod("p3", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st == nil || st.Code() != framework.Error {
		t.Fatalf("expected error status when instaslice missing, got %v", st)
	}
	allocs, err := client.OpenShiftOperatorV1alpha1().Allocations(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list allocations: %v", err)
	}
	if len(allocs.Items) != 0 {
		t.Fatalf("expected no allocations, got %d", len(allocs.Items))
	}
}
