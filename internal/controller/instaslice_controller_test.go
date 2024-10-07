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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
)

func TestChangesAllocationDeletionndFinalizer(t *testing.T) {
	var _ = Describe("InstasliceReconciler processInstasliceAllocation", func() {
		var (
			ctx                 context.Context
			r                   *InstasliceReconciler
			fakeClient          client.Client
			instaslice          *inferencev1alpha1.Instaslice
			pod                 *v1.Pod
			podUUID             string
			req                 ctrl.Request
			instasliceNamespace string
		)

		BeforeEach(func() {
			ctx = context.TODO()

			scheme := runtime.NewScheme()
			Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(v1.AddToScheme(scheme)).To(Succeed())

			fakeClient = fake.NewClientBuilder().WithScheme(scheme).Build()

			r = &InstasliceReconciler{
				Client: fakeClient,
			}

			instasliceNamespace = "default"
			podUUID = "test-pod-uuid"

			instaslice = &inferencev1alpha1.Instaslice{
				Spec: inferencev1alpha1.InstasliceSpec{
					Allocations: map[string]inferencev1alpha1.AllocationDetails{
						podUUID: {
							PodUUID:          podUUID,
							PodName:          "test-pod",
							Allocationstatus: inferencev1alpha1.AllocationStatusCreating,
						},
					},
				},
			}
			instaslice.Name = "test-instaslice"
			instaslice.Namespace = instasliceNamespace
			pod = &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: instasliceNamespace,
					UID:       types.UID(podUUID),
					Finalizers: []string{
						finalizerOrGateName,
					},
				},
			}

			Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
			Expect(fakeClient.Create(ctx, pod)).To(Succeed())

			req = ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-pod",
					Namespace: instasliceNamespace,
				},
			}
		})

		It("should delete instaslice allocation when allocation status is Deleted", func() {
			instaslice.Spec.Allocations[podUUID] = inferencev1alpha1.AllocationDetails{
				PodUUID:          podUUID,
				PodName:          "test-pod",
				Allocationstatus: inferencev1alpha1.AllocationStatusDeleted,
			}
			Expect(fakeClient.Update(ctx, instaslice)).To(Succeed())

			result, err := r.deleteInstasliceAllocation(ctx, instaslice.Name, instaslice.Spec.Allocations[podUUID])

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedInstaSlice := &inferencev1alpha1.Instaslice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: instaslice.Name, Namespace: instasliceNamespace}, updatedInstaSlice)).To(Succeed())
			_, allocationExists := updatedInstaSlice.Spec.Allocations[podUUID]
			Expect(allocationExists).To(BeFalse())
		})

		It("should remove finalizer after allocation is deleted", func() {
			instaslice.Spec.Allocations[podUUID] = inferencev1alpha1.AllocationDetails{
				PodUUID:          podUUID,
				PodName:          "test-pod",
				Allocationstatus: inferencev1alpha1.AllocationStatusDeleted,
			}
			Expect(fakeClient.Update(ctx, instaslice)).To(Succeed())

			result, err := r.removeInstaSliceFinalizer(ctx, req)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPod := &v1.Pod{}
			Expect(fakeClient.Get(ctx, req.NamespacedName, updatedPod)).To(Succeed())
			Expect(updatedPod.Finalizers).NotTo(ContainElement(finalizerOrGateName))
		})

		It("should set allocation status to Deleting if status is not Deleted", func() {
			result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, podUUID, instaslice.Spec.Allocations[podUUID], req)

			Expect(err).NotTo(HaveOccurred())

			updatedInstaSlice := &inferencev1alpha1.Instaslice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: instaslice.Name, Namespace: instasliceNamespace}, updatedInstaSlice)).To(Succeed())

			Expect(updatedInstaSlice.Spec.Allocations[podUUID].Allocationstatus).To(Equal(inferencev1alpha1.AllocationStatusDeleting))

			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should requeue if there is an error updating the instaslice", func() {
			r.Client = fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

			result, err := r.setInstasliceAllocationToDeleting(ctx, instaslice.Name, podUUID, instaslice.Spec.Allocations[podUUID], req)

			Expect(err).To(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())
		})
	})

}
