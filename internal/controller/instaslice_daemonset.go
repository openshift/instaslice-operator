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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	inferencev1alpha1 "codeflare.dev/instaslice/api/v1alpha1"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	nvdevice "github.com/NVIDIA/go-nvlib/pkg/nvlib/device"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstaSliceDaemonsetReconciler reconciles a InstaSliceDaemonset object
type InstaSliceDaemonsetReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	kubeClient *kubernetes.Clientset
	NodeName   string
}

//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

var discoveredGpusOnHost []string

// Additional handler used for making NVML calls.
type deviceHandler struct {
	nvdevice nvdevice.Interface
	nvml     nvml.Interface
}

type MigProfile struct {
	C              int
	G              int
	GB             int
	GIProfileID    int
	CIProfileID    int
	CIEngProfileID int
}

type ResPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

const (
	// AttributeMediaExtensions holds the string representation for the media extension MIG profile attribute.
	AttributeMediaExtensions = "me"
)

type preparedMig struct {
	gid     uint32
	miguuid string
	cid     uint32
}

var cachedPreparedMig = make(map[string]preparedMig)

func (r *InstaSliceDaemonsetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	nodeName := os.Getenv("NODE_NAME")
	nsName := types.NamespacedName{
		Name:      nodeName,
		Namespace: "default",
	}
	var instaslice inferencev1alpha1.Instaslice
	if err := r.Get(ctx, nsName, &instaslice); err != nil {
		log.FromContext(ctx).Error(err, "Error listing Instaslice")
	}

	for _, allocations := range instaslice.Spec.Allocations {
		if allocations.Allocationstatus == "creating" {
			//Assume pod only has one container with one GPU request
			log.FromContext(ctx).Info("creating allocation for ", "pod", allocations.PodName)
			var podUUID = allocations.PodUUID
			ret := nvml.Init()
			if ret != nvml.SUCCESS {
				log.FromContext(ctx).Error(ret, "Unable to initialize NVML")
			}

			availableGpus, ret := nvml.DeviceGetCount()
			if ret != nvml.SUCCESS {
				log.FromContext(ctx).Error(ret, "Unable to get device count")
			}

			if errCreatingInstaSliceResource := r.createInstaSliceResource(ctx, nodeName, allocations.PodName); errCreatingInstaSliceResource != nil {
				return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
			}

			deviceForMig, profileName, Giprofileid, Ciprofileid, CiEngProfileid, errGettingControllerAllocation := r.getAllocation(instaslice, allocations.PodUUID)
			if errGettingControllerAllocation != nil {
				log.FromContext(ctx).Error(errGettingControllerAllocation, "allocation was not found, retrying will not help")
				return ctrl.Result{}, nil
			}
			placement := nvml.GpuInstancePlacement{}
			for i := 0; i < availableGpus; i++ {
				existingAllocations := instaslice.Spec.Allocations[podUUID]

				device, ret := nvml.DeviceGetHandleByIndex(i)
				if ret != nvml.SUCCESS {
					log.FromContext(ctx).Error(ret, "Unable to get device at index")
				}

				uuid, ret := device.GetUUID()
				if ret != nvml.SUCCESS {
					log.FromContext(ctx).Error(ret, "Unable to get uuid of device at index")
				}
				if deviceForMig != uuid {
					continue
				}

				//Move to next GPU as this is not the selected GPU by the controller.
				if allocations.GPUUUID != uuid {
					continue
				}
				//TODO: any GPU can fail creating CI and GI
				if _, exists := cachedPreparedMig[allocations.PodName]; !exists {
					var giInfo nvml.GpuInstanceInfo
					log.FromContext(ctx).Info("Slice does not exists on GPU for ", "pod", allocations.PodName)

					device, retCodeForDevice := nvml.DeviceGetHandleByUUID(uuid)

					if retCodeForDevice != nvml.SUCCESS {
						log.FromContext(ctx).Error(ret, "error getting GPU device handle")
					}

					giProfileInfo, retCodeForGi := device.GetGpuInstanceProfileInfo(Giprofileid)
					if retCodeForGi != nvml.SUCCESS {
						log.FromContext(ctx).Error(retCodeForGi, "error getting GPU instance profile info", "giProfileInfo", giProfileInfo, "retCodeForGi", retCodeForGi)
					}

					log.FromContext(ctx).Info("The profile id is", "giProfileInfo", giProfileInfo.Id, "Memory", giProfileInfo.MemorySizeMB, "pod", podUUID)

					updatedPlacement, err := r.getAllocationsToprepare(ctx, placement, instaslice, allocations.PodUUID)
					if err != nil {
						//this should never happen, if it does then there is an issue with controller accounting logic
						log.FromContext(ctx).Error(err, "prepared already exists for ", "pod", allocations.PodName)
						return ctrl.Result{}, nil
					}

					gi, retCodeForGiWithPlacement := device.CreateGpuInstanceWithPlacement(&giProfileInfo, &updatedPlacement)
					if retCodeForGiWithPlacement != nvml.SUCCESS {
						log.FromContext(ctx).Error(retCodeForGiWithPlacement, "error creating GPU instance for ", "gi", &gi)
						//No point in retrying the same allocation
						return ctrl.Result{}, nil
					}
					giInfo, retForGiInfor := gi.GetInfo()
					if retForGiInfor != nvml.SUCCESS {
						log.FromContext(ctx).Error(retForGiInfor, "error getting GPU instance info for ", "giInfo", &giInfo)

					}
					//TODO: figure out the compute slice scenario, I think Kubernetes does not support this use case yet
					ciProfileInfo, retCodeForCiProfile := gi.GetComputeInstanceProfileInfo(Ciprofileid, CiEngProfileid)
					if retCodeForCiProfile != nvml.SUCCESS {
						//TODO: clean up GI and then return
						//No point in retrying the same allocation
						log.FromContext(ctx).Error(retCodeForCiProfile, "error getting Compute instance profile info for ", "ciProfileInfo", ciProfileInfo)
						return ctrl.Result{}, nil
					}
					ci, retCodeForComputeInstance := gi.CreateComputeInstance(&ciProfileInfo)
					if retCodeForComputeInstance != nvml.SUCCESS {
						log.FromContext(ctx).Error(retCodeForComputeInstance, "error creating Compute instance for ", "ci", ci)
					}

					//get created mig details
					giId, migUUID, ciId := r.getCreatedSliceDetails(ctx, giInfo, ret, device, uuid, profileName)
					//add ci and gi values to cache so that we avoid re-creating. if ci or gi creation fails, we need to clean up.
					cachedPreparedMig[allocations.PodName] = preparedMig{gid: giId, miguuid: migUUID, cid: ciId}
				}

				createdSliceDetails := cachedPreparedMig[allocations.PodName]
				log.FromContext(ctx).Info("The created cache details loaded are for allocation ", "pod name", allocations.PodName, "slice details", createdSliceDetails)
				//making sure that ci, gi and migUUID are not nil or dafault for the target pod.
				if createdSliceDetails.miguuid != "" {

					if errCreatingConfigMap := r.createConfigMap(ctx, createdSliceDetails.miguuid, existingAllocations.Namespace, existingAllocations.PodName); errCreatingConfigMap != nil {
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}

					if errAddingPrepared := r.createPreparedEntry(ctx, profileName, podUUID, allocations.GPUUUID, createdSliceDetails.gid, createdSliceDetails.cid, &instaslice, createdSliceDetails.miguuid); errAddingPrepared != nil {
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					nodeName := os.Getenv("NODE_NAME")
					if errUpdatingNodeCapacity := r.updateNodeCapacity(ctx, nodeName); errUpdatingNodeCapacity != nil {
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					var updateInstasliceObject inferencev1alpha1.Instaslice
					typeNamespacedName := types.NamespacedName{
						Name:      instaslice.Name,
						Namespace: "default", // TODO: modify
					}

					//TODO: could be merged with the deleted call below
					err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
					if err != nil {
						log.FromContext(ctx).Error(err, "error getting latest instaslice object")
					}
					existingAllocations.Allocationstatus = "created"
					updateInstasliceObject.Spec.Allocations[podUUID] = existingAllocations
					errForUpdate := r.Update(ctx, &updateInstasliceObject)
					if errForUpdate != nil {
						log.FromContext(ctx).Error(errForUpdate, "error adding prepared statement\n")
						return ctrl.Result{Requeue: true}, nil
					}
				}
				//if cache does not have a valid entry do nothing
				// return ctrl.Result{}, nil

			}

		}

		if allocations.Allocationstatus == "deleted" {
			log.FromContext(ctx).Info("Performing cleanup ", "pod", allocations.PodName)
			if errDeletingCm := r.deleteConfigMap(ctx, allocations.PodName, allocations.Namespace); errDeletingCm != nil {
				log.FromContext(ctx).Error(errDeletingCm, "error deleting configmap for ", "pod", allocations.PodName)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}

			if errDeletingInstaSliceResource := r.cleanUpInstaSliceResource(ctx, allocations.PodName); errDeletingInstaSliceResource != nil {
				log.FromContext(ctx).Error(errDeletingInstaSliceResource, "Error deleting InstaSlice resource object")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}

			nodeName := os.Getenv("NODE_NAME")
			if errUpdatingNodeCapacity := r.updateNodeCapacity(ctx, nodeName); errUpdatingNodeCapacity != nil {
				return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
			}
			deletePrepared := r.cleanUpCiAndGi(ctx, allocations.PodUUID, instaslice)
			log.FromContext(ctx).Info("Done deleting ci and gi for ", "pod", allocations.PodName)
			delete(cachedPreparedMig, allocations.PodName)

			//TODO: could be merged with the creating call above
			var updateInstasliceObject inferencev1alpha1.Instaslice
			typeNamespacedName := types.NamespacedName{
				Name:      instaslice.Name,
				Namespace: "default", // TODO: modify
			}
			err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
			if err != nil {
				log.FromContext(ctx).Error(err, "error getting latest instaslice object")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			delete(updateInstasliceObject.Spec.Prepared, deletePrepared)
			delete(updateInstasliceObject.Spec.Allocations, allocations.PodUUID)
			errUpdatingAllocation := r.Update(ctx, &updateInstasliceObject)
			if errUpdatingAllocation != nil {
				log.FromContext(ctx).Error(errUpdatingAllocation, "Error updating InstaSlice object for ", "pod", allocations.PodName)
				return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
			}

			// return ctrl.Result{}, nil
		}

	}

	return ctrl.Result{}, nil
}

func (r *InstaSliceDaemonsetReconciler) createInstaSliceResource(ctx context.Context, nodeName string, podName string) error {
	node := &v1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		log.FromContext(ctx).Error(err, "unable to fetch Node")
		return err
	}
	capacityKey := "org.instaslice/" + podName
	//desiredCapacity := resource.MustParse("1")
	if _, exists := node.Status.Capacity[v1.ResourceName(capacityKey)]; exists {
		log.FromContext(ctx).Info("Node already patched with ", "capacity", capacityKey)
		return nil
	}
	patchData, err := createPatchData("org.instaslice/"+podName, "1")
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to create correct json for patching node")
		return err
	}

	if err := r.Status().Patch(ctx, node, client.RawPatch(types.JSONPatchType, patchData)); err != nil {
		log.FromContext(ctx).Error(err, "unable to patch Node status")
		return err
	}
	return nil
}

func (r *InstaSliceDaemonsetReconciler) getAllocationsToprepare(ctx context.Context, placement nvml.GpuInstancePlacement, instaslice inferencev1alpha1.Instaslice, podUuid string) (nvml.GpuInstancePlacement, error) {
	allocationExists := false
	for _, prepared := range instaslice.Spec.Prepared {
		if prepared.PodUUID == podUuid {
			allocationExists = true
		}
	}
	for _, v := range instaslice.Spec.Allocations {
		if !allocationExists {
			if v.Allocationstatus == "creating" && v.PodUUID == podUuid {
				placement.Size = v.Size
				placement.Start = v.Start
				return placement, nil
			}
		}
	}
	//TODO: handle empty placement object
	log.FromContext(ctx).Info("placement not found for ", "podUuid", podUuid)
	return placement, fmt.Errorf("got prepared slice wait for object to be updated")
}

func (*InstaSliceDaemonsetReconciler) getCreatedSliceDetails(ctx context.Context, giInfo nvml.GpuInstanceInfo, ret nvml.Return, device nvml.Device, uuid string, profileName string) (uint32, string, uint32) {

	h := &deviceHandler{}
	h.nvml = nvml.New()
	h.nvdevice = nvdevice.New(nvdevice.WithNvml(h.nvml))

	ret1 := h.nvml.Init()
	if ret1 != nvml.SUCCESS {
		log.FromContext(ctx).Error(ret1, "Unable to initialize NVML")
	}
	nvlibParentDevice, err := h.nvdevice.NewDevice(device)
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to get nvlib GPU parent device for MIG UUID")
	}
	migs, err := nvlibParentDevice.GetMigDevices()
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to get MIG devices on GPU")
	}
	for _, mig := range migs {
		obtainedProfileName, _ := mig.GetProfile()
		giID, retForMigGPU := mig.GetGpuInstanceId()
		if retForMigGPU != nvml.SUCCESS {
			log.FromContext(ctx).Error(retForMigGPU, "error getting GPU instance ID for MIG device")
		}
		gpuInstance, errGettingMigGi := device.GetGpuInstanceById(giID)
		if errGettingMigGi != nvml.SUCCESS {
			log.FromContext(ctx).Error(errGettingMigGi, "Unable to get GPU instance")
		}

		if profileName == obtainedProfileName.String() && giID == int(giInfo.Id) {
			realizedMig, errGettingMigUid := mig.GetUUID()
			if errGettingMigUid != nvml.SUCCESS {
				log.FromContext(ctx).Error(errGettingMigGi, "Unable to get MIG uid")
			}
			migCid, errGettingMigComputeid := mig.GetComputeInstanceId()
			if errGettingMigComputeid != nvml.SUCCESS {
				log.FromContext(ctx).Error(errGettingMigComputeid, "Unable to get MIG ci")
			}
			ci, errGettingComputeInstanceId := gpuInstance.GetComputeInstanceById(migCid)
			if errGettingComputeInstanceId != nvml.SUCCESS {
				log.FromContext(ctx).Error(errGettingComputeInstanceId, "Unable to get MIG cid")
			}
			ciMigInfo, errGettingCiInfo := ci.GetInfo()
			if errGettingCiInfo != nvml.SUCCESS {
				log.FromContext(ctx).Error(errGettingCiInfo, "Unable to get ci info")
			}
			log.FromContext(ctx).Info("Prepared details", "giId", giInfo.Id, "migUUID", realizedMig, "ciId", ciMigInfo.Id)
			return giInfo.Id, realizedMig, ciMigInfo.Id
		}
	}
	//TODO: handle this error
	return 0, "", 0
}

func (r *InstaSliceDaemonsetReconciler) getAllocation(instaslice inferencev1alpha1.Instaslice, podUuid string) (string, string, int, int, int, error) {

	for _, v := range instaslice.Spec.Allocations {
		if v.Allocationstatus == "creating" && v.PodUUID == podUuid {
			return v.GPUUUID, v.Profile, v.Giprofileid, v.CIProfileID, v.CIEngProfileID, nil
		}
	}
	//TODO handle error
	return "", "", -1, -1, -1, fmt.Errorf("allocation with PodUUID %s not found", podUuid)
}

func (r *InstaSliceDaemonsetReconciler) cleanUpCiAndGi(ctx context.Context, podUuid string, instaslice inferencev1alpha1.Instaslice) string {
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		log.FromContext(ctx).Error(ret, "Unable to initialize NVML")
	}

	var candidateDel string
	prepared := instaslice.Spec.Prepared
	for migUUID, value := range prepared {
		if value.PodUUID == podUuid {
			parent, errRecievingDeviceHandle := nvml.DeviceGetHandleByUUID(value.Parent)
			if errRecievingDeviceHandle != nvml.SUCCESS {
				log.FromContext(ctx).Error(errRecievingDeviceHandle, "Error obtaining GPU handle")
			}
			gi, errRetrievingGi := parent.GetGpuInstanceById(int(value.Giinfoid))
			if errRetrievingGi != nvml.SUCCESS {
				log.FromContext(ctx).Error(errRetrievingGi, "Error obtaining GPU instance")
			}
			ci, errRetrievingCi := gi.GetComputeInstanceById(int(value.Ciinfoid))
			if errRetrievingCi != nvml.SUCCESS {
				log.FromContext(ctx).Error(errRetrievingCi, "Error obtaining Compute instance")
			}
			errDestroyingCi := ci.Destroy()
			if errDestroyingCi != nvml.SUCCESS {
				log.FromContext(ctx).Error(errDestroyingCi, "Error deleting Compute instance")
			}
			errDestroyingGi := gi.Destroy()
			if errDestroyingGi != nvml.SUCCESS {
				log.FromContext(ctx).Error(errDestroyingGi, "Error deleting GPU instance")
			}
			candidateDel = migUUID
			log.FromContext(ctx).Info("Done deleting MIG slice for pod", "UUID", value.PodUUID)
		}
	}

	return candidateDel
}

func (r *InstaSliceDaemonsetReconciler) cleanUpInstaSliceResource(ctx context.Context, podName string) error {
	nodeName := os.Getenv("NODE_NAME")
	deletePatch, err := deletePatchData(podName)
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to create delete json patch data")
		return err
	}

	// Apply the patch to remove the resource
	node := &v1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		log.FromContext(ctx).Error(err, "unable to fetch Node")
		return err
	}
	resourceName := v1.ResourceName(fmt.Sprintf("org.instaslice/%s", podName))
	//&& val.String() == "1"
	if _, ok := node.Status.Capacity[resourceName]; !ok {
		log.FromContext(ctx).Info("skipping non-existent deletion of instaslice resource for ", "pod", podName)
		return nil
	}
	if err := r.Status().Patch(ctx, node, client.RawPatch(types.JSONPatchType, deletePatch)); err != nil {
		log.FromContext(ctx).Error(err, "unable to patch Node status")
		return err
	}
	return nil
}

func (r *InstaSliceDaemonsetReconciler) createPreparedEntry(ctx context.Context, profileName string, podUUID string, deviceUUID string, giId uint32, ciId uint32, instaslice *inferencev1alpha1.Instaslice, migUUID string) error {
	existingPreparedDetails := instaslice.Spec.Prepared
	checkAPreparedDetails := existingPreparedDetails[migUUID]
	if checkAPreparedDetails.Ciinfoid == ciId && checkAPreparedDetails.Giinfoid == giId && checkAPreparedDetails.PodUUID == podUUID {
		log.FromContext(ctx).Info("updated prepared details already exists")
		return nil
	}
	updatedAllocation := instaslice.Spec.Allocations[podUUID]
	instaslicePrepared := inferencev1alpha1.PreparedDetails{
		Profile:  profileName,
		Start:    updatedAllocation.Start,
		Size:     updatedAllocation.Size,
		Parent:   deviceUUID,
		PodUUID:  podUUID,
		Giinfoid: giId,
		Ciinfoid: ciId,
	}
	if instaslice.Spec.Prepared == nil {
		instaslice.Spec.Prepared = make(map[string]inferencev1alpha1.PreparedDetails)
	}

	instaslice.Spec.Prepared[migUUID] = instaslicePrepared
	errForUpdate := r.Update(ctx, instaslice)
	if errForUpdate != nil {
		log.FromContext(ctx).Error(errForUpdate, "error adding prepared statement")
		return errForUpdate
	}
	return nil
}

// Reloads the configuration in the device plugin to update node capacity
// there is a possibility of double update, should that happen while we retry?
func (r *InstaSliceDaemonsetReconciler) updateNodeCapacity(ctx context.Context, nodeName string) error {
	node := &v1.Node{}
	nodeNameObject := types.NamespacedName{Name: nodeName}
	err := r.Get(ctx, nodeNameObject, node)
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to get node object")
		return err
	}
	// Label value should be maunally added when the cluster is setup.
	if value, exists := node.Labels["nvidia.com/device-plugin.config"]; exists && value == "update-capacity-1" {
		node.Labels["nvidia.com/device-plugin.config"] = "update-capacity"
	}

	if value, exists := node.Labels["nvidia.com/device-plugin.config"]; exists && value == "update-capacity" {
		node.Labels["nvidia.com/device-plugin.config"] = "update-capacity-1"
	}

	err = r.Update(ctx, node)
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to update Node")
		return err
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

	//make InstaSlice object when it does not exists
	//if it got restarted then use the existing state.
	nodeName := os.Getenv("NODE_NAME")

	//Init InstaSlice obj as the first thing when cache is loaded.
	//RunnableFunc is added to the manager.
	//This function waits for the manager to be elected (<-mgr.Elected()) and then runs InstaSlice init code.
	mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-mgr.Elected() // Wait for the manager to be elected
		var instaslice inferencev1alpha1.Instaslice
		typeNamespacedName := types.NamespacedName{
			Name: nodeName,
			//TODO: change namespace
			Namespace: "default",
		}
		errRetrievingInstaSliceForSetup := r.Get(ctx, typeNamespacedName, &instaslice)
		if errRetrievingInstaSliceForSetup != nil {
			log.FromContext(ctx).Error(errRetrievingInstaSliceForSetup, "unable to fetch InstaSlice resource for node")
			//TODO: should we do hard exit?
			//os.Exit(1)
		}
		if instaslice.Status.Processed != "true" || (instaslice.Name == "" && instaslice.Namespace == "") {
			_, errForDiscoveringGpus := r.discoverMigEnabledGpuWithSlices()
			if errForDiscoveringGpus != nil {
				log.FromContext(ctx).Error(errForDiscoveringGpus, "error discovering GPUs")
			}
		}
		return nil
	}))

	return nil
}

// Enable creation of controller caches to talk to the API server in order to perform
// object discovery in SetupWithManager
func (r *InstaSliceDaemonsetReconciler) setupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&inferencev1alpha1.Instaslice{}).Named("InstaSliceDaemonSet").
		Complete(r)
}

// This function discovers MIG devices as the plugin comes up. this is run exactly once.
func (r *InstaSliceDaemonsetReconciler) discoverMigEnabledGpuWithSlices() ([]string, error) {
	instaslice, _, gpuModelMap, failed, returnValue, errorDiscoveringProfiles := r.discoverAvailableProfilesOnGpus()
	if failed {
		return returnValue, errorDiscoveringProfiles
	}

	err := r.discoverDanglingSlices(instaslice)

	if err != nil {
		return nil, err
	}

	nodeName := os.Getenv("NODE_NAME")
	instaslice.Name = nodeName
	instaslice.Namespace = "default"
	instaslice.Spec.MigGPUUUID = gpuModelMap
	instaslice.Status.Processed = "true"
	//TODO: should we use context.TODO() ?
	customCtx := context.TODO()
	errToCreate := r.Create(customCtx, instaslice)
	if errToCreate != nil {
		return nil, errToCreate
	}

	// Object exists, update its status
	instaslice.Status.Processed = "true"
	if errForStatus := r.Status().Update(customCtx, instaslice); errForStatus != nil {
		return nil, errForStatus
	}

	return discoveredGpusOnHost, nil
}

func (r *InstaSliceDaemonsetReconciler) discoverAvailableProfilesOnGpus() (*inferencev1alpha1.Instaslice, nvml.Return, map[string]string, bool, []string, error) {
	instaslice := &inferencev1alpha1.Instaslice{}
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return nil, ret, nil, false, nil, ret
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, ret, nil, false, nil, ret
	}
	gpuModelMap := make(map[string]string)
	discoverProfilePerNode := true
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return nil, ret, nil, false, nil, ret
		}

		uuid, _ := device.GetUUID()
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
					return nil, ret, nil, false, nil, ret
				}

				memory, ret := device.GetMemoryInfo()
				if ret != nvml.SUCCESS {
					return nil, ret, nil, false, nil, ret
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
					return nil, 0, nil, true, nil, ret
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
	return instaslice, ret, gpuModelMap, false, nil, nil
}

func (r *InstaSliceDaemonsetReconciler) discoverDanglingSlices(instaslice *inferencev1alpha1.Instaslice) error {
	h := &deviceHandler{}
	h.nvml = nvml.New()
	h.nvdevice = nvdevice.New(nvdevice.WithNvml(h.nvml))

	errInitNvml := h.nvml.Init()
	if errInitNvml != nvml.SUCCESS {
		return errInitNvml
	}

	availableGpusOnNode, errObtainingDeviceCount := h.nvml.DeviceGetCount()
	if errObtainingDeviceCount != nvml.SUCCESS {
		return errObtainingDeviceCount
	}

	for i := 0; i < availableGpusOnNode; i++ {
		device, errObtainingDeviceHandle := h.nvml.DeviceGetHandleByIndex(i)
		if errObtainingDeviceHandle != nvml.SUCCESS {
			return errObtainingDeviceHandle
		}

		uuid, errObtainingDeviceUUID := device.GetUUID()
		if errObtainingDeviceUUID != nvml.SUCCESS {
			return errObtainingDeviceUUID
		}

		nvlibParentDevice, errObtainingParentDevice := h.nvdevice.NewDevice(device)
		if errObtainingParentDevice != nil {
			return errObtainingParentDevice
		}
		migs, errRetrievingMigDevices := nvlibParentDevice.GetMigDevices()
		if errRetrievingMigDevices != nil {
			return errRetrievingMigDevices
		}

		for _, mig := range migs {
			migUUID, _ := mig.GetUUID()
			profile, errForProfile := mig.GetProfile()
			if errForProfile != nil {
				return errForProfile
			}

			giID, errForMigGid := mig.GetGpuInstanceId()
			if errForMigGid != nvml.SUCCESS {
				return errForMigGid
			}
			gpuInstance, errRetrievingDeviceGid := device.GetGpuInstanceById(giID)
			if errRetrievingDeviceGid != nvml.SUCCESS {
				return errRetrievingDeviceGid
			}
			gpuInstanceInfo, errObtainingInfo := gpuInstance.GetInfo()
			if errObtainingInfo != nvml.SUCCESS {
				return errObtainingInfo
			}

			ciID, ret := mig.GetComputeInstanceId()
			if ret != nvml.SUCCESS {
				return ret
			}
			ci, ret := gpuInstance.GetComputeInstanceById(ciID)
			if ret != nvml.SUCCESS {
				return ret
			}
			ciInfo, ret := ci.GetInfo()
			if ret != nvml.SUCCESS {
				return ret
			}
			prepared := inferencev1alpha1.PreparedDetails{
				Profile:  profile.GetInfo().String(),
				Start:    gpuInstanceInfo.Placement.Start,
				Size:     gpuInstanceInfo.Placement.Size,
				Parent:   uuid,
				Giinfoid: gpuInstanceInfo.Id,
				Ciinfoid: ciInfo.Id,
			}
			if instaslice.Spec.Prepared == nil {
				instaslice.Spec.Prepared = make(map[string]inferencev1alpha1.PreparedDetails)
			}
			instaslice.Spec.Prepared[migUUID] = prepared
		}
	}
	return nil
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
		attr = append(attr, AttributeMediaExtensions)
	}
	return attr
}

// Create configmap which is used by Pods to consume MIG device
func (r *InstaSliceDaemonsetReconciler) createConfigMap(ctx context.Context, migGPUUUID string, namespace string, podName string) error {
	var configMap v1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, &configMap)
	if err != nil {
		log.FromContext(ctx).Info("ConfigMap not found, creating for ", "pod", podName, "migGPUUUID", migGPUUUID)
		configMapToCreate := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
			},
			Data: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": migGPUUUID,
				"CUDA_VISIBLE_DEVICES":   migGPUUUID,
			},
		}
		if err := r.Create(ctx, configMapToCreate); err != nil {
			log.FromContext(ctx).Error(err, "failed to create ConfigMap")
			return err
		}

	}
	return nil
}

// Manage lifecycle of configmap, delete it once the pod is deleted from the system
func (r *InstaSliceDaemonsetReconciler) deleteConfigMap(ctx context.Context, configMapName string, namespace string) error {
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
			log.FromContext(ctx).Error(err, "configmap not found for ", "pod", configMapName)
			return nil
		}
		return err
	}

	log.FromContext(ctx).Info("ConfigMap deleted successfully ", "name", configMapName)
	return nil
}

func createPatchData(resourceName string, resourceValue string) ([]byte, error) {
	patch := []ResPatchOperation{
		{Op: "add",
			Path:  fmt.Sprintf("/status/capacity/%s", strings.ReplaceAll(resourceName, "/", "~1")),
			Value: resourceValue,
		},
	}
	return json.Marshal(patch)
}

func deletePatchData(resourceName string) ([]byte, error) {
	patch := []ResPatchOperation{
		{Op: "remove",
			Path: fmt.Sprintf("/status/capacity/%s", strings.ReplaceAll("org.instaslice/"+resourceName, "/", "~1")),
		},
	}
	return json.Marshal(patch)
}
