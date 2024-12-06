/*
Copyright 2024.

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

package daemonset

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller"
	"github.com/openshift/instaslice-operator/internal/controller/config"
	"github.com/openshift/instaslice-operator/internal/controller/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstaSliceDaemonsetReconciler reconciles a InstaSliceDaemonset object
type InstaSliceDaemonsetReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	kubeClient *kubernetes.Clientset
	NodeName   string
	Config     *config.Config
}

//+kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;patch;watch
//+kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

var discoveredGpusOnHost []string

// this struct is created to represent profiles
// in human readable format and perform string comparison
// NVML provides int values which are hard to interpret.
type MigProfile struct {
	C              int
	G              int
	GB             int
	GIProfileID    int
	CIProfileID    int
	CIEngProfileID int
}

// we struct to patch node with instaslice object
type ResPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

type MigDeviceInfo struct {
	uuid   string
	giInfo *nvml.GpuInstanceInfo
	ciInfo *nvml.ComputeInstanceInfo
	start  uint32
	size   uint32
}

func (r *InstaSliceDaemonsetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	nodeName := r.NodeName

	if req.Name != nodeName {
		return ctrl.Result{}, nil
	}

	nsName := types.NamespacedName{
		Name:      nodeName,
		Namespace: controller.InstaSliceOperatorNamespace,
	}

	var instaslice inferencev1alpha1.Instaslice
	if err := r.Get(ctx, nsName, &instaslice); err != nil {
		log.Error(err, "error getting Instaslice object with ", "name", nodeName)
		return ctrl.Result{}, err
	}

	// Use ResourceVersion to ensure we're updating the latest version of the object
	latestInstaslice, err := r.getInstasliceObject(ctx, instaslice.Name, instaslice.Namespace)
	if err != nil {
		log.Error(err, "Error fetching latest InstaSlice object")
		return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, nil
	}

	// Ensure we are not processing an outdated version of the object
	if latestInstaslice.ResourceVersion != instaslice.ResourceVersion {
		return ctrl.Result{}, nil
	}

	for _, allocations := range instaslice.Spec.Allocations {
		// TODO: we make assumption that resources would always exists to delete
		// if user deletes abruptly, cm, instaslice resource, ci and gi may not exists
		// handle such scenario's.
		// delete first before creating new slice
		if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusDeleting && allocations.Nodename == nodeName {
			log.Info("performing cleanup ", "pod", allocations.PodName)
			if !r.Config.EmulatorModeEnable {
				err := r.cleanUpCiAndGi(ctx, allocations)
				if err != nil {
					// NVML shutdowm took time or NVML init may have failed.
					log.Error(err, "error cleaning up ci and gi retrying")
					return ctrl.Result{RequeueAfter: controller.Requeue2sDelay}, nil
				}
			}

			err = r.deleteConfigMap(ctx, allocations.Resourceidentifier, allocations.Namespace)
			if err != nil && !errors.IsNotFound(err) {
				log.Error(err, "error deleting config map for ", "pod", allocations.PodName)
				return ctrl.Result{Requeue: true}, nil
			}

			allocations.Allocationstatus = inferencev1alpha1.AllocationStatusDeleted
			err := utils.UpdateInstasliceAllocations(ctx, r.Client, instaslice.Name, allocations.PodUUID, allocations)
			if err != nil {
				log.Error(err, "error updating InstaSlice object for ", "pod", allocations.PodName)
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, nil
		}
		// create new slice by obeying controller allocation
		if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusCreating && allocations.Nodename == nodeName {
			// Assume pod only has one container with one GPU request
			log.Info("creating allocation for ", "pod", allocations.PodName)

			existingAllocations := instaslice.Spec.Allocations[allocations.PodUUID]

			if r.Config.EmulatorModeEnable {
				// configmap with fake MIG uuid
				if err := r.createConfigMap(ctx, allocations.Resourceidentifier, existingAllocations.Namespace, allocations.Resourceidentifier); err != nil {
					return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, nil
				}
				// Emulating cost to create CI and GI on a GPU
				time.Sleep(controller.Requeue1sDelay)
			}
			if !r.Config.EmulatorModeEnable {
				ret := nvml.Init()
				if ret != nvml.SUCCESS {
					log.Error(ret, "Unable to initialize NVML")
				}

				var shutdownErr error
				var createdMigInfos map[string]*MigDeviceInfo
				var err error

				defer func() {
					if shutdownErr = nvml.Shutdown(); shutdownErr != nvml.SUCCESS {
						log.Error(shutdownErr, "error to perform nvml.Shutdown")
					}
				}()
				if ret != nvml.SUCCESS {
					log.Error(ret, "Unable to get device count")
				}

				// TODO: any GPU can fail creating CI and GI
				// if simulator mode is on do not perform NVML calls
				// TODO: move this logic to a new vendor specific file
				placement := nvml.GpuInstancePlacement{
					Start: allocations.Start,
					Size:  allocations.Size,
				}
				// if the GPU is healthy DeviceGetHandleByUUID should never fail
				// if the call fails then we look in the cache so see if we can reuse
				// ci and gi or walk MIG devices to set allocation status to created.
				// the keep latency low for realizing slices.
				device, retCodeForDevice := nvml.DeviceGetHandleByUUID(allocations.GPUUUID)
				if retCodeForDevice != nvml.SUCCESS {
					log.Error(ret, "error getting GPU device handle")
				}
				var giProfileId, ciProfileId int
				for _, item := range instaslice.Spec.Migplacement {
					if item.Profile == allocations.Profile {
						giProfileId = item.Giprofileid
						ciProfileId = item.Giprofileid
						break
					}
				}
				giProfileInfo, retCodeForGi := device.GetGpuInstanceProfileInfo(giProfileId)
				if retCodeForGi != nvml.SUCCESS {
					log.Error(retCodeForGi, "error getting GPU instance profile info", "giProfileInfo", giProfileInfo, "retCodeForGi", retCodeForGi)
				}

				log.Info("The profile id is", "giProfileInfo", giProfileInfo.Id, "Memory", giProfileInfo.MemorySizeMB, "pod", allocations.PodUUID)
				createCiAndGi := true
				createdMigInfos, err = populateMigDeviceInfos(device)
				if err != nil {
					// MIG walking can fail but at this point we are unsure if slices exists
					// hence we optimistically try to create ci and gi.
					log.Error(err, "walking MIG devices failed")
					return ctrl.Result{}, err
				}
				// we should skip ci and gi creation for cases where etcd update fails or configmap creation
				// fails.
				for _, migDevice := range createdMigInfos {
					if migDevice.uuid == allocations.GPUUUID && migDevice.start == allocations.Start {
						createCiAndGi = false
						log.Info("skipping ci and gi creation for ", "pod", allocations.PodName, "parentgpu", allocations.GPUUUID, "start", allocations.Start)
						break
					}
				}
				// if ci and gi exist, we need to assign those to the respective allocation
				if createCiAndGi {
					createdMigInfos, err = r.createSliceAndPopulateMigInfos(ctx, device, allocations, giProfileInfo, placement, ciProfileId)
					if err != nil {
						log.Error(err, "mig creation not successful")
						return ctrl.Result{RequeueAfter: controller.Requeue2sDelay}, nil
					}
				}
				for migUuid, migDevice := range createdMigInfos {
					if migDevice.start == allocations.Start && migDevice.uuid == allocations.GPUUUID && giProfileInfo.Id == migDevice.giInfo.ProfileId {
						if err := r.createConfigMap(ctx, migUuid, existingAllocations.Namespace, allocations.Resourceidentifier); err != nil {
							return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, nil
						}
						log.Info("done creating mig slice for ", "pod", allocations.PodName, "parentgpu", allocations.GPUUUID, "miguuid", migUuid)
						break
					}
				}
			}
			updateInstasliceObject, err := r.getInstasliceObject(ctx, instaslice.Name, instaslice.Namespace)
			if err != nil {
				return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, nil
			}

			if updatedAllocation, ok := updateInstasliceObject.Spec.Allocations[allocations.PodUUID]; ok {
				updatedAllocation.Allocationstatus = inferencev1alpha1.AllocationStatusCreated
				if err := utils.UpdateInstasliceAllocations(ctx, r.Client, instaslice.Name, updatedAllocation.PodUUID, updatedAllocation); err != nil {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, nil
			}

		}
	}
	return ctrl.Result{}, nil
}

// deletes CI and GI in that order.
// TODO: split this method into two methods.
func (r *InstaSliceDaemonsetReconciler) cleanUpCiAndGi(ctx context.Context, allocation inferencev1alpha1.AllocationDetails) error {
	log := logr.FromContext(ctx)
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("unable to init NVML %v", ret)
	}
	var shutdownErr error

	defer func() {
		if shutdownErr = nvml.Shutdown(); shutdownErr != nvml.SUCCESS {
			log.Error(shutdownErr, "error to perform nvml.Shutdown")
		}
	}()

	parent, ret := nvml.DeviceGetHandleByUUID(allocation.GPUUUID)
	if ret != nvml.SUCCESS {
		log.Error(ret, "error obtaining GPU handle")
		return fmt.Errorf("unable to get device handle %v", ret)
	}

	migInfos, err := populateMigDeviceInfos(parent)
	if err != nil {
		return fmt.Errorf("unable walk migs %v", err)
	}
	for _, migdevice := range migInfos {
		if migdevice.uuid == allocation.GPUUUID && migdevice.start == allocation.Start {
			gi, ret := parent.GetGpuInstanceById(int(migdevice.giInfo.Id))
			if ret != nvml.SUCCESS {
				log.Error(ret, "error obtaining gpu instance for ", "poduuid", allocation.PodName)
				if ret == nvml.ERROR_NOT_FOUND {
					return fmt.Errorf("unable to find gi %v", ret)
				}
			}
			ci, ret := gi.GetComputeInstanceById(int(migdevice.ciInfo.Id))
			if ret != nvml.SUCCESS {
				log.Error(ret, "error obtaining computer instance for ", "poduuid", allocation.PodName)
				if ret == nvml.ERROR_NOT_FOUND {
					return fmt.Errorf("unable to find gi %v", ret)
				}
			}
			ret = ci.Destroy()
			if ret != nvml.SUCCESS {
				log.Error(ret, "failed to destroy Compute Instance", "ComputeInstanceId", migdevice.ciInfo.Id, "PodUUID", allocation.PodUUID)
				return fmt.Errorf("unable to destroy ci %v for %v", ret, allocation.PodName)
			}
			log.Info("successfully destroyed Compute Instance", "ComputeInstanceId", migdevice.ciInfo.Id)
			ret = gi.Destroy()
			if ret != nvml.SUCCESS {
				log.Error(ret, "failed to destroy GPU Instance", "GpuInstanceId", migdevice.giInfo.Id, "PodUUID", allocation.PodUUID)
				return fmt.Errorf("unable to destroy gi %v for %v", ret, allocation.PodName)
			}
			log.Info("successfully destroyed GPU Instance", "GpuInstanceId", migdevice.giInfo.Id)

			log.Info("deleted ci and gi for", "pod", allocation.PodName, logMigInfosSingleLine(migInfos))
			return nil

		}
	}
	exists, err := r.checkConfigMapExists(ctx, allocation.Resourceidentifier, allocation.Resourceidentifier)
	if err != nil {
		return err
	}
	if exists {
		log.Error(nil, "mig walking did not discover any slice for ", "pod", "migInfos", allocation.PodName, migInfos, logMigInfosSingleLine(migInfos))
		return fmt.Errorf("MIG slice not found for GPUUUID %v and Start %v", allocation.GPUUUID, allocation.Start)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstaSliceDaemonsetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	restConfig := mgr.GetConfig()

	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	if err := r.setupWithManager(mgr); err != nil {
		return err
	}

	// Init InstaSlice obj as the first thing when cache is loaded.
	// RunnableFunc is added to the manager.
	// This function waits for the manager to be elected (<-mgr.Elected()) and then runs InstaSlice init code.
	mgrAddErr := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		log := logr.FromContext(ctx)
		<-mgr.Elected() // Wait for the manager to be elected
		var instaslice inferencev1alpha1.Instaslice
		typeNamespacedName := types.NamespacedName{
			Name:      r.NodeName,
			Namespace: controller.InstaSliceOperatorNamespace,
		}
		errRetrievingInstaSliceForSetup := r.Get(ctx, typeNamespacedName, &instaslice)
		if errRetrievingInstaSliceForSetup != nil {
			log.Error(errRetrievingInstaSliceForSetup, "unable to fetch InstaSlice resource for node")
		}

		if !r.Config.EmulatorModeEnable {
			if !instaslice.Status.Processed || (instaslice.Name == "" && instaslice.Namespace == "") {
				_, errForDiscoveringGpus := r.discoverMigEnabledGpuWithSlices()
				if errForDiscoveringGpus != nil {
					log.Error(errForDiscoveringGpus, "error discovering GPUs")
				}
			}
		}

		errRetrievingInstaSlicePostSetup := r.Get(ctx, typeNamespacedName, &instaslice)
		if errRetrievingInstaSlicePostSetup != nil {
			log.Error(errRetrievingInstaSlicePostSetup, "unable to fetch InstaSlice resource for node")
			return errRetrievingInstaSlicePostSetup
		}

		if err := r.addMigCapacityToNode(ctx, &instaslice); err != nil {
			log.Error(err, "error adding mig capacity to node")
			return err
		}

		// Patch the node capacity with GPU memory in emulator mode
		if r.Config.EmulatorModeEnable {
			totalEmulatedGPUMemory := CalculateTotalMemoryGB(instaslice.Spec.MigGPUUUID)
			if err := r.patchNodeStatusForNode(ctx, r.NodeName, totalEmulatedGPUMemory); err != nil {
				return err
			}
		}

		return nil
	}))
	if mgrAddErr != nil {
		return mgrAddErr
	}

	return nil
}

// Enable creation of controller caches to talk to the API server in order to perform
// object discovery in SetupWithManager
func (r *InstaSliceDaemonsetReconciler) setupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&inferencev1alpha1.Instaslice{}).Named("InstaSliceDaemonSet").
		Complete(r)
}

func CalculateTotalMemoryGB(gpuInfoList map[string]string) int {
	totalMemoryGB := 0
	re := regexp.MustCompile(`(\d+)(GB)`)
	for _, gpuInfo := range gpuInfoList {
		matches := re.FindStringSubmatch(gpuInfo)
		if len(matches) == 3 {
			memoryGB, err := strconv.Atoi(matches[1])
			if err != nil {
				logr.FromContext(context.TODO()).Error(err, "unable to parse gpu memory value")
				continue
			}
			totalMemoryGB += memoryGB
		}
	}
	return totalMemoryGB
}

// This function discovers MIG devices as the plugin comes up. this is run exactly once.
func (r *InstaSliceDaemonsetReconciler) discoverMigEnabledGpuWithSlices() ([]string, error) {
	log := logr.FromContext(context.TODO())
	instaslice, _, gpuModelMap, failed, err := r.discoverAvailableProfilesOnGpus()
	if failed || err != nil {
		return nil, err
	}
	if instaslice == nil {
		// No MIG placements found while discovering profiles on GPUs
		// Hence, returning an error without creating an Instaslice object
		err := fmt.Errorf("unable to get instaslice object")
		return nil, err
	}

	totalMemoryGB := CalculateTotalMemoryGB(gpuModelMap)
	cpu, memory, err := r.classicalResourcesAndGPUMemOnNode(context.TODO(), r.NodeName, strconv.Itoa(totalMemoryGB))
	if err != nil {
		log.Error(err, "unable to get classical resources")
		os.Exit(1)
	}
	log.Info("classical resources obtained are ", "cpu", cpu, "memory", memory)
	instaslice.Spec.CpuOnNodeAtBoot = cpu
	instaslice.Spec.MemoryOnNodeAtBoot = memory
	instaslice.Name = r.NodeName
	instaslice.Namespace = controller.InstaSliceOperatorNamespace
	instaslice.Spec.MigGPUUUID = gpuModelMap
	instaslice.Status.Processed = true
	// TODO: should we use context.TODO() ?
	customCtx := context.TODO()
	errToCreate := r.Create(customCtx, instaslice)
	if errToCreate != nil {
		return nil, errToCreate
	}

	// Object exists, update its status
	instaslice.Status.Processed = true
	if errForStatus := r.Status().Update(customCtx, instaslice); errForStatus != nil {
		return nil, errForStatus
	}

	// Patch the node capacity to reflect the total GPU memory
	if err := r.patchNodeStatusForNode(customCtx, r.NodeName, totalMemoryGB); err != nil {
		return nil, err
	}

	return discoveredGpusOnHost, nil
}

func (r *InstaSliceDaemonsetReconciler) addMigCapacityToNode(ctx context.Context, instaslice *inferencev1alpha1.Instaslice) error {
	log := logr.FromContext(ctx)
	profilePlacements := make(map[string]int)
	node := &v1.Node{}
	nodeNameObject := types.NamespacedName{Name: instaslice.Name}
	if err := r.Get(ctx, nodeNameObject, node); err != nil {
		return err
	}
	for _, placement := range instaslice.Spec.Migplacement {
		for _, p := range placement.Placements {
			if p.Size > 0 {
				profilePlacements[placement.Profile]++
			}
		}
	}
	numGPUs := len(instaslice.Spec.MigGPUUUID)
	for profile, sum := range profilePlacements {
		profilePlacements[profile] = sum * numGPUs
	}
	patches := []map[string]interface{}{}
	for profile, count := range profilePlacements {
		resourceName := controller.OrgInstaslicePrefix + "mig-" + profile
		patches = append(patches, map[string]interface{}{
			"op":    "replace",
			"path":  "/status/capacity/" + strings.Replace(resourceName, "/", "~1", -1),
			"value": fmt.Sprintf("%d", count),
		})
	}

	patchData, err := json.Marshal(patches)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data: %v", err)
	}
	if err := r.Status().Patch(ctx, node, client.RawPatch(types.JSONPatchType, patchData)); err != nil {
		return fmt.Errorf("failed to patch node status: %v", err)
	}

	log.Info("Successfully patched node with possible maxMIG placement counts", "nodeName", instaslice.Name)
	return nil
}

// patchNodeStatusForNode fetches the node and patches its capacity with the given GPU memory
func (r *InstaSliceDaemonsetReconciler) patchNodeStatusForNode(ctx context.Context, nodeName string, totalMemoryGB int) error {
	log := logr.FromContext(ctx)
	// Fetch the node object
	node, err := r.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		log.Error(err, "unable to fetch Node")
		return err
	}

	// Patch the node capacity with total GPU memory

	// Create patch data for accelerator-memory-quota
	memory := resource.MustParse(fmt.Sprintf("%vGi", totalMemoryGB))
	patchData, err := createPatchData(controller.QuotaResourceName, memory)
	if err != nil {
		log.Error(err, "unable to create correct json for patching node")
		return err
	}
	// Apply the patch to the node capacity
	if err := r.Status().Patch(ctx, node, client.RawPatch(types.JSONPatchType, patchData)); err != nil {
		log.Error(err, "unable to patch Node capacity with accelerator GPU memory custom resource")
		return err
	}
	log.Info("Successfully patched node capacity with accelerator GPU memory custom resource", "Node", node.Name)

	return nil
}

func (r *InstaSliceDaemonsetReconciler) classicalResourcesAndGPUMemOnNode(ctx context.Context, nodeName string, totalGPUMemory string) (int64, int64, error) {
	log := logr.FromContext(ctx)
	node := &v1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		log.Error(err, "unable to retrieve cpu and memory resource on the node")
	}

	newResourceQuantity := resource.MustParse(totalGPUMemory + "Gi")
	// Convert the string to ResourceName
	resourceName := v1.ResourceName(controller.QuotaResourceName)
	if node.Status.Capacity == nil {
		resourceList := v1.ResourceList{
			resourceName: newResourceQuantity,
		}
		node.Status.Capacity = resourceList
	} else {
		node.Status.Capacity[resourceName] = newResourceQuantity
	}

	if err := r.Status().Update(ctx, node); err != nil {
		log.Error(err, "unable to patch the node with new resource")
		return 0, 0, err
	}

	// Allocatable = Capacity - System Reserved - Kube Reserved - eviction hard
	cpu := node.Status.Allocatable[v1.ResourceCPU]
	memory := node.Status.Allocatable[v1.ResourceMemory]
	cpuQuantity := cpu.Value()
	memoryQuantity := memory.Value()
	return cpuQuantity, memoryQuantity, nil
}

// during init time we need to discover GPU that are MIG enabled and slices if any on them to start making allocations of the next pods.
func (r *InstaSliceDaemonsetReconciler) discoverAvailableProfilesOnGpus() (*inferencev1alpha1.Instaslice, nvml.Return, map[string]string, bool, error) {
	log := logr.FromContext(context.TODO())
	instaslice := &inferencev1alpha1.Instaslice{}
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return nil, ret, nil, false, ret
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, ret, nil, false, ret
	}
	gpuModelMap := make(map[string]string)
	discoverProfilePerNode := true
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return nil, ret, nil, false, ret
		}

		uuid, _ := device.GetUUID()
		mode, _, ret := device.GetMigMode()
		if ret == nvml.ERROR_NOT_SUPPORTED {
			return instaslice, ret, gpuModelMap, false, fmt.Errorf("unable to detect mig mode")
		}
		if ret != nvml.SUCCESS {
			return instaslice, ret, gpuModelMap, false, fmt.Errorf("error getting MIG mode: %v", ret)
		}
		if mode != nvml.DEVICE_MIG_ENABLE {
			log.Info("mig mode not enabled on gpu with", "uuid", uuid)
			continue
		}
		gpuName, _ := device.GetName()
		gpuModelMap[uuid] = gpuName
		discoveredGpusOnHost = append(discoveredGpusOnHost, uuid)
		if discoverProfilePerNode {

			for i := 0; i < nvml.GPU_INSTANCE_PROFILE_COUNT; i++ {
				giProfileInfo, ret := device.GetGpuInstanceProfileInfo(i)
				if ret == nvml.ERROR_NOT_SUPPORTED {
					continue
				}
				if ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					return nil, ret, nil, false, ret
				}

				memory, ret := device.GetMemoryInfo()
				if ret != nvml.SUCCESS {
					return nil, ret, nil, false, ret
				}

				profile := NewMigProfile(i, i, nvml.COMPUTE_INSTANCE_ENGINE_PROFILE_SHARED, giProfileInfo.SliceCount, giProfileInfo.SliceCount, giProfileInfo.MemorySizeMB, memory.Total)

				giPossiblePlacements, ret := device.GetGpuInstancePossiblePlacements(&giProfileInfo)
				if ret == nvml.ERROR_NOT_SUPPORTED {
					continue
				}
				if ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					return nil, 0, nil, true, ret
				}
				placementsForProfile := []inferencev1alpha1.Placement{}
				for _, p := range giPossiblePlacements {
					placement := inferencev1alpha1.Placement{
						Size:  int(p.Size),
						Start: int(p.Start),
					}
					placementsForProfile = append(placementsForProfile, placement)
				}

				aggregatedPlacementsForProfile := inferencev1alpha1.Mig{
					Placements:     placementsForProfile,
					Profile:        profile.String(),
					Giprofileid:    i,
					CIProfileID:    profile.CIProfileID,
					CIEngProfileID: profile.CIEngProfileID,
				}
				instaslice.Spec.Migplacement = append(instaslice.Spec.Migplacement, aggregatedPlacementsForProfile)
			}
			discoverProfilePerNode = false
		}
	}
	return instaslice, ret, gpuModelMap, false, nil
}

// NewMigProfile constructs a new MigProfile struct using info from the giProfiles and ciProfiles used to create it.
func NewMigProfile(giProfileID, ciProfileID, ciEngProfileID int, giSliceCount, ciSliceCount uint32, migMemorySizeMB, totalDeviceMemoryBytes uint64) *MigProfile {
	return &MigProfile{
		C:              int(ciSliceCount),
		G:              int(giSliceCount),
		GB:             int(getMigMemorySizeInGB(totalDeviceMemoryBytes, migMemorySizeMB)),
		GIProfileID:    giProfileID,
		CIProfileID:    ciProfileID,
		CIEngProfileID: ciEngProfileID,
	}
}

// Helper function to get GPU memory size in GBs.
func getMigMemorySizeInGB(totalDeviceMemory, migMemorySizeMB uint64) uint64 {
	const fracDenominator = 8
	const oneMB = 1024 * 1024
	const oneGB = 1024 * 1024 * 1024
	fractionalGpuMem := (float64(migMemorySizeMB) * oneMB) / float64(totalDeviceMemory)
	fractionalGpuMem = math.Ceil(fractionalGpuMem*fracDenominator) / fracDenominator
	totalMemGB := float64((totalDeviceMemory + oneGB - 1) / oneGB)
	return uint64(math.Round(fractionalGpuMem * totalMemGB))
}

// String returns the string representation of a MigProfile.
func (m MigProfile) String() string {
	var suffix string
	if len(m.Attributes()) > 0 {
		suffix = "+" + strings.Join(m.Attributes(), ",")
	}
	if m.C == m.G {
		return fmt.Sprintf("%dg.%dgb%s", m.G, m.GB, suffix)
	}
	return fmt.Sprintf("%dc.%dg.%dgb%s", m.C, m.G, m.GB, suffix)
}

// Attributes returns the list of attributes associated with a MigProfile.
func (m MigProfile) Attributes() []string {
	var attr []string
	switch m.GIProfileID {
	case nvml.GPU_INSTANCE_PROFILE_1_SLICE_REV1:
		attr = append(attr, controller.AttributeMediaExtensions)
	}
	return attr
}

// Create configmap which is used by Pods to consume MIG device
func (r *InstaSliceDaemonsetReconciler) createConfigMap(ctx context.Context, migGPUUUID string, namespace string, resourceIdentifier string) error {
	log := logr.FromContext(ctx)
	var configMap v1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: resourceIdentifier, Namespace: namespace}, &configMap)
	if err != nil {
		log.Info("ConfigMap not found, creating for ", "name", resourceIdentifier, "migGPUUUID", migGPUUUID)
		configMapToCreate := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceIdentifier,
				Namespace: namespace,
			},
			Data: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": migGPUUUID,
				"CUDA_VISIBLE_DEVICES":   migGPUUUID,
			},
		}
		if err := r.Create(ctx, configMapToCreate); err != nil {
			log.Error(err, "failed to create ConfigMap")
			return err
		}

	}
	return nil
}

// Manage lifecycle of configmap, delete it once the pod is deleted from the system
func (r *InstaSliceDaemonsetReconciler) deleteConfigMap(ctx context.Context, configMapName string, namespace string) error {
	log := logr.FromContext(ctx)
	// Define the ConfigMap object with the name and namespace
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
	}

	err := r.Delete(ctx, configMap)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "configmap not found for ", "pod", configMapName)
			return nil
		}
		return err
	}

	log.Info("ConfigMap deleted successfully ", "name", configMapName)
	return nil
}

func createPatchData(resourceName string, resourceValue resource.Quantity) ([]byte, error) {
	patch := []ResPatchOperation{
		{
			Op:    "add",
			Path:  fmt.Sprintf("/status/capacity/%s", strings.ReplaceAll(resourceName, "/", "~1")),
			Value: resourceValue.String(),
		},
	}
	return json.Marshal(patch)
}

func walkMigDevices(d nvml.Device, f func(i int, d nvml.Device) error) error {
	count, ret := d.GetMaxMigDeviceCount()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("error getting max MIG device count: %v", ret)
	}

	for i := 0; i < count; i++ {
		device, ret := d.GetMigDeviceHandleByIndex(i)
		if ret == nvml.ERROR_NOT_FOUND {
			continue
		}
		if ret == nvml.ERROR_INVALID_ARGUMENT {
			continue
		}
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting MIG device handle at index '%v': %v", i, ret)
		}
		err := f(i, device)
		if err != nil {
			return err
		}
	}
	return nil
}

func populateMigDeviceInfos(device nvml.Device) (map[string]*MigDeviceInfo, error) {
	migInfos := make(map[string]*MigDeviceInfo)

	err := walkMigDevices(device, func(i int, migDevice nvml.Device) error {
		parentUuid, ret := device.GetUUID()
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting parent GPU UUID: %v", ret)
		}

		giID, ret := migDevice.GetGpuInstanceId()
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting GPU instance ID for MIG device: %v", ret)
		}

		gi, ret := device.GetGpuInstanceById(giID)
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting GPU instance for '%v': %v", giID, ret)
		}

		giInfo, ret := gi.GetInfo()
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting GPU instance info for '%v': %v", giID, ret)
		}

		ciID, ret := migDevice.GetComputeInstanceId()
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting Compute instance ID for MIG device: %v", ret)
		}

		ci, ret := gi.GetComputeInstanceById(ciID)
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting Compute instance for '%v': %v", ciID, ret)
		}

		ciInfo, ret := ci.GetInfo()
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting Compute instance info for '%v': %v", ciID, ret)
		}

		uuid, ret := migDevice.GetUUID()
		if ret != nvml.SUCCESS {
			return fmt.Errorf("error getting UUID for MIG device: %v", ret)
		}

		migInfos[uuid] = &MigDeviceInfo{
			uuid:   parentUuid,
			giInfo: &giInfo,
			ciInfo: &ciInfo,
			start:  giInfo.Placement.Start,
			size:   giInfo.Placement.Size,
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return migInfos, nil
}

// TODO move this to utils and refer to common function
func (r *InstaSliceDaemonsetReconciler) getInstasliceObject(ctx context.Context, instasliceName string, namespace string) (*inferencev1alpha1.Instaslice, error) {
	log := logr.FromContext(ctx)

	var updateInstasliceObject inferencev1alpha1.Instaslice

	typeNamespacedName := types.NamespacedName{
		Name:      instasliceName,
		Namespace: namespace,
	}

	err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
	if err != nil {
		log.Error(err, "Failed to get Instaslice object", "instasliceName", instasliceName, "namespace", namespace)
		return nil, err
	}

	return &updateInstasliceObject, nil
}

func (r *InstaSliceDaemonsetReconciler) createSliceAndPopulateMigInfos(ctx context.Context, device nvml.Device, allocations inferencev1alpha1.AllocationDetails, giProfileInfo nvml.GpuInstanceProfileInfo, placement nvml.GpuInstancePlacement, ciProfileId int) (map[string]*MigDeviceInfo, error) {
	log := logr.FromContext(ctx)

	log.Info("creating slice for", "pod", allocations.PodName)
	var gi nvml.GpuInstance
	var ret nvml.Return
	gi, ret = device.CreateGpuInstanceWithPlacement(&giProfileInfo, &placement)
	if ret != nvml.SUCCESS {
		switch ret {
		case nvml.ERROR_INSUFFICIENT_RESOURCES:
			// Handle insufficient resources case
			gpuInstances, ret := device.GetGpuInstances(&giProfileInfo)
			if ret != nvml.SUCCESS {
				log.Error(ret, "gpu instances cannot be listed")
				return nil, fmt.Errorf("gpu instances cannot be listed: %v", ret)
			}

			for _, gpuInstance := range gpuInstances {
				gpuInstanceInfo, ret := gpuInstance.GetInfo()
				if ret != nvml.SUCCESS {
					log.Error(ret, "unable to obtain gpu instance info")
					return nil, fmt.Errorf("unable to obtain gpu instance info: %v", ret)
				}

				parentUuid, ret := gpuInstanceInfo.Device.GetUUID()
				if ret != nvml.SUCCESS {
					log.Error(ret, "unable to obtain parent gpu uuuid")
					return nil, fmt.Errorf("unable to obtain parent gpu uuuid: %v", ret)
				}

				if gpuInstanceInfo.Placement.Start == allocations.Start && parentUuid == allocations.GPUUUID {
					gi, ret = device.GetGpuInstanceById(int(gpuInstanceInfo.Id))
					if ret != nvml.SUCCESS {
						log.Error(ret, "unable to obtain gi post iteration")
						return nil, fmt.Errorf("unable to obtain gi post iteration: %v", ret)
					}
				}
			}
		default:
			// this case is typically for scenario where ret is not equal to nvml.ERROR_INSUFFICIENT_RESOURCES
			log.Error(ret, "gpu instance creation errored out with unknown error")
			return nil, fmt.Errorf("gpu instance creation failed: %v", ret)
		}
	}

	ciProfileInfo, ret := gi.GetComputeInstanceProfileInfo(ciProfileId, 0)
	if ret != nvml.SUCCESS {
		log.Error(ret, "error getting compute instance profile info", "pod", allocations.PodName)
		return nil, fmt.Errorf("error getting compute instance profile info: %v", ret)
	}

	ci, ret := gi.CreateComputeInstance(&ciProfileInfo)
	if ret != nvml.SUCCESS {
		if ret != nvml.ERROR_INSUFFICIENT_RESOURCES {
			log.Error(ret, "error creating Compute instance", "ci", ci)
			return nil, fmt.Errorf("error creating compute instance: %v", ret)
		}
	}

	migInfos, err := populateMigDeviceInfos(device)
	if err != nil {
		log.Error(err, "unable to iterate over newly created MIG devices")
		return nil, fmt.Errorf("failed to populate MIG device infos: %v", err)
	}

	return migInfos, nil
}

func logMigInfosSingleLine(migInfos map[string]*MigDeviceInfo) string {
	var result string
	for key, info := range migInfos {
		giInfoStr := "nil"
		ciInfoStr := "nil"
		if info.giInfo != nil {
			giInfoStr = fmt.Sprintf("GpuInstanceId: %d", info.giInfo.Id)
		}
		if info.ciInfo != nil {
			ciInfoStr = fmt.Sprintf("ComputeInstanceId: %d", info.ciInfo.Id)
		}
		result += fmt.Sprintf("[Key: %s, UUID: %s, GI Info: %s, CI Info: %s, Start: %d, Size: %d] ",
			key, info.uuid, giInfoStr, ciInfoStr, info.start, info.size)
	}

	return result
}

func (r *InstaSliceDaemonsetReconciler) checkConfigMapExists(ctx context.Context, name, namespace string) (bool, error) {
	log := logr.FromContext(ctx)
	configMap := &v1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, configMap)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("ConfigMap not found", "name", name, "namespace", namespace)
			return false, nil
		}
		log.Error(err, "Error checking ConfigMap", "name", name, "namespace", namespace)
		return false, err
	}
	log.Info("ConfigMap exists", "name", name, "namespace", namespace)
	return true, nil
}
