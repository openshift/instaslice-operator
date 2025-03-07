package daemonset

import (
	context "context"
	"encoding/json"
	goerror "errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller"
	"github.com/openshift/instaslice-operator/internal/controller/config"
	"github.com/openshift/instaslice-operator/internal/controller/utils"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// InstaSliceDaemonsetReconciler reconciles an Instaslice object.
type InstaSliceDaemonsetReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	kubeClient *kubernetes.Clientset
	NodeName   string
	Config     *config.Config
}

// +kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;patch;watch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;list;update;patch;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
var (
	discoveredGpusOnHost []string
	initNvmlOnce         sync.Once
)

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

// MigDeviceInfo holds MIG device references discovered via NVML.
type MigDeviceInfo struct {
	uuid   string
	giInfo *nvml.GpuInstanceInfo
	ciInfo *nvml.ComputeInstanceInfo
	start  int32
	size   int32
}

func NewInstasliceDaemonsetReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	nodeName string,
	config *config.Config,
) (*InstaSliceDaemonsetReconciler, error) {

	r := &InstaSliceDaemonsetReconciler{
		Client:   client,
		Scheme:   scheme,
		NodeName: nodeName,
		Config:   config,
	}

	var err error
	initNvmlOnce.Do(func() {
		if config.EmulatorModeEnable {
			return
		}
		ret := nvml.Init()
		if ret != nvml.SUCCESS {
			err = goerror.New("Unable to initialize NVML")
		}
	})
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (r *InstaSliceDaemonsetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)

	if req.Name != r.NodeName {
		return ctrl.Result{}, nil
	}

	nsName := types.NamespacedName{
		Name:      r.NodeName,
		Namespace: controller.InstaSliceOperatorNamespace,
	}

	var instaslice inferencev1alpha1.Instaslice
	if err := r.Get(ctx, nsName, &instaslice); err != nil {
		log.Error(err, "Error getting Instaslice", "name", r.NodeName)
		return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, err
	}

	for podUID, allocResult := range instaslice.Status.PodAllocationResults {

		podRef := instaslice.Spec.PodAllocationRequests[podUID].PodRef

		// 1) Handle "deleting"
		if allocResult.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusDeleting &&
			allocResult.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated &&
			allocResult.Nodename == types.NodeName(r.NodeName) {

			log.Info("Performing cleanup for pod", "podRef", podRef)
			if !r.Config.EmulatorModeEnable {
				exists, err := r.checkConfigMapExists(ctx, string(allocResult.ConfigMapResourceIdentifier), podRef.Namespace)
				if err != nil {
					log.Error(err, "error checking configmap existence", "podRef", podRef)
					return ctrl.Result{RequeueAfter: controller.Requeue2sDelay}, err
				}
				if exists {
					err := r.cleanUpCiAndGi(ctx, &allocResult, podRef)
					if err != nil {
						// NVML shutdowm took time or NVML init may have failed.
						log.Error(err, "error cleaning up ci and gi retrying", podRef)
						return ctrl.Result{RequeueAfter: controller.Requeue2sDelay}, err
					}
				}
			}
			err := r.deleteConfigMap(ctx,
				string(allocResult.ConfigMapResourceIdentifier),
				podRef.Namespace)
			if err != nil && !errors.IsNotFound(err) {
				log.Error(err, "error deleting config map for pod", "pod", podRef.Name)
				return ctrl.Result{Requeue: true}, err
			}

			newAlloc := allocResult
			newAlloc.AllocationStatus.AllocationStatusDaemonset = inferencev1alpha1.AllocationStatusDeleted
			instaslice.Status.PodAllocationResults[podUID] = newAlloc
			if err := r.Status().Update(ctx, &instaslice); err != nil {
				log.Error(err, "error updating Instaslice status for pod cleanup", "podRef", podRef)
				return ctrl.Result{Requeue: true}, err
			}
			return ctrl.Result{}, nil
		}

		// 2) Handle "creating"
		if allocResult.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusCreating &&
			allocResult.AllocationStatus.AllocationStatusDaemonset == "" &&
			allocResult.Nodename == types.NodeName(r.NodeName) {
			exists, err := r.checkConfigMapExists(ctx, string(allocResult.ConfigMapResourceIdentifier), podRef.Namespace)
			if err != nil {
				log.Error(err, "error obtianing configmap", string(allocResult.ConfigMapResourceIdentifier))
				return ctrl.Result{RequeueAfter: controller.Requeue2sDelay}, err
			}
			log.Info("creating allocation for pod", "podRef", podRef)
			// We can look up the *request* in spec to see the profile or resource demands
			allocationRequest, haveReq := instaslice.Spec.PodAllocationRequests[podUID]
			if !haveReq {
				// There's no allocation request
				log.Info("No matching PodAllocationRequest for this result; skipping", podRef)
				continue
			}
			if !exists {
				if r.Config.EmulatorModeEnable {
					// configmap with fake MIG uuid
					err := r.createConfigMap(ctx,
						string(allocResult.ConfigMapResourceIdentifier),
						podRef.Namespace,
						string(allocResult.ConfigMapResourceIdentifier))
					if err != nil {
						log.Error(err, "failed to create config map (emulator mode)")
						return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, err
					}
					// Emulating cost to create CI and GI on a GPU
					time.Sleep(controller.Requeue1sDelay)
				} else {
					device, retCode := nvml.DeviceGetHandleByUUID(allocResult.GPUUUID)
					if retCode != nvml.SUCCESS {
						log.Error(retCode, "error getting GPU device handle", "gpuUUID", allocResult.GPUUUID)
						return ctrl.Result{}, goerror.New("error fetching GPU device handle")
					}

					selectedMig, ok := instaslice.Status.NodeResources.MigPlacement[allocationRequest.Profile]
					if !ok {
						log.Info("No suitable MIG profile in NodeResources; skipping creation", podRef, allocResult)
						continue
					}

					placement := nvml.GpuInstancePlacement{
						Start: uint32(allocResult.MigPlacement.Start),
						Size:  uint32(allocResult.MigPlacement.Size),
					}

					giProfileInfo, retGI := device.GetGpuInstanceProfileInfo(int(selectedMig.GIProfileID))
					if retGI != nvml.SUCCESS {
						log.Error(retGI, "error getting GPU instance profile info", "GIProfileID", selectedMig.GIProfileID)
						return ctrl.Result{}, goerror.New("cannot get GI profile info")
					}

					ciProfileID := selectedMig.CIProfileID

					createdMigInfos, err := r.createSliceAndPopulateMigInfos(
						ctx, device, giProfileInfo, placement, ciProfileID, podRef.Name)
					if err != nil {
						log.Error(err, "MIG creation not successful", "podRef", podRef)
						return ctrl.Result{RequeueAfter: controller.Requeue2sDelay}, err
					}

					for migUuid, migDevice := range createdMigInfos {
						if migDevice.start == allocResult.MigPlacement.Start && migDevice.uuid == allocResult.GPUUUID && giProfileInfo.Id == migDevice.giInfo.ProfileId {
							if err := r.createConfigMap(ctx, migUuid, podRef.Namespace, string(allocResult.ConfigMapResourceIdentifier)); err != nil {
								return ctrl.Result{RequeueAfter: controller.Requeue1sDelay}, err
							}
							log.Info("done creating mig slice for ", "pod", podRef.Name, "parentgpu", allocResult.GPUUUID, "miguuid", migUuid)
							break
						}
					}
				}
			}

			newAllocationRequest := instaslice.Spec.PodAllocationRequests[podUID]
			newAllocationResult := instaslice.Status.PodAllocationResults[podUID]
			newAllocationResult.AllocationStatus.AllocationStatusDaemonset = inferencev1alpha1.AllocationStatusCreated
			if err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, &newAllocationResult, &newAllocationRequest); err != nil {
				return ctrl.Result{Requeue: true}, err
			}

			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{}, nil
}

// cleanUpCiAndGi tears down the MIG compute instance and GPU instance.
func (r *InstaSliceDaemonsetReconciler) cleanUpCiAndGi(ctx context.Context, allocationResult *inferencev1alpha1.AllocationResult, podRef v1.ObjectReference) error {
	log := logr.FromContext(ctx)

	parent, ret := nvml.DeviceGetHandleByUUID(allocationResult.GPUUUID)
	if ret != nvml.SUCCESS {
		log.Error(ret, "error obtaining GPU handle for cleanup")
		return fmt.Errorf("unable to get device handle: %v", ret)
	}

	migInfos, err := populateMigDeviceInfos(parent)
	if err != nil {
		return fmt.Errorf("unable to walk MIGs: %v", err)
	}

	for miguuid, migdevice := range migInfos {
		if migdevice.uuid == allocationResult.GPUUUID && migdevice.start == allocationResult.MigPlacement.Start &&
			migdevice.size == allocationResult.MigPlacement.Size {
			gi, ret := parent.GetGpuInstanceById(int(migdevice.giInfo.Id))
			if ret != nvml.SUCCESS {
				log.Error(ret, "error obtaining gpu instance")
				return fmt.Errorf("unable to find GI: %v", ret)
			}
			ci, ret := gi.GetComputeInstanceById(int(migdevice.ciInfo.Id))
			if ret != nvml.SUCCESS {
				log.Error(ret, "error obtaining compute instance")
				return fmt.Errorf("unable to find CI: %v", ret)
			}
			// Destroy CI
			ret = ci.Destroy()
			if ret != nvml.SUCCESS {
				return fmt.Errorf("unable to destroy CI: %v", ret)
			}
			// Destroy GI
			ret = gi.Destroy()
			if ret != nvml.SUCCESS {
				return fmt.Errorf("unable to destroy GI: %v", ret)
			}

			log.Info("Successfully destroyed MIG resources", "allocationResult", allocationResult, "podRef", podRef, "MIGuuid", miguuid)
			return nil
		}
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
		err := r.Get(ctx, typeNamespacedName, &instaslice)
		if err != nil {
			log.Error(err, "Unable to fetch Instaslice for node", "nodeName", r.NodeName)
		}

		if r.Config.EmulatorModeEnable {
			fakeCapacity := utils.GenerateFakeCapacity(r.NodeName)
			err := r.Create(ctx, fakeCapacity)
			if err != nil && !errors.IsAlreadyExists(err) {
				log.Error(err, "could not create fake capacity", "node_name", r.NodeName)
				return err
			}
			err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 10*time.Second, true, func(ctx context.Context) (done bool, err error) {
				err = r.Get(ctx, typeNamespacedName, &instaslice)
				if err == nil {
					return true, nil // Success, stop polling
				}
				log.Error(err, "Retrying fetch fake capacity", "node_name", r.NodeName)
				return false, nil // Continue retrying
			})

			if err != nil {
				log.Error(err, "Failed to fetch fake capacity after retries", "node_name", r.NodeName)
			}
			fakeCapacity = utils.GenerateFakeCapacity(r.NodeName)
			instaslice.Name = fakeCapacity.Name
			instaslice.Namespace = fakeCapacity.Namespace
			instaslice.Status = fakeCapacity.Status
			err = r.Status().Update(ctx, &instaslice)
			if err != nil {
				log.Error(err, "could not update fake capacity", "node_name", r.NodeName)
				return err
			}
			// let the update propagate
			// time.Sleep(2 * time.Second)
			err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 10*time.Second, true, func(ctx context.Context) (done bool, err error) {
				err = r.Get(ctx, typeNamespacedName, &instaslice)
				if err != nil {
					log.Error(err, "Failed to fetch instaslice status", "node_name", r.NodeName)
					return false, nil
				}

				if len(instaslice.Status.NodeResources.NodeGPUs) == len(fakeCapacity.Status.NodeResources.NodeGPUs) {
					return true, nil
				}

				log.Info("Waiting for instaslice to become ready", "node_name", r.NodeName)
				return false, nil
			})

			if err != nil {
				log.Error(err, "Timed out waiting for instaslice status", "node_name", r.NodeName)
			}
		} else {
			if instaslice.Status.NodeResources.NodeGPUs == nil { // TODO - use those conditions!
				if _, e := r.discoverMigEnabledGpuWithSlices(); e != nil {
					log.Error(e, "Error discovering MIG GPUs on node init")
				}
			}
		}

		if err := r.Get(ctx, typeNamespacedName, &instaslice); err != nil {
			log.Error(err, "Unable to fetch Instaslice after discovery", "nodeName", r.NodeName)
			return err
		}
		if err := r.addMigCapacityToNode(ctx, &instaslice); err != nil {
			log.Error(err, "error adding mig capacity to node")
			return err
		}

		// Patch the node capacity with GPU memory in emulator mode
		if r.Config.EmulatorModeEnable {
			fakeCapacity := utils.GenerateFakeCapacity(r.NodeName)
			totalEmulatedGPUMemory, err := CalculateTotalMemoryGB(fakeCapacity.Status.NodeResources.NodeGPUs)
			if err != nil {
				log.Error(err, "unable to get the total GPU memory")
				return err
			}
			if err := r.patchNodeStatusForNode(ctx, r.NodeName, int(totalEmulatedGPUMemory)); err != nil {
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

func CalculateTotalMemoryGB(nodeGPUs []inferencev1alpha1.DiscoveredGPU) (float64, error) {
	var totalMemoryGB float64

	for _, nodeGPU := range nodeGPUs {
		totalMemoryGB += float64(nodeGPU.GPUMemory.AsApproximateFloat64() / (1024 * 1024 * 1024))
	}

	return totalMemoryGB, nil
}

// This function discovers MIG devices as the plugin comes up. this is run exactly once.
func (r *InstaSliceDaemonsetReconciler) discoverMigEnabledGpuWithSlices() ([]string, error) {
	log := logr.FromContext(context.TODO())

	instaslice := &inferencev1alpha1.Instaslice{}
	instaslice.Name = r.NodeName
	instaslice.Namespace = controller.InstaSliceOperatorNamespace

	customCtx := context.TODO()
	errToCreate := r.Create(customCtx, instaslice)
	if errToCreate != nil {
		return nil, errToCreate
	}
	instaslice, _, _, err := r.discoverAvailableProfilesOnGpus(instaslice)
	if err != nil {
		return nil, err
	}
	totalMemoryGB, err := CalculateTotalMemoryGB(instaslice.Status.NodeResources.NodeGPUs)
	if err != nil {
		log.Error(err, "unable to get GPU memory")
		return nil, err
	}
	nodeResourceList, err := r.classicalResourcesAndGPUMemOnNode(context.TODO(), r.NodeName, strconv.FormatFloat(totalMemoryGB, 'f', 2, 64))
	if err != nil {
		log.Error(err, "unable to get classical resources")
		os.Exit(1)
	}

	instaslice.Status.NodeResources.NodeResources = nodeResourceList

	log.Info("classical resources obtained are ", "cpu", nodeResourceList.Cpu().String(), "memory", nodeResourceList.Memory().String())
	if err = r.Status().Update(customCtx, instaslice); err != nil {
		return nil, err
	}

	// Patch the node capacity to reflect the total GPU memory
	if err := r.patchNodeStatusForNode(customCtx, r.NodeName, int(totalMemoryGB)); err != nil {
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
	for profile, placement := range instaslice.Status.NodeResources.MigPlacement {
		for _, p := range placement.Placements {
			if p.Size > 0 {
				profilePlacements[profile]++
			}
		}
	}
	numGPUs := len(instaslice.Status.NodeResources.NodeGPUs)
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

func (r *InstaSliceDaemonsetReconciler) classicalResourcesAndGPUMemOnNode(ctx context.Context, nodeName string, totalGPUMemory string) (corev1.ResourceList, error) {
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
		return nil, err
	}

	// Allocatable = Capacity - System Reserved - Kube Reserved - eviction hard
	resourceList := corev1.ResourceList{}
	resourceList[corev1.ResourceCPU] = node.Status.Allocatable[v1.ResourceCPU]
	resourceList[corev1.ResourceMemory] = node.Status.Allocatable[v1.ResourceMemory]

	return resourceList, nil
}

// during init time we need to discover GPU that are MIG enabled and slices if any on them to start making allocations of the next pods.
func (r *InstaSliceDaemonsetReconciler) discoverAvailableProfilesOnGpus(instaslice *inferencev1alpha1.Instaslice) (*inferencev1alpha1.Instaslice, nvml.Return, bool, error) {
	log := logr.FromContext(context.TODO())
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, ret, false, ret
	}
	gpuModelMap := make(map[string]string)

	nodeGPUs := make([]inferencev1alpha1.DiscoveredGPU, count)

	discoverProfilePerNode := true
	var memory nvml.Memory
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return nil, ret, false, ret
		}

		uuid, _ := device.GetUUID()
		mode, _, ret := device.GetMigMode()
		if ret == nvml.ERROR_NOT_SUPPORTED {
			return instaslice, ret, false, fmt.Errorf("unable to detect mig mode")
		}
		if ret != nvml.SUCCESS {
			return instaslice, ret, false, fmt.Errorf("error getting MIG mode: %v", ret)
		}
		if mode != nvml.DEVICE_MIG_ENABLE {
			log.Info("mig mode not enabled on gpu with", "uuid", uuid)
			continue
		}
		// TODO -
		memory, ret = device.GetMemoryInfo()
		if ret != nvml.SUCCESS {
			return nil, ret, false, ret
		}
		gpuName, _ := device.GetName()
		gpuModelMap[uuid] = gpuName
		nodeGPUs[i].GPUUUID = uuid
		nodeGPUs[i].GPUName = gpuName
		nodeGPUs[i].GPUMemory = *resource.NewQuantity(int64(memory.Total), resource.BinarySI)
		discoveredGpusOnHost = append(discoveredGpusOnHost, uuid)
		if discoverProfilePerNode {

			for j := 0; j < nvml.GPU_INSTANCE_PROFILE_COUNT; j++ {
				giProfileInfo, ret := device.GetGpuInstanceProfileInfo(j)
				if ret == nvml.ERROR_NOT_SUPPORTED {
					continue
				}
				if ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					return nil, ret, false, ret
				}

				memory, ret := device.GetMemoryInfo()
				if ret != nvml.SUCCESS {
					return nil, ret, false, ret
				}

				profile := NewMigProfile(j, j, nvml.COMPUTE_INSTANCE_ENGINE_PROFILE_SHARED, giProfileInfo.SliceCount, giProfileInfo.SliceCount, giProfileInfo.MemorySizeMB, memory.Total)

				giPossiblePlacements, ret := device.GetGpuInstancePossiblePlacements(&giProfileInfo)
				if ret == nvml.ERROR_NOT_SUPPORTED {
					continue
				}
				if ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					return nil, 0, true, ret
				}
				placementsForProfile := []inferencev1alpha1.Placement{}
				for _, p := range giPossiblePlacements {
					placement := inferencev1alpha1.Placement{
						Size:  int32(p.Size),
						Start: int32(p.Start),
					}
					placementsForProfile = append(placementsForProfile, placement)
				}

				aggregatedPlacementsForProfile := inferencev1alpha1.Mig{
					Placements:     placementsForProfile,
					GIProfileID:    int32(j),
					CIProfileID:    int32(profile.CIProfileID),
					CIEngProfileID: int32(profile.CIEngProfileID),
				}
				if instaslice.Status.NodeResources.MigPlacement == nil {
					instaslice.Status.NodeResources.MigPlacement = make(map[string]inferencev1alpha1.Mig)
				}
				instaslice.Status.NodeResources.MigPlacement[profile.String()] = aggregatedPlacementsForProfile
			}
			discoverProfilePerNode = false
		}
		instaslice.Status.NodeResources.NodeGPUs = nodeGPUs
	}
	return instaslice, ret, false, nil
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
			start:  int32(giInfo.Placement.Start),
			size:   int32(giInfo.Placement.Size),
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return migInfos, nil
}

func (r *InstaSliceDaemonsetReconciler) createSliceAndPopulateMigInfos(ctx context.Context, device nvml.Device, giProfileInfo nvml.GpuInstanceProfileInfo, placement nvml.GpuInstancePlacement, ciProfileId int32, podName string) (map[string]*MigDeviceInfo, error) {
	log := logr.FromContext(ctx)
	log.Info("creating slice for", "pod", podName)
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
				gpuUUid, ret := device.GetUUID()
				if ret != nvml.SUCCESS {
					log.Error(ret, "unable to obtain parent gpu uuuid")
				}
				if gpuInstanceInfo.Placement.Start == uint32(placement.Start) && parentUuid == gpuUUid {
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
		return nil, fmt.Errorf("error creating gpu instance profile with: %v", ret)
	}

	ciProfileInfo, ret := gi.GetComputeInstanceProfileInfo(int(ciProfileId), 0)
	if ret != nvml.SUCCESS {
		log.Error(ret, "error getting compute instance profile info", "pod", podName)
		return nil, fmt.Errorf("error getting compute instance profile info: %v", ret)
	}

	ci, ret := gi.CreateComputeInstance(&ciProfileInfo)
	if ret != nvml.SUCCESS {
		if ret != nvml.ERROR_INSUFFICIENT_RESOURCES {
			log.Error(ret, "error creating Compute instance", "ci", ci)
		}
	}

	migInfos, err := populateMigDeviceInfos(device)
	if err != nil {
		log.Error(err, "unable to iterate over newly created MIG devices")
		return nil, fmt.Errorf("failed to populate MIG device infos: %v", err)
	}

	return migInfos, nil
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
