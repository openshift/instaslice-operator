package mig

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

	instav1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	deviceplugins "github.com/openshift/instaslice-operator/pkg/daemonset/deviceplugins"
	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	instalisters "github.com/openshift/instaslice-operator/pkg/generated/listers/instasliceoperator/v1alpha1"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

const Name = "MigAccelerator"

// Plugin implements a scheduler PreBind extension for GPU allocation.
type Plugin struct {
	handle            framework.Handle
	instaClient       instaclient.Interface
	namespace         string
	instasliceLister  instalisters.NodeAcceleratorLister
	allocationIndexer cache.Indexer
}

// Args holds the scheduler plugin configuration.
type Args struct {
	Namespace string `json:"namespace,omitempty"`
}

var (
	_ framework.PreBindPlugin = &Plugin{}
	_ framework.FilterPlugin  = &Plugin{}
	_ framework.ScorePlugin   = &Plugin{}
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
			// composite key: "<nodename>/<gpuuuid>"
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.GPUUUID)
			return []string{key}, nil
		},
	})
	if err != nil {
		return nil, err
	}

	informerFactory.Start(ctx.Done())
	if ok := cache.WaitForCacheSync(ctx.Done(), allocInformer.HasSynced, instasliceInf.HasSynced); !ok {
		return nil, fmt.Errorf("failed to sync caches")
	}

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
	profiles := getPodProfileNames(pod)
	if len(profiles) == 0 {
		return nil
	}
	for _, prof := range profiles {
		klog.V(4).InfoS("searching GPUs for profile", "profile", prof, "node", node.Name)
		found := false
		for _, gpu := range resources.NodeGPUs {
			gpuAllocated, err := gpuAllocatedSlices(p.allocationIndexer, node.Name, gpu.GPUUUID)
			if err != nil {
				return framework.AsStatus(err)
			}
			newStart := getStartIndexFromAllocationResults(resources, prof, gpuAllocated)
			if newStart != int32(9) {
				klog.V(4).InfoS("found candidate GPU", "uuid", gpu.GPUUUID, "profile", prof, "node", node.Name)
				found = true
				break
			}
		}
		if !found {
			klog.V(4).InfoS("no suitable GPU found", "profile", prof, "node", node.Name)
			return framework.NewStatus(framework.Unschedulable, "no GPU available")
		}
	}
	return nil
}

// Score evaluates how many GPUs on the node can satisfy all requested profiles.
func (p *Plugin) Score(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) (int64, *framework.Status) {
	klog.V(4).InfoS("scoring node", "pod", klog.KObj(pod), "node", nodeName)
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
	profiles := getPodProfileNames(pod)
	if len(profiles) == 0 {
		return 0, nil
	}
	var minAvailable int64 = math.MaxInt64
	for _, profileName := range profiles {
		klog.V(4).InfoS("counting GPUs for profile", "profile", profileName, "node", nodeName)
		available := int64(0)
		for _, gpu := range resources.NodeGPUs {
			gpuAllocated, err := gpuAllocatedSlices(p.allocationIndexer, nodeName, gpu.GPUUUID)
			if err != nil {
				return 0, framework.AsStatus(err)
			}
			newStart := getStartIndexFromAllocationResults(resources, profileName, gpuAllocated)
			if newStart != int32(9) {
				available++
			}
		}
		if available < minAvailable {
			minAvailable = available
		}
		klog.V(4).InfoS("available GPUs for profile", "profile", profileName, "available", available, "node", nodeName)
	}
	if minAvailable == math.MaxInt64 {
		minAvailable = 0
	}
	klog.V(4).InfoS("node score computed", "node", nodeName, "score", minAvailable)
	return minAvailable, nil
}

func (p *Plugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

// PreBind selects a GPU and updates the NodeAccelerator object for the chosen node.
func (p *Plugin) PreBind(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) *framework.Status {
	klog.V(4).InfoS("prebind selecting GPU", "pod", klog.KObj(pod), "node", nodeName)
	instObj, err := p.instasliceLister.NodeAccelerators(p.namespace).Get(nodeName)
	if err != nil {
		return framework.AsStatus(err)
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
	var selectedGPU string
	var alloc *instav1alpha1.AllocationClaim
	for _, gpu := range resources.NodeGPUs {
		klog.V(4).InfoS("evaluating GPU", "uuid", gpu.GPUUUID, "node", nodeName)
		gpuAllocated, err := gpuAllocatedSlices(p.allocationIndexer, nodeName, gpu.GPUUUID)
		if err != nil {
			return framework.AsStatus(err)
		}
		profileName := extractProfileName(pod.Spec.Containers[0].Resources.Limits)
		newStart := getStartIndexFromAllocationResults(resources, profileName, gpuAllocated)
		if newStart == int32(9) {
			continue
		}
		size, discoveredGiprofile, Ciprofileid, Ciengprofileid := extractGpuProfile(resources, profileName)
		alloc = SetAllocationDetails(
			profileName,
			newStart,
			size,
			pod.GetUID(),
			types.NodeName(instObj.GetName()),
			instav1alpha1.AllocationClaimStatusCreated,
			discoveredGiprofile,
			Ciprofileid,
			Ciengprofileid,
			pod.GetNamespace(),
			pod.GetName(),
			gpu.GPUUUID,
			types.UID(pod.GetUID()),
		)
		selectedGPU = gpu.GPUUUID
		klog.V(4).InfoS("selected GPU", "uuid", gpu.GPUUUID, "profile", profileName, "node", nodeName)
		break
	}
	if selectedGPU == "" {
		klog.V(4).InfoS("no GPU available", "pod", klog.KObj(pod), "node", nodeName)
		return framework.NewStatus(framework.Unschedulable, "no GPU available")
	}
	created, err := p.instaClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").Create(ctx, alloc, metav1.CreateOptions{})
	if err != nil {
		return framework.AsStatus(err)
	}
	if _, err := deviceplugins.UpdateAllocationStatus(ctx, p.instaClient, created, instav1alpha1.AllocationClaimStatusCreated); err != nil {
		return framework.AsStatus(err)
	}
	klog.V(4).InfoS("instaslice GPU selected", "pod", klog.KObj(pod), "node", nodeName, "gpu", selectedGPU)
	return nil
}

// The helper functions below are mostly moved from the legacy scheduler.

func extractProfileName(limits corev1.ResourceList) string {
	profileName := ""
	for k := range limits {
		if strings.Contains(k.String(), "mig-") {
			re := regexp.MustCompile(`(\d+g\.\d+gb)`)
			match := re.FindStringSubmatch(k.String())
			if len(match) > 1 {
				profileName = match[1]
			}
		}
	}
	return profileName
}

// getPodProfileNames gathers all requested MIG profiles from regular,
// init and ephemeral containers.
func getPodProfileNames(pod *corev1.Pod) []string {
	var profiles []string
	for _, c := range pod.Spec.Containers {
		if p := extractProfileName(c.Resources.Limits); p != "" {
			profiles = append(profiles, p)
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if p := extractProfileName(c.Resources.Limits); p != "" {
			profiles = append(profiles, p)
		}
	}
	for _, c := range pod.Spec.EphemeralContainers {
		if p := extractProfileName(c.Resources.Limits); p != "" {
			profiles = append(profiles, p)
		}
	}
	return profiles
}

func SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName,
	allocationStatus instav1alpha1.AllocationClaimStatus, discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32,
	namespace string, podName string, gpuUuid string, resourceIdentifier types.UID) *instav1alpha1.AllocationClaim {
	return &instav1alpha1.AllocationClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      string(podUUID),
			Namespace: "das-operator",
		},
		Spec: instav1alpha1.AllocationClaimSpec{
			Profile: profileName,
			PodRef: corev1.ObjectReference{
				Kind:      "Pod",
				Namespace: namespace,
				Name:      podName,
				UID:       podUUID,
			},
			MigPlacement: instav1alpha1.Placement{
				Size:  size,
				Start: newStart,
			},
			GPUUUID:  gpuUuid,
			Nodename: nodename,
		},
		Status: allocationStatus,
	}
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

func gpuAllocatedSlices(indexer cache.Indexer, nodeName, gpuUUID string) ([8]int32, error) {
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
		for i := 0; i < int(alloc.Spec.MigPlacement.Size); i++ {
			gpuAllocatedIndex[int(alloc.Spec.MigPlacement.Start)+i] = 1
		}
	}
	return gpuAllocatedIndex, nil
}
