package mig

import (
	"context"
	"encoding/json"
	"fmt"
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
	instIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	_ = instIndexer.Add(inst)
	allocIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"node-gpu": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.GPUUUID)
			return []string{key}, nil
		},
	})

	lister := instalisters.NewNodeAcceleratorLister(instIndexer)
	p := &Plugin{instaClient: client, namespace: inst.Namespace, instasliceLister: lister, allocationIndexer: allocIndexer}
	pod := newTestPod("p1", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st != nil && !st.IsSuccess() {
		t.Fatalf("unexpected status: %v", st)
	}

	allocs, err := client.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list allocations: %v", err)
	}
	if len(allocs.Items) != 1 {
		t.Fatalf("expected one allocation, got %d", len(allocs.Items))
	}
	alloc := allocs.Items[0]
	if alloc.Spec.GPUUUID == "" {
		t.Fatalf("allocation not populated")
	}
	if alloc.Status != instav1.AllocationClaimStatusCreated {
		t.Fatalf("expected allocation status created, got %s", alloc.Status)
	}
}

func TestPreBindUnschedulable(t *testing.T) {
	ctx := context.Background()
	inst := utils.GenerateFakeCapacity("node1")
	var resources instav1.DiscoveredNodeResources
	_ = json.Unmarshal(inst.Status.NodeResources.Raw, &resources)
	existing := &instav1.AllocationClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "existing"},
		Spec: instav1.AllocationClaimSpec{
			GPUUUID:      resources.NodeGPUs[0].GPUUUID,
			Nodename:     types.NodeName("node1"),
			MigPlacement: instav1.Placement{Start: 0, Size: 8},
		},
	}
	client := fakeclient.NewSimpleClientset(inst, existing)

	instIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	_ = instIndexer.Add(inst)
	allocIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"node-gpu": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.GPUUUID)
			return []string{key}, nil
		},
	})
	_ = allocIndexer.Add(existing)

	lister := instalisters.NewNodeAcceleratorLister(instIndexer)
	p := &Plugin{instaClient: client, namespace: inst.Namespace, instasliceLister: lister, allocationIndexer: allocIndexer}
	pod := newTestPod("p2", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st != nil && !st.IsSuccess() {
		t.Fatalf("unexpected status: %v", st)
	}
	allocs, err := client.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list allocations: %v", err)
	}
	if len(allocs.Items) != 2 {
		t.Fatalf("expected two allocations, got %d", len(allocs.Items))
	}
	var created *instav1.AllocationClaim
	for i := range allocs.Items {
		if allocs.Items[i].Name == "p2" {
			created = &allocs.Items[i]
			break
		}
	}
	if created == nil {
		t.Fatalf("new allocation not found")
	}
	if created.Status != instav1.AllocationClaimStatusCreated {
		t.Fatalf("expected new allocation status created, got %s", created.Status)
	}
}

func TestPreBindInstasliceNotFound(t *testing.T) {
	ctx := context.Background()
	client := fakeclient.NewSimpleClientset() // no objects
	instIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	allocIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"node-gpu": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.GPUUUID)
			return []string{key}, nil
		},
	})
	lister := instalisters.NewNodeAcceleratorLister(instIndexer)
	p := &Plugin{instaClient: client, namespace: "instaslice-system", instasliceLister: lister, allocationIndexer: allocIndexer}
	pod := newTestPod("p3", "1g.5gb")

	st := p.PreBind(ctx, framework.NewCycleState(), pod, "node1")
	if st == nil || st.Code() != framework.Error {
		t.Fatalf("expected error status when instaslice missing, got %v", st)
	}
	allocs, err := client.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list allocations: %v", err)
	}
	if len(allocs.Items) != 0 {
		t.Fatalf("expected no allocations, got %d", len(allocs.Items))
	}
}

func TestGPUAllocatedSlicesNilIndexer(t *testing.T) {
	_, err := gpuAllocatedSlices(nil, "node1", "gpu1")
	if err == nil {
		t.Fatalf("expected error when indexer is nil")
	}
}
