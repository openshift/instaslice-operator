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
	"os"
	"regexp"
	"strings"
	"time"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
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

var (
	daemonSetlabel = map[string]string{"app": "controller-daemonset"}
)

//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;patch
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;delete

func (r *InstasliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)

	// 1. Ensure DaemonSet is deployed
	daemonSet := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: instasliceDaemonsetName, Namespace: operatorDeployNamespace}, daemonSet)
	if err != nil {
		if errors.IsNotFound(err) {
			// DaemonSet doesn't exist, so create it
			daemonSet = createInstaSliceDaemonSet(operatorDeployNamespace)
			err = r.Create(ctx, daemonSet)
			if err != nil {
				log.Error(err, "Failed to create DaemonSet")
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}
			log.Info("DaemonSet created successfully, waiting for pods to be ready")
			return ctrl.Result{RequeueAfter: requeueDelay10Sec}, nil
		}
		log.Error(err, "Failed to get DaemonSet")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	// 2. Check if at least one DaemonSet pod is ready
	var podList v1.PodList
	labelSelector := labels.SelectorFromSet(daemonSet.Spec.Selector.MatchLabels)

	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
		Namespace:     operatorDeployNamespace,
	}

	if err := r.List(ctx, &podList, listOptions); err != nil {
		log.Error(err, "Failed to list DaemonSet pods")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	// Check if at least one daemonset pod is ready
	for _, pod := range podList.Items {
		if pod.Status.Phase == v1.PodRunning && len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].Ready {
			// isAnyPodReady = true
			break
		}
	}
	if daemonSet.Status.NumberReady == 0 {
		log.Info("No DaemonSet pods are ready yet, waiting...")
		return ctrl.Result{RequeueAfter: requeueDelay10Sec}, nil
	}
	log.Info("At least one Instaslice DaemonSet pod is ready, continue reconcile...")

	// Continue with the rest of the reconciliation logic
	policy := &FirstFitPolicy{}
	pod := &v1.Pod{}
	var instasliceList inferencev1alpha1.InstasliceList
	if err = r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		log.Error(err, "Error listing Instaslice")
		return ctrl.Result{}, err
	}
	err = r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		// Error fetching the Pod
		if errors.IsNotFound(err) {
			log.Info("unable to fetch pod might be deleted")
			// TODO figure out why are allocations present post pod deletes?
			// https://github.com/openshift/instaslice-operator/issues/150
			for _, instaslice := range instasliceList.Items {
				for _, allocation := range instaslice.Spec.Allocations {
					if allocation.PodName == pod.Name {
						result, err := r.deleteInstasliceAllocation(ctx, instaslice.Name, allocation)
						if err != nil {
							return result, err
						}
					}
				}
			}
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch pod")
		return ctrl.Result{}, nil
	}
	// Pods with scheduling gates other than the InstaSlice gate are not ready to be scheduled and should be ignored
	if isPodGatedByOthers(pod) {
		return ctrl.Result{}, nil
	}

	isPodGated := checkIfPodGatedByInstaSlice(pod)

	if !isPodGated && !controllerutil.ContainsFinalizer(pod, finalizerName) {
		//logr.FromContext(ctx).Info("Ignoring ", "pod", pod.Name)
		return ctrl.Result{}, nil
	}

	// Add finalizer to the pod gated by InstaSlice
	if isPodGated && !controllerutil.ContainsFinalizer(pod, finalizerName) {
		pod.Finalizers = append(pod.Finalizers, finalizerName)
		errAddingFinalizer := r.Update(ctx, pod)
		if errAddingFinalizer != nil {
			log.Error(errAddingFinalizer, "failed to add finalizer to pod")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// failed pods are not deleted by InstaSlice, finalizer is removed so that user can
	// delete the pod.
	if pod.Status.Phase == v1.PodFailed && controllerutil.ContainsFinalizer(pod, finalizerName) {
		allocationNotFound := true
		for _, instaslice := range instasliceList.Items {
			for _, allocation := range instaslice.Spec.Allocations {
				if pod.UID == types.UID(allocation.PodUUID) {
					allocationNotFound = false
					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusCreating {
						return ctrl.Result{RequeueAfter: requeueDelay}, nil
					}
					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusCreated || allocation.Allocationstatus == inferencev1alpha1.AllocationStatusUngated {
						resultDeleting, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, string(pod.UID), allocation)
						if err != nil {
							return resultDeleting, nil
						}
						// return and rely on daemonset to se allocation status to created
						// this will cause podmap function to wakeup pod and perform clean up
						return ctrl.Result{}, nil
					}
					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
						resultRemove, err := r.removeInstasliceAllocation(ctx, instaslice.Name, allocation)
						if err != nil {
							return resultRemove, nil
						}
						// requeue for the finalizer to be removed
						return ctrl.Result{RequeueAfter: requeueDelay}, nil
					}
				}
			}
		}
		// pod can be terminated without any allocation
		if allocationNotFound && controllerutil.RemoveFinalizer(pod, finalizerName) {
			if err := r.Update(ctx, pod); err != nil {
				log.Error(err, "unable to update removal of finalizer, retrying")
				// requeing immediately as the finalizer removal gets lost
				return ctrl.Result{Requeue: true}, nil
			}
			log.Info("finalizer deleted for failed for ", "pod", pod.Name)
		}
		return ctrl.Result{}, nil
	}

	// pod is completed move allocation to deleting state and return
	if pod.Status.Phase == v1.PodSucceeded && controllerutil.ContainsFinalizer(pod, finalizerName) {
		allocationNotFound := true
		for _, instaslice := range instasliceList.Items {
			for _, allocation := range instaslice.Spec.Allocations {
				if allocation.PodUUID == string(pod.UID) {
					allocationNotFound = false
					if allocation.Allocationstatus != inferencev1alpha1.AllocationStatusDeleted {
						result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, string(pod.UID), allocation)
						if err != nil {
							return result, err
						}
						// return and rely on daemonset to se allocation status to created
						// this will cause podmap function to wakeup pod and perform clean up
						return ctrl.Result{}, nil
					}

					if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
						result, err := r.removeInstasliceAllocation(ctx, instaslice.Name, allocation)
						if err != nil {
							return result, nil
						}
						// requeue for the finalizer to be removed
						return ctrl.Result{RequeueAfter: requeueDelay}, nil
					}
				}

			}
		}

		// pod can be terminated as allocation was deleted in previous reconcile loop
		if allocationNotFound && controllerutil.RemoveFinalizer(pod, finalizerName) {
			if err := r.Update(ctx, pod); err != nil {
				// requeing immediately as the finalizer removal gets lost
				return ctrl.Result{Requeue: true}, nil
			}
			log.Info("finalizer deleted for succeeded ", "pod", pod.Name)
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
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					updateInstasliceObject.Spec.Allocations[podUuid] = allocation
					errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
					if errUpdatingInstaslice != nil {
						log.Info("unable to set instaslice to state deleted for ungated", "pod", allocation.PodName)
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
				}
				if podUuid == string(pod.UID) && allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
					result, err := r.removeInstasliceAllocation(ctx, instaslice.Name, allocation)
					if err != nil {
						return result, nil
					}
					if controllerutil.RemoveFinalizer(pod, finalizerName) {
						if err := r.Update(ctx, pod); err != nil {
							// requeing immediately as the finalizer removal gets lost
							return ctrl.Result{Requeue: true}, nil
						}
						log.Info("finalizer deleted for allocation status deleted ", "pod", pod.Name)
					}
				}
			}
		}

		return ctrl.Result{}, nil
	}
	// handle graceful termination of pods, wait for about 30 seconds from the time deletiontimestamp is set on the pod
	if !pod.DeletionTimestamp.IsZero() {
		log.Info("set status to deleting for ", "pod", pod.Name)
		if controllerutil.ContainsFinalizer(pod, finalizerName) {
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
								return ctrl.Result{Requeue: true}, nil
							}
							updateInstasliceObject.Spec.Allocations[podUuid] = allocation
							errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
							if errUpdatingInstaslice != nil {
								log.Info("unable to set instaslice to state deleted for ", "pod", allocation.PodName)
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
		// return error if there are no containers in the pod
		if len(pod.Spec.Containers) == 0 {
			return ctrl.Result{}, fmt.Errorf(noContainerInsidePodErr+", pod: %v", pod.Name)
		}
		//Assume pod only has one container with one GPU requests
		if len(pod.Spec.Containers) != 1 {
			return ctrl.Result{}, fmt.Errorf(multipleContainersUnsupportedErr+", pod: %v", pod.Name)
		}
		limits := pod.Spec.Containers[0].Resources.Limits
		profileName := r.extractProfileName(limits)
		var podHasNodeAllocation bool
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
				if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusCreated && allocations.PodUUID == string(pod.UID) {
					var updateInstasliceObject inferencev1alpha1.Instaslice
					typeNamespacedName := types.NamespacedName{
						Name:      instaslice.Name,
						Namespace: instaSliceOperatorNamespace,
					}
					errRetrievingInstaSlice := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
					if errRetrievingInstaSlice != nil {
						log.Error(errRetrievingInstaSlice, "error getting latest instaslice object")
						// In some cases the pod gets ungated but the InstaSlice object does not have the
						// correct allocation status. It could be because we were unable to get the latest InstaSlice object
						// hence we retry if we fail to get the latest object
						return ctrl.Result{Requeue: true}, nil
					}
					allocations.Allocationstatus = inferencev1alpha1.AllocationStatusUngated
					if updateInstasliceObject.Spec.Allocations == nil {
						updateInstasliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
					}
					updateInstasliceObject.Spec.Allocations[podUuid] = allocations
					if err := r.Update(ctx, &updateInstasliceObject); err != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					result, err := r.addNodeSelectorAndUngatePod(ctx, pod, allocations)
					if err != nil {
						return result, err
					}
				}
				// InstaSlice object got updated with ungated status but the controller failed
				// ungating the pod.
				if allocations.Allocationstatus == inferencev1alpha1.AllocationStatusUngated && allocations.PodUUID == string(pod.UID) {
					result, err := r.addNodeSelectorAndUngatePod(ctx, pod, allocations)
					if err != nil {
						return result, err
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
						log.Info("prepared allocation is yet to be deleted, retrying new allocation")
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
						return ctrl.Result{Requeue: true}, nil
					}
					log.Info("allocation obtained for ", "pod", allocDetails.PodName)
					if updateInstasliceObject.Spec.Allocations == nil {
						updateInstasliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
					}
					updateInstasliceObject.Spec.Allocations[string(pod.UID)] = *allocDetails
					if err := r.Update(ctx, &updateInstasliceObject); err != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					//allocation was successful
					return ctrl.Result{}, nil
				}
			}
		}

		//if the cluster does not have suitable node, requeue request
		if !podHasNodeAllocation {
			log.Info("no suitable node found in cluster for ", "pod", pod.Name)
			// Generate a random duration between 1 and 10 seconds
			randomDuration := time.Duration(rand.Intn(10)+1) * time.Second
			return ctrl.Result{RequeueAfter: randomDuration}, nil
		}

	}

	return ctrl.Result{}, nil
}

// create the DaemonSet object
func createInstaSliceDaemonSet(namespace string) *appsv1.DaemonSet {
	emulatorMode := os.Getenv("EMULATOR_MODE")
	if emulatorMode == "" {
		emulatorMode = emulatorModeFalse
	}
	instasliceDaemonsetImage := os.Getenv("RELATED_IMAGE_INSTASLICE_DAEMONSET")
	if instasliceDaemonsetImage == "" {
		instasliceDaemonsetImage = daemonSetImageName
	}
	// Base DaemonSet structure
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instasliceDaemonsetName,
			Namespace: namespace,
			Labels:    daemonSetlabel,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: daemonSetlabel,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: daemonSetlabel,
					Annotations: map[string]string{
						"kubectl.kubernetes.io/default-container": "daemonset",
					},
				},
				Spec: v1.PodSpec{
					ServiceAccountName:            serviceAccountName,
					TerminationGracePeriodSeconds: func(i int64) *int64 { return &i }(10),
					SecurityContext: &v1.PodSecurityContext{
						RunAsNonRoot: func(b bool) *bool { return &b }(false),
					},
					Containers: []v1.Container{
						{
							Name:  daemonSetName,
							Image: instasliceDaemonsetImage,
							// Commenting the image pull policy to support running e2e tests
							// to leverage the images built using the latest code
							// ImagePullPolicy: v1.PullAlways,
							Command: []string{
								"/daemonset",
							},
							Args: []string{
								"--leader-elect=false",
							},
							SecurityContext: &v1.SecurityContext{
								AllowPrivilegeEscalation: func(b bool) *bool { return &b }(true),
								Privileged:               func(b bool) *bool { return &b }(true),
								Capabilities: &v1.Capabilities{
									Add: []v1.Capability{"ALL"},
								},
							},
							Env: []v1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &v1.EnvVarSource{
										FieldRef: &v1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									Name:  "NVIDIA_MIG_CONFIG_DEVICES",
									Value: "all",
								},
								{
									Name:  "EMULATOR_MODE",
									Value: emulatorMode,
								},
							},
						},
					},
				},
			},
		},
	}

	return daemonSet
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
		if gate.Name == gateName {
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
		if gate.Name != gateName {
			return true
		}
	}
	return false
}

// podMapFunc maps pods to instaslice created allocations
func (r *InstasliceReconciler) podMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	instaslice, ok := obj.(*inferencev1alpha1.Instaslice)
	if ok {
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
		if gate.Name == gateName {
			podUpdate.Spec.SchedulingGates = append(podUpdate.Spec.SchedulingGates[:i], podUpdate.Spec.SchedulingGates[i+1:]...)
		}
	}
	return podUpdate
}

func (r *InstasliceReconciler) deleteInstasliceAllocation(ctx context.Context, instasliceName string, allocation inferencev1alpha1.AllocationDetails) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	var updateInstasliceObject inferencev1alpha1.Instaslice
	typeNamespacedName := types.NamespacedName{
		Name:      instasliceName,
		Namespace: "default", // TODO: modify
	}
	err := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
	if err != nil {
		log.Error(err, "error getting latest instaslice object")
		return ctrl.Result{RequeueAfter: requeueDelay}, err
	}
	delete(updateInstasliceObject.Spec.Allocations, allocation.PodUUID)
	errUpdatingAllocation := r.Update(ctx, &updateInstasliceObject)
	if errUpdatingAllocation != nil {
		log.Error(errUpdatingAllocation, "Error updating InstaSlice object for ", "pod", allocation.PodName)
		// deleted allocations are re-used by the controller, we can be slow to delete these
		return ctrl.Result{Requeue: true}, errUpdatingAllocation
	}
	log.Info("Done deleting allocation for ", "pod", allocation.PodName)
	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) removeInstaSliceFinalizer(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	latestPod := &v1.Pod{}
	errGettingPod := r.Get(ctx, req.NamespacedName, latestPod)
	if errGettingPod != nil {
		log.Error(errGettingPod, "error getting latest copy of pod")
		return ctrl.Result{Requeue: true}, errGettingPod
	}
	errRemovingFinalizer := controllerutil.RemoveFinalizer(latestPod, finalizerName)
	if !errRemovingFinalizer {
		log.Info("finalizer not deleted for ", "pod", latestPod.Name)
	}
	if err := r.Update(ctx, latestPod); err != nil {
		log.Info("unable to update removal of finalizer, retrying")
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

func (r *InstasliceReconciler) removeInstasliceAllocation(ctx context.Context, instasliceName string, allocation inferencev1alpha1.AllocationDetails) (ctrl.Result, error) {
	if allocation.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
		deleteResult, errDeletingAllocation := r.deleteInstasliceAllocation(ctx, instasliceName, allocation)
		if errDeletingAllocation != nil {
			return deleteResult, errDeletingAllocation
		}
	}
	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) setInstasliceAllocationToDeleting(ctx context.Context, instasliceName string, podUUID string, allocation inferencev1alpha1.AllocationDetails) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	allocation.Allocationstatus = inferencev1alpha1.AllocationStatusDeleting

	var updateInstasliceObject inferencev1alpha1.Instaslice
	typeNamespacedName := types.NamespacedName{
		Name:      instasliceName,
		Namespace: instaSliceOperatorNamespace, // TODO: modify if needed
	}
	errRetrievingInstaSlice := r.Get(ctx, typeNamespacedName, &updateInstasliceObject)
	if errRetrievingInstaSlice != nil {
		log.Error(errRetrievingInstaSlice, "error getting latest instaslice object")
		return ctrl.Result{Requeue: true}, errRetrievingInstaSlice
	}

	updateInstasliceObject.Spec.Allocations[podUUID] = allocation
	errUpdatingInstaslice := r.Update(ctx, &updateInstasliceObject)
	if errUpdatingInstaslice != nil {
		log.Info("unable to set instaslice to state ", "state", allocation.Allocationstatus, "pod", allocation.PodName)
		return ctrl.Result{Requeue: true}, errUpdatingInstaslice
	}

	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) addNodeSelectorAndUngatePod(ctx context.Context, pod *v1.Pod, allocations inferencev1alpha1.AllocationDetails) (ctrl.Result, error) {
	if pod.Spec.NodeSelector == nil {
		pod.Spec.NodeSelector = make(map[string]string)
	}
	pod.Spec.NodeSelector[NodeLabel] = allocations.Nodename

	ungatedPod := r.unGatePod(pod)
	err := r.Update(ctx, ungatedPod)
	if err != nil {
		logr.FromContext(ctx).Error(err, "error ungating pod")
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}
