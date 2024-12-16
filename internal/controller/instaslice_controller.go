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
	"sort"
	"strings"
	"time"

	"github.com/manifestival/manifestival"
	"github.com/openshift/instaslice-operator/internal/controller/utils"

	mfc "github.com/manifestival/controller-runtime-client"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller/config"
	mf "github.com/openshift/instaslice-operator/internal/controller/manifests"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// InstasliceReconciler reconciles a Instaslice object
type InstasliceReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	kubeClient         *kubernetes.Clientset
	Config             *config.Config
	RunningOnOpenShift bool
	allocationCache    map[types.UID]inferencev1alpha1.AllocationResult
	isCacheInitialized bool
}

// AllocationPolicy interface with a single method
type AllocationPolicy interface {
	SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName, allocationStatus inferencev1alpha1.AllocationStatus,
		discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32, namespace string, podName string, gpuUuid string, resourceIndetifier types.UID,
		nodeResourceList v1.ResourceList) (*inferencev1alpha1.AllocationRequest, *inferencev1alpha1.AllocationResult)
}

// not implemented
type RightToLeftPolicy struct{}

// not implemented
type LeftToRightPolicy struct{}

// first fit policy is implemented at the moment
type FirstFitPolicy struct{}

var daemonSetlabel = map[string]string{"app": "controller-daemonset"}

//+kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.redhat.com,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;patch;watch
//+kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;list;update;patch;watch
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=list
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=create;update;get;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *InstasliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)

	if r.RunningOnOpenShift {
		err := r.ReconcileSCC(ctx)
		if err != nil {
			log.Error(err, "Failed to reconcile SCC")
			return ctrl.Result{}, err
		}
	}

	// 1. Ensure DaemonSet is deployed
	daemonSet := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: InstasliceDaemonsetName, Namespace: InstaSliceOperatorNamespace}, daemonSet)
	if err != nil {
		if errors.IsNotFound(err) {
			// DaemonSet doesn't exist, so create it
			daemonSet = r.createInstaSliceDaemonSet(InstaSliceOperatorNamespace)
			err = r.Create(ctx, daemonSet)
			if err != nil {
				log.Error(err, "Failed to create DaemonSet")
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}
			log.Info("DaemonSet created successfully, waiting for pods to be ready")
			return ctrl.Result{RequeueAfter: requeue10sDelay}, nil
		}
		log.Error(err, "Failed to get DaemonSet")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	// 2. Check if at least one DaemonSet pod is ready
	var podList v1.PodList
	labelSelector := labels.SelectorFromSet(daemonSet.Spec.Selector.MatchLabels)

	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
		Namespace:     InstaSliceOperatorNamespace,
	}

	if err := r.List(ctx, &podList, listOptions); err != nil {
		log.Error(err, "Failed to list DaemonSet pods")
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	// Check if at least one daemonset pod is ready
	isAnyPodReady := false
	for _, pod := range podList.Items {
		if pod.Status.Phase == v1.PodRunning && len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].Ready {
			isAnyPodReady = true
			break
		}
	}
	if daemonSet.Status.NumberReady == 0 && !isAnyPodReady {
		log.Info("No DaemonSet pods are ready yet, waiting...")
		return ctrl.Result{RequeueAfter: requeue10sDelay}, nil
	}
	// TODO: should we rebuild cache on node failure?

	// Continue with the rest of the reconciliation logic
	policy := &FirstFitPolicy{}
	pod := &v1.Pod{}
	var instasliceList inferencev1alpha1.InstasliceList
	if err = r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		log.Error(err, "Error getting Instaslice object")
		return ctrl.Result{}, err
	}
	err = r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		// Error fetching the Pod
		if errors.IsNotFound(err) {
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

	if !isPodGated && !controllerutil.ContainsFinalizer(pod, FinalizerName) {
		return ctrl.Result{}, nil
	}

	// Add finalizer to the pod gated by InstaSlice
	if isPodGated && !controllerutil.ContainsFinalizer(pod, FinalizerName) {
		pod.Finalizers = append(pod.Finalizers, FinalizerName)
		err := r.Update(ctx, pod)
		if err != nil {
			log.Error(err, "failed to add finalizer to pod")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// failed pods are not deleted by InstaSlice, finalizer is removed so that user can
	// delete the pod.
	if pod.Status.Phase == v1.PodFailed && controllerutil.ContainsFinalizer(pod, FinalizerName) {
		for _, instaslice := range instasliceList.Items {
			for uuid, allocation := range instaslice.Status.PodAllocationResults {
				allocRequest := instaslice.Spec.PodAllocationRequests[uuid]
				if pod.UID == uuid {
					if allocation.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusCreating && allocation.AllocationStatus.AllocationStatusDaemonset == "" {
						return ctrl.Result{RequeueAfter: Requeue2sDelay}, nil
					}
					if allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated || allocation.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusUngated {
						resultDeleting, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, &allocation, &allocRequest)
						if err != nil {
							return resultDeleting, nil
						}
						// return and rely on daemonset to se allocation status to created
						// this will cause podmap function to wakeup pod and perform clean up
						return ctrl.Result{}, nil
					}
					if allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
						err := r.removeInstasliceAllocation(ctx, instaslice.Name, &allocation)
						if err != nil {
							return ctrl.Result{}, err
						}
						r.CleanupOrphanedAllocations(ctx, &instasliceList)
						// update DeployedPodTotal Metrics by setting value to 0 as pod allocation is deleted and pod is no loger consuming slices
						if err = r.UpdateDeployedPodTotalMetrics(string(allocation.Nodename), allocation.GPUUUID, allocRequest.PodRef.Namespace, allocRequest.PodRef.Name, allocRequest.Profile, 0); err != nil {
							log.Error(err, "Failed to update deployed pod metrics", "nodeName", allocation.Nodename)
						}
						//update compatible profiles metrics
						if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name); err != nil {
							log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", instaslice.Name)
						}
						// requeue for the finalizer to be removed
						return ctrl.Result{RequeueAfter: Requeue2sDelay}, nil
					}
					return ctrl.Result{}, nil
				}
			}
		}
		// pod can be terminated without any allocation
		if controllerutil.RemoveFinalizer(pod, FinalizerName) {
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
	if pod.Status.Phase == v1.PodSucceeded && controllerutil.ContainsFinalizer(pod, FinalizerName) {
		for _, instaslice := range instasliceList.Items {
			for uuid, allocation := range instaslice.Status.PodAllocationResults {
				if uuid == pod.UID {
					allocRequest := instaslice.Spec.PodAllocationRequests[uuid]
					if allocation.AllocationStatus.AllocationStatusDaemonset != inferencev1alpha1.AllocationStatusDeleted {
						log.Info("setting status to deleting", "pod", pod.Name)
						result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, &allocation, &allocRequest)
						if err != nil {
							return result, err
						}
						// return and rely on daemonset to se allocation status to created
						// this will cause podmap function to wakeup pod and perform clean up
						return ctrl.Result{}, nil
					}

					if allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
						err := r.removeInstasliceAllocation(ctx, instaslice.Name, &allocation)
						if err != nil {
							return ctrl.Result{}, err
						}
						r.CleanupOrphanedAllocations(ctx, &instasliceList)
						// update DeployedPodTotal Metrics by setting value to 0 as pod allocation is deleted and pod is no loger consuming slices
						if err = r.UpdateDeployedPodTotalMetrics(string(allocation.Nodename), allocation.GPUUUID, allocRequest.PodRef.Namespace, allocRequest.PodRef.Name, allocRequest.Profile, 0); err != nil {
							log.Error(err, "Failed to update deployed pod metrics", "nodeName", allocation.Nodename)
						}
						//update compatible profiles metrics
						if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name); err != nil {
							log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", instaslice.Name)
						}
						// requeue for the finalizer to be removed
						return ctrl.Result{RequeueAfter: Requeue2sDelay}, nil
					}
					return ctrl.Result{}, nil
				}
			}
		}

		// pod can be terminated as allocation was deleted in previous reconcile loop
		if controllerutil.RemoveFinalizer(pod, FinalizerName) {
			if err := r.Update(ctx, pod); err != nil {
				// requeing immediately as the finalizer removal gets lost
				return ctrl.Result{Requeue: true}, nil
			}
			log.Info("finalizer deleted for succeeded ", "pod", pod.Name)
		}
		return ctrl.Result{}, nil
	}

	// handle deleted pod that never gets ungated
	// set allocation status to deleting to cleanup resources if any
	if !pod.DeletionTimestamp.IsZero() && isPodGated {
		// allocation can be in creating or created while the user deletes the pod.
		for _, instaslice := range instasliceList.Items {
			for podUuid, allocation := range instaslice.Status.PodAllocationResults {
				allocRequest := instaslice.Spec.PodAllocationRequests[podUuid]
				if podUuid == pod.UID && (allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated) {
					allocation.AllocationStatus.AllocationStatusController = inferencev1alpha1.AllocationStatusDeleting
					if err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, &allocation, &allocRequest); err != nil {
						log.Info("unable to set instaslice to state deleted for ungated", "pod", pod.Name)
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					return ctrl.Result{}, nil
				}
				if podUuid == pod.UID && allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
					err := r.removeInstasliceAllocation(ctx, instaslice.Name, &allocation)
					if err != nil {
						return ctrl.Result{}, err
					}
					r.CleanupOrphanedAllocations(ctx, &instasliceList)
					// update DeployedPodTotal Metrics by setting value to 0 as pod allocation is deleted and pod is no loger consuming slices
					if err = r.UpdateDeployedPodTotalMetrics(string(allocation.Nodename), allocation.GPUUUID, allocRequest.PodRef.Namespace, allocRequest.PodRef.Name, allocRequest.Profile, 0); err != nil {
						log.Error(err, "Failed to update deployed pod metrics", "nodeName", allocation.Nodename)
					}
					//update compatible profiles metrics
					if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name); err != nil {
						log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", instaslice.Name)
					}
					if controllerutil.RemoveFinalizer(pod, FinalizerName) {
						if err := r.Update(ctx, pod); err != nil {
							// requeing immediately as the finalizer removal gets lost
							return ctrl.Result{Requeue: true}, nil
						}
						log.Info("finalizer deleted for allocation status deleted ", "pod", pod.Name)
					}
					return ctrl.Result{}, nil
				}
			}
		}
		return ctrl.Result{}, nil
	}
	// handle graceful termination of pods, wait for about 30 seconds from the time deletiontimestamp is set on the pod
	if !pod.DeletionTimestamp.IsZero() {
		log.Info("set status to deleting for ", "pod", pod.Name)
		if controllerutil.ContainsFinalizer(pod, FinalizerName) {
			for _, instaslice := range instasliceList.Items {
				for podUuid, allocation := range instaslice.Status.PodAllocationResults {
					if podUuid == pod.UID {
						if allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
							allocRequest := instaslice.Spec.PodAllocationRequests[podUuid]
							err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, &allocation, &allocRequest)
							if err != nil {
								return ctrl.Result{}, err
							}
							resultRemove, err := r.removeInstaSliceFinalizer(ctx, req)
							if err != nil {
								return resultRemove, err
							}
						}
						elapsed := time.Since(pod.DeletionTimestamp.Time)
						if elapsed > 30*time.Second {
							allocation.AllocationStatus.AllocationStatusController = inferencev1alpha1.AllocationStatusDeleting
							allocRequest := instaslice.Spec.PodAllocationRequests[podUuid]
							if err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, &allocation, &allocRequest); err != nil {
								log.Info("unable to set instaslice to state deleted for ", "pod", pod.Name)
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
		// Assume pod only has one container with one GPU requests
		if len(pod.Spec.Containers) != 1 {
			return ctrl.Result{}, fmt.Errorf(multipleContainersUnsupportedErr+", pod: %v", pod.Name)
		}
		limits := pod.Spec.Containers[0].Resources.Limits
		profileName := r.extractProfileName(limits)
		var podHasNodeAllocation bool
		// search if pod has allocation in any of the instaslice object in the cluster
		// TODO: allocations may get slower as the cluster size increases
		for _, instaslice := range instasliceList.Items {
			for uuid := range instaslice.Spec.PodAllocationRequests {
				// no matter the state if allocations exists for a pod skip such a pod
				if uuid == pod.UID {
					podHasNodeAllocation = true
				}
			}
		}

		for _, instaslice := range instasliceList.Items {
			for uuid, allocations := range instaslice.Status.PodAllocationResults {
				if allocations.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated && uuid == pod.UID {
					allocations.AllocationStatus.AllocationStatusController = inferencev1alpha1.AllocationStatusUngated
					allocRequest := instaslice.Spec.PodAllocationRequests[uuid]
					if err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, &allocations, &allocRequest); err != nil {
						return ctrl.Result{Requeue: true}, err
					}
					result, err := r.addNodeSelectorAndUngatePod(ctx, pod, &allocations)
					if err != nil {
						return result, err
					}
					break
				}
				// InstaSlice object got updated with ungated status but the controller failed
				// ungating the pod.
				if allocations.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusUngated && uuid == pod.UID {
					result, err := r.addNodeSelectorAndUngatePod(ctx, pod, &allocations)
					if err != nil {
						return result, err
					}
				}
			}
			// Fetch latest Instaslice state before updating metrics
			updatedInstaslice, err := r.getInstasliceObject(ctx, instaslice.Name, instaslice.Namespace)
			if err != nil {
				log.Error(err, "Failed to get latest Instaslice object", "instaslice", instaslice.Name)
				return ctrl.Result{Requeue: true}, nil
			}
			// update compatible profiles metrics
			if err := r.UpdateCompatibleProfilesMetrics(*updatedInstaslice, instaslice.Name); err != nil {
				log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", updatedInstaslice.Name)
			}
		}
		// pod does not have an allocation yet, make allocation
		// find the node
		if !podHasNodeAllocation {
			sort.Slice(instasliceList.Items, func(i, j int) bool {
				// Sort by Name in ascending order
				return instasliceList.Items[i].Name < instasliceList.Items[j].Name
			})
			err := r.rebuildAllocationCache(ctx)
			if err != nil {
				return ctrl.Result{}, err
			}

			r.CleanupOrphanedAllocations(ctx, &instasliceList)
			for _, instaslice := range instasliceList.Items {
				// find the GPU on the node and the GPU index where the slice can be created
				allocRequest, allocResult, err := r.findNodeAndDeviceForASlice(ctx, &instaslice, profileName, policy, pod)
				if err != nil {
					continue
				}
				// allocation was successful
				r.updateCacheWithNewAllocation(allocRequest.PodRef.UID, *allocResult)
				podHasNodeAllocation = true
				if podHasNodeAllocation {
					err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, allocResult, allocRequest)
					if err != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					// allocation was successful
					// update deployed pod total metrics
					if err := r.UpdateDeployedPodTotalMetrics(string(allocResult.Nodename), allocResult.GPUUUID, allocRequest.PodRef.Namespace, allocRequest.PodRef.Name, allocRequest.Profile, allocResult.MigPlacement.Size); err != nil {
						log.Error(err, "Failed to update deployed pod metrics (node: %s, namespce: %s, pod: %s): %w", allocResult.Nodename, allocRequest.PodRef.Namespace, allocRequest.PodRef.Name, err)
					}
					// update total processed GPU slices metrics
					if err = r.IncrementTotalProcessedGpuSliceMetrics(instaslice, string(allocResult.Nodename), allocResult.GPUUUID, profileName, pod); err != nil {
						log.Error(err, "Failed to update total processed GPU slices metric", "nodeName", allocResult.Nodename, "gpuID", allocResult.GPUUUID)
					}
					return ctrl.Result{}, nil
				}
			}
		}

		// if the cluster does not have suitable node, requeue request
		if !podHasNodeAllocation {
			log.Info("no suitable node found in cluster for ", "pod", pod.Name)
			// Generate a random duration between 1 and 10 seconds
			randomDuration := time.Duration(rand.Intn(10)+1) * time.Second
			return ctrl.Result{RequeueAfter: randomDuration}, nil
		}

	}
	return ctrl.Result{}, nil
}

// createInstaSliceDaemonSet - create the DaemonSet object
func (r *InstasliceReconciler) createInstaSliceDaemonSet(namespace string) *appsv1.DaemonSet {
	emulatorMode := r.Config.EmulatorModeEnable
	instasliceDaemonsetImage := r.Config.DaemonsetImage

	// Base DaemonSet structure
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InstasliceDaemonsetName,
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
					NodeSelector: map[string]string{
						"nvidia.com/mig.capable": "true",
					},
					Containers: []v1.Container{
						{
							Name:            daemonSetName,
							Image:           instasliceDaemonsetImage,
							ImagePullPolicy: v1.PullAlways,
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
									Value: fmt.Sprintf("%v", emulatorMode),
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
func (*InstasliceReconciler) extractGpuProfile(instaslice *inferencev1alpha1.Instaslice, profileName string) (int32, int32, int32, int32) {
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

// isPodSchedulingGated checks if a pod has a scheduling gate and is actively blocked
func checkIfPodGatedByInstaSlice(pod *v1.Pod) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == GateName {
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
		if gate.Name != GateName {
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
		for uuidAllocResult, allocationResult := range instaslice.Status.PodAllocationResults {
			if allocationResult.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated || allocationResult.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
				for uuidAllocRequest, allocationRequest := range instaslice.Spec.PodAllocationRequests {
					if uuidAllocRequest == uuidAllocResult {
						requests = append(requests, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Namespace: allocationRequest.PodRef.Namespace,
								Name:      allocationRequest.PodRef.Name,
							},
						})
					}
				}
			}
		}
	}
	return requests
}

// Initialize Prometheus-compatible profiles metrics when the controller starts
// Adds a background goroutine that waits for Instaslice objects.
// Proceeds to setupWithManager(mgr) to start the reconciler
// Does not block the controller from reconciling
// UpdateCompatibleProfilesMetrics only updates Prometheus metrics,in-memory and do not persist in etcd
// TODO: support daemonset fault tolerance and controller fault tolerance (skipping an update for a faster boot)
func (r *InstasliceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	restConfig := mgr.GetConfig()
	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	mgrAddErr := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		log := logr.FromContext(ctx)
		<-mgr.Elected() // Wait for leader election before executing
		// Retry mechanism to wait for Instaslice objects
		var instasliceList inferencev1alpha1.InstasliceList
		retryErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			if err := r.List(ctx, &instasliceList); err != nil {
				log.Error(err, "Failed to list Instaslice objects, retrying...")
				return false, nil
			}
			if len(instasliceList.Items) > 0 {
				log.Info("Instaslice objects found", "count", len(instasliceList.Items))
				return true, nil
			}
			log.Info("No Instaslice objects found, waiting...")
			return false, nil
		})
		if retryErr != nil {
			log.Error(retryErr, "Failed to fetch Instaslice objects after retries")
			return nil // Do not block the controller from running, meaning reconciler starts in parallel
		}
		// Iterate over Instaslices and update Prometheus metrics
		for _, instaslice := range instasliceList.Items {
			if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name); err != nil {
				log.Error(err, "Failed to update compatible profiles metrics", "instaslice", instaslice.Name)
			}
		}
		log.Info("Successfully initialized compatible profiles metrics for all Instaslice objects")
		return nil
	}))

	if mgrAddErr != nil {
		return mgrAddErr
	}

	// Continue with setting up the controller
	return r.setupWithManager(mgr) // Return error directly for readability
}

// Enable creation of controller
func (r *InstasliceReconciler) setupWithManager(mgr ctrl.Manager) error {
	restConfig := mgr.GetConfig()
	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	err = ctrl.NewControllerManagedBy(mgr).
		For(&v1.Pod{}).Named("InstaSlice-controller").
		Watches(&inferencev1alpha1.Instaslice{}, handler.EnqueueRequestsFromMapFunc(r.podMapFunc)).
		Watches(&v1.Node{},
			&handler.EnqueueRequestForObject{}).
		Complete(r)

	if err != nil {
		log := mgr.GetLogger() // Get logger from the manager
		log.Error(err, "Failed to set up Instaslice controller")
		return err
	}

	log := mgr.GetLogger()
	log.Info("Successfully set up Instaslice controller")
	return nil
}

func (r *InstasliceReconciler) unGatePod(podUpdate *v1.Pod) *v1.Pod {
	for i, gate := range podUpdate.Spec.SchedulingGates {
		if gate.Name == GateName {
			podUpdate.Spec.SchedulingGates = append(podUpdate.Spec.SchedulingGates[:i], podUpdate.Spec.SchedulingGates[i+1:]...)
		}
	}
	return podUpdate
}

func (r *InstasliceReconciler) removeInstaSliceFinalizer(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	latestPod := &v1.Pod{}
	err := r.Get(ctx, req.NamespacedName, latestPod)
	if err != nil {
		log.Error(err, "error getting latest copy of pod")
		return ctrl.Result{Requeue: true}, err
	}
	ok := controllerutil.RemoveFinalizer(latestPod, FinalizerName)
	if !ok {
		log.Info("finalizer not deleted for ", "pod", latestPod.Name)
		return ctrl.Result{Requeue: true}, err
	}
	if err := r.Update(ctx, latestPod); err != nil {
		log.Info("unable to update removal of finalizer, retrying")
		return ctrl.Result{Requeue: true}, err
	}
	return ctrl.Result{}, nil
}

// Policy based allocation - FirstFit
func (r *FirstFitPolicy) SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName,
	allocationStatus inferencev1alpha1.AllocationStatus, discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32,
	namespace string, podName string, gpuUuid string, resourceIdentifier types.UID, availableResourceList v1.ResourceList) (*inferencev1alpha1.AllocationRequest, *inferencev1alpha1.AllocationResult) {
	return &inferencev1alpha1.AllocationRequest{
			Profile: profileName,
			Resources: v1.ResourceRequirements{
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    *availableResourceList.Cpu(),
					v1.ResourceMemory: *availableResourceList.Memory(),
				},
			},
			PodRef: v1.ObjectReference{
				Kind:      "Pod",
				Namespace: namespace,
				Name:      podName,
				UID:       podUUID,
			},
		}, &inferencev1alpha1.AllocationResult{
			MigPlacement: inferencev1alpha1.Placement{
				Size:  size,
				Start: newStart,
			},
			GPUUUID:                     gpuUuid,
			Nodename:                    nodename,
			AllocationStatus:            allocationStatus,
			ConfigMapResourceIdentifier: resourceIdentifier,
			Conditions:                  []metav1.Condition{},
		}
}

// Policy based allocation - LeftToRIght
func (l *LeftToRightPolicy) SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName,
	allocationStatus inferencev1alpha1.AllocationStatus, discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32,
	namespace string, podName string, gpuUuid string, resourceIdentifier types.UID, availableResourceList v1.ResourceList) *inferencev1alpha1.AllocationRequest {
	// Implement the left-to-right policy here
	return &inferencev1alpha1.AllocationRequest{}
}

// Policy based allocation - RigghToLeft
func (l *RightToLeftPolicy) SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName,
	allocationStatus inferencev1alpha1.AllocationStatus, discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32,
	namespace string, podName string, gpuUuid string, resourceIdentifier types.UID, availableResourceList v1.ResourceList) *inferencev1alpha1.AllocationRequest {
	// Implement the left-to-right policy here
	return &inferencev1alpha1.AllocationRequest{}
}

func (r *InstasliceReconciler) removeInstasliceAllocation(ctx context.Context, instasliceName string, allocation *inferencev1alpha1.AllocationResult) error {
	if allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
		err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instasliceName, nil, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *InstasliceReconciler) setInstasliceAllocationToDeleting(ctx context.Context, instasliceName string, allocResult *inferencev1alpha1.AllocationResult, allocRequest *inferencev1alpha1.AllocationRequest) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	allocResult.AllocationStatus.AllocationStatusController = inferencev1alpha1.AllocationStatusDeleting
	if err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instasliceName, allocResult, allocRequest); err != nil {
		log.Info("unable to set instaslice to state ", "state", allocResult.AllocationStatus.AllocationStatusController, "pod", allocRequest.PodRef.Name)
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) addNodeSelectorAndUngatePod(ctx context.Context, pod *v1.Pod, allocResult *inferencev1alpha1.AllocationResult) (ctrl.Result, error) {
	if pod.Spec.NodeSelector == nil {
		pod.Spec.NodeSelector = make(map[string]string)
	}
	pod.Spec.NodeSelector[NodeLabel] = string(allocResult.Nodename)

	ungatedPod := r.unGatePod(pod)
	err := r.Update(ctx, ungatedPod)
	if err != nil {
		logr.FromContext(ctx).Error(err, "error ungating pod")
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}

// TODO move this to utils and refer to common function
func (r *InstasliceReconciler) getInstasliceObject(ctx context.Context, instasliceName string, namespace string) (*inferencev1alpha1.Instaslice, error) {
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

func (r *InstasliceReconciler) ReconcileSCC(ctx context.Context) error {
	manifests, err := mf.GetResourcesManifests(r.Config.ManifestConfigDir)
	if err != nil {
		return err
	}
	sccs := manifests.Filter(manifestival.ByKind("SecurityContextConstraints"))
	sccs.Client = mfc.NewClient(r.Client)
	return sccs.Apply()
}
