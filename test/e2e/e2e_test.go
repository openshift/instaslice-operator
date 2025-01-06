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

package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller"
	"github.com/openshift/instaslice-operator/internal/controller/daemonset"
	"github.com/openshift/instaslice-operator/test/e2e/resources"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"k8s.io/client-go/kubernetes/scheme"

	"k8s.io/client-go/rest"
	//+kubebuilder:scaffold:imports
)

var (
	criBin                 = "docker"
	kubectlBin             = "kubectl"
	namespace              = "instaslice-system"
	emulated               = false
	nodeName               = "kind-e2e-control-plane"
	controllerManagerLabel = map[string]string{
		"control-plane": "controller-manager",
	}

	controllerImage string
	daemonsetImage  string
	ctx             context.Context

	instasliceObj inferencev1alpha1.Instaslice

	cfg       *rest.Config
	k8sClient client.Client
)

type TemplateVars struct {
	NodeName string
}

var templateVars TemplateVars

func init() {
	if env := os.Getenv("KIND_NAME"); env != "" {
		nodeName = fmt.Sprintf("%v-control-plane", env)
	}
	if env := os.Getenv("IMG"); env != "" {
		controllerImage = env
	}
	if env := os.Getenv("IMG_DMST"); env != "" {
		daemonsetImage = env
	}
	if env := os.Getenv("EMULATOR_MODE"); env != "" {
		emulated = true
	}
	if env := os.Getenv("CRI_BIN"); env != "" {
		criBin = env
	}
	if env := os.Getenv("KUBECTL_BIN"); env != "" {
		kubectlBin = env
	}

}

var _ = BeforeSuite(func() {
	var err error

	cfg, err = config.GetConfig()
	Expect(err).NotTo(HaveOccurred(), "Failed to get Kubernetes config")
	Expect(cfg).NotTo(BeNil())

	err = inferencev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	ctx = context.TODO()

	node, err := getNodeName(controllerManagerLabel)
	Expect(err).NotTo(HaveOccurred())
	if node != "" && err == nil {
		nodeName = node
		templateVars.NodeName = node
	} else {
		templateVars.NodeName = nodeName
	}

	GinkgoWriter.Printf("cri-bin: %v\n", criBin)
	GinkgoWriter.Printf("kubectl-bin: %v\n", kubectlBin)
	GinkgoWriter.Printf("namespace: %v\n", namespace)
	GinkgoWriter.Printf("emulated: %v\n", emulated)
	GinkgoWriter.Printf("node-name: %v\n", nodeName)
	GinkgoWriter.Printf("controller-image: %v\n", controllerImage)
	GinkgoWriter.Printf("daemonset-image: %v\n", daemonsetImage)
})

// TODO: add more test cases -
// 1. delete instaslice object, fill the object with dangling slices ie no capacity available and
// verify that allocation should not exists in instaslice object.
// 2. check size and index value based on different mig slice profiles requested.
// 3. submit 3 pods with 3g.20gb slice and verify that two allocations exists in instaslice object.
// 4. submit a test pod with 1g.20gb slice and later delete it. verify the allocation status to be
// in state deleting
var _ = Describe("controller", Ordered, func() {
	BeforeEach(func() {
		err := k8sClient.Create(ctx, resources.GenerateFakeCapacity(templateVars.NodeName))
		Expect(err).NotTo(HaveOccurred(), "Failed to create instaslice object")

		err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
		Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice resource")

		timeout := 2 * time.Minute
		pollInterval := 5 * time.Second

		daemonSet := &appsv1.DaemonSet{}

		Eventually(func() error {
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: controller.InstaSliceOperatorNamespace,
				Name: controller.InstasliceDaemonsetName}, daemonSet)
			if err != nil {
				return fmt.Errorf("failed to get DaemonSet: %v", err)
			}

			if daemonSet.Status.DesiredNumberScheduled != daemonSet.Status.NumberReady {
				return fmt.Errorf("DaemonSet not ready, desired: %d, ready: %d",
					daemonSet.Status.DesiredNumberScheduled, daemonSet.Status.NumberReady)
			}

			return nil
		}, timeout, pollInterval).Should(Succeed(), "DaemonSet rollout status check failed")
	})

	Context("Operator", func() {
		It("should create a pod with no requests and check if finalizer exists", func() {
			pod := resources.GetVectorAddFinalizerPod()
			err := k8sClient.Create(ctx, pod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			})

			Eventually(func() error {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, pod)
				if err != nil {
					return err
				}

				for _, finalizer := range pod.ObjectMeta.Finalizers {
					if finalizer == controller.FinalizerName {
						return nil // Finalizer found
					}
				}

				return fmt.Errorf("finalizer %s not found on Pod %s", controller.FinalizerName, pod.Name)
			}, time.Minute, 5*time.Second).Should(Succeed(), "Failed to verify finalizer on Pod")
		})
		It("should create a pod with no requests and check the allocation in instaslice object", func() {
			pod := resources.GetVectorAddNoReqPod()
			err := k8sClient.Create(ctx, pod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			})

			Eventually(func() error {
				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == pod.Name {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %+v ", pod)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should create a pod with small requests and check the allocation in instaslice object", func() {
			pod := resources.GetVectorAddSmallReqPod()
			err := k8sClient.Create(ctx, pod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			})

			Eventually(func() error {
				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == pod.Name {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %+v ", pod)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should create a pod with large memory requests and check the allocation in instaslice object", func() {
			pod := resources.GetVectorAddLargeMemPod()
			err := k8sClient.Create(ctx, pod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			})
			Consistently(func() error {
				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == pod.Name {
						return fmt.Errorf("PodName %s found in allocations", pod.Name)
					}
				}
				return nil
			}, 1*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should create a pod with large cpu requests and check the allocation in instaslice object", func() {
			pod := resources.GetVectorAddLargeCPUPod()
			err := k8sClient.Create(ctx, pod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			})

			Consistently(func() error {
				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == pod.Name {
						return fmt.Errorf("PodName %s found in allocations", pod.Name)
					}
				}
				return nil
			}, 1*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should create a deployment and check the allocation in instaslice object", func() {
			deployment := resources.GetSleepDeployment()
			err := k8sClient.Create(ctx, deployment)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the deployment")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, deployment)
				if err != nil {
					log.Printf("Error deleting the deployment %+v: %+v", deployment, err)
				}
			})

			Eventually(func() error {
				podList := &corev1.PodList{}
				labelSelector := client.MatchingLabels{"app": "sleep-app"}
				err := k8sClient.List(ctx, podList, client.InNamespace(deployment.Namespace), labelSelector)
				if err != nil {
					return err
				}

				if len(podList.Items) == 0 {
					return fmt.Errorf("no pods found for deployment %s", deployment.Name)
				}

				podName := podList.Items[0].Name

				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == podName {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %s", podName)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should create a statefulset and check the allocation in instaslice object", func() {
			statefulSet := resources.GetSleepStatefulSet()
			err := k8sClient.Create(ctx, statefulSet)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the statefulSet")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, statefulSet)
				if err != nil {
					log.Printf("Error deleting the statefulSet %+v: %+v", statefulSet, err)
				}
			})

			Eventually(func() error {
				podList := &corev1.PodList{}
				labelSelector := client.MatchingLabels{"app": "sleep-stateful"}
				err := k8sClient.List(ctx, podList, client.InNamespace(statefulSet.Namespace), labelSelector)
				if err != nil {
					return err
				}

				if len(podList.Items) == 0 {
					return fmt.Errorf("no pods found for statefulSet %s", statefulSet.Name)
				}

				podName := podList.Items[0].Name

				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == podName {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %s", podName)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should create a job and check the allocation in instaslice object", func() {
			job := resources.GetSleepJob()
			err := k8sClient.Create(ctx, job)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the job")

			DeferCleanup(func() {
				propagationPolicy := metav1.DeletePropagationForeground
				err = k8sClient.Delete(ctx, job, &client.DeleteOptions{
					PropagationPolicy: &propagationPolicy,
				})
				if err != nil {
					log.Printf("Error deleting the job %+v: %+v", job, err)
				}
			})

			Eventually(func() error {
				podList := &corev1.PodList{}
				labelSelector := client.MatchingLabels{"app": "sleep-job"}
				err := k8sClient.List(ctx, podList, client.InNamespace(job.Namespace), labelSelector)
				if err != nil {
					return err
				}

				if len(podList.Items) == 0 {
					return fmt.Errorf("no pods found for job %s", job.Name)
				}

				podName := podList.Items[0].Name

				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
				if err != nil {
					return err
				}
				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.PodName == podName {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %s", podName)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should verify all MIG slice capacities are as expected before submitting pods", func() {
			err := k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeName, Namespace: namespace}, &instasliceObj)
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve Instaslice object")

			expectedCapacities := map[string]int{
				"instaslice.redhat.com/mig-1g.5gb":    len(instasliceObj.Spec.MigGPUUUID) * 7,
				"instaslice.redhat.com/mig-1g.10gb":   len(instasliceObj.Spec.MigGPUUUID) * 4,
				"instaslice.redhat.com/mig-1g.5gb+me": len(instasliceObj.Spec.MigGPUUUID) * 7,
				"instaslice.redhat.com/mig-2g.10gb":   len(instasliceObj.Spec.MigGPUUUID) * 3,
				"instaslice.redhat.com/mig-3g.20gb":   len(instasliceObj.Spec.MigGPUUUID) * 2,
				"instaslice.redhat.com/mig-4g.20gb":   len(instasliceObj.Spec.MigGPUUUID) * 1,
				"instaslice.redhat.com/mig-7g.40gb":   len(instasliceObj.Spec.MigGPUUUID) * 1,
			}

			node := &corev1.Node{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeName}, node)
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve the node object")

			validateMIGCapacity := func(sliceType string, expectedCapacity int) error {
				migCapacity, found := node.Status.Capacity[corev1.ResourceName(sliceType)]
				if !found {
					return fmt.Errorf("MIG capacity '%s' not found on node %s", sliceType, templateVars.NodeName)
				}

				actualCapacity, parsed := migCapacity.AsInt64()
				if !parsed {
					return fmt.Errorf("failed to parse MIG capacity value %s for slice %s", migCapacity.String(), sliceType)
				}

				if actualCapacity != int64(expectedCapacity) {
					return fmt.Errorf("expected MIG capacity %d for slice %s, but got %d", expectedCapacity, sliceType, actualCapacity)
				}
				return nil
			}

			for sliceType, expectedCapacity := range expectedCapacities {
				Expect(validateMIGCapacity(sliceType, expectedCapacity)).To(Succeed(), fmt.Sprintf("MIG capacity validation failed for %s", sliceType))
			}
		})
		It("should verify the existence of pod allocations", func() {
			pods := resources.GetMultiPods()
			for _, pod := range pods {
				err := k8sClient.Create(ctx, pod)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create pod %s", pod.Name))
			}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeName, Namespace: namespace}, &instasliceObj)
				if err != nil {
					fmt.Printf("Failed to get Instaslice object: %v\n", err)
					return false
				}

				var assignedGPUUUID string
				allAssignedToOneGPU := true

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.Allocationstatus != inferencev1alpha1.AllocationStatusUngated {
						return false
					}

					// Since we are requesting 7 slices of type mig-1g.5gb, all 7 pods must be
					// assigned to the same GPU
					if assignedGPUUUID == "" {
						assignedGPUUUID = allocation.GPUUUID
					} else if allocation.GPUUUID != assignedGPUUUID {
						allAssignedToOneGPU = false
						break
					}
				}

				return allAssignedToOneGPU
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Not all allocations are in the 'ungated' state after the timeout")

			for _, pod := range pods {
				err := k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeName, Namespace: namespace}, &instasliceObj)
				if err != nil {
					fmt.Printf("Failed to get Instaslice object: %v\n", err)
					return false
				}

				return len(instasliceObj.Spec.Allocations) == 0
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Expected Instaslice object Allocations to be empty")
		})
		It("should verify that the Kubernetes node has the specified resource and matches total GPU memory", func() {
			Expect(len(instasliceObj.Spec.MigGPUUUID)).To(Equal(2))
			totalMemoryGB := daemonset.CalculateTotalMemoryGB(instasliceObj.Spec.MigGPUUUID)

			By(fmt.Sprintf("Verifying that node has custom resource %s", controller.QuotaResourceName))
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeName}, node)
			Expect(err).NotTo(HaveOccurred(), "Failed to get the node")

			acceleratorMemory, exists := node.Status.Capacity[corev1.ResourceName(controller.QuotaResourceName)]
			Expect(exists).To(BeTrue(), fmt.Sprintf("%s not found in Node object", controller.QuotaResourceName))

			Expect(acceleratorMemory.Value()/(1024*1024*1024)).To(Equal(int64(totalMemoryGB)),
				fmt.Sprintf("%s on node does not match total GPU memory in Instaslice object", controller.QuotaResourceName))
		})
		It("should create a pod with small requests and check the allocation state in instaslice object", func() {
			pod := resources.GetVectorAddSmallReqPod()
			err := k8sClient.Create(ctx, pod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the pod")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			})

			observedStatuses := []string{}
			Eventually(func() string {
				Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)).To(Succeed())

				allocation, _ := instasliceObj.Spec.Allocations[string(pod.UID)]
				currentStatus := string(allocation.Allocationstatus)
				if currentStatus != "" {
					if len(observedStatuses) == 0 || (observedStatuses[len(observedStatuses)-1] != currentStatus) {
						observedStatuses = append(observedStatuses, currentStatus)
					}
				}
				return currentStatus
			}, time.Minute, time.Millisecond*5).Should(Equal("ungated"))
			Expect(observedStatuses).To(Equal([]string{"creating", "created", "ungated"}))
		})
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &instasliceObj)
	})
})

func getNodeName(label map[string]string) (string, error) {

	var nodeName string

	podList := &corev1.PodList{}
	err := k8sClient.List(ctx, podList, client.MatchingLabels(label))
	if err != nil {
		return "", fmt.Errorf("unable to list pods: %v", err)
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != "" {
			nodeName = pod.Spec.NodeName
			return nodeName, nil
		}
	}

	return "", fmt.Errorf("no node name found for pods with label: %v", label)
}
