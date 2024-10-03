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

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
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
	SetAllocationDetails(profileName string, newStart, size uint32, podUUID string, nodename string, processed string,
		discoveredGiprofile int, Ciprofileid int, Ciengprofileid int, namespace string, podName string, gpuUuid string, resourceIndetifier string,
		cpumilli int64, memory int64) *inferencev1alpha1.AllocationDetails
}

// not implemented
type RightToLeftPolicy struct{}

// not implemented
type LeftToRightPolicy struct{}

// first fit policy is implemented at the moment
type FirstFitPolicy struct{}

//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;patch

func (r *InstasliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	policy := &FirstFitPolicy{}
	pod := &v1.Pod{}
	var instasliceList inferencev1alpha1.InstasliceList
	if err := r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		log.FromContext(ctx).Error(err, "Error listing Instaslice")
	}
	err := r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		// Error fetching the Pod
		if errors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "unable to fetch pod might be deleted")
			// TODO figure out why are allocations present post pod deletes?
			for _, instaslice := range instasliceList.Items {
				for _, allocation := range instaslice.Spec.Allocations {
					if allocation.PodName == pod.Name {
						r.deleteInstasliceAllocation(ctx, instaslice.Name, allocation)
					}
				}
			}
			return ctrl.Result{}, nil
		}
		log.FromContext(ctx).Error(err, "unable to fetch pod")
		return ctrl.Result{}, nil
	}

	// Pods with scheduling gates other than the InstaSlice gate are not ready to be scheduled and should be ignored
	if isPodGatedByOthers(pod) {
		//log.FromContext(ctx).Info("Ignoring gated pod", "pod", pod.Name)
		return ctrl.Result{}, nil
	}

	isPodGated := checkIfPodGatedByInstaSlice(pod)

	if !isPodGated && !controllerutil.ContainsFinalizer(pod, finalizerOrGateName) {
		//log.FromContext(ctx).Info("Ignoring ", "pod", pod.Name)
		return ctrl.Result{}, nil
	}

	// Add finalizer to the pod gated by InstaSlice
	if isPodGated && !controllerutil.ContainsFinalizer(pod, finalizerOrGateName) {
		pod.Finalizers = append(pod.Finalizers, finalizerOrGateName)
		errAddingFinalizer := r.Update(ctx, pod)
		if errAddingFinalizer != nil {
			log.FromContext(ctx).Error(errAddingFinalizer, "failed to add finalizer to pod")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// failed pods are not deleted by InstaSlice, finalizer is removed so that user can
	// delete the pod.
	if pod.Status.Phase == v1.PodFailed && controllerutil.ContainsFinalizer(pod, finalizerOrGateName) {
		allocationNotFound := true
		for _, instaslice := range instasliceList.Items {
			for _, allocation := range instaslice.Spec.Allocations {
				if pod.UID == types.UID(allocation.PodUUID) {
					allocationNotFound = false
					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusCreating {
						return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
					}
					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusCreated || allocation.Allocationstatus == inferencev1alpha1.AllocationStatusUngated {
						resultDeleting, errInDeleting := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, string(pod.UID), allocation, req)
						if errInDeleting != nil {
							return resultDeleting, nil
						}
						// return and rely on daemonset to se allocation status to created
						// this will cause podmap function to wakeup pod and perform clean up
						return ctrl.Result{}, nil
					}
					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
						resultRemove, errInRemove := r.removeInstasliceAllocation(ctx, instaslice.Name, string(pod.UID), allocation, req)
						if errInRemove != nil {
							return resultRemove, nil
						}
						// requeue for the finalizer to be removed
						return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
					}
				}
			}
		}
		// pod can be terminated without any allocation
		if allocationNotFound && controllerutil.RemoveFinalizer(pod, finalizerOrGateName) {
			if err := r.Update(ctx, pod); err != nil {
				log.FromContext(ctx).Error(err, "unable to update removal of finalizer, retrying")
				// requeing immediately as the finalizer removal gets lost
				return ctrl.Result{Requeue: true}, nil
			}
			log.FromContext(ctx).Info("finalizer deleted for failed for ", "pod", pod.Name)
		}
		return ctrl.Result{}, nil
	}

	// pod is completed move allocation to deleting state and return
	if pod.Status.Phase == v1.PodSucceeded && controllerutil.ContainsFinalizer(pod, finalizerOrGateName) {
		allocationNotFound := true
		for _, instaslice := range instasliceList.Items {
			for _, allocation := range instaslice.Spec.Allocations {
				if allocation.PodUUID == string(pod.UID) {
					allocationNotFound = false
					if allocation.Allocationstatus != inferencev1alpha1.AllocationStatusDeleted {
						result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, string(pod.UID), allocation, req)
						if err != nil {
							return result, err
						}
						// return and rely on daemonset to se allocation status to created
						// this will cause podmap function to wakeup pod and perform clean up
						return ctrl.Result{}, nil
					}

					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
						result, err := r.removeInstasliceAllocation(ctx, instaslice.Name, string(pod.UID), allocation, req)
						if err != nil {
							return result, nil
						}
						// requeue for the finalizer to be removed
						return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
					}
				}

			}
		}

		// pod can be terminated as allocation was deleted in previous reconcile loop
		if allocationNotFound && controllerutil.RemoveFinalizer(pod, finalizerOrGateName) {
			if err := r.Update(ctx, pod); err != nil {
				log.FromContext(ctx).Error(err, "unable to update removal of finalizer, retrying")
				// requeing immediately as the finalizer removal gets lost
				return ctrl.Result{Requeue: true}, nil
			}
			log.FromContext(ctx).Info("finalizer deleted for succeeded ", "pod", pod.Name)
		}
		return ctrl.Result{}, nil
	}

	// handle deleted pod that never gets ungated
	//set allocation status to deleting to cleanup resources if any
	if !pod.DeletionTimestamp.IsZero() && isPodGated {
		// allocation can be in creating or created while the user deletes the pod.
		for _, instaslice := range instasliceList.Items {
			for podUuid, allocation := range instaslice.Spec.Allocations {
				if podUuid == string(pod.UID) && (allocation.Allocationstatus == inferencev1alpha1.AllocationStatusCreated) {
					allocation.Allocationstatus = inferencev1alpha1.AllocationStatusDeleting
					var updateInstasliceObject inferencev1alpha1.Instaslice
					typeNamespacedName := types.NamespacedName{
						Name:      instaslice.Name,
						Namespace: instaSliceOperatorNamespace, // TODO: modify
					}
					err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
					if err != nil {
						log.FromContext(ctx).Error(err, "error getting latest instaslice object")
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					updateInstasliceObject.Spec.Allocations[podUuid] = allocation
					errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
					if errUpdatingInstaslice != nil {
						log.FromContext(ctx).Info("unable to set instaslice to state deleted for ungated", "pod", allocation.PodName)
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
				}
				if podUuid == string(pod.UID) && allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
					result, err := r.removeInstasliceAllocation(ctx, instaslice.Name, string(pod.UID), allocation, req)
					if err != nil {
						return result, nil
					}
					if controllerutil.RemoveFinalizer(pod, finalizerOrGateName) {
						if err := r.Update(ctx, pod); err != nil {
							log.FromContext(ctx).Error(err, "unable to update removal of finalizer, retrying")
							// requeing immediately as the finalizer removal gets lost
							return ctrl.Result{Requeue: true}, nil
						}
						log.FromContext(ctx).Info("finalizer deleted for allocation status deleted ", "pod", pod.Name)
					}
				}
			}
		}

		return ctrl.Result{}, nil
	}
	// handle graceful termination of pods, wait for about 30 seconds from the time deletiontimestamp is set on the pod
	if !pod.DeletionTimestamp.IsZero() {
		log.FromContext(ctx).Info("set status to deleting for ", "pod", pod.Name)
		if controllerutil.ContainsFinalizer(pod, finalizerOrGateName) {
			for _, instaslice := range instasliceList.Items {
				for podUuid, allocation := range instaslice.Spec.Allocations {
					if podUuid == string(pod.UID) {
						if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
							resultDelete, errDeletingAllocation := r.deleteInstasliceAllocation(ctx, instaslice.Name, allocation)
							if errDeletingAllocation != nil {
								return resultDelete, errDeletingAllocation
							}
							resultRemove, errRemovingFinalizer := r.removeInstaSliceFinalizer(ctx, req)
							if errDeletingAllocation != nil {
								return resultRemove, errRemovingFinalizer
							}
						}
						elapsed := time.Since(pod.DeletionTimestamp.Time)
						if elapsed > 30*time.Second {
							allocation.Allocationstatus = inferencev1alpha1.AllocationStatusDeleting
							var updateInstasliceObject inferencev1alpha1.Instaslice
							typeNamespacedName := types.NamespacedName{
								Name:      instaslice.Name,
								Namespace: instaSliceOperatorNamespace, // TODO: modify
							}
							err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
							if err != nil {
								log.FromContext(ctx).Error(err, "error getting latest instaslice object")
								return ctrl.Result{Requeue: true}, nil
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
		podHasNodeAllocation := false
		// search if pod has allocation in any of the instaslice object in the cluster
		//TODO: allocations may get slower as the cluster size increases
		for _, instaslice := range instasliceList.Items {
			for _, allocations := range instaslice.Spec.Allocations {
				// no matter the state if allocations exists for a pod skip such a pod
				if allocations.PodUUID == string(pod.UID) {
					podHasNodeAllocation = true
				}
			}
		}
		for _, instaslice := range instasliceList.Items {
			for podUuid, allocations := range instaslice.Spec.Allocations {
				// Check if the allocatable resources contain eg: nvidia.com/mig-1g.5gb
				resourceProfile := "nvidia.com/mig-" + allocations.Profile
				gpuSliceAllocatable := 0
				node := &v1.Node{}
				nodeNameObject := types.NamespacedName{Name: instaslice.Name}
				errRetrievingNodeObj := r.Get(ctx, nodeNameObject, node)
				if errRetrievingNodeObj != nil {
					log.FromContext(ctx).Error(err, "unable to get node object")
					return ctrl.Result{Requeue: true}, nil
				}
				for resourceName, quantity := range node.Status.Allocatable {
					// Check if the resource name starts with "org.instaslice"
					if strings.HasPrefix(string(resourceName), resourceProfile) {
						gpuSliceAllocatable += int(quantity.Value())
					}
				}
				extResCap := 0
				for resourceName, _ := range node.Status.Allocatable {
					// Check if the resource name starts with "org.instaslice"
					if strings.HasPrefix(string(resourceName), orgInstaslicePrefix) {
						extResCap++
					}
				}

				preparedCount := 0
				for resourceName, _ := range instaslice.Spec.Prepared {
					// Check if the resource name starts with "org.instaslice"
					if strings.HasPrefix(string(resourceName), orgInstaslicePrefix) {
						preparedCount++
					}
				}
				gpuOperatorPodOk, err := r.isPatternPodRunningAndHealthy(ctx, "nvidia-device-plugin-daemonset", "gpu-operator")
				if err != nil {
					log.FromContext(ctx).Info("gpu operator pod not found")
					return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
				}
				if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusCreated && allocations.PodUUID == string(pod.UID) {
					var updateInstasliceObject inferencev1alpha1.Instaslice
					typeNamespacedName := types.NamespacedName{
						Name:      instaslice.Name,
						Namespace: "default", // TODO: modify
					}
					errRetrievingInstaSlice := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
					if errRetrievingInstaSlice != nil {
						log.FromContext(ctx).Error(err, "error getting latest instaslice object")
						// In some cases the pod gets ungated but the InstaSlice object does not have the
						// correct allocation status. It could be because we were unable to get the latest InstaSlice object
						// hence we retry if we fail to get the latest object
						return ctrl.Result{Requeue: true}, nil
					}
					allocations.Allocationstatus = inferencev1alpha1.AllocationStatusUngated
					instaslice.Spec.Allocations[podUuid] = allocations
					if updateInstasliceObject.Spec.Allocations == nil {
						updateInstasliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
					}
					updateInstasliceObject.Spec.Allocations[podUuid] = allocations
					if err := r.Update(ctx, &updateInstasliceObject); err != nil {
						log.FromContext(ctx).Error(err, "Error updating instaslice allocations")
						return ctrl.Result{Requeue: true}, nil
					}
					log.FromContext(ctx).Info("resource values are ", "gpuSliceAllocatable", gpuSliceAllocatable, "extResCap", extResCap, "gpiOperatorPodOk", gpuOperatorPodOk)
					// (gpuSliceAllocatable == extResCap) &&
					if (gpuSliceAllocatable-preparedCount >= 1) && gpuOperatorPodOk {
						pod := r.unGatePod(pod)
						errForUngating := r.Update(ctx, pod)
						if errForUngating != nil {
							log.FromContext(ctx).Error(errForUngating, "failed to ungate pod")
							return ctrl.Result{Requeue: true}, nil
						}
					} else {
						log.FromContext(ctx).Info("extended allocatable resource not found in status created")
						return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
					}
				}
				// InstaSlice object got updated with ungated status but the controller failed
				// ungating the pod.
				if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusUngated && allocations.PodUUID == string(pod.UID) {
					log.FromContext(ctx).Info("resource values are in ungated are ", "gpuSliceAllocatable", gpuSliceAllocatable, "extResCap", extResCap, "gpiOperatorPodOk", gpuOperatorPodOk)
					// && (gpuSliceAllocatable == extResCap)
					if gpuSliceAllocatable != 0 && gpuOperatorPodOk {
						pod := r.unGatePod(pod)
						errForUngating := r.Update(ctx, pod)
						if errForUngating != nil {
							log.FromContext(ctx).Error(errForUngating, "failed to ungate pod")
							return ctrl.Result{Requeue: true}, nil
						}
					} else {
						log.FromContext(ctx).Info("extended allocatable resource not found in status ungated")
						return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
					}
				}
			}
		}
		// pod does not have an allocation yet, make allocation
		// find the node
		if !podHasNodeAllocation {
			for _, instaslice := range instasliceList.Items {
				// find the GPU on the node and the GPU index where the slice can be created
				allocDetails, err := r.findNodeAndDeviceForASlice(ctx, &instaslice, profileName, policy, pod)
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
						return ctrl.Result{Requeue: true}, nil
					}
					log.FromContext(ctx).Info("allocation obtained for ", "pod", allocDetails.PodName)
					if updateInstasliceObject.Spec.Allocations == nil {
						updateInstasliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
					}
					updateInstasliceObject.Spec.Allocations[string(pod.UID)] = *allocDetails
					log.FromContext(ctx).Info("allocation details for ", "allocDetails", *allocDetails)
					if err := r.Update(ctx, &updateInstasliceObject); err != nil {
						log.FromContext(ctx).Error(err, "Error updating instaslice allocations")
						return ctrl.Result{Requeue: true}, nil
					}
					//allocation was successful
					return ctrl.Result{}, nil
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

// Extract profile name from the container limits spec
func (*InstasliceReconciler) extractProfileName(limits v1.ResourceList) string {
	profileName := ""
	for k := range limits {
		if strings.Contains(k.String(), "mig-") {

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

func checkIfPodGatedByInstaSlice(pod *v1.Pod) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == finalizerOrGateName {
			if pod.Status.Phase == v1.PodPending && strings.Contains(pod.Status.Conditions[0].Message, "blocked") {
				return true
			}
		}
	}
	return false
}

// isPodGatedByOthers looks for scheduling gates distinct from the InstaSlice gate
func isPodGatedByOthers(pod *v1.Pod) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name != finalizerOrGateName {
			return true
		}
	}
	return false
}

// podMapFunc maps pods to instaslice created allocations
func (r *InstasliceReconciler) podMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	instaslice := obj.(*inferencev1alpha1.Instaslice)
	var requests []reconcile.Request
	for _, allocation := range instaslice.Spec.Allocations {
		if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusCreated || allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: allocation.Namespace,
					Name:      allocation.PodName,
				},
			})
		}
	}
	return requests
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
		if gate.Name == finalizerOrGateName {
			podUpdate.Spec.SchedulingGates = append(podUpdate.Spec.SchedulingGates[:i], podUpdate.Spec.SchedulingGates[i+1:]...)
		}
	}
	return podUpdate
}

func (r *InstasliceReconciler) deleteInstasliceAllocation(ctx context.Context, instasliceName string, allocation inferencev1alpha1.AllocationDetails) (ctrl.Result, error) {
	var updateInstasliceObject inferencev1alpha1.Instaslice
	typeNamespacedName := types.NamespacedName{
		Name:      instasliceName,
		Namespace: "default", // TODO: modify
	}
	err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
	if err != nil {
		log.FromContext(ctx).Error(err, "error getting latest instaslice object")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}
	delete(updateInstasliceObject.Spec.Allocations, allocation.PodUUID)
	errUpdatingAllocation := r.Update(ctx, &updateInstasliceObject)
	if errUpdatingAllocation != nil {
		log.FromContext(ctx).Error(errUpdatingAllocation, "Error updating InstaSlice object for ", "pod", allocation.PodName)
		// deleted allocations are re-used by the controller, we can be slow to delete these
		return ctrl.Result{Requeue: true}, errUpdatingAllocation
	}
	log.FromContext(ctx).Info("Done deleting allocation for ", "pod", allocation.PodName)
	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) removeInstaSliceFinalizer(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	latestPod := &v1.Pod{}
	errGettingPod := r.Get(ctx, req.NamespacedName, latestPod)
	if errGettingPod != nil {
		log.FromContext(ctx).Error(errGettingPod, "error getting latest copy of pod")
		return ctrl.Result{Requeue: true}, errGettingPod
	}
	errRemovingFinalizer := controllerutil.RemoveFinalizer(latestPod, finalizerOrGateName)
	if !errRemovingFinalizer {
		log.FromContext(ctx).Info("finalizer not deleted for ", "pod", latestPod.Name)
	}
	if err := r.Update(ctx, latestPod); err != nil {
		log.FromContext(ctx).Info("unable to update removal of finalizer, retrying")
		return ctrl.Result{Requeue: true}, err
	}
	return ctrl.Result{}, nil
}

// Policy based allocation - FirstFit
func (r *FirstFitPolicy) SetAllocationDetails(profileName string, newStart, size uint32, podUUID, nodename string,
	processed string, discoveredGiprofile int, Ciprofileid int, Ciengprofileid int,
	namespace string, podName string, gpuUuid string, resourceIdentifier string, cpuMilli int64, memory int64) *inferencev1alpha1.AllocationDetails {
	return &inferencev1alpha1.AllocationDetails{
		Profile:            profileName,
		Start:              newStart,
		Size:               size,
		PodUUID:            podUUID,
		Nodename:           nodename,
		Allocationstatus:   inferencev1alpha1.AllocationStatus(processed),
		Namespace:          namespace,
		PodName:            podName,
		GPUUUID:            gpuUuid,
		Resourceidentifier: resourceIdentifier,
		Cpu:                cpuMilli,
		Memory:             memory,
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

func (r *InstasliceReconciler) removeInstasliceAllocation(ctx context.Context, instasliceName string, podUUID string, allocation inferencev1alpha1.AllocationDetails, req ctrl.Request) (ctrl.Result, error) {
	if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
		deleteResult, errDeletingAllocation := r.deleteInstasliceAllocation(ctx, instasliceName, allocation)
		if errDeletingAllocation != nil {
			return deleteResult, errDeletingAllocation
		}

		// removeResult, errRemovingFinalizer := r.removeInstaSliceFinalizer(ctx, req)
		// if errRemovingFinalizer != nil {
		// 	return removeResult, errRemovingFinalizer
		// }
	}
	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) setInstasliceAllocationToDeleting(ctx context.Context, instasliceName string, podUUID string, allocation inferencev1alpha1.AllocationDetails, req ctrl.Request) (ctrl.Result, error) {

	allocation.Allocationstatus = inferencev1alpha1.AllocationStatusDeleting

	var updateInstasliceObject inferencev1alpha1.Instaslice
	typeNamespacedName := types.NamespacedName{
		Name:      instasliceName,
		Namespace: instaSliceOperatorNamespace, // TODO: modify if needed
	}
	errRetrievingInstaSlice := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
	if errRetrievingInstaSlice != nil {
		log.FromContext(ctx).Error(errRetrievingInstaSlice, "error getting latest instaslice object")
		return ctrl.Result{Requeue: true}, errRetrievingInstaSlice
	}

	updateInstasliceObject.Spec.Allocations[podUUID] = allocation
	errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
	if errUpdatingInstaslice != nil {
		log.FromContext(ctx).Info("unable to set instaslice to state ", "state", allocation.Allocationstatus, "pod", allocation.PodName)
		return ctrl.Result{Requeue: true}, errUpdatingInstaslice
	}

	return ctrl.Result{}, nil
}

// TODO: remove or change
func (r *InstasliceReconciler) isPatternPodRunningAndHealthy(ctx context.Context, pattern string, namespace string) (bool, error) {
	podList := &v1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
	}

	err := r.List(ctx, podList, listOpts...)
	if err != nil {
		log.FromContext(ctx).Error(err, "unable to list pods in namespace", "namespace", namespace)
		return false, err
	}

	for _, pod := range podList.Items {
		if strings.HasPrefix(pod.Name, pattern) {
			if pod.Status.Phase != v1.PodRunning {
				log.FromContext(ctx).Info("Pod is not in Running phase", "podName", pod.Name, "namespace", namespace)
				return false, nil
			}

			for _, condition := range pod.Status.Conditions {
				if condition.Type == v1.PodReady && condition.Status != v1.ConditionTrue {
					log.FromContext(ctx).Info("Pod is not Ready", "podName", pod.Name, "namespace", namespace)
					return false, nil
				}
			}

			log.FromContext(ctx).Info("Pod is Running and Ready", "podName", pod.Name, "namespace", namespace)
			return true, nil
		}
	}

	log.FromContext(ctx).Info("No pod matching the pattern was found", "pattern", pattern, "namespace", namespace)
	return false, nil
}
