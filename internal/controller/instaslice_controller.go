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
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	inferencev1alpha1 "codeflare.dev/instaslice/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// InstasliceReconciler reconciles a Instaslice object
type InstasliceReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	kubeClient *kubernetes.Clientset
}

// AllocationPolicy interface with a single method
type AllocationPolicy interface {
	SetAllocationDetails(profileName string, newStart, size uint32, podUUID string, nodename string, processed string, discoveredGiprofile int, Ciprofileid int, Ciengprofileid int, namespace string, podName string, gpuUuid string) *inferencev1alpha1.AllocationDetails
}

type RightToLeftPolicy struct{}

type LeftToRightPolicy struct{}

type FirstFitPolicy struct{}

//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;create;update;patch;delete

func (r *InstasliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	var policy AllocationPolicy
	policy = &FirstFitPolicy{}
	pod := &v1.Pod{}
	var isPodGated = false
	err := r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		// Error fetching the Pod
		if errors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "unable to fetch pod might be deleted")
			return ctrl.Result{}, nil
		}
		log.FromContext(ctx).Error(err, "unable to fetch pod")
		return ctrl.Result{}, nil
	}

	isPodGated = checkIfPodGated(pod, isPodGated)

	if !isPodGated && !controllerutil.ContainsFinalizer(pod, "org.instaslice/accelarator") {
		log.FromContext(ctx).Info("Ignoring ", "pod", pod.Name)
		return ctrl.Result{}, nil
	}

	var instasliceList inferencev1alpha1.InstasliceList

	if err := r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		log.FromContext(ctx).Error(err, "Error listing Instaslice")
	}
	// pod is completed move allocation to deleted state and go to next pod
	if pod.Status.Phase == v1.PodSucceeded && controllerutil.ContainsFinalizer(pod, "org.instaslice/accelarator") {
		for _, instaslice := range instasliceList.Items {
			for podUuid, allocation := range instaslice.Spec.Allocations {
				if podUuid == string(pod.UID) {
					log.FromContext(ctx).Info("deleting allocation for completed ", "pod", allocation.PodName)
					allocation.Allocationstatus = "deleted"
					var updateInstasliceObject inferencev1alpha1.Instaslice
					typeNamespacedName := types.NamespacedName{
						Name:      instaslice.Name,
						Namespace: "default", // TODO: modify
					}
					err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
					if err != nil {
						log.FromContext(ctx).Error(err, "error getting latest instaslice object")
					}
					updateInstasliceObject.Spec.Allocations[podUuid] = allocation
					errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
					if errUpdatingInstaslice != nil {
						log.FromContext(ctx).Info("unable to set instaslice to state deleted for ", "pod", allocation.PodName)
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}

					latestPod := &v1.Pod{}
					errGettingPod := r.Get(ctx, req.NamespacedName, latestPod)
					if errGettingPod != nil {
						//TODO: should we retry?
						log.FromContext(ctx).Error(errGettingPod, "error getting latest copy of pod")
					}
					errRemovingFinalizer := controllerutil.RemoveFinalizer(latestPod, "org.instaslice/accelarator")
					if !errRemovingFinalizer {
						log.FromContext(ctx).Info("finalizer not deleted for ", "pod", pod.Name)
					}
					if err := r.Update(ctx, latestPod); err != nil {
						log.FromContext(ctx).Info("unable to update removal of finalizer, retrying")
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
				}
			}
		}
		return ctrl.Result{}, nil
	}

	// handle deleted pod that never gets ungated
	if !pod.DeletionTimestamp.IsZero() && isPodGated {
		if controllerutil.RemoveFinalizer(pod, "org.instaslice/accelarator") {
			if err := r.Update(ctx, pod); err != nil {
				log.FromContext(ctx).Info("unable to update removal of finalizer, retrying")
				return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
			}
			log.FromContext(ctx).Info("finalizer deleted")
		}
		return ctrl.Result{}, nil
	}
	// handle graceful termination of pods, wait for about 30 seconds from the time deletiontimestamp is set on the pod
	if !pod.DeletionTimestamp.IsZero() {
		log.FromContext(ctx).Info("set status to deleted for ", "pod", pod.Name)
		if controllerutil.ContainsFinalizer(pod, "org.instaslice/accelarator") {
			if controllerutil.RemoveFinalizer(pod, "org.instaslice/accelarator") {
				if err := r.Update(ctx, pod); err != nil {
					log.FromContext(ctx).Info("unable to update removal of finalizer, retrying")
					return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
				}
				log.FromContext(ctx).Info("finalizer deleted")
			}
			for _, instaslice := range instasliceList.Items {
				for podUuid, allocation := range instaslice.Spec.Allocations {
					if podUuid == string(pod.UID) {
						elapsed := time.Since(pod.DeletionTimestamp.Time)
						if elapsed > 30*time.Second {
							allocation.Allocationstatus = "deleted"
							var updateInstasliceObject inferencev1alpha1.Instaslice
							typeNamespacedName := types.NamespacedName{
								Name:      instaslice.Name,
								Namespace: "default", // TODO: modify
							}
							err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
							if err != nil {
								log.FromContext(ctx).Error(err, "error getting latest instaslice object")
							}
							updateInstasliceObject.Spec.Allocations[podUuid] = allocation
							errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
							if errUpdatingInstaslice != nil {
								log.FromContext(ctx).Info("unable to set instaslice to state deleted for ", "pod", allocation.PodName)
								return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
							}
						} else {
							remainingTime := 30*time.Second - elapsed
							return ctrl.Result{RequeueAfter: remainingTime}, nil
						}
					}
				}
			}

		}
		// exit after handling deletion event for a pod.
		return ctrl.Result{}, nil
	}

	// find allocation in the cluster for the pod
	// set allocationstatus to creating when controller adds the allocation
	// check for allocationstatus as created when daemonset is done realizing the slice on the GPU node.
	// set allocationstatus to ungated and ungate the pod so that the workload can begin execution.
	if isPodGated {
		//Assume pod only has one container with one GPU requests
		if len(pod.Spec.Containers) != 1 {
			return ctrl.Result{}, fmt.Errorf("multiple containers per pod not supported")
		}
		limits := pod.Spec.Containers[0].Resources.Limits
		profileName := r.extractProfileName(limits)
		for _, instaslice := range instasliceList.Items {
			for podUuid, allocations := range instaslice.Spec.Allocations {
				if allocations.Allocationstatus == "created" && allocations.PodUUID == string(pod.UID) {
					pod := r.unGatePod(pod)
					errForUngating := r.Update(ctx, pod)
					if errForUngating != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					allocations.Allocationstatus = "ungated"
					instaslice.Spec.Allocations[podUuid] = allocations
					var updateInstasliceObject inferencev1alpha1.Instaslice
					typeNamespacedName := types.NamespacedName{
						Name:      instaslice.Name,
						Namespace: "default", // TODO: modify
					}
					err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
					if err != nil {
						log.FromContext(ctx).Error(err, "error getting latest instaslice object")
					}
					if updateInstasliceObject.Spec.Allocations == nil {
						updateInstasliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
					}
					updateInstasliceObject.Spec.Allocations[podUuid] = allocations
					if err := r.Update(ctx, &updateInstasliceObject); err != nil {
						log.FromContext(ctx).Error(err, "Error updating instaslice allocations")
						return ctrl.Result{Requeue: true}, nil
					}
				}
				//pod allocation was found in instaslice object, move to next pod
				// return ctrl.Result{}, nil
			}
		}
		// pod does not have an allocation yet, make allocation
		// find the node
		podHasNodeAllocation := false
		for _, instaslice := range instasliceList.Items {
			// find the GPU on the node and the GPU index where the slice can be created
			allocDetails, err := r.findDeviceForASlice(&instaslice, profileName, policy, pod)
			if err != nil {
				continue
			}
			podHasNodeAllocation = true
			for _, item := range instaslice.Spec.Prepared {
				if item.Parent == allocDetails.GPUUUID && item.Size == allocDetails.Size && item.Start == allocDetails.Start {
					log.FromContext(ctx).Info("prepared allocation is yet to be deleted, retrying new allocation")
					return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
				}
			}
			if podHasNodeAllocation {
				var updateInstasliceObject inferencev1alpha1.Instaslice
				typeNamespacedName := types.NamespacedName{
					Name:      instaslice.Name,
					Namespace: "default", // TODO: modify
				}
				err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
				if err != nil {
					log.FromContext(ctx).Error(err, "error getting latest instaslice object")
				}
				log.FromContext(ctx).Info("allocation obtained for ", "pod", allocDetails.PodName)
				if updateInstasliceObject.Spec.Allocations == nil {
					updateInstasliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
				}
				updateInstasliceObject.Spec.Allocations[string(pod.UID)] = *allocDetails
				if err := r.Update(ctx, &updateInstasliceObject); err != nil {
					log.FromContext(ctx).Error(err, "Error updating instaslice allocations")
					return ctrl.Result{Requeue: true}, nil
				}
			}
		}
		//if the cluster does not have suitable node, requeue request
		if !podHasNodeAllocation {
			log.FromContext(ctx).Info("no suitable node found in cluster for ", "pod", pod.Name)
			// Generate a random duration between 1 and 10 seconds
			randomDuration := time.Duration(rand.Intn(10)+1) * time.Second
			return ctrl.Result{RequeueAfter: randomDuration}, nil
		}

	}

	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) findDeviceForASlice(instaslice *inferencev1alpha1.Instaslice, profileName string, policy AllocationPolicy, pod *v1.Pod) (*inferencev1alpha1.AllocationDetails, error) {
	//TODO: discover this value, this may work for A100 and H100 for now.
	for gpuuuid, _ := range instaslice.Spec.MigGPUUUID {
		if instaslice.Spec.Allocations == nil {
			instaslice.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
		}
		newStart := r.getStartIndexFromPreparedState(instaslice, gpuuuid, profileName)
		//size cannot be 9 atleast for A100s 40GB/80GB and H100 variants
		notValidIndex := uint32(9)
		if newStart == notValidIndex {
			//Move to next GPU
			continue
		}
		size, discoveredGiprofile, Ciprofileid, Ciengprofileid := r.extractGpuProfile(instaslice, profileName)
		allocDetails := policy.SetAllocationDetails(profileName, uint32(newStart), uint32(size),
			string(pod.UID), instaslice.Name, "creating", discoveredGiprofile,
			Ciprofileid, Ciengprofileid, pod.Namespace, pod.Name, gpuuuid)
		//instaslice.Spec.Allocations[string(pod.UID)] = *allocDetails
		return allocDetails, nil
	}

	return nil, fmt.Errorf("failed to find allocatable gpu")
}

// Extract profile name from the container limits spec
func (*InstasliceReconciler) extractProfileName(limits v1.ResourceList) string {
	profileName := ""
	for k, _ := range limits {
		if strings.Contains(k.String(), "nvidia") {

			re := regexp.MustCompile(`(\d+g\.\d+gb)`)
			match := re.FindStringSubmatch(k.String())
			if len(match) > 1 {
				profileName = match[1]
			} else {
				log.Log.Info("No match found")
			}
		}
	}
	return profileName
}

// Extract NVML specific attributes for GPUs, this will change for different generations of the GPU.
func (*InstasliceReconciler) extractGpuProfile(instaslice *inferencev1alpha1.Instaslice, profileName string) (int, int, int, int) {
	var size int
	var discoveredGiprofile int
	var Ciprofileid int
	var Ciengprofileid int
	for _, item := range instaslice.Spec.Migplacement {
		if item.Profile == profileName {
			for _, aPlacement := range item.Placements {
				size = aPlacement.Size
				discoveredGiprofile = item.Giprofileid
				Ciprofileid = item.CIProfileID
				Ciengprofileid = item.CIEngProfileID
				break
			}
		}
	}
	return size, discoveredGiprofile, Ciprofileid, Ciengprofileid
}

// accounting logic that finds the correct GPU and index where a slice could be placed.
func (*InstasliceReconciler) getStartIndexFromPreparedState(instaslice *inferencev1alpha1.Instaslice, gpuUUID string, profileName string) uint32 {
	//TODO: generalize, A100 and H100 have 8 indexes for 3g and 7g and 7 for rest, so go with 8 and we are bounded by
	//only valid placement indexes for a profile.
	var gpuAllocatedIndex [8]uint32
	// clean slate init
	for i := range gpuAllocatedIndex {
		gpuAllocatedIndex[i] = 0
	}
	//TODO: remove this once we start using GPU operator with device plugin fix
	//&& item.PodUUID == ""
	for _, item := range instaslice.Spec.Prepared {
		if item.Parent == gpuUUID {
			for i := 0; i < int(item.Size); i++ {
				gpuAllocatedIndex[int(item.Start)+i] = 1
			}

		}
	}

	for _, item := range instaslice.Spec.Allocations {
		if item.GPUUUID == gpuUUID {
			for i := 0; i < int(item.Size); i++ {
				gpuAllocatedIndex[int(item.Start)+i] = 1
			}
		}
	}

	var neededContinousSlot int
	var possiblePlacements []int
	for _, placement := range instaslice.Spec.Migplacement {
		if placement.Profile == profileName {
			neededContinousSlot = placement.Placements[0].Size
			for _, placement := range placement.Placements {
				possiblePlacements = append(possiblePlacements, placement.Start)
			}
			break
		}
	}
	//TODO: generalize for other hardware models like A30, no slices can be placed on 9th index
	//if we return 9 then assume no valid index is found.
	var newStart = uint32(9)
	for _, value := range possiblePlacements {
		if gpuAllocatedIndex[value] == 0 {
			if neededContinousSlot == 1 {
				newStart = uint32(value)
				break
			}
			if neededContinousSlot == 2 {
				if value+neededContinousSlot < len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 {
						newStart = uint32(value)
						break
					}
				}

			}
			if neededContinousSlot == 4 {
				if value+neededContinousSlot < len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 && gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 {
						newStart = uint32(value)
						break
					}
				}
			}

			if neededContinousSlot == 8 {
				//special case
				if value+neededContinousSlot < len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 &&
						gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 &&
						gpuAllocatedIndex[value+4] == 0 && gpuAllocatedIndex[value+5] == 0 &&
						gpuAllocatedIndex[value+6] == 0 && gpuAllocatedIndex[value+7] == 0 {
						newStart = uint32(value)
					}
				}
			}
		}

	}

	return newStart
}

func checkIfPodGated(pod *v1.Pod, isPodGated bool) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == "org.instaslice/accelarator" {
			if pod.Status.Phase == v1.PodPending && strings.Contains(pod.Status.Conditions[0].Message, "blocked") {
				isPodGated = true
			}
		}
	}
	return isPodGated
}

// podMapFunc maps pods to instaslice created allocations
func (r *InstasliceReconciler) podMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	instaslice := obj.(*inferencev1alpha1.Instaslice)
	for _, allocation := range instaslice.Spec.Allocations {
		if allocation.Allocationstatus == "created" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: allocation.Namespace, Name: allocation.PodName}}}
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstasliceReconciler) SetupWithManager(mgr ctrl.Manager) error {

	restConfig := mgr.GetConfig()

	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Pod{}).Named("InstaSlice-controller").
		Watches(&inferencev1alpha1.Instaslice{}, handler.EnqueueRequestsFromMapFunc(r.podMapFunc)).
		Complete(r)
}

func (r *InstasliceReconciler) unGatePod(podUpdate *v1.Pod) *v1.Pod {
	for i, gate := range podUpdate.Spec.SchedulingGates {
		if gate.Name == "org.instaslice/accelarator" {
			podUpdate.Spec.SchedulingGates = append(podUpdate.Spec.SchedulingGates[:i], podUpdate.Spec.SchedulingGates[i+1:]...)
		}
	}
	return podUpdate
}

// Policy based allocation - FirstFit
func (r *FirstFitPolicy) SetAllocationDetails(profileName string, newStart, size uint32, podUUID, nodename string,
	processed string, discoveredGiprofile int, Ciprofileid int, Ciengprofileid int,
	namespace string, podName string, gpuUuid string) *inferencev1alpha1.AllocationDetails {
	return &inferencev1alpha1.AllocationDetails{
		Profile:          profileName,
		Start:            uint32(newStart),
		Size:             uint32(size),
		PodUUID:          podUUID,
		Nodename:         nodename,
		Allocationstatus: processed,
		Giprofileid:      discoveredGiprofile,
		CIProfileID:      Ciprofileid,
		CIEngProfileID:   Ciengprofileid,
		Namespace:        namespace,
		PodName:          podName,
		GPUUUID:          gpuUuid,
	}
}

// Policy based allocation - LeftToRIght
func (l *LeftToRightPolicy) SetAllocationDetails(profileName string, newStart, size uint32, podUUID, nodename string,
	processed string, discoveredGiprofile int, Ciprofileid int, Ciengprofileid int,
	namespace string, podName string, gpuUuid string) *inferencev1alpha1.AllocationDetails {
	// Implement the left-to-right policy here
	return &inferencev1alpha1.AllocationDetails{}
}

// Policy based allocation - RigghToLeft
func (l *RightToLeftPolicy) SetAllocationDetails(profileName string, newStart, size uint32, podUUID, nodename string,
	processed string, discoveredGiprofile int, Ciprofileid int, Ciengprofileid int,
	namespace string, podName string, gpuUuid string) *inferencev1alpha1.AllocationDetails {
	// Implement the left-to-right policy here
	return &inferencev1alpha1.AllocationDetails{}
}

func isPodDeletionProcessed(str string, arr []string) bool {
	for _, v := range arr {
		if v == str {
			return false
		}
	}
	return true
}
