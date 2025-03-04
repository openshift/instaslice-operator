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
	"math/rand"
	"reflect"
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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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

type NodeReconciler struct {
	client.Client
}

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

func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var node v1.Node
	err := r.Get(ctx, req.NamespacedName, &node)
	log.FromContext(ctx).Info("entering node reconcile")
	instasliceKey := types.NamespacedName{
		Namespace: InstaSliceOperatorNamespace,
		Name:      node.Name,
	}
	if err != nil {
		if errors.IsNotFound(err) {
			instaslice := &inferencev1alpha1.Instaslice{}
			instasliceKey := types.NamespacedName{Name: req.Name} // Assuming CR name matches node name

			if err := r.Get(ctx, instasliceKey, instaslice); err != nil {
				if errors.IsNotFound(err) {
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}

			// Delete the InstaSlice CR
			if err := r.Delete(ctx, instaslice); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete InstaSlice CR for deleted node %s: %w", req.Name, err)
			}
			log.FromContext(ctx).Info("Done deleting instaslice CR", "name", instaslice.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if val, exists := node.Labels["nvidia.com/mig.strategy"]; exists && val != "mixed" {
		instaslice := &inferencev1alpha1.Instaslice{}
		err := r.Get(ctx, instasliceKey, instaslice)
		if err != nil {
			if errors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}

		if err := r.Delete(ctx, instaslice); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete InstaSlice CR for node %s: %w", node.Name, err)
		}
		log.FromContext(ctx).Info("Done deleting instaslice CR", "name", instaslice.Name)
		if err := r.cleanUpNodeResources(ctx, &node); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to clean up node resources for node %s: %w", node.Name, err)
		}
		log.FromContext(ctx).Info("Done deleting instaslice extended resources")
	}

	return ctrl.Result{}, nil
}

func (r *InstasliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)

	if r.RunningOnOpenShift {
		err := r.ReconcileSCC(ctx)
		if err != nil {
			log.Error(err, "Failed to reconcile SCC")
			return ctrl.Result{}, err
		}
	}
	// rebuild cache on node failure
	node := &v1.Node{}
	err := r.Get(ctx, req.NamespacedName, node)
	if err == nil {
		for _, condition := range node.Status.Conditions {
			if condition.Type == v1.NodeReady && condition.Status != v1.ConditionTrue {
				log.Info("Detected a node going down", "node:", node.Name)
				err := r.rebuildAllocationCache(ctx)
				if err != nil {
					return ctrl.Result{}, err
				}
				break
			}
		}
		return ctrl.Result{}, nil
	}

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
				if pod.UID == uuid {
					if allocation.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusCreating && allocation.AllocationStatus.AllocationStatusDaemonset == "" {
						return ctrl.Result{RequeueAfter: Requeue2sDelay}, nil
					}
					if allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated || allocation.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusUngated {
						allocRequest := instaslice.Spec.PodAllocationRequests[uuid]
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
					if allocation.AllocationStatus.AllocationStatusDaemonset != inferencev1alpha1.AllocationStatusDeleted {
						allocRequest := instaslice.Spec.PodAllocationRequests[uuid]
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
				if podUuid == pod.UID && (allocation.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusCreated) {
					allocation.AllocationStatus.AllocationStatusController = inferencev1alpha1.AllocationStatusDeleting
					allocRequest := instaslice.Spec.PodAllocationRequests[podUuid]
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
			for _, instaslice := range instasliceList.Items {
				// find the GPU on the node and the GPU index where the slice can be created
				allocRequest, allocResult, err := r.findNodeAndDeviceForASlice(ctx, &instaslice, profileName, policy, pod)
				if err != nil {
					continue
				}
				podHasNodeAllocation = true
				if podHasNodeAllocation {
					err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, allocResult, allocRequest)
					if err != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					// allocation was successful
					r.updateCacheWithNewAllocation(allocRequest.PodRef.UID, *allocResult)
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
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

// create the DaemonSet object
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
						"nvidia.com/mig.strategy": "mixed",
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

// SetupWithManager sets up the controller with the Manager.
func (r *InstasliceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	restConfig := mgr.GetConfig()

	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	if err := r.setupWithManager(mgr); err != nil {
		return err
	}

	mgrAddErr := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-mgr.Elected()
		// Ensure DaemonSet is deployed at startup
		if err := r.ensureDaemonSet(ctx); err != nil {
			return fmt.Errorf("failed to ensure DaemonSet: %w", err)
		}
		return nil
	}))

	if mgrAddErr != nil {
		return mgrAddErr
	}
	return nil
}

func hasInstasliceMutation(obj client.Object) bool {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		return false
	}

	if value, exists := pod.Annotations["instaslice.redhat.com/mutated"]; exists && value == "true" {
		return true
	}

	return false
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

func (r *InstasliceReconciler) ensureDaemonSet(ctx context.Context) error {
	log := logr.FromContext(ctx)
	daemonSet := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: InstasliceDaemonsetName, Namespace: InstaSliceOperatorNamespace}, daemonSet)
	if err != nil {
		if errors.IsNotFound(err) {
			daemonSet = r.createInstaSliceDaemonSet(InstaSliceOperatorNamespace)
			if err := r.Create(ctx, daemonSet); err != nil {
				log.Error(err, "Failed to create DaemonSet")
				return err
			}
			log.Info("DaemonSet created successfully")
		} else {
			log.Error(err, "Failed to get DaemonSet")
			return err
		}
	}

	pollInterval := 5 * time.Second
	timeout := 2 * time.Minute

	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log.Info("Waiting for at least one DaemonSet pod to be ready...")

	err = wait.PollUntilContextTimeout(ctxWithTimeout, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		isReady, err := r.isDaemonSetPodReady(ctx, daemonSet)
		if err != nil {
			log.Error(err, "Failed to check DaemonSet pod readiness")
			return false, err
		}
		return isReady, nil
	})

	if err != nil {
		log.Error(err, "Timeout waiting for DaemonSet pod readiness")
		return err
	}

	log.Info("At least one DaemonSet pod is ready")
	return nil
}

func (r *InstasliceReconciler) isDaemonSetPodReady(ctx context.Context, daemonSet *appsv1.DaemonSet) (bool, error) {
	var podList v1.PodList
	labelSelector := labels.SelectorFromSet(daemonSet.Spec.Selector.MatchLabels)

	listOptions := &client.ListOptions{
		LabelSelector: labelSelector,
		Namespace:     daemonSet.Namespace,
	}

	if err := r.List(ctx, &podList, listOptions); err != nil {
		return false, err
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase == v1.PodRunning && len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].Ready {
			return true, nil
		}
	}

	return false, nil
}

// Enable creation of controller caches to talk to the API server in order to perform
// object discovery in SetupWithManager
func (r *InstasliceReconciler) setupWithManager(mgr ctrl.Manager) error {
	instaslicePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasInstasliceMutation(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return hasInstasliceMutation(e.ObjectNew) || !isEqual(e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return hasInstasliceMutation(e.Object)
		},
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&v1.Pod{}).Named("InstaSlice-controller").
		Watches(&inferencev1alpha1.Instaslice{}, handler.EnqueueRequestsFromMapFunc(r.podMapFunc)).
		WithEventFilter(instaslicePredicate).
		Complete(r); err != nil {
		return err
	}
	nodePredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode := e.ObjectOld.(*v1.Node)
			newNode := e.ObjectNew.(*v1.Node)
			oldMig, oldExists := oldNode.Labels["nvidia.com/mig.strategy"]
			newMig, newExists := newNode.Labels["nvidia.com/mig.strategy"]

			return (oldExists != newExists) || (oldMig != newMig)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
	}
	nodeReconciler := &NodeReconciler{Client: mgr.GetClient()}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&v1.Node{}).Named("Node-controller").
		WithEventFilter(nodePredicate).
		Complete(nodeReconciler); err != nil {
		return err
	}
	return nil
}

func isEqual(oldObj, newObj client.Object) bool {
	oldInstaslice, ok1 := oldObj.(*inferencev1alpha1.Instaslice)
	newInstaslice, ok2 := newObj.(*inferencev1alpha1.Instaslice)

	if !ok1 || !ok2 {
		return false // Type mismatch, consider this an update
	}

	// Compare the relevant fields that matter for reconciliation
	return reflect.DeepEqual(oldInstaslice.Spec, newInstaslice.Spec) &&
		reflect.DeepEqual(oldInstaslice.Status, newInstaslice.Status)
}

// cleanUpNodeResources removes custom resources from the node
func (r *NodeReconciler) cleanUpNodeResources(ctx context.Context, node *v1.Node) error {
	resourceRegex := regexp.MustCompile(`^instaslice\.redhat\.com/(.*)`)
	patchData := map[string]interface{}{
		"status": map[string]interface{}{
			"capacity":    map[string]interface{}{},
			"allocatable": map[string]interface{}{},
		},
	}

	for resource := range node.Status.Capacity {
		if resourceRegex.MatchString(string(resource)) {
			patchData["status"].(map[string]interface{})["capacity"].(map[string]interface{})[string(resource)] = nil
		}
	}
	for resource := range node.Status.Allocatable {
		if resourceRegex.MatchString(string(resource)) {
			patchData["status"].(map[string]interface{})["allocatable"].(map[string]interface{})[string(resource)] = nil
		}
	}

	if len(patchData["status"].(map[string]interface{})["capacity"].(map[string]interface{})) == 0 &&
		len(patchData["status"].(map[string]interface{})["allocatable"].(map[string]interface{})) == 0 {
		return nil
	}

	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data: %w", err)
	}

	return r.Status().Patch(ctx, node, client.RawPatch(types.MergePatchType, patchBytes))
}
