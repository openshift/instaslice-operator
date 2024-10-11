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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;patch;watch
//+kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

var discoveredGpusOnHost []string

// Additional handler used for making NVML calls.
type deviceHandler struct {
	nvdevice nvdevice.Interface
	nvml     nvml.Interface
}

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

// struct to get ci and gi after a mig has been created.
type preparedMig struct {
	gid     uint32
	miguuid string
	cid     uint32
}

type MigDeviceInfo struct {
	uuid   string
	giInfo *nvml.GpuInstanceInfo
	ciInfo *nvml.ComputeInstanceInfo
	start  uint32
	size   uint32
}

// TODO: remove once we figure out NVML calls that does CI and GI discovery
var cachedPreparedMig = make(map[string]preparedMig)

const requeue2sDelay = 2 * time.Second
const requeue1sDelay = 1 * time.Second

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

	emulatorMode := os.Getenv("EMULATOR_MODE")
	log.FromContext(ctx).Info("daemonset simulator mode ", "enabled", emulatorMode)
	for _, allocations := range instaslice.Spec.Allocations {
		//TODO: we make assumption that resources would always exists to delete
		// if user deletes abruptly, cm, instaslice resource, ci and gi may not exists
		// handle such scenario's.
		// delete first before creating new slice
		if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusDeleting && allocations.Nodename == nodeName {
			log.FromContext(ctx).Info("performing cleanup ", "pod", allocations.PodName)
			if errDeletingCm := r.deleteConfigMap(ctx, allocations.Resourceidentifier, allocations.Namespace); errDeletingCm != nil {
				return ctrl.Result{Requeue: true}, nil
			}

			if emulatorMode == emulatorModeFalse {
				err := r.cleanUpCiAndGi(ctx, allocations.PodUUID, instaslice)
				if err != nil {
					// NVML shutdowm took time or NVML init may have failed.
					log.FromContext(ctx).Error(err, "error cleaning up ci and gi retrying")
					return ctrl.Result{RequeueAfter: requeue2sDelay}, nil
				}
				log.FromContext(ctx).Info("done deleting ci and gi for ", "pod", allocations.PodName)
			}
			//TODO: could be merged with the creating call above
			var updateInstasliceObject inferencev1alpha1.Instaslice
			typeNamespacedName := types.NamespacedName{
				Name:      instaslice.Name,
				Namespace: "default", // TODO: modify
			}
			err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
			if err != nil {
				return ctrl.Result{Requeue: true}, nil
			}

			// In simulator node no need to explicitly specify prepared UUID
			// it will be searched and deleted
			var searchToDelPrepared string
			for migUuid, v := range updateInstasliceObject.Spec.Prepared {
				if v.PodUUID == allocations.PodUUID {
					searchToDelPrepared = migUuid
				}
			}
			delete(updateInstasliceObject.Spec.Prepared, searchToDelPrepared)

			allocations.Allocationstatus = inferencev1alpha1.AllocationStatusDeleted
			updateInstasliceObject.Spec.Allocations[allocations.PodUUID] = allocations
			errUpdatingAllocation := r.Update(ctx, &updateInstasliceObject)
			if errUpdatingAllocation != nil {
				log.FromContext(ctx).Error(errUpdatingAllocation, "error updating InstaSlice object for ", "pod", allocations.PodName)
				return ctrl.Result{Requeue: true}, nil
			}
		}
		// create new slice by obeying controller allocation
		if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusCreating && allocations.Nodename == nodeName {
			//Assume pod only has one container with one GPU request
			log.FromContext(ctx).Info("creating allocation for ", "pod", allocations.PodName)
			var podUUID = allocations.PodUUID
			_, profileName, resourceIdentifier, errGettingControllerAllocation := r.getAllocation(instaslice, allocations.PodUUID)
			if errGettingControllerAllocation != nil {
				log.FromContext(ctx).Error(errGettingControllerAllocation, "allocation was not found, retrying will not help")
				return ctrl.Result{}, nil
			}

			existingAllocations := instaslice.Spec.Allocations[podUUID]

			if emulatorMode == emulatorModeTrue {
				// Emulating cost to create CI and GI on a GPU
				time.Sleep(requeue1sDelay)
				cachedPreparedMig[allocations.PodUUID] = preparedMig{gid: 0, miguuid: allocations.PodUUID, cid: 0}

			}
			if emulatorMode == emulatorModeFalse {
				for migUUID, prepared := range instaslice.Spec.Prepared {
					if prepared.PodUUID == allocations.PodUUID {
						cachedPreparedMig[allocations.PodUUID] = preparedMig{gid: prepared.Giinfoid, miguuid: migUUID, cid: prepared.Ciinfoid}
						break // Exit the loop after the first match
					}
				}
				ret := nvml.Init()
				if ret != nvml.SUCCESS {
					log.FromContext(ctx).Error(ret, "Unable to initialize NVML")
				}
				// TODO: make function createCiAndGi and move this logic
				var shutdownErr error

				defer func() {
					if shutdownErr = nvml.Shutdown(); shutdownErr != nvml.SUCCESS {
						log.FromContext(ctx).Error(shutdownErr, "error to perform nvml.Shutdown")
					}
				}()
				if ret != nvml.SUCCESS {
					log.FromContext(ctx).Error(ret, "Unable to get device count")
				}
				//TODO: any GPU can fail creating CI and GI
				// if simulator mode is on do not perform NVML calls
				// TODO: move this logic to a new vendor specific file
				if prep, exists := cachedPreparedMig[allocations.PodUUID]; !exists || isPreparedMigEmpty(prep) {
					placement := nvml.GpuInstancePlacement{}
					// if the GPU is healthy DeviceGetHandleByUUID should never fail
					// if the call fails then we look in the cache so see if we can reuse
					// ci and gi or walk MIG devices to set allocation status to created.
					// the keep latency low for realizing slices.
					device, retCodeForDevice := nvml.DeviceGetHandleByUUID(allocations.GPUUUID)
					if retCodeForDevice != nvml.SUCCESS {
						log.FromContext(ctx).Error(ret, "error getting GPU device handle")
					}
					var giProfileId, ciProfileId int
					for _, item := range instaslice.Spec.Migplacement {
						if item.Profile == profileName {
							giProfileId = item.Giprofileid
							ciProfileId = item.Giprofileid
						}
					}
					giProfileInfo, retCodeForGi := device.GetGpuInstanceProfileInfo(giProfileId)
					if retCodeForGi != nvml.SUCCESS {
						log.FromContext(ctx).Error(retCodeForGi, "error getting GPU instance profile info", "giProfileInfo", giProfileInfo, "retCodeForGi", retCodeForGi)
					}

					log.FromContext(ctx).Info("The profile id is", "giProfileInfo", giProfileInfo.Id, "Memory", giProfileInfo.MemorySizeMB, "pod", podUUID)
					createCiAndGi := true
					updatedPlacement, err := r.getAllocationsToprepare(placement, instaslice, allocations.PodUUID)
					if err != nil {
						log.FromContext(ctx).Error(err, "prepared already exists will not create ci and gi for ", "pod", allocations.PodName)
						createCiAndGi = false
					}
					migInfos := make(map[string]*MigDeviceInfo)
					err = walkMigDevices(device, func(i int, migDevice nvml.Device) error {
						parentUuid, ret := device.GetUUID()
						if ret != nvml.SUCCESS {
							return fmt.Errorf("error getting parent GPU UUID : %v", ret)
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
					// if ci and gi exist, we need to assign those to the respective allocation
					for migUuid, migDevice := range migInfos {
						// a (nvidia) GPU can get max of 7 workloads that can have same gi profile info on a GPU
						// collect such similar profiles and bind it to allocation chosen by controller and add it to cache
						if migDevice.giInfo.ProfileId == giProfileInfo.Id && migDevice.uuid == allocations.GPUUUID {
							// search the slice chosen by the controller and add to cache when value is empty or does not exists
							if existingPreparedMig, exists := cachedPreparedMig[allocations.PodUUID]; exists {
								if existingPreparedMig.miguuid == "" && allocations.Size == migDevice.size && allocations.Start == migDevice.start {
									cachedPreparedMig[allocations.PodUUID] = preparedMig{gid: migDevice.giInfo.Id, miguuid: migUuid, cid: migDevice.ciInfo.Id}
									createCiAndGi = false
									break
								}
							}
						}
					}
					if err != nil {
						// MIG walking can fail but at this point we are unsure if slices exists
						// hence we optimistically try to create ci and gi.
						log.FromContext(ctx).Error(err, "walking MIG devices failed")
					}
					if createCiAndGi {
						var gi nvml.GpuInstance
						var retCodeForGiWithPlacement nvml.Return
						gi, retCodeForGiWithPlacement = device.CreateGpuInstanceWithPlacement(&giProfileInfo, &updatedPlacement)
						if retCodeForGiWithPlacement != nvml.SUCCESS {
							if retCodeForGiWithPlacement == nvml.ERROR_INSUFFICIENT_RESOURCES {
								log.FromContext(ctx).Error(retCodeForGiWithPlacement, "gpu instance already exists")
							} else {
								log.FromContext(ctx).Error(retCodeForGiWithPlacement, "gi creation errored out with unknown error")
								return ctrl.Result{RequeueAfter: requeue2sDelay}, nil
							}
						}

						giInfo, retForGiInfor := gi.GetInfo()
						if retForGiInfor != nvml.SUCCESS {
							log.FromContext(ctx).Error(retForGiInfor, "error getting GPU instance info for ", "giInfo", &giInfo)

						}
						//TODO: figure out the compute slice scenario, I think Kubernetes does not support this use case yet
						ciProfileInfo, retCodeForCiProfile := gi.GetComputeInstanceProfileInfo(ciProfileId, 0)
						if retCodeForCiProfile != nvml.SUCCESS {
							log.FromContext(ctx).Error(retCodeForGiWithPlacement, "error getting compute instance profile info for ", "pod", allocations.PodName)
						}
						ci, retCodeForComputeInstance := gi.CreateComputeInstance(&ciProfileInfo)
						if retCodeForComputeInstance != nvml.SUCCESS {
							log.FromContext(ctx).Error(retCodeForComputeInstance, "error creating Compute instance for ", "ci", ci)
						}

						//get created mig details
						// TODO replace this with walk function
						giId, migUUID, ciId, errGettingSliceDetails := r.getCreatedSliceDetails(ctx, giInfo, device, profileName)
						if errGettingSliceDetails != nil {
							log.FromContext(ctx).Error(errGettingSliceDetails, "slice details not found in prepared section", "pod", allocations.PodName)
							return ctrl.Result{RequeueAfter: requeue2sDelay}, nil
						}
						//add ci and gi values to cache so that we avoid re-creating. if ci or gi creation fails, we need to clean up.
						cachedPreparedMig[allocations.PodUUID] = preparedMig{gid: giId, miguuid: migUUID, cid: ciId}

					}

				}
			}
			createdSliceDetails := cachedPreparedMig[allocations.PodUUID]
			//making sure that ci, gi and migUUID are not nil or dafault for the target pod.
			if createdSliceDetails.miguuid != "" {
				if errCreatingConfigMap := r.createConfigMap(ctx, createdSliceDetails.miguuid, existingAllocations.Namespace, resourceIdentifier); errCreatingConfigMap != nil {
					return ctrl.Result{RequeueAfter: requeue1sDelay}, nil
				}

				if errAddingPrepared := r.createPreparedEntry(ctx, profileName, podUUID, allocations.GPUUUID, createdSliceDetails.gid, createdSliceDetails.cid, &instaslice, createdSliceDetails.miguuid); errAddingPrepared != nil {
					return ctrl.Result{RequeueAfter: requeue1sDelay}, nil
				}
				var updateInstasliceObject inferencev1alpha1.Instaslice
				typeNamespacedName := types.NamespacedName{
					Name:      instaslice.Name,
					Namespace: "default", // TODO: modify
				}

				//TODO: could be merged with the deleted call below
				err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
				if err != nil {
					return ctrl.Result{RequeueAfter: requeue1sDelay}, nil
				}
				updatedAllocation := updateInstasliceObject.Spec.Allocations[podUUID]
				// updated object is still in creating status, chances are user has not yet deleted
				// set status to created.
				if updatedAllocation.Allocationstatus == existingAllocations.Allocationstatus {
					existingAllocations.Allocationstatus = inferencev1alpha1.AllocationStatusCreated
				} else {
					// Add the new allocation status which is not created and let the daemonset handle in next reconcile
					log.FromContext(ctx).Info("allocation status changed for ", "pod", allocations.PodName, "status", updatedAllocation.Allocationstatus)
					existingAllocations.Allocationstatus = updatedAllocation.Allocationstatus
				}
				updateInstasliceObject.Spec.Allocations[podUUID] = existingAllocations
				errForUpdate := r.Update(ctx, &updateInstasliceObject)
				if errForUpdate != nil {
					return ctrl.Result{Requeue: true}, nil
				}
			}
			delete(cachedPreparedMig, allocations.PodUUID)
		}
	}
	return ctrl.Result{}, nil
}

// controller will set allocations that need to created (prepared) on the GPU nodes.
func (r *InstaSliceDaemonsetReconciler) getAllocationsToprepare(placement nvml.GpuInstancePlacement, instaslice inferencev1alpha1.Instaslice, podUuid string) (nvml.GpuInstancePlacement, error) {
	allocationExists := false
	for _, prepared := range instaslice.Spec.Prepared {
		if prepared.PodUUID == podUuid {
			allocationExists = true
		}
	}
	for _, v := range instaslice.Spec.Allocations {
		if !allocationExists {
			if v.Allocationstatus == inferencev1alpha1.AllocationStatusCreating && v.PodUUID == podUuid {
				placement.Size = v.Size
				placement.Start = v.Start
				return placement, nil
			}
		}
	}
	return placement, fmt.Errorf("got prepared slice wait for object to be updated")
}

// when a slice is created we do a discovery again to get MIG uuid device details
func (*InstaSliceDaemonsetReconciler) getCreatedSliceDetails(ctx context.Context, giInfo nvml.GpuInstanceInfo, device nvml.Device, profileName string) (uint32, string, uint32, error) {
	var giIdError, ciMigInfoError uint32
	// setting large number to return error
	giIdError = 1000
	ciMigInfoError = 1000
	realizedMigError := ""
	h := &deviceHandler{}
	h.nvml = nvml.New()
	h.nvdevice = nvdevice.New(h.nvml)

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
			return giInfo.Id, realizedMig, ciMigInfo.Id, nil
		}
	}
	return giIdError, realizedMigError, ciMigInfoError, fmt.Errorf("unable to get prepared details")
}

// controller provides placement we do a read from allocation object.
// TODO: see if this method can be removed to simplify code
func (r *InstaSliceDaemonsetReconciler) getAllocation(instaslice inferencev1alpha1.Instaslice, podUuid string) (string, string, string, error) {
	var gpuUUID, profile, resourceIdentifier string

	for _, v := range instaslice.Spec.Allocations {
		if v.Allocationstatus == inferencev1alpha1.AllocationStatusCreating && v.PodUUID == podUuid {
			return v.GPUUUID, v.Profile, v.Resourceidentifier, nil
		}
	}

	return gpuUUID, profile, resourceIdentifier, fmt.Errorf("allocation with PodUUID %s not found", podUuid)
}

// deletes CI and GI in that order.
// TODO: split this method into two methods.
func (r *InstaSliceDaemonsetReconciler) cleanUpCiAndGi(ctx context.Context, podUuid string, instaslice inferencev1alpha1.Instaslice) error {
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		log.FromContext(ctx).Error(ret, "Unable to initialize NVML")
	}
	var shutdownErr error

	defer func() {
		if shutdownErr = nvml.Shutdown(); shutdownErr != nvml.SUCCESS {
			log.FromContext(ctx).Error(shutdownErr, "error to perform nvml.Shutdown")
		}
	}()

	prepared := instaslice.Spec.Prepared
	for _, value := range prepared {
		if value.PodUUID == podUuid {
			parent, errRecievingDeviceHandle := nvml.DeviceGetHandleByUUID(value.Parent)
			if errRecievingDeviceHandle != nvml.SUCCESS {
				log.FromContext(ctx).Error(errRecievingDeviceHandle, "error obtaining GPU handle")
				return errRecievingDeviceHandle
			}
			gi, errRetrievingGi := parent.GetGpuInstanceById(int(value.Giinfoid))
			gIFound := true
			if errRetrievingGi != nvml.SUCCESS {
				log.FromContext(ctx).Error(errRetrievingGi, "error obtaining GPU instance for ", "poduuid", value.PodUUID)
				if errRetrievingGi == nvml.ERROR_NOT_FOUND {
					gIFound = false
				} else {
					return errRetrievingGi
				}
			}
			cIFound := true
			if gIFound {
				ci, errRetrievingCi := gi.GetComputeInstanceById(int(value.Ciinfoid))
				if errRetrievingCi != nvml.SUCCESS {
					log.FromContext(ctx).Error(errRetrievingCi, "error obtaining compute instance")
					if errRetrievingCi == nvml.ERROR_NOT_FOUND {
						cIFound = false
					} else {
						return errRetrievingCi
					}
				}

				if cIFound {
					errDestroyingCi := ci.Destroy()
					if errDestroyingCi != nvml.SUCCESS {
						log.FromContext(ctx).Error(errDestroyingCi, "error deleting compute instance")
						return errDestroyingCi
					}
				}
			}
			if gIFound {
				errDestroyingGi := gi.Destroy()
				if errDestroyingGi != nvml.SUCCESS {
					log.FromContext(ctx).Error(errDestroyingGi, "error deleting GPU instance")
					return errDestroyingGi
				}
			}
		}
	}

	return nil
}

// prepared entry is created when a GPU slice exists on a node.
func (r *InstaSliceDaemonsetReconciler) createPreparedEntry(ctx context.Context, profileName string, podUUID string, deviceUUID string, giId uint32, ciId uint32, instaslice *inferencev1alpha1.Instaslice, migUUID string) error {
	existingPreparedDetails := instaslice.Spec.Prepared
	checkAPreparedDetails := existingPreparedDetails[migUUID]
	if checkAPreparedDetails.Ciinfoid == ciId && checkAPreparedDetails.Giinfoid == giId && checkAPreparedDetails.PodUUID == podUUID {
		//updated prepared details already exists
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
		return errForUpdate
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
	emulatorMode := os.Getenv("EMULATOR_MODE")
	//Init InstaSlice obj as the first thing when cache is loaded.
	//RunnableFunc is added to the manager.
	//This function waits for the manager to be elected (<-mgr.Elected()) and then runs InstaSlice init code.
	mgrAddErr := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-mgr.Elected() // Wait for the manager to be elected
		var instaslice inferencev1alpha1.Instaslice
		typeNamespacedName := types.NamespacedName{
			Name:      nodeName,
			Namespace: "default", //TODO: change namespace
		}
		errRetrievingInstaSliceForSetup := r.Get(ctx, typeNamespacedName, &instaslice)
		if errRetrievingInstaSliceForSetup != nil {
			log.FromContext(ctx).Error(errRetrievingInstaSliceForSetup, "unable to fetch InstaSlice resource for node")
		}

		if emulatorMode == emulatorModeFalse {
			if !instaslice.Status.Processed || (instaslice.Name == "" && instaslice.Namespace == "") {
				_, errForDiscoveringGpus := r.discoverMigEnabledGpuWithSlices()
				if errForDiscoveringGpus != nil {
					log.FromContext(ctx).Error(errForDiscoveringGpus, "error discovering GPUs")
				}
			}
		}

		errRetrievingInstaSlicePostSetup := r.Get(ctx, typeNamespacedName, &instaslice)
		if errRetrievingInstaSlicePostSetup != nil {
			log.FromContext(ctx).Error(errRetrievingInstaSlicePostSetup, "unable to fetch InstaSlice resource for node")
			return errRetrievingInstaSlicePostSetup
		}

		if err := r.addMigCapacityToNode(ctx, &instaslice); err != nil {
			log.FromContext(ctx).Error(err, "error adding mig capacity to node")
			return err
		}

		// Patch the node capacity with GPU memory in emulator mode
		if emulatorMode == emulatorModeTrue {
			totalEmulatedGPUMemory := calculateTotalMemoryGB(instaslice.Spec.MigGPUUUID)
			log.FromContext(context.TODO()).Info("MIG INFO: ", "MIG", instaslice.Spec.MigGPUUUID)
			if err := r.patchNodeStatusForNode(ctx, nodeName, totalEmulatedGPUMemory); err != nil {
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

func calculateTotalMemoryGB(gpuInfoList map[string]string) int {
	totalMemoryGB := 0
	re := regexp.MustCompile(`(\d+)(GB)`)
	for _, gpuInfo := range gpuInfoList {
		matches := re.FindStringSubmatch(gpuInfo)
		if len(matches) == 3 {
			memoryGB, err := strconv.Atoi(matches[1])
			if err != nil {
				log.FromContext(context.TODO()).Error(err, "unable to parse gpu memory value")
				continue
			}
			totalMemoryGB += memoryGB
		}
	}
	return totalMemoryGB
}

// This function discovers MIG devices as the plugin comes up. this is run exactly once.
func (r *InstaSliceDaemonsetReconciler) discoverMigEnabledGpuWithSlices() ([]string, error) {

	instaslice, _, gpuModelMap, failed, errorDiscoveringProfiles := r.discoverAvailableProfilesOnGpus()
	if failed {
		return nil, errorDiscoveringProfiles
	}

	totalMemoryGB := calculateTotalMemoryGB(gpuModelMap)
	err := r.discoverDanglingSlices(instaslice)

	if err != nil {
		return nil, err
	}

	nodeName := os.Getenv("NODE_NAME")
	cpu, memory, err := r.classicalResourcesAndGPUMemOnNode(context.TODO(), nodeName, strconv.Itoa(totalMemoryGB))
	if err != nil {
		log.FromContext(context.TODO()).Error(err, "unable to get classical resources")
		os.Exit(1)
	}
	log.FromContext(context.TODO()).Info("classical resources obtained are ", "cpu", cpu, "memory", memory)
	instaslice.Spec.CpuOnNodeAtBoot = cpu
	instaslice.Spec.MemoryOnNodeAtBoot = memory
	instaslice.Name = nodeName
	instaslice.Namespace = "default"
	instaslice.Spec.MigGPUUUID = gpuModelMap
	instaslice.Status.Processed = true
	//TODO: should we use context.TODO() ?
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
	if err := r.patchNodeStatusForNode(customCtx, nodeName, totalMemoryGB); err != nil {
		return nil, err
	}

	return discoveredGpusOnHost, nil
}

func (r *InstaSliceDaemonsetReconciler) addMigCapacityToNode(ctx context.Context, instaslice *inferencev1alpha1.Instaslice) error {
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
		resourceName := orgInstaslicePrefix + "mig-" + profile
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

	log.FromContext(ctx).Info("Successfully patched node with possible maxMIG placement counts", "nodeName", instaslice.Name)
	return nil
}

// patchNodeStatusForNode fetches the node and patches its capacity with the given GPU memory
func (r *InstaSliceDaemonsetReconciler) patchNodeStatusForNode(ctx context.Context, nodeName string, totalMemoryGB int) error {
	// Fetch the node object
	node, err := r.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to fetch Node")
		return err
	}

	// Patch the node capacity with total GPU memory
	if err := r.patchNodeStatus(ctx, node, strconv.Itoa(totalMemoryGB)+"Gi"); err != nil {
		return err
	}

	return nil
}

// patch node with accelerator memory capacity
func (r *InstaSliceDaemonsetReconciler) patchNodeStatus(ctx context.Context, node *v1.Node, memory string) error {
	logger := log.FromContext(ctx)

	// Create patch data for accelerator-memory-quota
	patchData, err := createPatchData(quotaResourceName, memory)
	if err != nil {
		logger.Error(err, "unable to create correct json for patching node")
		return err
	}

	// Apply the patch to the node capacity
	if err := r.Status().Patch(ctx, node, client.RawPatch(types.JSONPatchType, patchData)); err != nil {
		logger.Error(err, "unable to patch Node capacity with accelerator GPU memory custom resource")
		return err
	}

	logger.Info("Successfully patched node capacity with accelerator GPU memory custom resource", "Node", node.Name)
	return nil
}

func (r *InstaSliceDaemonsetReconciler) classicalResourcesAndGPUMemOnNode(ctx context.Context, nodeName string, totalGPUMemory string) (int64, int64, error) {
	node := &v1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		log.FromContext(ctx).Error(err, "unable to retrieve cpu and memory resource on the node")
	}

	newResourceQuantity := resource.MustParse(totalGPUMemory + "Gi")
	// Convert the string to ResourceName
	resourceName := v1.ResourceName(quotaResourceName)
	node.Status.Capacity[resourceName] = newResourceQuantity

	if err := r.Status().Update(ctx, node); err != nil {
		log.FromContext(ctx).Error(err, "unable to patch the node with new resource")
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

// TODO: remove this logic once we are able to use clean slate GPUs from upstream GPU operator fixes
func (r *InstaSliceDaemonsetReconciler) discoverDanglingSlices(instaslice *inferencev1alpha1.Instaslice) error {
	h := &deviceHandler{}
	h.nvml = nvml.New()
	h.nvdevice = nvdevice.New(h.nvml)

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
func (r *InstaSliceDaemonsetReconciler) createConfigMap(ctx context.Context, migGPUUUID string, namespace string, resourceIdentifier string) error {
	var configMap v1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: resourceIdentifier, Namespace: namespace}, &configMap)
	if err != nil {
		log.FromContext(ctx).Info("ConfigMap not found, creating for ", "pod", resourceIdentifier, "migGPUUUID", migGPUUUID)
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

func isPreparedMigEmpty(pm preparedMig) bool {
	return pm.miguuid == "" && pm.gid == 0 && pm.cid == 0
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
