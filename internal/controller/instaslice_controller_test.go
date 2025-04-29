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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

var _ = Describe("NodeReconciler", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		rNode      *NodeReconciler
		node       *v1.Node
		instaslice *inferencev1alpha1.Instaslice
		objs       []client.Object
	)

	BeforeEach(func() {
		ctx = context.TODO()
		scheme = runtime.NewScheme()
		_ = v1.AddToScheme(scheme)
		_ = inferencev1alpha1.AddToScheme(scheme)
		objs = []client.Object{}
	})

	newFakeClient := func() client.Client {
		return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	}

	Describe("NodeReconciler", func() {
		BeforeEach(func() {
			node = &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "fake-node",
					Labels: map[string]string{
						"nvidia.com/mig.capable": "true",
					},
				},
				Status: v1.NodeStatus{
					Capacity:    v1.ResourceList{},
					Allocatable: v1.ResourceList{},
					NodeInfo:    v1.NodeSystemInfo{BootID: "boot-123"},
				},
			}
			objs = append(objs, node)
		})

		It("should do nothing if node is labeled as managed", func() {
			node.Labels["instaslice.redhat.com/managed"] = "true"
			instaslice = &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: InstaSliceOperatorNamespace},
			}
			objs = append(objs, instaslice)

			rNode = &NodeReconciler{Client: newFakeClient()}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}}

			result, err := rNode.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Ensure the Instaslice CR still exists
			cr := &inferencev1alpha1.Instaslice{}
			err = rNode.Get(ctx, client.ObjectKey{Name: node.Name, Namespace: InstaSliceOperatorNamespace}, cr)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should delete Instaslice and cleanup node if managed label is missing", func() {
			instaslice = &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: InstaSliceOperatorNamespace},
			}
			objs = append(objs, instaslice)

			node.Status.Capacity["instaslice.redhat.com/resource1"] = resource.MustParse("5")
			node.Status.Allocatable["instaslice.redhat.com/resource1"] = resource.MustParse("5")

			rNode = &NodeReconciler{Client: newFakeClient()}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}}

			result, err := rNode.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify Instaslice CR was deleted
			cr := &inferencev1alpha1.Instaslice{}
			err = rNode.Get(ctx, client.ObjectKey{Name: node.Name, Namespace: InstaSliceOperatorNamespace}, cr)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			// Verify node resources were cleaned
			nodeUpdated := &v1.Node{}
			err = rNode.Get(ctx, client.ObjectKey{Name: node.Name}, nodeUpdated)
			Expect(err).ToNot(HaveOccurred())
			Expect(nodeUpdated.Status.Capacity).ToNot(HaveKey("instaslice.redhat.com/resource1"))
			Expect(nodeUpdated.Status.Allocatable).ToNot(HaveKey("instaslice.redhat.com/resource1"))
		})
	})
})

var _ = Describe("isDaemonSetPodReady", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		daemonSet  *appsv1.DaemonSet
		reconciler *InstasliceReconciler
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		_ = v1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)

		ctx = context.TODO()

		daemonSet = &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-ds",
				Namespace: "test-ns",
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "ds"},
				},
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "ds"},
					},
				},
			},
		}
	})

	It("should return false when no pods exist", func() {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(daemonSet).Build()
		reconciler = &InstasliceReconciler{Client: client}
		ready, err := reconciler.isDaemonSetPodReady(ctx, daemonSet)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeFalse())
	})

	It("should return false when pod exists but is not ready", func() {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "test-ns",
				Labels:    map[string]string{"app": "ds"},
			},
			Status: v1.PodStatus{
				Phase:             v1.PodRunning,
				ContainerStatuses: []v1.ContainerStatus{{Ready: false}},
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(daemonSet, pod).Build()
		reconciler = &InstasliceReconciler{Client: client}
		ready, err := reconciler.isDaemonSetPodReady(ctx, daemonSet)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeFalse())
	})

	It("should return true when pod exists and is ready", func() {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-2",
				Namespace: "test-ns",
				Labels:    map[string]string{"app": "ds"},
			},
			Status: v1.PodStatus{
				Phase:             v1.PodRunning,
				ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(daemonSet, pod).Build()
		reconciler = &InstasliceReconciler{Client: client}
		ready, err := reconciler.isDaemonSetPodReady(ctx, daemonSet)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue())
	})
})

var _ = Describe("ensureDaemonSetExists", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		client     client.Client
		reconciler *InstasliceReconciler
		daemonSet  *appsv1.DaemonSet
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		_ = appsv1.AddToScheme(scheme)
		ctx = context.TODO()
		daemonSet = &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      InstasliceDaemonsetName,
				Namespace: InstaSliceOperatorNamespace,
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "instaslice"},
				},
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "instaslice"},
					},
				},
			},
		}
	})

	It("should create DaemonSet if not found", func() {
		client = fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler = &InstasliceReconciler{
			Client:     client,
			createDSFn: func(ns string) *appsv1.DaemonSet { return daemonSet },
		}
		err := reconciler.ensureDaemonSetExists(ctx)
		Expect(err).NotTo(HaveOccurred())
		created := &appsv1.DaemonSet{}
		err = client.Get(ctx, types.NamespacedName{Name: InstasliceDaemonsetName, Namespace: InstaSliceOperatorNamespace}, created)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should skip creation if DaemonSet already exists", func() {
		client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(daemonSet).Build()
		reconciler = &InstasliceReconciler{
			Client:     client,
			createDSFn: func(ns string) *appsv1.DaemonSet { return daemonSet },
		}
		err := reconciler.ensureDaemonSetExists(ctx)
		Expect(err).NotTo(HaveOccurred())
	})
})

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

			podUUID = "test-pod-uuid-1"

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

			podUUID = "test-pod-uuid-3"
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
					NodeResources: inferencev1alpha1.DiscoveredNodeResources{BootID: "fake-boot-id"},
				},
			}
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: instaslice.Name,
				},
				Status: v1.NodeStatus{
					NodeInfo: v1.NodeSystemInfo{BootID: instaslice.Status.NodeResources.BootID},
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

			Expect(fakeClient.Create(ctx, node)).To(Succeed())
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
			ctx := context.TODO()
			scheme := runtime.NewScheme()
			_ = v1.AddToScheme(scheme)
			_ = inferencev1alpha1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			podUID := types.UID("test-pod-uid-1")

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: InstaSliceOperatorNamespace,
					UID:       podUID,
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					RestartPolicy:   v1.RestartPolicyOnFailure,
					SchedulingGates: []v1.PodSchedulingGate{{Name: GateName}},
					Containers: []v1.Container{{
						Name:  "vectoradd-cpu",
						Image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
							},
						},
					}},
				},
				Status: v1.PodStatus{Phase: v1.PodPending},
			}

			daemonSet := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      InstasliceDaemonsetName,
					Namespace: InstaSliceOperatorNamespace,
					Labels:    daemonSetlabel,
				},
			}
			daemonSetPod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod",
					Namespace: InstaSliceOperatorNamespace,
					Labels:    daemonSetlabel,
				},
				Status: v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{{Ready: true}}},
			}

			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: v1.NodeStatus{
					NodeInfo: v1.NodeSystemInfo{BootID: "bootid-123"},
				},
			}

			instaslice := &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-node",
					Namespace: InstaSliceOperatorNamespace,
				},
				Spec: inferencev1alpha1.InstasliceSpec{
					PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{},
				},
				Status: inferencev1alpha1.InstasliceStatus{
					NodeResources:        inferencev1alpha1.DiscoveredNodeResources{BootID: "bootid-123"},
					PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{},
				},
			}

			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				pod, daemonSet, daemonSetPod, node, instaslice,
			).Build()

			r := &InstasliceReconciler{Client: cl, Config: config.ConfigFromEnvironment()}

			req := ctrl.Request{NamespacedName: types.NamespacedName{
				Name:      "test-pod",
				Namespace: InstaSliceOperatorNamespace,
			}}

			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())
		})

		It("should handle a pod having the limits and AllocationStatus set to Created", func() {
			ctx := context.TODO()
			scheme := runtime.NewScheme()
			_ = v1.AddToScheme(scheme)
			_ = inferencev1alpha1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			podUID := types.UID("test-pod-uid-2")

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-created",
					Namespace: InstaSliceOperatorNamespace,
					UID:       podUID,
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					RestartPolicy:   v1.RestartPolicyOnFailure,
					SchedulingGates: []v1.PodSchedulingGate{{Name: GateName}},
					Containers: []v1.Container{{
						Name:  "vectoradd-cpu",
						Image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
							},
						},
					}},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}

			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: v1.NodeStatus{
					NodeInfo: v1.NodeSystemInfo{BootID: "bootid-456"},
				},
			}

			instaslice := &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-node",
					Namespace: InstaSliceOperatorNamespace,
				},
				Spec: inferencev1alpha1.InstasliceSpec{
					PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{
						podUID: {
							Profile: "test-profile",
							PodRef: v1.ObjectReference{
								Name:      "test-pod-created",
								Namespace: InstaSliceOperatorNamespace,
								UID:       podUID,
							},
						},
					},
				},
				Status: inferencev1alpha1.InstasliceStatus{
					NodeResources: inferencev1alpha1.DiscoveredNodeResources{
						BootID: "bootid-456",
					},
					PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{
						podUID: {
							AllocationStatus: inferencev1alpha1.AllocationStatus{
								AllocationStatusDaemonset: inferencev1alpha1.AllocationStatusCreated,
							},
							GPUUUID:                     "fake-gpu",
							Nodename:                    "test-node",
							ConfigMapResourceIdentifier: "configmap-uid",
						},
					},
				},
			}

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pod, node, instaslice).
				WithStatusSubresource(&inferencev1alpha1.Instaslice{}).
				Build()

			r := &InstasliceReconciler{Client: cl, Config: config.ConfigFromEnvironment()}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-pod-created", Namespace: InstaSliceOperatorNamespace}}
			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{Requeue: true}))

			// Validate Pod not modified inappropriately
			fetchedPod := &v1.Pod{}
			err = cl.Get(ctx, client.ObjectKey{Name: "test-pod-created", Namespace: InstaSliceOperatorNamespace}, fetchedPod)
			Expect(err).NotTo(HaveOccurred())
			Expect(fetchedPod.Finalizers).To(ContainElement(FinalizerName))
			Expect(fetchedPod.Spec.SchedulingGates).ToNot(ContainElement(v1.PodSchedulingGate{Name: GateName}))

			// Validate Instaslice status remains correct
			fetchedSlice := &inferencev1alpha1.Instaslice{}
			err = cl.Get(ctx, client.ObjectKey{Name: "test-node", Namespace: InstaSliceOperatorNamespace}, fetchedSlice)
			Expect(err).NotTo(HaveOccurred())
			resultStatus := fetchedSlice.Status.PodAllocationResults[podUID]
			Expect(resultStatus.AllocationStatus.AllocationStatusDaemonset).To(Equal(inferencev1alpha1.AllocationStatusCreated))
			Expect(resultStatus.GPUUUID).To(Equal("fake-gpu"))

		})

		It("should handle a pod having the limits and no allocation details present in the Instaslice", func() {
			ctx := context.TODO()
			scheme := runtime.NewScheme()
			_ = v1.AddToScheme(scheme)
			_ = inferencev1alpha1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			podUID := types.UID("test-pod-uid-3")

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-noalloc",
					Namespace: InstaSliceOperatorNamespace,
					UID:       podUID,
					Finalizers: []string{
						FinalizerName,
					},
				},
				Spec: v1.PodSpec{
					RestartPolicy:   v1.RestartPolicyOnFailure,
					SchedulingGates: []v1.PodSchedulingGate{{Name: GateName}},
					Containers: []v1.Container{{
						Name:  "vectoradd-cpu",
						Image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
							},
						},
					}},
				},
				Status: v1.PodStatus{Phase: v1.PodPending, Conditions: []v1.PodCondition{{Message: "blocked"}}},
			}

			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: v1.NodeStatus{
					NodeInfo: v1.NodeSystemInfo{BootID: "bootid-789"},
				},
			}

			instaslice := &inferencev1alpha1.Instaslice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-node",
					Namespace: InstaSliceOperatorNamespace,
				},
				Spec: inferencev1alpha1.InstasliceSpec{
					PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{},
				},
				Status: inferencev1alpha1.InstasliceStatus{
					NodeResources:        inferencev1alpha1.DiscoveredNodeResources{BootID: "bootid-789"},
					PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{},
				},
			}

			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, node, instaslice).Build()

			r := &InstasliceReconciler{Client: cl, Config: config.ConfigFromEnvironment()}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-pod-noalloc", Namespace: InstaSliceOperatorNamespace}}
			result, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(Equal(false))
			// Pod is untouched (gate not removed, finalizer intact)
			fetchedPod := &v1.Pod{}
			err = cl.Get(ctx, client.ObjectKey{Name: "test-pod-noalloc", Namespace: InstaSliceOperatorNamespace}, fetchedPod)
			Expect(err).ToNot(HaveOccurred())
			Expect(fetchedPod.Finalizers).To(ContainElement(FinalizerName))
			Expect(fetchedPod.Spec.SchedulingGates).To(ContainElement(v1.PodSchedulingGate{Name: GateName}))
			// Instaslice is untouched
			fetchedSlice := &inferencev1alpha1.Instaslice{}
			err = cl.Get(ctx, client.ObjectKey{Name: "test-node", Namespace: InstaSliceOperatorNamespace}, fetchedSlice)
			Expect(err).ToNot(HaveOccurred())
			Expect(fetchedSlice.Spec.PodAllocationRequests).To(BeEmpty())
			Expect(fetchedSlice.Status.PodAllocationResults).To(BeEmpty())

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
		podUUID:             types.UID("test-pod-uuid-4"),
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

// Test `sortGPUs`
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

// Test `getStartIndexFromPreparedState`
func TestGetStartIndexFromPreparedState(t *testing.T) {
	Describe("getStartIndexFromPreparedState", func() {
		It("should return the correct start index for a profile", func() {
			instaslice := &inferencev1alpha1.Instaslice{
				Status: inferencev1alpha1.InstasliceStatus{
					NodeResources: inferencev1alpha1.DiscoveredNodeResources{
						MigPlacement: map[string]inferencev1alpha1.Mig{
							"2g.10gb": {
								Placements: []inferencev1alpha1.Placement{{Size: 2, Start: 1}},
							},
						},
					},
				},
			}
			gpuAllocatedIndex := [8]int32{1, 0, 0, 0, 0, 0, 0, 0}
			Expect((&InstasliceReconciler{}).getStartIndexFromAllocationResults(instaslice, "2g.10gb", gpuAllocatedIndex, nil, true)).To(Equal(int32(1)))
		})
	})
}

// Test `calculateProfileFitOnGPU`
var _ = Describe("calculateProfileFitOnGPU", func() {
	var (
		ctx        context.Context
		fakeClient client.Client
		r          *InstasliceReconciler
		instaslice *inferencev1alpha1.Instaslice
		pod        *v1.Pod
		podUUID    string
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme := runtime.NewScheme()
		Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(v1.AddToScheme(scheme)).To(Succeed())

		fakeClient = fake.NewClientBuilder().WithScheme(scheme).Build()
		r = &InstasliceReconciler{
			Client: fakeClient,
		}
		podUUID = "test-pod-uuid-6"
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
				Namespace: "instaslice-operator",
			},
			Status: inferencev1alpha1.InstasliceStatus{
				NodeResources: inferencev1alpha1.DiscoveredNodeResources{
					MigPlacement: map[string]inferencev1alpha1.Mig{
						"4g.20gb": {
							Placements: []inferencev1alpha1.Placement{{Size: 4, Start: 0}},
						},
					},
				},
			},
		}

		Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
	})

	It("should return the correct actual slice size allocated", func() {
		actualSize, err := r.calculateProfileFitOnGPU(instaslice, "4g.20gb", "gpu-1", false, pod)
		Expect(err).ToNot(HaveOccurred())
		Expect(actualSize).To(Equal(int32(4)))
	})
})

// Test `calculateProfileFitOnGPU for slices fit simulation`
func TestGetTotalFitForProfileOnGPU(t *testing.T) {
	var _ = Describe("GetTotalFitForProfileOnGPU", func() {
		tests := []struct {
			name          string
			instasliceObj *inferencev1alpha1.Instaslice
			profileName   string
			gpuuid        string
			remaining     int32
			expectedFit   int32
		}{
			{
				name: "Zero remaining capacity",
				instasliceObj: &inferencev1alpha1.Instaslice{
					Status: inferencev1alpha1.InstasliceStatus{
						NodeResources: inferencev1alpha1.DiscoveredNodeResources{
							MigPlacement: map[string]inferencev1alpha1.Mig{
								"1g.10gb": {
									Placements: []inferencev1alpha1.Placement{},
								},
							},
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
				expectedFit: 0,
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
				expectedFit: 1,
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
				expectedFit: 1,
			},
		}
		for _, tt := range tests {
			It(tt.name, func() {
				r := &InstasliceReconciler{}
				fit, err := r.calculateProfileFitOnGPU(tt.instasliceObj, tt.profileName, tt.gpuuid, true, nil)

				if tt.name == "Profile not found" || tt.name == "Zero remaining capacity" {
					Expect(err).To(HaveOccurred(), "Expected error for missing profile or empty placement")
				} else {
					Expect(err).ToNot(HaveOccurred(), "calculateProfileFitOnGPU should not return an error")
				}
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
				NodeResources: inferencev1alpha1.DiscoveredNodeResources{
					MigPlacement: map[string]inferencev1alpha1.Mig{
						"1g.5gb": {
							Placements: []inferencev1alpha1.Placement{
								{Size: 1, Start: 0}, // Example placement
							},
						},
						"1g.10gb": { // Also add the missing profile from the failing test
							Placements: []inferencev1alpha1.Placement{
								{Size: 2, Start: 0},
							},
						},
					},
				},
			},
		}

		Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
	})

	// Test to prevent double metric incrementation
	It("should not increment metrics more than once", func() {

		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "gpu-1", "1g.5gb", 1)
		Expect(instasliceMetrics.processedSlices.WithLabelValues("node-1", "gpu-1")).NotTo(BeNil())
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

		// Check cleanup of incompatible profiles
		r.UpdateCompatibleProfilesMetrics(*instaslice, "node-1")

		Expect(instasliceMetrics.compatibleProfiles.WithLabelValues("1g.5gb", "node-1")).NotTo(BeNil())
		Expect(instasliceMetrics.compatibleProfiles.WithLabelValues("2g.10gb", "node-1")).NotTo(BeNil())
	})
})
