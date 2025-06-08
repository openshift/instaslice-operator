package gpu

import (
	"context"
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

	instav1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

const Name = "InstasliceGPU"

// Plugin implements a scheduler PreBind extension for GPU allocation.
type Plugin struct {
	handle      framework.Handle
	instaClient instaclient.Interface
	namespace   string
}

// Args holds the scheduler plugin configuration.
type Args struct {
	Namespace string `json:"namespace,omitempty"`
}

var _ framework.PreBindPlugin = &Plugin{}

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
			ns = "instaslice-system"
		}
	}

	informerFactory := instainformers.NewSharedInformerFactoryWithOptions(
		instaClient, 10*time.Minute,
		instainformers.WithNamespace(ns),
	)
	allocInformer := informerFactory.
		OpenShiftOperator().
		V1alpha1().
		Allocations().
		Informer()

	// Register a single “node-gpu” composite indexer:
	err = allocInformer.AddIndexers(cache.Indexers{
		"node-gpu": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1alpha1.Allocation)
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

	// indexer usage
	// indexer := allocInformer.GetIndexer()

	// nodeName := "node-42"
	// gpuUUID := "abc123"
	// compositeKey := fmt.Sprintf("%s/%s", nodeName, gpuUUID)

	// objs, err := indexer.ByIndex("node-gpu", compositeKey)
	// if err != nil {
	// 	// handle error
	// }

	// for _, obj := range objs {
	// 	alloc := obj.(*instav1alpha1.Allocation)
	// 	fmt.Printf("Found Allocation %s on node %s for GPU %s\n",
	// 		alloc.Name, alloc.Spec.Nodename, alloc.Spec.GPUUUID)
	// }

	informerFactory.Start(ctx.Done())

	return &Plugin{handle: handle, instaClient: instaClient, namespace: ns}, nil
}

// PreBind selects a GPU and updates the Instaslice object for the chosen node.
func (p *Plugin) PreBind(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeName string) *framework.Status {
	instObj, err := p.instaClient.OpenShiftOperatorV1alpha1().Instaslices("instaslice-system").Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return framework.AsStatus(err)
	}
	var selectedGPU string
	var alloc *instav1alpha1.Allocation
	for _, gpu := range instObj.Status.NodeResources.NodeGPUs {
		gpuAllocated := gpuAllocatedSlices(instObj, gpu.GPUUUID)
		profileName := extractProfileName(pod.Spec.Containers[0].Resources.Limits)
		newStart := getStartIndexFromAllocationResults(instObj, profileName, gpuAllocated)
		if newStart == int32(9) {
			continue
		}
		size, discoveredGiprofile, Ciprofileid, Ciengprofileid := extractGpuProfile(instObj, profileName)
		alloc = SetAllocationDetails(
			profileName,
			newStart,
			size,
			pod.GetUID(),
			types.NodeName(instObj.GetName()),
			instav1alpha1.AllocationStatus{AllocationStatusController: string(instav1alpha1.AllocationStatusCreating)},
			discoveredGiprofile,
			Ciprofileid,
			Ciengprofileid,
			pod.GetNamespace(),
			pod.GetName(),
			gpu.GPUUUID,
			types.UID(pod.GetUID()),
		)
		selectedGPU = gpu.GPUUUID
		break
	}
	if selectedGPU == "" {
		return framework.NewStatus(framework.Unschedulable, "no GPU available")
	}
	if _, err := p.instaClient.OpenShiftOperatorV1alpha1().Allocations("instaslice-system").Create(ctx, alloc, metav1.CreateOptions{}); err != nil {
		return framework.AsStatus(err)
	}
	klog.InfoS("instaslice GPU selected ", "pod", klog.KObj(pod), "node", nodeName, "gpu", selectedGPU)
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

func SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName,
	allocationStatus instav1alpha1.AllocationStatus, discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32,
	namespace string, podName string, gpuUuid string, resourceIdentifier types.UID) *instav1alpha1.Allocation {
	return &instav1alpha1.Allocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      string(podUUID),
			Namespace: "instaslice-system",
		},
		Spec: instav1alpha1.AllocationSpec{
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

func extractGpuProfile(instaslice *instav1alpha1.Instaslice, profileName string) (int32, int32, int32, int32) {
	var size int32
	var discoveredGiprofile int32
	var Ciprofileid int32
	var Ciengprofileid int32
	for profName, placement := range instaslice.Status.NodeResources.MigPlacement {
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

func getStartIndexFromAllocationResults(instaslice *instav1alpha1.Instaslice, profileName string, gpuAllocatedIndex [8]int32) int32 {
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
	for profile, placement := range instaslice.Status.NodeResources.MigPlacement {
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

func gpuAllocatedSlices(instObj *instav1alpha1.Instaslice, gpuUUID string) [8]int32 {
	var gpuAllocatedIndex [8]int32
	for i := range gpuAllocatedIndex {
		gpuAllocatedIndex[i] = 0
	}
	for _, allocResult := range instObj.Status.PodAllocationResults {
		for i := 0; i < int(allocResult.MigPlacement.Size); i++ {
			gpuAllocatedIndex[int(allocResult.MigPlacement.Start)+i] = 1
		}
	}
	return gpuAllocatedIndex
}
