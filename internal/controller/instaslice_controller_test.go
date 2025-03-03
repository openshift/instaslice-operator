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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller/config"
	"github.com/openshift/instaslice-operator/internal/controller/utils"
)

func TestChangesAllocationDeletionAndFinalizer(t *testing.T) {
	_ = Describe("InstasliceReconciler processInstasliceAllocation", func() {
		var (
			ctx        context.Context
			r          *InstasliceReconciler
			fakeClient client.Client
			instaslice *inferencev1alpha1.Instaslice
			pod        *v1.Pod
			podUUID    string
			req        ctrl.Request
		)

		BeforeEach(func() {
			ctx = context.TODO()

			scheme := runtime.NewScheme()
			Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(v1.AddToScheme(scheme)).To(Succeed())

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&inferencev1alpha1.Instaslice{}).
				Build()
			config := config.ConfigFromEnvironment()

			r = &InstasliceReconciler{
				Client: fakeClient,
				Config: config,
			}

			podUUID = "test-pod-uuid"

			pod = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: InstaSliceOperatorNamespace,
					UID:       types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
			}

			Expect(fakeClient.Create(ctx, pod)).To(Succeed())

			instaslice = &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-instaslice",
					Namespace: InstaSliceOperatorNamespace,
				},
				Spec: inferencev1alpha1.InstasliceSpec{
					PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{
						types.UID(podUUID): {
							Profile: "test-profile",
							PodRef: v1.ObjectReference{
								Name:      pod.Name,
								Namespace: InstaSliceOperatorNamespace,
								UID:       pod.UID,
							},
							Resources: v1.ResourceRequirements{},
						},
					},
				},
				Status: inferencev1alpha1.InstasliceStatus{
					PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{
						types.UID(podUUID): {
							AllocationStatus:            inferencev1alpha1.AllocationStatus{AllocationStatusController: inferencev1alpha1.AllocationStatusCreating},
							GPUUUID:                     "GPU-12345",
							Nodename:                    "fake-node",
							ConfigMapResourceIdentifier: "fake-configmap-uid",
						},
					},
				},
			}

			Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())

			req = ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-pod",
					Namespace: InstaSliceOperatorNamespace,
				},
			}
		})

		It("should remove pod allocation from PodAllocationResults when allocation status is Deleted", func() {
			instaslice.Status.PodAllocationResults[pod.GetUID()] = inferencev1alpha1.AllocationResult{
				AllocationStatus: inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusDeleted},
				GPUUUID:          "fake-gpu-uuid",
				Nodename:         "fake-node",
			}
			Expect(fakeClient.Status().Update(ctx, instaslice)).To(Succeed())

			allocationResult := instaslice.Status.PodAllocationResults[pod.GetUID()]
			allocationRequest := instaslice.Spec.PodAllocationRequests[pod.GetUID()]
			err := utils.UpdateOrDeleteInstasliceAllocations(ctx, r.Client, instaslice.Name, &allocationResult, &allocationRequest)
			Expect(err).NotTo(HaveOccurred())

			updatedInstaSlice := &inferencev1alpha1.Instaslice{}
			Expect(fakeClient.Get(
				ctx,
				types.NamespacedName{
					Name:      instaslice.Name,
					Namespace: InstaSliceOperatorNamespace,
				},
				updatedInstaSlice,
			)).To(Succeed())

			_, allocationExists := updatedInstaSlice.Status.PodAllocationResults[pod.GetUID()]
			Expect(allocationExists).To(BeFalse())
		})

		It("should remove finalizer after allocation is deleted", func() {
			instaslice.Status.PodAllocationResults[types.UID(podUUID)] = inferencev1alpha1.AllocationResult{
				AllocationStatus: inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusDeleted},
				GPUUUID:          "fake-gpu-uuid",
				Nodename:         "fake-node",
			}
			Expect(fakeClient.Status().Update(ctx, instaslice)).To(Succeed())

			result, err := r.removeInstaSliceFinalizer(ctx, req)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPod := &v1.Pod{}
			Expect(fakeClient.Get(ctx, req.NamespacedName, updatedPod)).To(Succeed())
			Expect(updatedPod.Finalizers).NotTo(ContainElement(FinalizerName))
		})

		It("should set allocation status to Deleting if status is not Deleted", func() {
			allocationResult := instaslice.Status.PodAllocationResults[pod.GetUID()]
			allocationRequest := instaslice.Spec.PodAllocationRequests[pod.GetUID()]
			result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, &allocationResult, &allocationRequest)

			Expect(err).NotTo(HaveOccurred())

			updatedInstaSlice := &inferencev1alpha1.Instaslice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: instaslice.Name, Namespace: InstaSliceOperatorNamespace}, updatedInstaSlice)).To(Succeed())

			Expect(updatedInstaSlice.Status.PodAllocationResults[pod.GetUID()].AllocationStatus.AllocationStatusController).To(Equal(inferencev1alpha1.AllocationStatusDeleting))

			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should requeue if there is an error updating the instaslice", func() {
			r.Client = fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
			allocationResult := instaslice.Status.PodAllocationResults[pod.GetUID()]
			allocationRequest := instaslice.Spec.PodAllocationRequests[pod.GetUID()]
			result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, &allocationResult, &allocationRequest)

			Expect(err).To(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())
		})
	})
}

func TestInstasliceDaemonsetCreation_Reconcile(t *testing.T) {
	_ = Describe("InstasliceReconciler", func() {
		var (
			r               *InstasliceReconciler
			fakeClient      client.Client
			scheme          *runtime.Scheme
			ctx             context.Context
			req             ctrl.Request
			daemonSet       *appsv1.DaemonSet
			reconcileResult ctrl.Result
			reconcileErr    error
		)

		BeforeEach(func() {
			scheme = runtime.NewScheme()
			Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(v1.AddToScheme(scheme)).To(Succeed())
			Expect(appsv1.AddToScheme(scheme)).To(Succeed()) // Ensure DaemonSet is registered

			fakeClient = fake.NewClientBuilder().WithScheme(scheme).Build()

			config := config.ConfigFromEnvironment()
			r = &InstasliceReconciler{
				Client: fakeClient,
				Scheme: scheme,
				Config: config,
			}

			ctx = context.Background()

			// Mock DaemonSet object
			daemonSet = &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "instaslice-operator-controller-daemonset",
					Namespace: InstaSliceOperatorNamespace,
				},
				Spec: appsv1.DaemonSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "controller-daemonset",
						},
					},
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "controller-daemonset",
							},
						},
					},
				},
			}

			req = ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "instaslice-operator-controller-daemonset",
					Namespace: InstaSliceOperatorNamespace,
				},
			}
		})

		When("DaemonSet does not exist", func() {
			It("should create the DaemonSet", func() {
				reconcileResult, reconcileErr = r.Reconcile(ctx, req)

				Expect(reconcileErr).NotTo(HaveOccurred())
				Expect(reconcileResult.RequeueAfter).To(Equal(10 * time.Second))

				// Check that the DaemonSet was created
				createdDaemonSet := &appsv1.DaemonSet{}
				err := fakeClient.Get(ctx, req.NamespacedName, createdDaemonSet)
				Expect(err).NotTo(HaveOccurred())
				Expect(createdDaemonSet.Name).To(Equal("instaslice-operator-controller-daemonset"))
			})
		})

		When("DaemonSet exists but no pods are ready", func() {
			BeforeEach(func() {
				daemonSet.Status.DesiredNumberScheduled = 1
				daemonSet.Status.NumberReady = 0 // No pods are ready
				_ = fakeClient.Create(ctx, daemonSet)

				// Create a pod for the DaemonSet, but it's not ready
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "instaslice-pod-1",
						Namespace: "instaslice-system",
						Labels:    daemonSet.Spec.Selector.MatchLabels,
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
						ContainerStatuses: []v1.ContainerStatus{
							{
								Ready: false, // Pod is not ready
							},
						},
					},
				}
				_ = fakeClient.Create(ctx, pod)
			})

			It("should requeue until at least one DaemonSet pod is ready", func() {
				reconcileResult, reconcileErr = r.Reconcile(ctx, req)

				Expect(reconcileErr).NotTo(HaveOccurred())
				Expect(reconcileResult.RequeueAfter).To(Equal(10 * time.Second))
			})
		})

		When("DaemonSet exists and at least one pod is ready", func() {
			BeforeEach(func() {
				daemonSet.Status.DesiredNumberScheduled = 1
				daemonSet.Status.NumberReady = 1 // At least one pod is ready
				_ = fakeClient.Create(ctx, daemonSet)

				// Create a ready pod for the DaemonSet
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "instaslice-pod-1",
						Namespace: "instaslice-system",
						Labels:    daemonSet.Spec.Selector.MatchLabels,
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
						ContainerStatuses: []v1.ContainerStatus{
							{
								Ready: true, // Pod is ready
							},
						},
					},
				}
				_ = fakeClient.Create(ctx, pod)
			})

			It("should not requeue", func() {
				reconcileResult, reconcileErr = r.Reconcile(ctx, req)

				Expect(reconcileErr).NotTo(HaveOccurred())
				Expect(reconcileResult.RequeueAfter).To(BeZero())
			})
		})
	})
}

func TestInstasliceReconciler_Reconcile(t *testing.T) {
	_ = Describe("InstasliceReconciler Reconcile Loop", func() {
		var (
			ctx        context.Context
			r          *InstasliceReconciler
			fakeClient client.Client
			instaslice *inferencev1alpha1.Instaslice
			pod        *v1.Pod
			podUUID    string
			req        ctrl.Request
		)

		BeforeEach(func() {
			ctx = context.TODO()

			scheme := runtime.NewScheme()
			Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(v1.AddToScheme(scheme)).To(Succeed())
			Expect(appsv1.AddToScheme(scheme)).To(Succeed()) // Ensure DaemonSet is registered

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&inferencev1alpha1.Instaslice{}).
				Build()

			config := config.ConfigFromEnvironment()

			r = &InstasliceReconciler{
				Client: fakeClient,
				Config: config,
			}

			podUUID = "test-pod-uuid"
			pod = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: InstaSliceOperatorNamespace,
					UID:       types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}

			instaslice = &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-instaslice",
					Namespace: InstaSliceOperatorNamespace,
				},
				Spec: inferencev1alpha1.InstasliceSpec{
					PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{
						types.UID(podUUID): {
							Profile: "test-profile",
							PodRef: v1.ObjectReference{
								Name:      pod.Name,
								Namespace: InstaSliceOperatorNamespace,
								UID:       pod.UID,
							},
							Resources: v1.ResourceRequirements{},
						},
					},
				},
				Status: inferencev1alpha1.InstasliceStatus{
					PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{
						types.UID(podUUID): {
							AllocationStatus:            inferencev1alpha1.AllocationStatus{AllocationStatusController: inferencev1alpha1.AllocationStatusCreating},
							GPUUUID:                     "GPU-12345",
							Nodename:                    "fake-node",
							ConfigMapResourceIdentifier: "fake-configmap-uid",
						},
					},
				},
			}

			daemonSet := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "instaslice-operator-controller-daemonset",
					Namespace: "instaslice-system",
				},
				Spec: appsv1.DaemonSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "controller-daemonset",
						},
					},
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "controller-daemonset",
							},
						},
					},
				},
				Status: appsv1.DaemonSetStatus{
					DesiredNumberScheduled: 3,
					NumberReady:            1, // one daemonset pod ready
				},
			}

			Expect(fakeClient.Create(ctx, daemonSet)).To(Succeed())
			Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())

			// Create a pod that matches the DaemonSet's selector
			daemonSetPod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-1",
					Namespace: "instaslice-system",
					Labels: map[string]string{
						"app": "controller-daemonset",
					},
				},
				Status: v1.PodStatus{Phase: v1.PodRunning}, // Set the status of the DaemonSet pod
			}

			// Create the DaemonSet pod
			Expect(fakeClient.Create(ctx, daemonSetPod)).To(Succeed())

			req = ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-pod",
					Namespace: InstaSliceOperatorNamespace,
				},
			}
		})

		It("should confirm at least one pod is present for the DaemonSet", func() {
			var podList v1.PodList
			err := fakeClient.List(ctx, &podList, client.InNamespace("instaslice-system"))
			Expect(err).NotTo(HaveOccurred())
			Expect(len(podList.Items)).To(BeNumerically(">", 0)) // Ensure at least one pod is present
		})

		It("should not reconcile for an unknown pod", func() {
			// replace the reconcile request with an unknown-pod name which isn't present in the system
			req.Name = "unknown-pod"
			result, err := r.Reconcile(ctx, req)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should not reconcile for an unknown scheduling gates", func() {
			// update the scheduling gates of the pod by unknown name
			pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{
				Name: "example.com/accelerator",
			})
			Expect(fakeClient.Update(ctx, pod)).To(Succeed())
			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should return from the reconcile if both gates and the finalizer are not present", func() {
			// update the scheduling gates, Finalizer to nil
			pod.Spec.SchedulingGates = nil
			pod.Finalizers = nil
			Expect(fakeClient.Update(ctx, pod)).To(Succeed())
			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should return if the containers are not present", func() {
			// update the scheduling gates of the pod by unknown name
			pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{
				Name: GateName,
			})
			pod.Finalizers = nil
			Expect(fakeClient.Update(ctx, pod)).To(Succeed())
			result, err := r.Reconcile(ctx, req)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(noContainerInsidePodErr))
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should reconcile a Failed Pod and remove the finalizer if the instaslice allocations are not present", func() {
			failedPodName := "failed-pod"
			// Define a Failed pod
			pod = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      failedPodName,
					Namespace: InstaSliceOperatorNamespace,
					// PodUUID has to be same as the one present in the instaslice allocations
					// to process the allocations
					UID: types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					SchedulingGates: append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: GateName}),
				},
				Status: v1.PodStatus{Phase: v1.PodFailed, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())
			// reconcile request over the failed pod name
			req.Name = failedPodName
			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			// As the allocations are present in the Instaslice Object, expecting a return from
			// the reconcile function without removing the Finalizer
			Expect(result).To(Equal(ctrl.Result{RequeueAfter: Requeue2sDelay}))

			// Update the podUUID and observe the Finalizer is not present inside the Failed pod
			// as the corresponding allocation details are not present inside the Instaslice Allocations
			pod.UID = types.UID(failedPodName)
			Expect(fakeClient.Update(ctx, pod)).To(Succeed())
			req.Name = failedPodName
			result, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			newPod := &v1.Pod{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: failedPodName, Namespace: InstaSliceOperatorNamespace}, newPod)).To(Succeed())
			Expect(newPod.Finalizers).ToNot(ContainElement(FinalizerName))
		})

		It("should reconcile a Succeeded Pod and remove the finalizer if the instaslice allocations are not present", func() {
			succeededPodName := "succeeded-pod"
			// Define a succeeded pod
			pod = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      succeededPodName,
					Namespace: InstaSliceOperatorNamespace,
					// PodUUID has to be same as the one present in the instaslice allocations
					// to process the allocations
					UID: types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					SchedulingGates: append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: GateName}),
				},
				Status: v1.PodStatus{Phase: v1.PodSucceeded, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())
			// reconcile request over the succeeded pod name
			req.Name = succeededPodName
			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			// As the allocations are present in the Instaslice Object, expecting a return from
			// the reconcile function without removing the Finalizer
			Expect(result).To(Equal(ctrl.Result{}))

			// Update the podUUID and observe the Finalizer is not present inside the Succeeded pod
			// as the corresponding allocation details are not present inside the Instaslice Allocations
			pod.UID = types.UID(succeededPodName)
			Expect(fakeClient.Update(ctx, pod)).To(Succeed())
			req.Name = succeededPodName
			result, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			newPod := &v1.Pod{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: succeededPodName, Namespace: InstaSliceOperatorNamespace}, newPod)).To(Succeed())
			Expect(newPod.Finalizers).ToNot(ContainElement(FinalizerName))
		})

		It("should return from reconcile when more than 1 container is present in a pod", func() {
			// Define a pod with more than a container
			pod = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-1",
					Namespace: InstaSliceOperatorNamespace,
					// PodUUID has to be same as the one present in the instaslice allocations
					// to process the allocations
					UID: types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					SchedulingGates: append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: GateName}),
					Containers:      []v1.Container{{Name: "test-container-1"}, {Name: "test-container-2"}},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())
			// reconcile request over the pod name
			req.Name = pod.Name
			result, err := r.Reconcile(ctx, req)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(multipleContainersUnsupportedErr))
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should handle a pod having the limits defined with a valid profile", func() {
			pod := &v1.Pod{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Pod",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-1",
					Namespace: InstaSliceOperatorNamespace,
					UID:       types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					RestartPolicy:   v1.RestartPolicyOnFailure,
					SchedulingGates: append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: GateName}),
					Containers: []v1.Container{
						{
							Name:  "vectoradd-cpu",
							Image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("500m"),
									v1.ResourceMemory: resource.MustParse("256Mi"),
								},
								// Define a valid profile
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
							Command: []string{
								"sh",
								"-c",
								"sleep 20",
							},
						},
					},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())
			// reconcile request over the pod name
			req.Name = pod.Name
			result, err := r.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Requeue).To(Equal(false))
		})

		It("should handle a pod having the limits and AllocationStatus set to Created", func() {
			pod := &v1.Pod{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Pod",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-1",
					Namespace: InstaSliceOperatorNamespace,
					UID:       types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					RestartPolicy:   v1.RestartPolicyOnFailure,
					SchedulingGates: append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: GateName}),
					Containers: []v1.Container{
						{
							Name:  "vectoradd-cpu",
							Image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("500m"),
									v1.ResourceMemory: resource.MustParse("256Mi"),
								},
								// Define a valid profile
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
							Command: []string{
								"sh",
								"-c",
								"sleep 20",
							},
						},
					},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())
			// Update the Instaslice object's Allocation status of the pod to AllocationStatusCreated

			currentSlice := &inferencev1alpha1.Instaslice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{
				Name:      "test-instaslice",
				Namespace: InstaSliceOperatorNamespace,
			}, currentSlice)).To(Succeed())

			currentSlice.Spec.PodAllocationRequests[types.UID(podUUID)] = inferencev1alpha1.AllocationRequest{
				Profile: "test-profile",
				PodRef: v1.ObjectReference{
					Name:      pod.Name,
					Namespace: InstaSliceOperatorNamespace,
					UID:       types.UID(podUUID),
				},
			}

			currentSlice.Status.PodAllocationResults[types.UID(podUUID)] = inferencev1alpha1.AllocationResult{
				AllocationStatus:            inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusCreated},
				GPUUUID:                     "fake-gpu-uuid",
				Nodename:                    "fake-node",
				ConfigMapResourceIdentifier: "fake-configmap-uid",
			}

			Expect(fakeClient.Update(ctx, currentSlice)).To(Succeed())
			req.Name = pod.Name
			result, err := r.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should handle a pod having the limits and no allocation details present in the Instaslice", func() {
			pod := &v1.Pod{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Pod",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-1",
					Namespace: InstaSliceOperatorNamespace,
					UID:       types.UID(podUUID),
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					RestartPolicy:   v1.RestartPolicyOnFailure,
					SchedulingGates: append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: GateName}),
					Containers: []v1.Container{
						{
							Name:  "vectoradd-cpu",
							Image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("500m"),
									v1.ResourceMemory: resource.MustParse("256Mi"),
								},
								// Define a valid profile
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
							Command: []string{
								"sh",
								"-c",
								"sleep 20",
							},
						},
					},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())
			// Update the instaslice with a unknown podUUID and expect Pod not to have allocations

			currentSlice := &inferencev1alpha1.Instaslice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{
				Name:      "test-instaslice",
				Namespace: InstaSliceOperatorNamespace,
			}, currentSlice)).To(Succeed())

			currentSlice.Spec.PodAllocationRequests[types.UID(podUUID)], currentSlice.Status.PodAllocationResults[types.UID(podUUID)] =
				inferencev1alpha1.AllocationRequest{
					Profile: "test-profile",
					PodRef: v1.ObjectReference{
						Name:      pod.Name,
						Namespace: InstaSliceOperatorNamespace,
						UID:       types.UID(podUUID),
					},
					Resources: v1.ResourceRequirements{},
				},
				inferencev1alpha1.AllocationResult{
					AllocationStatus:            inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusCreated},
					GPUUUID:                     "fake-gpu-uuid",
					Nodename:                    "fake-node",
					ConfigMapResourceIdentifier: "fake-configmap-uid",
				}

			Expect(fakeClient.Update(ctx, instaslice)).To(Succeed())
			req.Name = pod.Name
			result, err := r.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Requeue).To(Equal(false))
		})
	})
}

func TestInstasliceReconciler_extractGpuProfile(t *testing.T) {
	type fields struct {
		Client     client.Client
		Scheme     *runtime.Scheme
		kubeClient *kubernetes.Clientset
	}
	type args struct {
		instaslice  *inferencev1alpha1.Instaslice
		profileName string
	}
	scheme := runtime.NewScheme()
	newFields := fields{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme: scheme,
	}
	instaslice := new(inferencev1alpha1.Instaslice)

	instaslice.Status.NodeResources = inferencev1alpha1.DiscoveredNodeResources{
		MigPlacement: map[string]inferencev1alpha1.Mig{
			"1g.5gb": {
				Placements: []inferencev1alpha1.Placement{
					{Size: 1, Start: 0},
				},
				GIProfileID:    0,
				CIProfileID:    1,
				CIEngProfileID: 2,
			},
		},
	}

	newArgs := args{
		profileName: "1g.5gb",
		instaslice:  instaslice,
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int32
		want1  int32
		want2  int32
		want3  int32
	}{
		{"Test-case", newFields, newArgs, 1, 0, 1, 2},
	}
	config := config.ConfigFromEnvironment()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &InstasliceReconciler{
				Client:     tt.fields.Client,
				Scheme:     tt.fields.Scheme,
				kubeClient: tt.fields.kubeClient,
				Config:     config,
			}
			got, got1, got2, got3 := in.extractGpuProfile(tt.args.instaslice, tt.args.profileName)
			assert.Equalf(t, tt.want, got, "extractGpuProfile(%v, %v)", tt.args.instaslice, tt.args.profileName)
			assert.Equalf(t, tt.want1, got1, "extractGpuProfile(%v, %v)", tt.args.instaslice, tt.args.profileName)
			assert.Equalf(t, tt.want2, got2, "extractGpuProfile(%v, %v)", tt.args.instaslice, tt.args.profileName)
			assert.Equalf(t, tt.want3, got3, "extractGpuProfile(%v, %v)", tt.args.instaslice, tt.args.profileName)
		})
	}
}

func TestInstasliceReconciler_podMapFunc(t *testing.T) {
	type fields struct {
		Client     client.Client
		Scheme     *runtime.Scheme
		kubeClient *kubernetes.Clientset
	}
	type args struct {
		ctx context.Context
		obj client.Object
	}
	scheme := runtime.NewScheme()
	newFields := fields{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme: scheme,
	}
	instaslice := new(inferencev1alpha1.Instaslice)
	podUID := types.UID("pod-uuid")

	instaslice.Spec.PodAllocationRequests = map[types.UID]inferencev1alpha1.AllocationRequest{
		podUID: {
			Profile: "1g.5gb",
			PodRef: v1.ObjectReference{
				Name:      "test-pod",
				Namespace: InstaSliceOperatorNamespace,
				UID:       podUID,
			},
		},
	}
	instaslice.Status.PodAllocationResults = map[types.UID]inferencev1alpha1.AllocationResult{
		podUID: {
			AllocationStatus: inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusDeleted},
			Nodename:         "my-node",
			GPUUUID:          "GPU-abc123",
		},
	}

	newArgs := args{
		ctx: context.TODO(),
		obj: instaslice,
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []reconcile.Request
	}{
		{"test-case-1", newFields, newArgs, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: InstaSliceOperatorNamespace, Name: "test-pod"}}}},
		{"test-case-2", newFields, *new(args), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &InstasliceReconciler{
				Client:     tt.fields.Client,
				Scheme:     tt.fields.Scheme,
				kubeClient: tt.fields.kubeClient,
			}
			assert.Equalf(t, tt.want, r.podMapFunc(tt.args.ctx, tt.args.obj), "podMapFunc(%v, %v)", tt.args.ctx, tt.args.obj)
		})
	}
}

func TestFirstFitPolicy_SetAllocationDetails(t *testing.T) {
	type args struct {
		profileName                 string
		newStart                    int32
		size                        int32
		podUUID                     types.UID
		nodename                    types.NodeName
		allocationStatus            inferencev1alpha1.AllocationStatus
		discoveredGiprofile         int32
		Ciprofileid                 int32
		Ciengprofileid              int32
		namespace                   string
		podName                     string
		gpuUuid                     string
		resourceIdentifier          types.UID
		availableClassicalResources v1.ResourceList
	}

	newArgs := args{
		profileName:         "1g.5gb",
		newStart:            0,
		size:                1,
		podUUID:             types.UID("test-pod-uuid"),
		nodename:            types.NodeName("kind-control-plane"),
		allocationStatus:    inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusCreated},
		discoveredGiprofile: 0,
		Ciprofileid:         0,
		Ciengprofileid:      0,
		namespace:           "instaslice-operator",
		podName:             "test-pod",
		gpuUuid:             "A-100",
		resourceIdentifier:  types.UID("abcd-1234"),
		availableClassicalResources: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse("500m"),
			v1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}

	wantResult := inferencev1alpha1.AllocationResult{
		MigPlacement: inferencev1alpha1.Placement{
			Start: newArgs.newStart,
			Size:  newArgs.size,
		},
		GPUUUID:                     newArgs.gpuUuid,
		Nodename:                    newArgs.nodename,
		AllocationStatus:            inferencev1alpha1.AllocationStatus{AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusCreated},
		ConfigMapResourceIdentifier: newArgs.resourceIdentifier,
		Conditions:                  []metav1.Condition{},
	}

	tests := []struct {
		name string
		args args
		want *inferencev1alpha1.AllocationResult
	}{
		{"test-case", newArgs, &wantResult},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &FirstFitPolicy{}

			_, got := r.SetAllocationDetails(
				tt.args.profileName,
				tt.args.newStart,
				tt.args.size,
				tt.args.podUUID,
				tt.args.nodename,
				tt.args.allocationStatus,
				tt.args.discoveredGiprofile,
				tt.args.Ciprofileid,
				tt.args.Ciengprofileid,
				tt.args.namespace,
				tt.args.podName,
				tt.args.gpuUuid,
				tt.args.resourceIdentifier,
				tt.args.availableClassicalResources,
			)

			assert.Equalf(
				t,
				tt.want,
				got,
				"AllocationResult(%v, %v, %v, %v, %v, %v, %v, %v, %v, %v, %v, %v, %v, %v, %v)",
				tt.args.profileName, tt.args.newStart, tt.args.size, tt.args.podUUID, tt.args.nodename, tt.args.allocationStatus,
				tt.args.discoveredGiprofile, tt.args.Ciprofileid, tt.args.Ciengprofileid, tt.args.namespace,
				tt.args.podName, tt.args.gpuUuid, tt.args.resourceIdentifier, tt.args.availableClassicalResources,
			)
		})
	}
}

func TestGPUSort(t *testing.T) {
	Describe("SortGPUs", func() {
		It("should sort GPU UUIDs in ascending order", func() {
			instaslice := &inferencev1alpha1.Instaslice{
				Status: inferencev1alpha1.InstasliceStatus{
					NodeResources: inferencev1alpha1.DiscoveredNodeResources{
						NodeGPUs: []inferencev1alpha1.DiscoveredGPU{
							{GPUUUID: "gpu3", GPUName: "uuid3"},
							{GPUUUID: "gpu1", GPUName: "uuid1"},
							{GPUUUID: "gpu2", GPUName: "uuid2"},
						},
					},
				},
			}

			sortedUUIDs := sortGPUs(instaslice)
			expected := []string{"gpu1", "gpu2", "gpu3"}

			Expect(sortedUUIDs).To(Equal(expected))
		})
	})
}

func TestGetTotalFitForProfileOnGPU(t *testing.T) {
	var _ = Describe("GetTotalFitForProfileOnGPU", func() {
		tests := []struct {
			name          string
			instasliceObj *inferencev1alpha1.Instaslice
			profileName   string
			size          int32
			remaining     int32
			expectedFit   int32
		}{
			{
				name: "Zero remaining capacity",
				instasliceObj: &inferencev1alpha1.Instaslice{
					Status: inferencev1alpha1.InstasliceStatus{
						NodeResources: inferencev1alpha1.DiscoveredNodeResources{
							MigPlacement: map[string]inferencev1alpha1.Mig{},
						},
					},
				},
				profileName: "1g.10gb",
				remaining:   0,
				expectedFit: 0,
			},
			{
				name: "Profile not found",
				instasliceObj: &inferencev1alpha1.Instaslice{
					Status: inferencev1alpha1.InstasliceStatus{
						NodeResources: inferencev1alpha1.DiscoveredNodeResources{
							MigPlacement: map[string]inferencev1alpha1.Mig{},
						},
					},
				},
				profileName: "non-existent-profile",
				remaining:   10,
				expectedFit: 0,
			},
			{
				name: "Special case: 7g.40gb with exact fit",
				instasliceObj: &inferencev1alpha1.Instaslice{
					Status: inferencev1alpha1.InstasliceStatus{
						NodeResources: inferencev1alpha1.DiscoveredNodeResources{
							MigPlacement: map[string]inferencev1alpha1.Mig{
								"7g.40gb": {
									Placements: []inferencev1alpha1.Placement{{Size: 7}},
								},
							},
						},
					},
				},
				profileName: "7g.40gb",
				remaining:   7,
				expectedFit: 1,
			},
			{
				name: "Regular profile fitting multiple times",
				instasliceObj: &inferencev1alpha1.Instaslice{
					Status: inferencev1alpha1.InstasliceStatus{
						NodeResources: inferencev1alpha1.DiscoveredNodeResources{
							MigPlacement: map[string]inferencev1alpha1.Mig{
								"3g.20gb": {
									Placements: []inferencev1alpha1.Placement{{Size: 4}},
								},
							},
						},
					},
				},
				profileName: "3g.20gb",
				remaining:   9,
				expectedFit: 2,
			},
			{
				name: "Exact fit scenario",
				instasliceObj: &inferencev1alpha1.Instaslice{
					Status: inferencev1alpha1.InstasliceStatus{
						NodeResources: inferencev1alpha1.DiscoveredNodeResources{
							MigPlacement: map[string]inferencev1alpha1.Mig{
								"2g.10gb": {
									Placements: []inferencev1alpha1.Placement{{Size: 2}},
								},
							},
						},
					},
				},
				profileName: "2g.10gb",
				remaining:   4,
				expectedFit: 2,
			},
		}

		for _, tt := range tests {
			It(tt.name, func() {
				r := &InstasliceReconciler{}
				fit := r.getTotalFitForProfileOnGPU(tt.instasliceObj, tt.profileName, tt.size, tt.remaining)
				Expect(fit).To(Equal(tt.expectedFit))
			})
		}
	})
}

// Mock client that forces an update error
type FakeFailingClient struct {
	client.Client
	FailOnUpdate bool
}

func (f *FakeFailingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if f.FailOnUpdate {
		return fmt.Errorf("simulated update failure")
	}
	return f.Client.Update(ctx, obj, opts...)
}

var _ = Describe("Metrics Incrementation", func() {
	var (
		ctx        context.Context
		r          *InstasliceReconciler
		fakeClient client.Client
		instaslice *inferencev1alpha1.Instaslice
		pod        *v1.Pod
		podUUID    string
	)

	BeforeEach(func() {
		ctx = context.TODO()

		scheme := runtime.NewScheme()
		Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(v1.AddToScheme(scheme)).To(Succeed())

		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&inferencev1alpha1.Instaslice{}).
			Build()
		config := config.ConfigFromEnvironment()

		r = &InstasliceReconciler{
			Client: fakeClient,
			Config: config,
		}

		podUUID = "test-pod-uuid-2"

		pod = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: InstaSliceOperatorNamespace,
				UID:       types.UID(podUUID),
				Finalizers: []string{
					FinalizerName,
				},
			},
		}

		Expect(fakeClient.Create(ctx, pod)).To(Succeed())

		instaslice = &inferencev1alpha1.Instaslice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-instaslice",
				Namespace: InstaSliceOperatorNamespace,
			},
			Spec: inferencev1alpha1.InstasliceSpec{
				PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{
					types.UID(podUUID): {
						Profile: "test-profile",
						PodRef: v1.ObjectReference{
							Name:      pod.Name,
							Namespace: InstaSliceOperatorNamespace,
							UID:       pod.UID,
						},
						Resources: v1.ResourceRequirements{},
					},
				},
			},
			Status: inferencev1alpha1.InstasliceStatus{
				PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{
					types.UID(podUUID): {
						AllocationStatus:            inferencev1alpha1.AllocationStatus{AllocationStatusController: inferencev1alpha1.AllocationStatusCreating},
						GPUUUID:                     "GPU-12345",
						Nodename:                    "fake-node",
						ConfigMapResourceIdentifier: "fake-configmap-uid",
					},
				},
			},
		}

		Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
	})

	// Test for updateMetricsAllSlotsFree
	It("should update metrics with all slots free when no allocations exist", func() {
		instaslice.Spec.PodAllocationRequests = nil
		err := r.updateMetricsAllSlotsFree(ctx, inferencev1alpha1.InstasliceList{Items: []inferencev1alpha1.Instaslice{*instaslice}})
		Expect(err).ToNot(HaveOccurred(), "updateMetrics should not return an error")
		Expect(len(instaslice.Spec.PodAllocationRequests)).To(Equal(0))
	})

	// Test to prevent double metric incrementation
	It("should not increment metrics more than once for the same generation", func() {
		instaslice.Status.ObservedGeneration = instaslice.Generation // Metrics already processed

		err := r.IncrementTotalProcessedGpuSliceMetrics("node-1", "gpu-1", 4, 0, "1g.5gb")
		Expect(err).ToNot(HaveOccurred()) // Should skip incrementation
	})

	// Test for allocation change triggering metric reprocessing
	It("should trigger metric reprocessing when generation changes", func() {
		instaslice.Generation = 2 // Simulate spec change

		err := r.IncrementTotalProcessedGpuSliceMetrics("node-1", "gpu-1", 4, 0, "1g.5gb")
		Expect(err).ToNot(HaveOccurred())

		instaslice.Status.ObservedGeneration = instaslice.Generation
		Expect(instaslice.Status.ObservedGeneration).To(Equal(int64(2)))
	})

	// Validate allocation changes
	It("should correctly reflect allocation changes in metrics", func() {
		instaslice.Status.PodAllocationResults["alloc-1"] = inferencev1alpha1.AllocationResult{
			Nodename: "node-1",
			GPUUUID:  "gpu-1",
			MigPlacement: inferencev1alpha1.Placement{
				Size:  3,
				Start: 0,
			},
		}

		// Simulate initial metrics processing
		instaslice.Generation = 1 // Explicitly set generation
		err := r.updateMetrics(ctx, inferencev1alpha1.InstasliceList{Items: []inferencev1alpha1.Instaslice{*instaslice}})
		Expect(err).ToNot(HaveOccurred(), "updateMetrics should not return an error")

		// Ensure status is updated after processing
		instaslice.Status.ObservedGeneration = instaslice.Generation
		_ = r.Status().Update(ctx, instaslice)

		Expect(instaslice.Status.ObservedGeneration).To(Equal(int64(1)))

		// Check cleanup of incompatible profiles
		err = r.UpdateCompatibleProfilesMetrics(*instaslice, "node-1", map[string]int32{"gpu-1": 0})
		Expect(err).ToNot(HaveOccurred()) // Ensure the function runs without errors

		Expect(instasliceMetrics.compatibleProfiles.WithLabelValues("1g.5gb", "node-1")).NotTo(BeNil())
		Expect(instasliceMetrics.compatibleProfiles.WithLabelValues("2g.10gb", "node-1")).NotTo(BeNil())

		// Simulate spec update
		instaslice.Generation = 3
		err = r.updateMetrics(ctx, inferencev1alpha1.InstasliceList{Items: []inferencev1alpha1.Instaslice{*instaslice}})
		Expect(err).ToNot(HaveOccurred(), "updateMetrics should not return an error")

		// Update status again
		instaslice.Status.ObservedGeneration = instaslice.Generation
		_ = r.Status().Update(ctx, instaslice)

		Expect(instaslice.Status.ObservedGeneration).To(Equal(int64(3)))
	})
})
