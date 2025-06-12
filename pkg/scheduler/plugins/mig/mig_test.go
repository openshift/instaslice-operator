package mig

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
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

func newInitContainerPod(uid, profile string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Name:      "pod-" + uid,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{
					Name: "init",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/mig-" + profile): resource.MustParse("1"),
						},
					},
				},
			},
			Containers: []corev1.Container{{Name: "c1"}},
		},
	}
}

func newEphemeralContainerPod(uid, profile string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Name:      "pod-" + uid,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1"}},
			EphemeralContainers: []corev1.EphemeralContainer{
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{
						Name:  "debug",
						Image: "busybox",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceName("nvidia.com/mig-" + profile): resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}
}

func newNoProfilePod(uid string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Name:      "pod-" + uid,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c1"}},
		},
	}
}

func newPlugin(objs ...runtime.Object) *Plugin {
	instIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	allocIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"node-gpu": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.GPUUUID)
			return []string{key}, nil
		},
	})
	ns := "das-operator"
	for _, obj := range objs {
		switch o := obj.(type) {
		case *instav1.NodeAccelerator:
			_ = instIndexer.Add(o)
			ns = o.Namespace
		case *instav1.AllocationClaim:
			_ = allocIndexer.Add(o)
		}
	}
	lister := instalisters.NewNodeAcceleratorLister(instIndexer)
	client := fakeclient.NewSimpleClientset(objs...)
	return &Plugin{
		instaClient:       client,
		namespace:         ns,
		instasliceLister:  lister,
		allocationIndexer: allocIndexer,
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
	p := &Plugin{instaClient: client, namespace: "das-operator", instasliceLister: lister, allocationIndexer: allocIndexer}
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

func TestFilter(t *testing.T) {
	cases := []struct {
		name   string
		setup  func() (*Plugin, *corev1.Pod, *framework.NodeInfo)
		expect framework.Code
	}{
		{
			name: "success",
			// Single node with free MIG slices. The pod requests one
			// 1g.5gb slice which is available so scheduling
			// succeeds.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newTestPod("s", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
		},
		{
			name: "unschedulable",
			// All GPUs on the node are already allocated. The pod
			// still requests a slice so Filter should return
			// Unschedulable.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				var res instav1.DiscoveredNodeResources
				_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				p := newPlugin(inst, ex1, ex2)
				pod := newTestPod("u", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Unschedulable,
		},
		{
			name: "no label",
			// Node lacks the mig.capable label. The plugin must
			// reject it.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newTestPod("nl", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}})
				return p, pod, ni
			},
			expect: framework.Unschedulable,
		},
		{
			name: "init container",
			// Pod requests the profile from an init container. The
			// node has capacity so the pod should be allowed.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newInitContainerPod("ic", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
		},
		{
			name: "ephemeral container",
			// Pod requests the profile through an ephemeral
			// container. Filter should parse the request and find a
			// free slice.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newEphemeralContainerPod("ec", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
		},
		{
			name: "node not found",
			// NodeInfo is missing a Node object so Filter returns
			// an error code.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newTestPod("nf", "1g.5gb")
				ni := framework.NewNodeInfo()
				return p, pod, ni
			},
			expect: framework.Error,
		},
		{
			name: "lister error",
			// Lister has no NodeAccelerator object which results in
			// Unschedulable status.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				p := newPlugin() // client has no object so lister fails
				pod := newTestPod("le", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Unschedulable,
		},
		{
			name: "unsupported accelerator",
			// Node uses a different accelerator type; pod requests a
			// MIG profile so scheduling should fail.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				inst.Spec.AcceleratorType = "foo"
				p := newPlugin(inst)
				pod := newTestPod("ua", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Unschedulable,
		},
		{
			name: "unmarshal error",
			// Node resources JSON is corrupted leading to an error
			// when Filter tries to parse it.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				inst.Status.NodeResources.Raw = []byte("{invalid}")
				p := newPlugin(inst)
				pod := newTestPod("um", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Error,
		},
		{
			name: "no profiles",
			// Pod does not request any MIG profiles. Filter should
			// allow it without further checks.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newNoProfilePod("np")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
		},
		{
			name: "indexer nil",
			// Allocation indexer is nil which should produce an
			// error status.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				p.allocationIndexer = nil
				pod := newTestPod("ai", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Error,
		},
		{
			name: "multi node success",
			// Two nodes exist and only node1 has free slices. Filter
			// should succeed when evaluating node1.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				node1 := utils.GenerateFakeCapacity("node1")
				node2 := utils.GenerateFakeCapacity("node2")
				var res instav1.DiscoveredNodeResources
				_ = json.Unmarshal(node2.Status.NodeResources.Raw, &res)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex1"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex2"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				p := newPlugin(node1, node2, ex1, ex2)
				pod := newTestPod("mn1", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
		},
		{
			name: "multi node unschedulable",
			// Evaluating node2 which has all GPUs allocated should
			// return Unschedulable while node1 remains unused.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				node1 := utils.GenerateFakeCapacity("node1")
				node2 := utils.GenerateFakeCapacity("node2")
				var res instav1.DiscoveredNodeResources
				_ = json.Unmarshal(node2.Status.NodeResources.Raw, &res)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex1"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex2"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				p := newPlugin(node1, node2, ex1, ex2)
				pod := newTestPod("mn2", "1g.5gb")
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Unschedulable,
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, pod, ni := tc.setup()
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			code := framework.Success
			if st != nil {
				code = st.Code()
			}
			if code != tc.expect {
				t.Fatalf("expected %v, got %v", tc.expect, code)
			}
		})
	}
}

func TestScore(t *testing.T) {
	cases := []struct {
		name   string
		setup  func() (*Plugin, *corev1.Pod, string)
		expect int64
		status framework.Code
	}{
		{
			name: "basic",
			// Node has two GPUs with free slices. Score should
			// report two available placements for the requested
			// profile.
			setup: func() (*Plugin, *corev1.Pod, string) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newTestPod("s", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 2,
			status: framework.Success,
		},
		{
			name: "init container",
			// Profile request comes from an init container. Score
			// should count available GPUs just like for regular
			// containers.
			setup: func() (*Plugin, *corev1.Pod, string) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newInitContainerPod("ic", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 2,
			status: framework.Success,
		},
		{
			name: "ephemeral container",
			// Ephemeral container requests the profile. The node
			// has capacity so score remains 2.
			setup: func() (*Plugin, *corev1.Pod, string) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newEphemeralContainerPod("ec", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 2,
			status: framework.Success,
		},
		{
			name: "no profiles",
			// Pod does not ask for any MIG profile. Score returns 0
			// because there is nothing to evaluate.
			setup: func() (*Plugin, *corev1.Pod, string) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newNoProfilePod("np")
				return p, pod, "node1"
			},
			expect: 0,
			status: framework.Success,
		},
		{
			name: "lister error",
			// NodeAccelerator object is missing which results in an
			// error when computing the score.
			setup: func() (*Plugin, *corev1.Pod, string) {
				p := newPlugin() // lister missing node
				pod := newTestPod("le", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 0,
			status: framework.Error,
		},
		{
			name: "unmarshal error",
			// Corrupted NodeResources field should surface as an
			// error status.
			setup: func() (*Plugin, *corev1.Pod, string) {
				inst := utils.GenerateFakeCapacity("node1")
				inst.Status.NodeResources.Raw = []byte("{bad}")
				p := newPlugin(inst)
				pod := newTestPod("ue", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 0,
			status: framework.Error,
		},
		{
			name: "indexer nil",
			// Nil allocation indexer prevents slice counting and
			// should return an error.
			setup: func() (*Plugin, *corev1.Pod, string) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				p.allocationIndexer = nil
				pod := newTestPod("ai", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 0,
			status: framework.Error,
		},
		{
			name: "multi node node1",
			// Node1 has free slices while node2 is fully allocated.
			// Scoring node1 should yield 2 available GPUs.
			setup: func() (*Plugin, *corev1.Pod, string) {
				node1 := utils.GenerateFakeCapacity("node1")
				node2 := utils.GenerateFakeCapacity("node2")
				var res instav1.DiscoveredNodeResources
				_ = json.Unmarshal(node2.Status.NodeResources.Raw, &res)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex1"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex2"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				p := newPlugin(node1, node2, ex1, ex2)
				pod := newTestPod("mn1", "1g.5gb")
				return p, pod, "node1"
			},
			expect: 2,
			status: framework.Success,
		},
		{
			name: "multi node node2",
			// Node2 GPUs are fully used, so score for node2 should
			// be zero.
			setup: func() (*Plugin, *corev1.Pod, string) {
				node1 := utils.GenerateFakeCapacity("node1")
				node2 := utils.GenerateFakeCapacity("node2")
				var res instav1.DiscoveredNodeResources
				_ = json.Unmarshal(node2.Status.NodeResources.Raw, &res)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex1"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex2"}, Spec: instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}}
				p := newPlugin(node1, node2, ex1, ex2)
				pod := newTestPod("mn2", "1g.5gb")
				return p, pod, "node2"
			},
			expect: 0,
			status: framework.Success,
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, pod, node := tc.setup()
			score, st := p.Score(ctx, framework.NewCycleState(), pod, node)
			code := framework.Success
			if st != nil {
				code = st.Code()
			}
			if code != tc.status {
				t.Fatalf("expected status %v, got %v", tc.status, code)
			}
			if score != tc.expect {
				t.Fatalf("expected score %d, got %d", tc.expect, score)
			}
		})
	}
}
