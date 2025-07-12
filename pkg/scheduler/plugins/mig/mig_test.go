package mig

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	instalisters "github.com/openshift/instaslice-operator/pkg/generated/listers/dasoperator/v1alpha1"
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
							corev1.ResourceName("mig.das.com/" + profile): resource.MustParse("1"),
						},
					},
				},
			},
		},
	}
}

func newCountPod(uid, profile string, count int) *corev1.Pod {
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
							corev1.ResourceName("mig.das.com/" + profile): resource.MustParse(strconv.Itoa(count)),
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
							corev1.ResourceName("mig.das.com/" + profile): resource.MustParse("1"),
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
								corev1.ResourceName("mig.das.com/" + profile): resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}
}

func newMultiContainerPod(uid string, profiles []string) *corev1.Pod {
	containers := make([]corev1.Container, len(profiles))
	for i, p := range profiles {
		containers[i] = corev1.Container{
			Name: fmt.Sprintf("c%d", i),
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceName("mig.das.com/" + p): resource.MustParse("1"),
				},
			},
		}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(uid),
			Name:      "pod-" + uid,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{Containers: containers},
	}
}

func newMultiProfilePod(uid string, profiles []string) *corev1.Pod {
	limits := corev1.ResourceList{}
	for _, p := range profiles {
		limits[corev1.ResourceName("mig.das.com/"+p)] = resource.MustParse("1")
	}
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
						Limits: limits,
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
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s/%s", spec.Nodename, spec.GPUUUID)
			return []string{key}, nil
		},
		"pod-uid": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			return []string{string(spec.PodRef.UID)}, nil
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

func TestGPUAllocatedSlicesNilIndexer(t *testing.T) {
	_, err := gpuAllocatedSlices(nil, "node1", "gpu1", "")
	if err == nil {
		t.Fatalf("expected error when indexer is nil")
	}
}

func TestFilter(t *testing.T) {
	cases := []struct {
		name     string
		setup    func() (*Plugin, *corev1.Pod, *framework.NodeInfo)
		expect   framework.Code
		validate func(*testing.T, *Plugin)
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
				spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
				raw1, _ := json.Marshal(&spec1)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
				spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
				raw2, _ := json.Marshal(&spec2)
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
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
			name: "multi profile container",
			// Single container specifies multiple profiles. Filter should
			// succeed when enough slices are free.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newMultiProfilePod("mp", []string{"1g.5gb", "2g.10gb"})
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
		},
		{
			name: "multi count",
			// Single container requests the same profile multiple times via the
			// extended resource quantity. Filter should create one claim per count.
			setup: func() (*Plugin, *corev1.Pod, *framework.NodeInfo) {
				inst := utils.GenerateFakeCapacity("node1")
				p := newPlugin(inst)
				pod := newCountPod("mc", "1g.5gb", 2)
				ni := framework.NewNodeInfo()
				ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
				return p, pod, ni
			},
			expect: framework.Success,
			validate: func(t *testing.T, p *Plugin) {
				allocs, _ := p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).List(context.Background(), metav1.ListOptions{})
				if len(allocs.Items) != 2 {
					t.Fatalf("expected 2 AllocationClaims, got %d", len(allocs.Items))
				}
			},
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
				spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
				raw1, _ := json.Marshal(&spec1)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
				spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
				raw2, _ := json.Marshal(&spec2)
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
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
				spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
				raw1, _ := json.Marshal(&spec1)
				ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
				spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node2"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
				raw2, _ := json.Marshal(&spec2)
				ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: node2.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
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
			if tc.validate != nil {
				tc.validate(t, p)
			}
		})
	}
}

func TestFilterValidCombinations(t *testing.T) {
	combos := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}

	parse := func(s string) []string {
		var profiles []string
		for _, part := range strings.Split(s, " + ") {
			fields := strings.SplitN(part, "x", 2)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			for i := 0; i < n; i++ {
				profiles = append(profiles, fields[1])
			}
		}
		return profiles
	}

	ctx := context.Background()
	for i, cmb := range combos {
		t.Run(fmt.Sprintf("combo-%d", i), func(t *testing.T) {
			inst := utils.GenerateFakeCapacity("node1")
			p := newPlugin(inst)
			pod := newMultiContainerPod(fmt.Sprintf("cmb-%d", i), parse(cmb))
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil {
				if st.Code() != framework.Success {
					t.Fatalf("expected success, got %v", st.Code())
				}
			}
		})
	}
}
func TestFilterMaxProfileCounts(t *testing.T) {
	cases := []struct {
		profile string
		max     int
	}{
		{"1g.5gb", 7},
		{"1g.5gb+me", 1},
		{"1g.10gb", 4},
		{"2g.10gb", 3},
		{"3g.20gb", 2},
		{"4g.20gb", 1},
		{"7g.40gb", 1},
	}
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			inst := utils.GenerateFakeCapacity("node1")
			p := newPlugin(inst)
			var profiles []string
			for i := 0; i < tc.max; i++ {
				profiles = append(profiles, tc.profile)
			}
			pod := newMultiContainerPod(tc.profile+"-max", profiles)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil && st.Code() != framework.Success {
				t.Fatalf("expected success, got %v", st.Code())
			}
		})
		t.Run(tc.profile+"-exceed", func(t *testing.T) {
			inst := utils.GenerateFakeCapacity("node1")
			var res instav1.DiscoveredNodeResources
			_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
			spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw1, _ := json.Marshal(&spec1)
			ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
			spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw2, _ := json.Marshal(&spec2)
			ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
			p := newPlugin(inst, ex1, ex2)
			var profiles []string
			for i := 0; i < tc.max+1; i++ {
				profiles = append(profiles, tc.profile)
			}
			pod := newMultiContainerPod(tc.profile+"-ex", profiles)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st == nil || st.Code() != framework.Unschedulable {
				if st == nil {
					t.Fatalf("expected unschedulable, got success")
				} else {
					t.Fatalf("expected unschedulable, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterInvalidCombinations(t *testing.T) {
	combos := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}
	parse := func(s string) []string {
		var profiles []string
		for _, part := range strings.Split(s, " + ") {
			fields := strings.SplitN(part, "x", 2)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			for i := 0; i < n; i++ {
				profiles = append(profiles, fields[1])
			}
		}
		return profiles
	}
	ctx := context.Background()
	for i, cmb := range combos {
		t.Run(fmt.Sprintf("inv-%d", i), func(t *testing.T) {
			inst := utils.GenerateFakeCapacity("node1")
			var res instav1.DiscoveredNodeResources
			_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
			spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw1, _ := json.Marshal(&spec1)
			ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
			spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw2, _ := json.Marshal(&spec2)
			ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
			p := newPlugin(inst, ex1, ex2)
			pod := newMultiContainerPod(fmt.Sprintf("inv-%d", i), parse(cmb))
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st == nil || st.Code() != framework.Unschedulable {
				if st == nil {
					t.Fatalf("expected unschedulable, got success")
				} else {
					t.Fatalf("expected unschedulable, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterValidCombinationsH100(t *testing.T) {
	base := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}
	replacer := strings.NewReplacer(
		"1g.5gb", "1g.10gb",
		"1g.5gb+me", "1g.10gb+me",
		"1g.10gb", "1g.20gb",
		"2g.10gb", "2g.20gb",
		"3g.20gb", "3g.40gb",
		"4g.20gb", "4g.40gb",
		"7g.40gb", "7g.80gb",
	)
	combos := make([]string, 0, len(base))
	for _, c := range base {
		combos = append(combos, replacer.Replace(c))
	}

	parse := func(s string) []string {
		var profiles []string
		for _, part := range strings.Split(s, " + ") {
			fields := strings.SplitN(part, "x", 2)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			for i := 0; i < n; i++ {
				profiles = append(profiles, fields[1])
			}
		}
		return profiles
	}

	ctx := context.Background()
	for i, cmb := range combos {
		t.Run(fmt.Sprintf("h100-combo-%d", i), func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH100PCIE80GB("node1")
			p := newPlugin(inst)
			pod := newMultiContainerPod(fmt.Sprintf("h100-cmb-%d", i), parse(cmb))
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil {
				if st.Code() != framework.Success {
					t.Fatalf("expected success, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterMaxProfileCountsH100(t *testing.T) {
	cases := []struct {
		profile string
		max     int
	}{
		{"1g.10gb", 7},
		{"1g.10gb+me", 1},
		{"1g.20gb", 4},
		{"2g.20gb", 3},
		{"3g.40gb", 2},
		{"4g.40gb", 1},
		{"7g.80gb", 1},
	}
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH100PCIE80GB("node1")
			p := newPlugin(inst)
			var profiles []string
			for i := 0; i < tc.max; i++ {
				profiles = append(profiles, tc.profile)
			}
			pod := newMultiContainerPod(tc.profile+"-max", profiles)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil && st.Code() != framework.Success {
				t.Fatalf("expected success, got %v", st.Code())
			}
		})
		t.Run(tc.profile+"-exceed", func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH100PCIE80GB("node1")
			var res instav1.DiscoveredNodeResources
			_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
			spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw1, _ := json.Marshal(&spec1)
			ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
			spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw2, _ := json.Marshal(&spec2)
			ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
			p := newPlugin(inst, ex1, ex2)
			var profiles []string
			for i := 0; i < tc.max+1; i++ {
				profiles = append(profiles, tc.profile)
			}
			pod := newMultiContainerPod(tc.profile+"-ex", profiles)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st == nil || st.Code() != framework.Unschedulable {
				if st == nil {
					t.Fatalf("expected unschedulable, got success")
				} else {
					t.Fatalf("expected unschedulable, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterInvalidCombinationsH100(t *testing.T) {
	base := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}
	replacer := strings.NewReplacer(
		"1g.5gb", "1g.10gb",
		"1g.5gb+me", "1g.10gb+me",
		"1g.10gb", "1g.20gb",
		"2g.10gb", "2g.20gb",
		"3g.20gb", "3g.40gb",
		"4g.20gb", "4g.40gb",
		"7g.40gb", "7g.80gb",
	)
	combos := make([]string, 0, len(base))
	for _, c := range base {
		combos = append(combos, replacer.Replace(c))
	}
	parse := func(s string) []string {
		var profiles []string
		for _, part := range strings.Split(s, " + ") {
			fields := strings.SplitN(part, "x", 2)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			for i := 0; i < n; i++ {
				profiles = append(profiles, fields[1])
			}
		}
		return profiles
	}
	ctx := context.Background()
	for i, cmb := range combos {
		t.Run(fmt.Sprintf("h100-inv-%d", i), func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH100PCIE80GB("node1")
			var res instav1.DiscoveredNodeResources
			_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
			spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw1, _ := json.Marshal(&spec1)
			ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
			spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw2, _ := json.Marshal(&spec2)
			ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
			p := newPlugin(inst, ex1, ex2)
			pod := newMultiContainerPod(fmt.Sprintf("h100-inv-%d", i), parse(cmb))
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st == nil || st.Code() != framework.Unschedulable {
				if st == nil {
					t.Fatalf("expected unschedulable, got success")
				} else {
					t.Fatalf("expected unschedulable, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterValidCombinationsH200(t *testing.T) {
	base := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}
	replacer := strings.NewReplacer(
		"1g.5gb", "1g.18gb",
		"1g.5gb+me", "1g.18gb+me",
		"1g.10gb", "1g.35gb",
		"2g.10gb", "2g.35gb",
		"3g.20gb", "3g.71gb",
		"4g.20gb", "4g.71gb",
		"7g.40gb", "7g.141gb",
	)
	combos := make([]string, 0, len(base))
	for _, c := range base {
		combos = append(combos, replacer.Replace(c))
	}
	parse := func(s string) []string {
		var profiles []string
		for _, part := range strings.Split(s, " + ") {
			fields := strings.SplitN(part, "x", 2)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			for i := 0; i < n; i++ {
				profiles = append(profiles, fields[1])
			}
		}
		return profiles
	}
	ctx := context.Background()
	for i, cmb := range combos {
		t.Run(fmt.Sprintf("h200-combo-%d", i), func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH200SXM5141GB("node1")
			p := newPlugin(inst)
			pod := newMultiContainerPod(fmt.Sprintf("h200-cmb-%d", i), parse(cmb))
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil {
				if st.Code() != framework.Success {
					t.Fatalf("expected success, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterMaxProfileCountsH200(t *testing.T) {
	cases := []struct {
		profile string
		max     int
	}{
		{"1g.18gb", 7},
		{"1g.18gb+me", 1},
		{"1g.35gb", 4},
		{"2g.35gb", 3},
		{"3g.71gb", 2},
		{"4g.71gb", 1},
		{"7g.141gb", 1},
	}
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH200SXM5141GB("node1")
			p := newPlugin(inst)
			var profiles []string
			for i := 0; i < tc.max; i++ {
				profiles = append(profiles, tc.profile)
			}
			pod := newMultiContainerPod(tc.profile+"-max", profiles)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil && st.Code() != framework.Success {
				t.Fatalf("expected success, got %v", st.Code())
			}
		})
		t.Run(tc.profile+"-exceed", func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH200SXM5141GB("node1")
			var res instav1.DiscoveredNodeResources
			_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
			spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw1, _ := json.Marshal(&spec1)
			ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
			spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw2, _ := json.Marshal(&spec2)
			ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
			p := newPlugin(inst, ex1, ex2)
			var profiles []string
			for i := 0; i < tc.max+1; i++ {
				profiles = append(profiles, tc.profile)
			}
			pod := newMultiContainerPod(tc.profile+"-ex", profiles)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st == nil || st.Code() != framework.Unschedulable {
				if st == nil {
					t.Fatalf("expected unschedulable, got success")
				} else {
					t.Fatalf("expected unschedulable, got %v", st.Code())
				}
			}
		})
	}
}

func TestFilterInvalidCombinationsH200(t *testing.T) {
	base := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}
	replacer := strings.NewReplacer(
		"1g.5gb", "1g.18gb",
		"1g.5gb+me", "1g.18gb+me",
		"1g.10gb", "1g.35gb",
		"2g.10gb", "2g.35gb",
		"3g.20gb", "3g.71gb",
		"4g.20gb", "4g.71gb",
		"7g.40gb", "7g.141gb",
	)
	combos := make([]string, 0, len(base))
	for _, c := range base {
		combos = append(combos, replacer.Replace(c))
	}
	parse := func(s string) []string {
		var profiles []string
		for _, part := range strings.Split(s, " + ") {
			fields := strings.SplitN(part, "x", 2)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			for i := 0; i < n; i++ {
				profiles = append(profiles, fields[1])
			}
		}
		return profiles
	}
	ctx := context.Background()
	for i, cmb := range combos {
		t.Run(fmt.Sprintf("h200-inv-%d", i), func(t *testing.T) {
			inst := utils.GenerateFakeCapacityH200SXM5141GB("node1")
			var res instav1.DiscoveredNodeResources
			_ = json.Unmarshal(inst.Status.NodeResources.Raw, &res)
			spec1 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[0].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw1, _ := json.Marshal(&spec1)
			ex1 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex1"}, Spec: runtime.RawExtension{Raw: raw1}}
			spec2 := instav1.AllocationClaimSpec{GPUUUID: res.NodeGPUs[1].GPUUUID, Nodename: types.NodeName("node1"), MigPlacement: instav1.Placement{Start: 0, Size: 8}}
			raw2, _ := json.Marshal(&spec2)
			ex2 := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: "ex2"}, Spec: runtime.RawExtension{Raw: raw2}}
			p := newPlugin(inst, ex1, ex2)
			pod := newMultiContainerPod(fmt.Sprintf("h200-inv-%d", i), parse(cmb))
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st == nil || st.Code() != framework.Unschedulable {
				if st == nil {
					t.Fatalf("expected unschedulable, got success")
				} else {
					t.Fatalf("expected unschedulable, got %v", st.Code())
				}
			}
		})
	}
}

// TestFilterWorksForAllGPUModels ensures that the plugin Filter works with
// NodeAccelerator objects for other supported GPUs.
func TestFilterWorksForAllGPUModels(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(string) *instav1.NodeAccelerator
		profile string
	}{
		{"a100-pcie-80", utils.GenerateFakeCapacityA100PCIE80GB, "1g.10gb"},
		{"a100-sxm4-40", utils.GenerateFakeCapacityA100SXM440GB, "1g.5gb"},
		{"a100-sxm4-80", utils.GenerateFakeCapacityA100SXM480GB, "1g.10gb"},
		{"h100-sxm5-80", utils.GenerateFakeCapacityH100SXM580GB, "1g.10gb"},
		{"h100-pcie-80", utils.GenerateFakeCapacityH100PCIE80GB, "1g.10gb"},
		{"h100-sxm5-94", utils.GenerateFakeCapacityH100SXM594GB, "1g.10gb"},
		{"h100-pcie-94", utils.GenerateFakeCapacityH100PCIE94GB, "1g.10gb"},
		{"h100-gh200-96", utils.GenerateFakeCapacityH100GH20096GB, "1g.10gb"},
		{"h200-sxm5-141", utils.GenerateFakeCapacityH200SXM5141GB, "1g.18gb"},
		{"a30-24", utils.GenerateFakeCapacityA30PCIE24GB, "1g.6gb"},
	}
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := tc.fn("node1")
			p := newPlugin(inst)
			pod := newTestPod(tc.name, tc.profile)
			ni := framework.NewNodeInfo()
			ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
			st := p.Filter(ctx, framework.NewCycleState(), pod, ni)
			if st != nil && st.Code() != framework.Success {
				t.Fatalf("expected success, got %v", st.Code())
			}
		})
	}
}

// syncAllocations copies all AllocationClaims from the fake client to the
// plugin indexer so that subsequent plugin operations see a consistent view.
func syncAllocations(ctx context.Context, p *Plugin) error {
	allocs, err := p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	objs := make([]interface{}, len(allocs.Items))
	for i := range allocs.Items {
		objs[i] = &allocs.Items[i]
	}
	return p.allocationIndexer.Replace(objs, "0")
}

// TestScheduleMultipleSliceTypes simulates scheduling many pods requesting
// different MIG slice profiles across multiple nodes. It exercises the Filter,
// Score and PreBind methods.
func TestScheduleMultipleSliceTypes(t *testing.T) {
	ctx := context.Background()

	// create three fake nodes with capacity
	nodes := []string{"node1", "node2", "node3"}
	var objects []runtime.Object
	for _, n := range nodes {
		objects = append(objects, utils.GenerateFakeCapacity(n))
	}

	p := newPlugin(objects...)

	nodeInfos := map[string]*framework.NodeInfo{}
	var gpuCount int
	for _, n := range nodes {
		ni := framework.NewNodeInfo()
		ni.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n, Labels: map[string]string{"nvidia.com/mig.capable": "true"}}})
		nodeInfos[n] = ni

		inst, err := p.instasliceLister.NodeAccelerators(p.namespace).Get(n)
		if err != nil {
			t.Fatalf("failed to get NodeAccelerator: %v", err)
		}
		var res instav1.DiscoveredNodeResources
		if err := json.Unmarshal(inst.Status.NodeResources.Raw, &res); err != nil {
			t.Fatalf("failed to unmarshal resources: %v", err)
		}
		gpuCount += len(res.NodeGPUs)
	}

	pods1g := gpuCount * 2
	pods2g := gpuCount * 2

	schedule := func(pod *corev1.Pod) error {
		cycle := framework.NewCycleState()
		candidates := map[string]int64{}

		for _, n := range nodes {
			st := p.Filter(ctx, cycle, pod, nodeInfos[n])
			if st != nil && st.Code() != framework.Success {
				continue
			}
			if err := syncAllocations(ctx, p); err != nil {
				return err
			}
			score, st := p.Score(ctx, cycle, pod, n)
			if st != nil && st.Code() != framework.Success {
				return fmt.Errorf("score failed: %v", st.Code())
			}
			candidates[n] = score
		}
		if len(candidates) == 0 {
			return fmt.Errorf("unschedulable")
		}

		// pick the highest scoring node
		bestNode := ""
		bestScore := int64(-1)
		for n, sc := range candidates {
			if sc > bestScore || bestNode == "" {
				bestScore = sc
				bestNode = n
			}
		}

		if err := syncAllocations(ctx, p); err != nil {
			return err
		}
		if st := p.PreBind(ctx, cycle, pod, bestNode); st != nil && st.Code() != framework.Success {
			return fmt.Errorf("prebind failed: %v", st.Code())
		}
		return syncAllocations(ctx, p)
	}

	// schedule pods requesting 1g.5gb slices
	for i := 0; i < pods1g; i++ {
		pod := newTestPod(fmt.Sprintf("1g-%d", i), "1g.5gb")
		if err := schedule(pod); err != nil {
			t.Fatalf("schedule 1g pod %s: %v", pod.Name, err)
		}
	}

	// schedule pods requesting 2g.10gb slices
	for i := 0; i < pods2g; i++ {
		pod := newTestPod(fmt.Sprintf("2g-%d", i), "2g.10gb")
		if err := schedule(pod); err != nil {
			t.Fatalf("schedule 2g pod %s: %v", pod.Name, err)
		}
	}

	// verify all AllocationClaims are in Created state
	allocs, err := p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list allocations: %v", err)
	}
	if len(allocs.Items) != pods1g+pods2g {
		t.Fatalf("expected %d allocations, got %d", pods1g+pods2g, len(allocs.Items))
	}
	for _, a := range allocs.Items {
		if a.Status.State != instav1.AllocationClaimStatusCreated {
			t.Fatalf("allocation %s not created", a.Name)
		}
	}

	// attempt another 2g pod - should be unschedulable on all nodes
	over := newTestPod("over", "2g.10gb")
	cycle := framework.NewCycleState()
	schedulable := false
	for _, n := range nodes {
		st := p.Filter(ctx, cycle, over, nodeInfos[n])
		if st == nil || st.Code() == framework.Success {
			schedulable = true
		}
		if err := syncAllocations(ctx, p); err != nil {
			t.Fatalf("sync allocations: %v", err)
		}
	}
	if schedulable {
		t.Fatalf("expected overcommit pod to be unschedulable")
	}
}
