/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"fmt"
	"sync"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	corelisters "k8s.io/client-go/listers/core/v1"
	utilcache "k8s.io/client-go/tools/cache"
)

type fakePodLister struct {
	pods []*v1.Pod
}

func (f *fakePodLister) List(_ labels.Selector) ([]*v1.Pod, error) {
	return f.pods, nil
}

func (f *fakePodLister) Pods(namespace string) corelisters.PodNamespaceLister {
	panic("not implemented")
}

type fakeNodeLister struct {
	nodes []*v1.Node
}

func (f *fakeNodeLister) List(_ labels.Selector) ([]*v1.Node, error) {
	return f.nodes, nil
}

func (f *fakeNodeLister) Get(name string) (*v1.Node, error) {
	for _, n := range f.nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return nil, fmt.Errorf("node %q not found", name)
}

func newNode(name, cpu, mem, storage, ephemeral string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse(cpu),
				v1.ResourceMemory:           resource.MustParse(mem),
				v1.ResourceStorage:          resource.MustParse(storage),
				v1.ResourceEphemeralStorage: resource.MustParse(ephemeral),
			},
		},
	}
}

func newPod(ns, name, node, cpu, mem, storage, ephemeral string, phase v1.PodPhase) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			UID:       types.UID(ns + "-" + name),
		},
		Spec: v1.PodSpec{
			NodeName: node,
			Containers: []v1.Container{
				{
					Name:  "c",
					Image: "pause",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:              resource.MustParse(cpu),
							v1.ResourceMemory:           resource.MustParse(mem),
							v1.ResourceStorage:          resource.MustParse(storage),
							v1.ResourceEphemeralStorage: resource.MustParse(ephemeral),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{Phase: phase},
	}
}

func n(name string) *v1.Node {
	return newNode(name, "4000m", "8Gi", "100Gi", "50Gi")
}

func qty(s string) int64 {
	q := resource.MustParse(s)
	return (&q).Value()
}

func TestNodeHandlers(t *testing.T) {
	rc := NewResourceCache()
	h := rc.ResourceEventHandlerForNode()

	n := newNode("worker1", "4000m", "8Gi", "100Gi", "50Gi")
	h.AddFunc(n)

	if got := len(rc.nodes); got != 1 {
		t.Fatalf("expected 1 node after add, got %d", got)
	}

	n2 := n.DeepCopy()
	n2.Status.Allocatable[v1.ResourceCPU] = resource.MustParse("2000m")
	n2.Status.Allocatable[v1.ResourceMemory] = resource.MustParse("6Gi")
	n2.Status.Allocatable[v1.ResourceStorage] = resource.MustParse("80Gi")
	n2.Status.Allocatable[v1.ResourceEphemeralStorage] = resource.MustParse("30Gi")

	h.UpdateFunc(n, n2)

	rc.RLock()
	alloc := rc.nodes["worker1"].Allocatable
	rc.RUnlock()

	if alloc.MilliCPU != 2000 {
		t.Errorf("CPU allocatable not updated: got %d, want %d", alloc.MilliCPU, 2000)
	}
	if alloc.Memory != qty("6Gi") {
		t.Errorf("Memory allocatable not updated: got %d, want %d", alloc.Memory, qty("6Gi"))
	}
	if alloc.Storage != qty("80Gi") {
		t.Errorf("Storage allocatable not updated: got %d, want %d", alloc.Storage, qty("80Gi"))
	}
	if alloc.EphemeralStorage != qty("30Gi") {
		t.Errorf("Ephemeral storage allocatable not updated: got %d, want %d", alloc.EphemeralStorage, qty("30Gi"))
	}

	h.DeleteFunc(n2)
	if got := len(rc.nodes); got != 0 {
		t.Fatalf("expected 0 nodes after delete, got %d", got)
	}
}

func TestPodLifecycleHandlers(t *testing.T) {
	rc := NewResourceCache()
	rc.ResourceEventHandlerForNode().AddFunc(n("nodeA"))
	ph := rc.ResourceEventHandlerForPod()

	type step struct {
		name           string
		oldPod         *v1.Pod
		newPod         *v1.Pod
		expect         LocalResource
		expectFitCheck *v1.Pod
		expectFit      bool
	}

	p1Pending := newPod("ns", "p1", "", "1000m", "1Gi", "10Gi", "5Gi", v1.PodPending)
	p1Bound := p1Pending.DeepCopy()
	p1Bound.Spec.NodeName = "nodeA"
	p1Bound.Status.Phase = v1.PodRunning

	p1Resized := p1Bound.DeepCopy()
	p1Resized.Spec.Containers[0].Resources.Requests[v1.ResourceEphemeralStorage] = resource.MustParse("8Gi")

	p1Succeeded := p1Resized.DeepCopy()
	p1Succeeded.Status.Phase = v1.PodSucceeded

	p1Failed := p1Resized.DeepCopy()
	p1Failed.Status.Phase = v1.PodFailed

	p2 := newPod("ns", "p2", "nodeA", "3000m", "6Gi", "80Gi", "40Gi", v1.PodRunning)

	steps := []step{
		{
			name:           "add pending pod, should not count towards usage",
			newPod:         p1Pending,
			expect:         LocalResource{},
			expectFitCheck: newPod("ns", "probe", "nodeA", "4000m", "7Gi", "90Gi", "40Gi", v1.PodRunning),
			expectFit:      true,
		},
		{
			name:   "bind pod to node",
			oldPod: p1Pending,
			newPod: p1Bound,
			expect: LocalResource{
				MilliCPU:         1000,
				Memory:           qty("1Gi"),
				Storage:          qty("10Gi"),
				EphemeralStorage: qty("5Gi"),
			},
		},
		{
			name:   "add second pod",
			newPod: p2,
			expect: LocalResource{
				MilliCPU:         4000,
				Memory:           qty("7Gi"),
				Storage:          qty("90Gi"),
				EphemeralStorage: qty("45Gi"),
			},
			expectFitCheck: newPod("ns", "too-big", "nodeA", "1000m", "1Mi", "20Gi", "10Gi", v1.PodRunning),
			expectFit:      false,
		},
		{
			name:   "resize first pod ephemeral storage",
			oldPod: p1Bound,
			newPod: p1Resized,
			expect: LocalResource{
				MilliCPU:         4000,
				Memory:           qty("7Gi"),
				Storage:          qty("90Gi"),
				EphemeralStorage: qty("48Gi"),
			},
		},
		{
			name:   "mark first pod as failed",
			oldPod: p1Resized,
			newPod: p1Failed,
			expect: LocalResource{
				MilliCPU:         3000,
				Memory:           qty("6Gi"),
				Storage:          qty("80Gi"),
				EphemeralStorage: qty("40Gi"),
			},
		},
		{
			name:   "delete failed pod",
			oldPod: p1Failed,
			newPod: nil, // handled as tombstone
			expect: LocalResource{
				MilliCPU:         3000,
				Memory:           qty("6Gi"),
				Storage:          qty("80Gi"),
				EphemeralStorage: qty("40Gi"),
			},
		},
	}

	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if s.oldPod == nil && s.newPod != nil {
				ph.AddFunc(s.newPod)
			} else if s.oldPod != nil && s.newPod != nil {
				ph.UpdateFunc(s.oldPod, s.newPod)
			} else if s.oldPod != nil && s.newPod == nil {
				tomb := utilcache.DeletedFinalStateUnknown{Key: s.oldPod.Namespace + "/" + s.oldPod.Name, Obj: s.oldPod}
				ph.DeleteFunc(tomb)
			}

			if s.expectFitCheck != nil {
				fits := rc.Fits("nodeA", s.expectFitCheck)
				if fits != s.expectFit {
					t.Fatalf("unexpected Fit() result after %q: got %v, want %v", s.name, fits, s.expectFit)
				}
			}

			rc.RLock()
			usage := rc.nodes["nodeA"].Requested
			rc.RUnlock()

			if usage != s.expect {
				t.Fatalf("unexpected resource usage after %q:\n got: %+v\nwant: %+v", s.name, usage, s.expect)
			}
		})
	}
}

func TestRebuildResourceCache(t *testing.T) {
	rc := NewResourceCache()

	nodes := []*v1.Node{
		n("nodeA"),
		n("nodeB"),
	}

	pods := []*v1.Pod{
		newPod("ns", "pod1", "nodeA", "1000m", "1Gi", "10Gi", "5Gi", v1.PodRunning),
		newPod("ns", "pod2", "nodeA", "500m", "512Mi", "5Gi", "2Gi", v1.PodSucceeded), // should be ignored
		newPod("ns", "pod3", "nodeB", "2000m", "2Gi", "20Gi", "10Gi", v1.PodRunning),
		newPod("ns", "pod4", "", "1000m", "1Gi", "5Gi", "3Gi", v1.PodPending),      // not scheduled, should be ignored
		newPod("ns", "pod5", "nodeB", "1000m", "1Gi", "10Gi", "5Gi", v1.PodFailed), // should be ignored
	}

	nodeLister := &fakeNodeLister{nodes: nodes}
	podLister := &fakePodLister{pods: pods}

	err := rc.Rebuild(nodeLister, podLister)
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}

	expected := map[string]LocalResource{
		"nodeA": {
			MilliCPU:         1000,
			Memory:           qty("1Gi"),
			Storage:          qty("10Gi"),
			EphemeralStorage: qty("5Gi"),
		},
		"nodeB": {
			MilliCPU:         2000,
			Memory:           qty("2Gi"),
			Storage:          qty("20Gi"),
			EphemeralStorage: qty("10Gi"),
		},
	}

	rc.RLock()
	defer rc.RUnlock()
	for node, want := range expected {
		got, ok := rc.nodes[node]
		if !ok {
			t.Errorf("expected node %s in cache, but not found", node)
			continue
		}
		if got.Requested != want {
			t.Errorf("resource usage mismatch for node %s:\n  got:  %+v\n  want: %+v", node, got.Requested, want)
		}
	}
}

func TestResourceCacheConcurrencySafety(t *testing.T) {
	rc := NewResourceCache()
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Start goroutines that add and delete nodes
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			node := n(fmt.Sprintf("node-%d", i))
			rc.ResourceEventHandlerForNode().AddFunc(node)
			if i%3 == 0 {
				rc.ResourceEventHandlerForNode().DeleteFunc(node)
			}
		}
	}()

	// Start goroutines that add and delete pods
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			pod := newPod("default", fmt.Sprintf("pod-%d", i), fmt.Sprintf("node-%d", i%100), "100m", "128Mi", "1Gi", "512Mi", v1.PodRunning)
			rc.ResourceEventHandlerForPod().AddFunc(pod)
			if i%4 == 0 {
				rc.ResourceEventHandlerForPod().DeleteFunc(pod)
			}
		}
	}()

	// Start goroutines that call Fits concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			nodeName := fmt.Sprintf("node-%d", i%100)
			pod := newPod("default", "probe", nodeName, "100m", "64Mi", "1Gi", "256Mi", v1.PodRunning)
			_ = rc.Fits(nodeName, pod)
		}
	}()

	// Wait for all goroutines to finish
	wg.Wait()
	close(stop)

	// If the test runs without panic or data race, it passes.
}
