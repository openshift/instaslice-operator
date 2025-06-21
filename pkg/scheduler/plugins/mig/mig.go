package mig

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	instav1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	deviceplugins "github.com/openshift/instaslice-operator/pkg/daemonset/deviceplugins"
	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	instalisters "github.com/openshift/instaslice-operator/pkg/generated/listers/dasoperator/v1alpha1"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

const Name = "MigAccelerator"

// Plugin implements a scheduler Filter extension for GPU allocation.
type Plugin struct {
	handle            framework.Handle
	instaClient       instaclient.Interface
	namespace         string
	instasliceLister  instalisters.NodeAcceleratorLister
	allocationIndexer cache.Indexer
}

func getAllocationClaimSpec(a *instav1alpha1.AllocationClaim) (instav1alpha1.AllocationClaimSpec, error) {
	if a == nil {
		return instav1alpha1.AllocationClaimSpec{}, fmt.Errorf("allocation claim is nil")
	}
	var spec instav1alpha1.AllocationClaimSpec
	if len(a.Spec.Raw) == 0 {
		return spec, fmt.Errorf("allocation claim spec is empty")
	}
	if err := json.Unmarshal(a.Spec.Raw, &spec); err != nil {
		return instav1alpha1.AllocationClaimSpec{}, fmt.Errorf("failed to decode allocation spec: %w", err)
	}
	return spec, nil
}

// cleanupAllocationClaims deletes any AllocationClaims for the given pod
// that remain in the Created status when the pod is deleted.
func cleanupAllocationClaims(ctx context.Context, client instaclient.Interface, indexer cache.Indexer, pod *corev1.Pod) {
	uid := string(pod.GetUID())
	items, err := indexer.ByIndex("pod-uid", uid)
	if err != nil {
		klog.ErrorS(err, "listing AllocationClaims for deleted pod", "pod", klog.KObj(pod))
		return
	}
	for _, obj := range items {
		alloc, ok := obj.(*instav1alpha1.AllocationClaim)
		if !ok || alloc.Status.State != instav1alpha1.AllocationClaimStatusCreated {
			continue
		}
		if err := client.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Delete(ctx, alloc.Name, metav1.DeleteOptions{}); err != nil {
			klog.ErrorS(err, "deleting stale AllocationClaim for deleted pod", "alloc", klog.KObj(alloc), "pod", klog.KObj(pod))
		} else {
			klog.InfoS("deleted stale AllocationClaim for deleted pod", "alloc", klog.KObj(alloc), "pod", klog.KObj(pod))
		}
	}
}

// registerPodDeleteHandlerFunc attaches a delete handler to the podInformer to cleanup stale AllocationClaims.
func registerPodDeleteHandlerFunc(podInformer cache.SharedIndexInformer, client instaclient.Interface, indexer cache.Indexer, ctx context.Context) {
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			var pod *corev1.Pod
			switch t := obj.(type) {
			case *corev1.Pod:
				pod = t
			case cache.DeletedFinalStateUnknown:
				if p, ok := t.Obj.(*corev1.Pod); ok {
					pod = p
				} else {
					return
				}
			default:
				return
			}
			cleanupAllocationClaims(ctx, client, indexer, pod)
		},
	})
}

// registerPodDeleteHandler watches for Pod deletions and invokes cleanupAllocationClaims.
func registerPodDeleteHandler(ctx context.Context, client instaclient.Interface, indexer cache.Indexer, handle framework.Handle) {
	// delegate to the informer-based helper
	pi := handle.SharedInformerFactory().Core().V1().Pods().Informer()
	registerPodDeleteHandlerFunc(pi, client, indexer, ctx)
}

// Args holds the scheduler plugin configuration.
type Args struct {
	Namespace string `json:"namespace,omitempty"`
}

var (
	_ framework.FilterPlugin  = &Plugin{}
	_ framework.ScorePlugin   = &Plugin{}
	_ framework.PreBindPlugin = &Plugin{}
)

// Name returns the plugin name.
func (p *Plugin) Name() string { return Name }

// New initializes a new plugin and returns it.
func New(ctx context.Context, args runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	restCfg := rest.CopyConfig(handle.KubeConfig())
	restCfg.AcceptContentTypes = "application/json"
	restCfg.ContentType = "application/json"
	instaClient, err := instaclient.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	cfg := Args{}
	if args != nil {
		if err := frameworkruntime.DecodeInto(args, &cfg); err != nil {
			return nil, err
		}
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = os.Getenv("INSTASLICE_NAMESPACE")
		if ns == "" {
			ns = "das-operator"
		}
	}

	informerFactory := instainformers.NewSharedInformerFactoryWithOptions(
		instaClient, 10*time.Minute,
		instainformers.WithNamespace(ns),
	)
	allocInformer := informerFactory.
		OpenShiftOperator().
		V1alpha1().
		AllocationClaims().
		Informer()

	instasliceInformer := informerFactory.
		OpenShiftOperator().
		V1alpha1().
		NodeAccelerators()
	instasliceInf := instasliceInformer.Informer()
	instasliceLister := instasliceInformer.Lister()

	// Register a single “node-gpu” composite indexer:
	err = allocInformer.AddIndexers(cache.Indexers{
		"node-gpu": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1alpha1.AllocationClaim)
			if !ok {
				return nil, nil
			}
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			// composite key: "<nodename>/<gpuuuid>"
			key := fmt.Sprintf("%s/%s", spec.Nodename, spec.GPUUUID)
			return []string{key}, nil
		},
		// pod-uid index lets us look up claims for a Pod by its UID
		"pod-uid": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1alpha1.AllocationClaim)
			if !ok {
				return nil, nil
			}
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			return []string{string(spec.PodRef.UID)}, nil
		},
	})
	if err != nil {
		return nil, err
	}

	informerFactory.Start(ctx.Done())
	if ok := cache.WaitForCacheSync(ctx.Done(), allocInformer.HasSynced, instasliceInf.HasSynced); !ok {
		return nil, fmt.Errorf("failed to sync caches")
	}

	// register a Pod delete handler for cleaning up stale AllocationClaims
	registerPodDeleteHandler(ctx, instaClient, allocInformer.GetIndexer(), handle)

	return &Plugin{
		handle:            handle,
		instaClient:       instaClient,
		namespace:         ns,
		instasliceLister:  instasliceLister,
		allocationIndexer: allocInformer.GetIndexer(),
	}, nil
}

// Filter checks if the given node has an available MIG slice for the pod.
func (p *Plugin) Filter(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}
	if val, ok := node.Labels["nvidia.com/mig.capable"]; !ok || val != "true" {
		return framework.NewStatus(framework.Unschedulable, "node not MIG capable")
	}
	klog.V(4).InfoS("checking MIG availability", "pod", klog.KObj(pod), "node", node.Name)

	instObj, err := p.instasliceLister.NodeAccelerators(p.namespace).Get(node.Name)
	if err != nil {
		return framework.NewStatus(framework.Unschedulable, err.Error())
	}
	if instObj.Spec.AcceleratorType != "" && instObj.Spec.AcceleratorType != "nvidia-mig" {
		return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("unsupported acceleratorType %s", instObj.Spec.AcceleratorType))
	}
	var resources instav1alpha1.DiscoveredNodeResources
	if len(instObj.Status.NodeResources.Raw) > 0 {
		if err := json.Unmarshal(instObj.Status.NodeResources.Raw, &resources); err != nil {
			return framework.AsStatus(err)
		}
	}

	// remove any existing claims for this pod
	if p.allocationIndexer != nil {
		items, err := p.allocationIndexer.ByIndex("pod-uid", string(pod.UID))
		if err == nil {
			for _, obj := range items {
				if ac, ok := obj.(*instav1alpha1.AllocationClaim); ok {
					_ = p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(ac.Namespace).Delete(ctx, ac.Name, metav1.DeleteOptions{})
				}
			}
		}
	}

	profiles := getPodProfileNames(pod)
	if len(profiles) == 0 {
		return nil
	}

	// Build container list
	var containers []corev1.Container
	containers = append(containers, pod.Spec.Containers...)
	containers = append(containers, pod.Spec.InitContainers...)
	for _, ec := range pod.Spec.EphemeralContainers {
		containers = append(containers, corev1.Container{Name: ec.Name, Resources: ec.Resources})
	}

	// Build map of allocated slices per GPU excluding this pod's claims
	gpuAllocated := make(map[string][8]int32)
	for _, gpu := range resources.NodeGPUs {
		allocIdx, err := gpuAllocatedSlices(p.allocationIndexer, node.Name, gpu.GPUUUID, pod.UID)
		if err != nil {
			return framework.AsStatus(err)
		}
		gpuAllocated[gpu.GPUUUID] = allocIdx
	}

	var claims []*instav1alpha1.AllocationClaim

	for _, c := range containers {
		profs := extractProfileNames(c.Resources.Limits)
		for j, profileName := range profs {
			allocated := false
			for _, gpu := range resources.NodeGPUs {
				idx := gpuAllocated[gpu.GPUUUID]
				newStart := getStartIndexFromAllocationResults(resources, profileName, idx)
				if newStart == int32(9) {
					continue
				}
				size, _, _, _ := extractGpuProfile(resources, profileName)
				specObj := instav1alpha1.AllocationClaimSpec{
					Profile: profileName,
					PodRef: corev1.ObjectReference{
						Kind:      "Pod",
						Namespace: pod.GetNamespace(),
						Name:      pod.GetName(),
						UID:       pod.GetUID(),
					},
					MigPlacement: instav1alpha1.Placement{
						Size:  size,
						Start: newStart,
					},
					GPUUUID:  gpu.GPUUUID,
					Nodename: types.NodeName(instObj.GetName()),
				}
				rawSpec, _ := json.Marshal(&specObj)
				alloc := &instav1alpha1.AllocationClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%s-%d", pod.GetUID(), c.Name, j),
						Namespace: p.namespace,
					},
					Spec: runtime.RawExtension{Raw: rawSpec},
				}
				created, err := p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).Create(ctx, alloc, metav1.CreateOptions{})
				if err != nil {
					if apierrors.IsAlreadyExists(err) {
						created, err = p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(p.namespace).Get(ctx, alloc.Name, metav1.GetOptions{})
						if err != nil {
							return framework.AsStatus(err)
						}
					} else {
						for _, ac := range claims {
							_ = p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(ac.Namespace).Delete(ctx, ac.Name, metav1.DeleteOptions{})
						}
						return framework.AsStatus(err)
					}
				}
				if _, err := deviceplugins.UpdateAllocationStatus(ctx, p.instaClient, created, instav1alpha1.AllocationClaimStatusStaged); err != nil {
					for _, ac := range claims {
						_ = p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(ac.Namespace).Delete(ctx, ac.Name, metav1.DeleteOptions{})
					}
					_ = p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(created.Namespace).Delete(ctx, created.Name, metav1.DeleteOptions{})
					return framework.AsStatus(err)
				}
				for i := int32(0); i < size; i++ {
					idx[newStart+i] = 1
				}
				gpuAllocated[gpu.GPUUUID] = idx
				claims = append(claims, created)
				allocated = true
				klog.V(4).InfoS("selected GPU", "uuid", gpu.GPUUUID, "profile", profileName, "node", node.Name, "container", c.Name)
				break
			}
			if !allocated {
				klog.V(4).InfoS("no GPU available", "pod", klog.KObj(pod), "node", node.Name)
				for _, ac := range claims {
					_ = p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(ac.Namespace).Delete(ctx, ac.Name, metav1.DeleteOptions{})
				}
				return framework.NewStatus(framework.Unschedulable, "no GPU available")
			}
		}
	}

	if len(claims) > 0 {
		klog.V(4).InfoS("instaslice GPU staged", "pod", klog.KObj(pod), "node", node.Name)
	}
	return nil
}

// Score favors nodes with the most remaining free MIG slice capacity after
// accounting for all AllocationClaims including those staged for this Pod.
func (p *Plugin) Score(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) (int64, *framework.Status) {
	instObj, err := p.instasliceLister.NodeAccelerators(p.namespace).Get(nodeName)
	if err != nil {
		return 0, framework.AsStatus(err)
	}

	var resources instav1alpha1.DiscoveredNodeResources
	if len(instObj.Status.NodeResources.Raw) > 0 {
		if err := json.Unmarshal(instObj.Status.NodeResources.Raw, &resources); err != nil {
			return 0, framework.AsStatus(err)
		}
	}

	var freeSlots, totalSlots int32
	for _, gpu := range resources.NodeGPUs {
		idx, err := gpuAllocatedSlices(p.allocationIndexer, nodeName, gpu.GPUUUID, "")
		if err != nil {
			return 0, framework.AsStatus(err)
		}
		totalSlots += int32(len(idx))
		for _, v := range idx {
			if v == 0 {
				freeSlots++
			}
		}
	}

	if totalSlots == 0 {
		return 0, nil
	}
	score := int64(freeSlots) * framework.MaxNodeScore / int64(totalSlots)
	return score, nil
}

// PreBind finalizes AllocationClaims on the chosen node and cleans up staged
// claims on the other nodes.
func (p *Plugin) PreBind(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) *framework.Status {
	items, err := p.allocationIndexer.ByIndex("pod-uid", string(pod.UID))
	if err != nil {
		return framework.AsStatus(err)
	}
	for _, obj := range items {
		alloc, ok := obj.(*instav1alpha1.AllocationClaim)
		if !ok {
			continue
		}
		spec, err := getAllocationClaimSpec(alloc)
		if err != nil {
			klog.ErrorS(err, "decoding AllocationClaim", "alloc", klog.KObj(alloc))
			continue
		}
		if string(spec.Nodename) == nodeName {
			if alloc.Status.State != instav1alpha1.AllocationClaimStatusCreated {
				if _, err := deviceplugins.UpdateAllocationStatus(ctx, p.instaClient, alloc, instav1alpha1.AllocationClaimStatusCreated); err != nil {
					return framework.AsStatus(err)
				}
			}
		} else {
			if err := p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Delete(ctx, alloc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return framework.AsStatus(err)
			}
		}
	}
	return nil
}

// ScoreExtensions returns nil as the plugin does not implement score extensions.
func (p *Plugin) ScoreExtensions() framework.ScoreExtensions { return nil }

// The helper functions below are mostly moved from the legacy scheduler.

func extractProfileNames(limits corev1.ResourceList) []string {
	var profiles []string
	for k := range limits {
		key := k.String()
		if strings.Contains(key, "nvidia.com/mig-") || strings.Contains(key, "mig.das.com/") {
			re := regexp.MustCompile(`(\d+g\.\d+gb)`)
			match := re.FindStringSubmatch(key)
			if len(match) > 1 {
				profiles = append(profiles, match[1])
			}
		}
	}
	return profiles
}

// getPodProfileNames gathers all requested MIG profiles from regular,
// init and ephemeral containers.
func getPodProfileNames(pod *corev1.Pod) []string {
	var profiles []string
	for _, c := range pod.Spec.Containers {
		profiles = append(profiles, extractProfileNames(c.Resources.Limits)...)
	}
	for _, c := range pod.Spec.InitContainers {
		profiles = append(profiles, extractProfileNames(c.Resources.Limits)...)
	}
	for _, c := range pod.Spec.EphemeralContainers {
		profiles = append(profiles, extractProfileNames(c.Resources.Limits)...)
	}
	return profiles
}

func extractGpuProfile(resources instav1alpha1.DiscoveredNodeResources, profileName string) (int32, int32, int32, int32) {
	var size int32
	var discoveredGiprofile int32
	var Ciprofileid int32
	var Ciengprofileid int32
	for profName, placement := range resources.MigPlacement {
		if profName == profileName {
			for _, aPlacement := range placement.Placements {
				size = aPlacement.Size
				discoveredGiprofile = placement.GIProfileID
				Ciprofileid = placement.CIProfileID
				Ciengprofileid = placement.CIEngProfileID
				break
			}
		}
	}
	return size, discoveredGiprofile, Ciprofileid, Ciengprofileid
}

// getStartIndexFromAllocationResults finds a free placement index on the GPU
// that satisfies the requested MIG profile. It returns 9 when no slot exists.
func getStartIndexFromAllocationResults(resources instav1alpha1.DiscoveredNodeResources, profileName string, gpuAllocatedIndex [8]int32) int32 {
	allAllocated := true
	for _, allocated := range gpuAllocatedIndex {
		if allocated != 1 {
			allAllocated = false
			break
		}
	}
	if allAllocated {
		return int32(9)
	}
	var neededContinousSlot int32
	var possiblePlacements []int32
	for profile, placement := range resources.MigPlacement {
		if profile == profileName {
			neededContinousSlot = placement.Placements[0].Size
			for _, placement := range placement.Placements {
				possiblePlacements = append(possiblePlacements, placement.Start)
			}
			break
		}
	}
	var newStart = int32(9)
	for _, value := range possiblePlacements {
		if gpuAllocatedIndex[value] == 0 {
			if neededContinousSlot == 1 {
				newStart = value
				break
			}
			if neededContinousSlot == 2 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 {
						newStart = value
						break
					}
				}
			}
			if neededContinousSlot == 4 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 && gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 {
						newStart = value
						break
					}
				}
			}
			if neededContinousSlot == 8 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 &&
						gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 &&
						gpuAllocatedIndex[value+4] == 0 && gpuAllocatedIndex[value+5] == 0 &&
						gpuAllocatedIndex[value+6] == 0 && gpuAllocatedIndex[value+7] == 0 {
						newStart = value
					}
				}
			}
		}
	}
	return newStart
}

func gpuAllocatedSlices(indexer cache.Indexer, nodeName, gpuUUID string, skipUID types.UID) ([8]int32, error) {
	var gpuAllocatedIndex [8]int32
	for i := range gpuAllocatedIndex {
		gpuAllocatedIndex[i] = 0
	}
	if indexer == nil {
		err := fmt.Errorf("allocation indexer is nil")
		klog.ErrorS(err, "indexer not initialized")
		return gpuAllocatedIndex, err
	}
	key := fmt.Sprintf("%s/%s", nodeName, gpuUUID)
	objs, err := indexer.ByIndex("node-gpu", key)
	if err != nil {
		klog.ErrorS(err, "unable to fetch allocations from indexer")
		return gpuAllocatedIndex, err
	}
	for _, obj := range objs {
		alloc, ok := obj.(*instav1alpha1.AllocationClaim)
		if !ok {
			continue
		}
		spec, err := getAllocationClaimSpec(alloc)
		if err != nil {
			klog.ErrorS(err, "unable to decode allocation spec")
			continue
		}
		if skipUID != "" && spec.PodRef.UID == skipUID {
			continue
		}
		for i := 0; i < int(spec.MigPlacement.Size); i++ {
			gpuAllocatedIndex[int(spec.MigPlacement.Start)+i] = 1
		}
	}
	return gpuAllocatedIndex, nil
}
