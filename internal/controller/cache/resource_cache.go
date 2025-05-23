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
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
	utilcache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	kuResource "k8s.io/kubectl/pkg/util/resource"
)

// LocalResource represents CPU and memory in basic units
type LocalResource struct {
	MilliCPU         int64 // in millicores
	Memory           int64 // in bytes
	Storage          int64 // in bytes
	EphemeralStorage int64 // in bytes
}

// LocalNodeInfo mimics scheduler.framework.NodeInfo for our resource tracking
type LocalNodeInfo struct {
	Requested   LocalResource
	Allocatable LocalResource
}

type ResourceCache struct {
	sync.RWMutex
	nodes map[string]*LocalNodeInfo
}

func NewResourceCache() *ResourceCache {
	return &ResourceCache{nodes: map[string]*LocalNodeInfo{}}
}

func (c *ResourceCache) ResourceEventHandlerForNode() utilcache.ResourceEventHandlerFuncs {
	return utilcache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			n := obj.(*v1.Node)
			c.Lock()
			defer c.Unlock()

			alloc := convertToLocalResource(n.Status.Allocatable)
			c.nodes[n.Name] = &LocalNodeInfo{
				Allocatable: alloc,
			}
			klog.V(1).Infof("Node added: %s Allocatable: CPU=%v Mem=%v Storage=%v EphemeralStorage=%v",
				n.Name, alloc.MilliCPU, alloc.Memory, alloc.Storage, alloc.EphemeralStorage)
		},

		UpdateFunc: func(_, newObj interface{}) {
			n := newObj.(*v1.Node)
			c.Lock()
			defer c.Unlock()
			if ni, ok := c.nodes[n.Name]; ok {
				alloc := convertToLocalResource(n.Status.Allocatable)
				ni.Allocatable = alloc
				klog.V(2).Infof("Node updated: %s", n.Name)
			}
		},

		DeleteFunc: func(obj interface{}) {
			n := obj.(*v1.Node)
			c.Lock()
			delete(c.nodes, n.Name)
			c.Unlock()
			klog.V(1).Infof("Node deleted: %s", n.Name)
		},
	}
}

func (c *ResourceCache) ResourceEventHandlerForPod() utilcache.ResourceEventHandlerFuncs {
	return utilcache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			p := obj.(*v1.Pod)
			if p.Spec.NodeName != "" {
				c.addPodToNode(p)
			}
		},

		UpdateFunc: func(oldObj, newObj interface{}) {
			oldP := oldObj.(*v1.Pod)
			newP := newObj.(*v1.Pod)

			if oldP.Spec.NodeName != newP.Spec.NodeName {
				if oldP.Spec.NodeName != "" {
					c.removePodFromNode(oldP)
				}
				if eligible(newP) {
					c.addPodToNode(newP)
				}
				return
			}

			if oldP.Spec.NodeName != "" &&
				((oldP.DeletionTimestamp == nil && newP.DeletionTimestamp != nil) ||
					newP.Status.Phase == v1.PodSucceeded || newP.Status.Phase == v1.PodFailed) {
				c.removePodFromNode(oldP)
				return
			}

			if eligible(newP) && !equalRequests(oldP, newP) {
				c.removePodFromNode(oldP)
				c.addPodToNode(newP)
			}
		},

		DeleteFunc: func(obj interface{}) {
			if p, ok := getPod(obj); ok && p.Spec.NodeName != "" {
				// Skip deletion if pod has already been cleaned up due to terminal phase
				if p.Status.Phase == v1.PodSucceeded || p.Status.Phase == v1.PodFailed {
					klog.V(5).Infof("Skipping delete for already-finished pod: %s/%s", p.Namespace, p.Name)
					return
				}
				c.removePodFromNode(p)
			}
		},
	}
}

func getPod(obj interface{}) (*v1.Pod, bool) {
	switch t := obj.(type) {
	case *v1.Pod:
		return t, true
	case utilcache.DeletedFinalStateUnknown:
		if pod, ok := t.Obj.(*v1.Pod); ok {
			return pod, true
		}
		return nil, false
	default:
		return nil, false
	}
}

func eligible(p *v1.Pod) bool {
	return p.Spec.NodeName != "" && p.DeletionTimestamp == nil &&
		p.Status.Phase != v1.PodSucceeded && p.Status.Phase != v1.PodFailed
}

func equalRequests(a, b *v1.Pod) bool {
	ar, _ := kuResource.PodRequestsAndLimits(a)
	br, _ := kuResource.PodRequestsAndLimits(b)
	return ar.Cpu().Cmp(*br.Cpu()) == 0 &&
		ar.Memory().Cmp(*br.Memory()) == 0 &&
		ar.Storage().Cmp(*br.Storage()) == 0 &&
		ar.StorageEphemeral().Cmp(*br.StorageEphemeral()) == 0
}

func (c *ResourceCache) addPodToNode(p *v1.Pod) {
	c.Lock()
	defer c.Unlock()
	if ni, ok := c.nodes[p.Spec.NodeName]; ok {
		req, _ := kuResource.PodRequestsAndLimits(p)
		ni.Requested.MilliCPU += req.Cpu().MilliValue()
		ni.Requested.Memory += req.Memory().Value()
		ni.Requested.Storage += req.Storage().Value()
		ni.Requested.EphemeralStorage += req.StorageEphemeral().Value()
	}
}

func (c *ResourceCache) removePodFromNode(p *v1.Pod) {
	c.Lock()
	defer c.Unlock()
	if ni, ok := c.nodes[p.Spec.NodeName]; ok {
		req, _ := kuResource.PodRequestsAndLimits(p)
		ni.Requested.MilliCPU -= req.Cpu().MilliValue()
		ni.Requested.Memory -= req.Memory().Value()
		ni.Requested.Storage -= req.Storage().Value()
		ni.Requested.EphemeralStorage -= req.StorageEphemeral().Value()
	}
}

func (c *ResourceCache) Rebuild(
	nodeLister corelisters.NodeLister,
	podLister corelisters.PodLister) error {
	klog.Info("Rebuilding resource cache...")
	tmp := make(map[string]*LocalNodeInfo)

	nodes, err := nodeLister.List(labels.Everything())
	if err != nil {
		return err
	}
	for _, n := range nodes {
		alloc := convertToLocalResource(n.Status.Allocatable)
		tmp[n.Name] = &LocalNodeInfo{
			Allocatable: LocalResource{
				MilliCPU:         alloc.MilliCPU,
				Memory:           alloc.Memory,
				Storage:          alloc.Storage,
				EphemeralStorage: alloc.EphemeralStorage,
			},
		}
	}

	pods, err := podLister.List(labels.Everything())
	if err != nil {
		return err
	}
	for _, p := range pods {
		if !eligible(p) {
			continue
		}
		req, _ := kuResource.PodRequestsAndLimits(p)
		if ni, ok := tmp[p.Spec.NodeName]; ok {
			ni.Requested.MilliCPU += req.Cpu().MilliValue()
			ni.Requested.Memory += req.Memory().Value()
			ni.Requested.Storage += req.Storage().Value()
			ni.Requested.EphemeralStorage += req.StorageEphemeral().Value()
		}
	}

	c.Lock()
	c.nodes = tmp
	c.Unlock()
	klog.Info("Resource cache rebuild complete.")
	return nil
}

func (c *ResourceCache) DebugDump() {
	c.RLock()
	defer c.RUnlock()
	klog.Info("===== Resource Cache Dump =====")
	for name, ni := range c.nodes {
		klog.Infof("Node: %s", name)
		klog.Infof("  CPU:    %dm used / %dm allocatable", ni.Requested.MilliCPU, ni.Allocatable.MilliCPU)
		klog.Infof("  Memory: %d bytes used / %d bytes allocatable", ni.Requested.Memory, ni.Allocatable.Memory)
		klog.Infof("  Storage: %d used / %d allocatable", ni.Requested.Storage, ni.Allocatable.Storage)
		klog.Infof("  EphemeralStorage: %d used / %d allocatable", ni.Requested.EphemeralStorage, ni.Allocatable.EphemeralStorage)
	}
	klog.Info("================================")
}

func (c *ResourceCache) Fits(nodeName string, pod *v1.Pod) bool {
	req, _ := kuResource.PodRequestsAndLimits(pod)

	c.RLock()
	ni, ok := c.nodes[nodeName]
	c.RUnlock()
	if !ok {
		klog.Infof("Fits: node %q not found in cache", nodeName)
		return false
	}

	availableCPU := ni.Allocatable.MilliCPU - ni.Requested.MilliCPU - req.Cpu().MilliValue()
	availableMem := ni.Allocatable.Memory - ni.Requested.Memory - req.Memory().Value()
	availableStorage := ni.Allocatable.Storage - ni.Requested.Storage - req.Storage().Value()
	availableEph := ni.Allocatable.EphemeralStorage - ni.Requested.EphemeralStorage - req.StorageEphemeral().Value()

	fits := availableCPU >= 0 && availableMem >= 0 && availableStorage >= 0 && availableEph >= 0

	// This logging would be helpful while debugging
	klog.Infof("Fits check for pod %s/%s on node %s: fits=%v", pod.Namespace, pod.Name, nodeName, fits)
	klog.Infof("  CPU:      alloc=%dm used=%dm req=%dm → remaining=%dm",
		ni.Allocatable.MilliCPU, ni.Requested.MilliCPU, req.Cpu().MilliValue(), availableCPU)
	klog.Infof("  Memory:   alloc=%d used=%d req=%d → remaining=%d",
		ni.Allocatable.Memory, ni.Requested.Memory, req.Memory().Value(), availableMem)
	klog.Infof("  Storage:  alloc=%d used=%d req=%d → remaining=%d",
		ni.Allocatable.Storage, ni.Requested.Storage, req.Storage().Value(), availableStorage)
	klog.Infof("  Ephemeral alloc=%d used=%d req=%d → remaining=%d",
		ni.Allocatable.EphemeralStorage, ni.Requested.EphemeralStorage, req.StorageEphemeral().Value(), availableEph)

	return fits
}

func convertToLocalResource(res v1.ResourceList) LocalResource {
	cpuQty := res[v1.ResourceCPU]
	memQty := res[v1.ResourceMemory]
	storageQty := res[v1.ResourceStorage]
	ephemeralStorageQty := res[v1.ResourceEphemeralStorage]
	return LocalResource{
		MilliCPU:         cpuQty.MilliValue(),
		Memory:           memQty.Value(),
		Storage:          storageQty.Value(),
		EphemeralStorage: ephemeralStorageQty.Value(),
	}
}
