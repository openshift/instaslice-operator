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
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller"

	"github.com/openshift/instaslice-operator/internal/controller/daemonset"
	"github.com/openshift/instaslice-operator/test/e2e/resources"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
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

	instasliceObjs *inferencev1alpha1.InstasliceList

	cfg       *rest.Config
	k8sClient client.Client
	clientSet *kubernetes.Clientset
)

const (
	instasliceMetricSvc      = "instaslice-operator-controller-manager-metrics-service"
	instasliceServiceAccount = "instaslice-operator-controller-manager"
)

type TemplateVars struct {
	NodeNames []string
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
	switch strings.ToLower(os.Getenv("EMULATOR_MODE")) {
	case "true":
		emulated = true
	case "false":
		emulated = false
	default:
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

	clientSet, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(clientSet).NotTo(BeNil())

	ctx = context.TODO()
	instasliceObjs = &inferencev1alpha1.InstasliceList{}

	nodeNames, err := getNodeNames(controllerManagerLabel)
	Expect(err).NotTo(HaveOccurred())
	if len(nodeNames) > 0 && err == nil {
		templateVars.NodeNames = nodeNames
	} else {
		templateVars.NodeNames = []string{nodeNames[0]}
	}

	GinkgoWriter.Printf("cri-bin: %v\n", criBin)
	GinkgoWriter.Printf("kubectl-bin: %v\n", kubectlBin)
	GinkgoWriter.Printf("namespace: %v\n", namespace)
	GinkgoWriter.Printf("emulated: %v\n", emulated)
	GinkgoWriter.Printf("node names: %v\n", templateVars.NodeNames)
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
		err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
		Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice resource")

		timeout := 2 * time.Minute
		pollInterval := 5 * time.Second

		daemonSet := &appsv1.DaemonSet{}

		Eventually(func() error {
			err := k8sClient.Get(ctx, client.ObjectKey{
				Namespace: controller.InstaSliceOperatorNamespace,
				Name:      controller.InstasliceDaemonsetName,
			}, daemonSet)
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
		It("should ensure the metrics endpoint is serving metrics", func() {
			By("validating that the metrics service is available")
			var svc corev1.Service
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instasliceMetricSvc}, &svc)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token := serviceAccountToken()
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				var endPoints corev1.Endpoints
				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instasliceMetricSvc}, &endPoints)
				g.Expect(err).NotTo(HaveOccurred())
				if len(endPoints.Subsets) != 0 {
					g.Expect(endPoints.Subsets[0].String()).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
				}
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			metricsPod := resources.GetMetricPod(token)
			err = k8sClient.Create(ctx, metricsPod)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				var pod corev1.Pod
				err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: metricsPod.Name}, &pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(pod.Status.Phase)).To(Equal("Succeeded"), "Metrics pod status not matched")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
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
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{
					Namespace: namespace,
				})
				if err != nil {
					return err
				}

				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
						return nil
					}
				}

				return fmt.Errorf("No valid allocation result found for the pod %q in namespace %q", pod.Name, pod.Namespace)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid PodAllocationResult")
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
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					return err
				}
				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
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
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					return err
				}

				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
						return fmt.Errorf("GPU allocation found for the pod %+v", pod)
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
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					return err
				}

				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
						return fmt.Errorf("GPU allocation found for the pod %+v", pod)
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

				pod := podList.Items[0]

				err = k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					return err
				}

				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
						return nil
					}
				}

				return fmt.Errorf("No valid allocation found for the pod %s", pod.Name)
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

				pod := podList.Items[0]

				err = k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					return err
				}

				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %s", pod.Name)
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

				pod := podList.Items[0]

				err = k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					return err
				}

				for _, instaslice := range instasliceObjs.Items {
					podAllocationResult := instaslice.Status.PodAllocationResults[pod.UID]
					if podAllocationResult.GPUUUID != "" {
						return nil
					}
				}
				return fmt.Errorf("No valid allocation found for the pod %s", pod.Name)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Expected Instaslice object with valid allocations")
		})
		It("should verify all MIG slice capacities are as expected before submitting pods", func() {
			err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve Instaslice object")

			for _, instasliceObj := range instasliceObjs.Items {
				numGPUs := len(instasliceObj.Status.NodeResources.NodeGPUs)

				expectedCapacities := map[string]int{
					"instaslice.redhat.com/mig-1g.5gb":    numGPUs * 7,
					"instaslice.redhat.com/mig-1g.10gb":   numGPUs * 4,
					"instaslice.redhat.com/mig-1g.5gb+me": numGPUs * 7,
					"instaslice.redhat.com/mig-2g.10gb":   numGPUs * 3,
					"instaslice.redhat.com/mig-3g.20gb":   numGPUs * 2,
					"instaslice.redhat.com/mig-4g.20gb":   numGPUs * 1,
					"instaslice.redhat.com/mig-7g.40gb":   numGPUs * 1,
				}

				node := &corev1.Node{}
				err = k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeNames[0]}, node)
				Expect(err).NotTo(HaveOccurred(), "Failed to retrieve the node object")

				validateMIGCapacity := func(sliceType string, expectedCapacity int) error {
					migCapacity, found := node.Status.Capacity[corev1.ResourceName(sliceType)]
					if !found {
						return fmt.Errorf("MIG capacity '%s' not found on node %s", sliceType, templateVars.NodeNames[0])
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
			}
		})
		It("should verify the existence of pod allocations", func() {
			Eventually(func() bool {
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					fmt.Printf("Failed to get Instaslice object: %v\n", err)
					return false
				}

				for _, instasliceObj := range instasliceObjs.Items {
					if len(instasliceObj.Status.PodAllocationResults) != 0 {
						return false
					}
				}
				return true
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Expected Instaslice object Allocations to be empty")

			pods := resources.GetMultiPods()
			for _, pod := range pods {
				err := k8sClient.Create(ctx, pod)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create pod %s", pod.Name))
			}

			Eventually(func() bool {
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					fmt.Printf("Failed to get Instaslice object: %v\n", err)
					return false
				}

				uniqueAllocationResults := make(map[*inferencev1alpha1.AllocationResult]struct{})
				uniqueAllocatedGUUID := make(map[string]struct{})

				for _, instasliceObj := range instasliceObjs.Items {
					for _, allocation := range instasliceObj.Status.PodAllocationResults {
						if allocation.AllocationStatus.AllocationStatusController == inferencev1alpha1.AllocationStatusUngated {
							uniqueAllocationResults[&allocation] = struct{}{}
							uniqueAllocatedGUUID[allocation.GPUUUID] = struct{}{}
						}
					}
				}

				if len(uniqueAllocationResults) == len(pods) && len(uniqueAllocatedGUUID) == 1 {
					return true
				}

				return false
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Not all allocations are in the 'ungated' state after the timeout")

			for _, pod := range pods {
				err := k8sClient.Delete(ctx, pod)
				if err != nil {
					log.Printf("Error deleting the pod %+v: %+v", pod, err)
				}
			}

			Eventually(func() bool {
				err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
				if err != nil {
					fmt.Printf("Failed to get Instaslice object: %v\n", err)
					return false
				}

				for _, instasliceObj := range instasliceObjs.Items {
					if len(instasliceObj.Status.PodAllocationResults) != 0 {
						return false
					}
				}
				return true
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Expected Instaslice object Allocations to be empty")
		})
		It("should verify that the Kubernetes node has the specified resource and matches total GPU memory", func() {
			var totalMemoryGB float64
			err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve Instaslice object")
			node := &corev1.Node{}
			err = k8sClient.Get(ctx, client.ObjectKey{Name: templateVars.NodeNames[0]}, node)
			if emulated {
				// Here, we compare the accelerator memory with the memory fetched from parsing the GPU name
				// Ex: Parsing the "NVIDIA A100-SXM4-40GB" GPU results in 40GB
				// This gets compared with the memory that the daemonset patches the node
				Expect(len(instasliceObjs.Items[0].Status.NodeResources.NodeGPUs)).To(Equal(2))
				for _, instasliceObj := range instasliceObjs.Items {
					memoryGB, err := daemonset.CalculateTotalMemoryGB(instasliceObj.Status.NodeResources.NodeGPUs)
					Expect(err).NotTo(HaveOccurred(), "Failed to get total GPU memory")
					totalMemoryGB += memoryGB
				}
			} else {
				// This test case assumes that all the associated GPUs of a node have homogeneous configuration
				// Ex: 4 GPUs of type "NVIDIA A100-SXM4-40GB", 2 GPUs of type "NVIDIA A100-SXM4-80GB" etc.
				// This helps in correct calculation of total GPU memory(i.e. count * memory) that gets compared with the accelerator memory
				gpuMemory, exists := node.Labels[controller.GPUMemoryLabelName]
				Expect(exists).To(BeTrue(), fmt.Sprintf("%s not found in Node object", controller.GPUMemoryLabelName))
				memory, err := strconv.Atoi(gpuMemory)
				Expect(err).To(BeNil(), fmt.Sprintf("unable to fetch gpu memory from node object %s, node: %s", controller.GPUMemoryLabelName, node.Name))
				gpuCount, exists := node.Labels[controller.GPUCountLabelName]
				Expect(exists).To(BeTrue(), fmt.Sprintf("%s not found in Node object", controller.GPUCountLabelName))
				count, err := strconv.Atoi(gpuCount)
				Expect(err).To(BeNil(), fmt.Sprintf("unable to fetch gpu count from node object %s, node: %s", controller.GPUCountLabelName, node.Name))
				totalMemoryGB = float64((memory * count) / 1024)
			}
			By(fmt.Sprintf("Verifying that node has custom resource %s", controller.QuotaResourceName))
			Expect(err).NotTo(HaveOccurred(), "Failed to get the node")

			acceleratorMemory, exists := node.Status.Capacity[corev1.ResourceName(controller.QuotaResourceName)]
			Expect(exists).To(BeTrue(), fmt.Sprintf("%s not found in Node object", controller.QuotaResourceName))

			Expect(acceleratorMemory.Value()/(1024*1024*1024)).To(Equal(int64(totalMemoryGB)),
				fmt.Sprintf("%s on node does not match total GPU memory in Instaslice object", controller.QuotaResourceName))
		})
		It("should verify run to completion GPU workload on GPUs", func() {
			if emulated {
				Skip("Skipping because EmulatorMode is true")
			}
			err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve Instaslice object")
			referenceLen := len(instasliceObjs.Items[0].Status.NodeResources.NodeGPUs)
			for _, obj := range instasliceObjs.Items {
				currentLen := len(obj.Status.NodeResources.NodeGPUs)
				Expect(currentLen).To(Equal(referenceLen), "Object %s has a different number of GPUs", obj.Name)
			}
			numNewNames := referenceLen * 7
			podTemplate := resources.GetTestGPURunToCompletionWorkload()

			DeferCleanup(func() {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace(podTemplate.Namespace))
				if err != nil {
					log.Printf("Failed to list pods: %v\n", err)
					return
				}

				for _, pod := range podList.Items {
					err := k8sClient.Delete(ctx, &pod)
					if err != nil {
						log.Printf("Failed to delete pod %s: %v\n", pod.Name, err)
					} else {
						log.Printf("Deleted pod: %s\n", pod.Name)
					}
				}
			})
			for i := 1; i <= numNewNames; i++ {
				newName := fmt.Sprintf("cuda-vectoradd-%d", i+1)
				pod := podTemplate.DeepCopy()
				pod.Name = newName
				pod.Spec.Containers[0].Name = newName

				err := k8sClient.Create(ctx, pod)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create pod %s", newName))
			}

			Eventually(func() bool {
				allCompleted := true
				for i := 1; i <= numNewNames; i++ {
					podName := fmt.Sprintf("cuda-vectoradd-%d", i+1)
					pod := &corev1.Pod{}
					err := k8sClient.Get(ctx, client.ObjectKey{Namespace: podTemplate.Namespace, Name: podName}, pod)
					if err != nil || pod.Status.Phase != corev1.PodSucceeded {
						allCompleted = false
						break
					}
				}
				return allCompleted
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Not all pods completed successfully")
		})
		It("should verify all 1g profiles of GPUs are consumed", func() {
			if emulated {
				Skip("Skipping because EmulatorMode is true")
			}
			podTemplateLongRunning := resources.GetTestGPULongRunningWorkload()
			err := k8sClient.List(ctx, instasliceObjs, &client.ListOptions{Namespace: namespace})
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve Instaslice object")
			referenceLen := len(instasliceObjs.Items[0].Status.NodeResources.NodeGPUs)
			for _, obj := range instasliceObjs.Items {
				currentLen := len(obj.Status.NodeResources.NodeGPUs)
				Expect(currentLen).To(Equal(referenceLen), "Object %s has a different MigGPUUUID length", obj.Name)
			}
			longRunningCount := referenceLen*7 + 1
			for i := 1; i <= longRunningCount; i++ {
				newName := fmt.Sprintf("cuda-vectoradd-longrunning%d", i+1)
				pod := podTemplateLongRunning.DeepCopy()
				pod.Name = newName
				pod.Spec.Containers[0].Name = newName

				err := k8sClient.Create(ctx, pod)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create pod %s", newName))
			}
			DeferCleanup(func() {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace(podTemplateLongRunning.Namespace))
				if err != nil {
					fmt.Printf("Failed to list pods: %v\n", err)
					return
				}

				for _, pod := range podList.Items {
					err := k8sClient.Delete(ctx, &pod)
					if err != nil {
						fmt.Printf("Failed to delete pod %s: %v\n", pod.Name, err)
					} else {
						fmt.Printf("Deleted pod: %s\n", pod.Name)
					}
				}
			})
			Eventually(func() bool {
				countRunning := 0
				for i := 1; i <= longRunningCount; i++ {
					podName := fmt.Sprintf("cuda-vectoradd-longrunning%d", i+1)
					pod := &corev1.Pod{}
					err := k8sClient.Get(ctx, client.ObjectKey{Namespace: podTemplateLongRunning.Namespace, Name: podName}, pod)
					if err != nil {
						log.Printf("Failed to get pod %s: %v", podName, err)
						return false
					}

					if pod.Status.Phase == corev1.PodRunning {
						countRunning++
					}
				}
				return longRunningCount-countRunning == 1
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "1 workload should be pending")
		})
	})
})

func getNodeNames(label map[string]string) ([]string, error) {
	nodeNames := make([]string, 0)
	if !emulated {
		labelSelector := "nvidia.com/mig.capable=true"
		nodes, err := clientSet.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return nil, err
		}
		for _, node := range nodes.Items {
			nodeNames = append(nodeNames, node.Name)
		}
		if len(nodeNames) == 0 {
			return nil, fmt.Errorf("no node name found for pods with label: %v", label)
		}
		return nodeNames, nil
	}

	podList := &corev1.PodList{}
	err := k8sClient.List(ctx, podList, client.MatchingLabels(label))
	if err != nil {
		return nil, fmt.Errorf("unable to list pods: %v", err)
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != "" {
			return append(nodeNames, pod.Spec.NodeName), nil
		}
	}

	return nil, fmt.Errorf("no node name found for pods with label: %v", label)
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() string {
	var out string
	verifyTokenCreation := func(g Gomega) {
		// Construct the TokenRequest object
		tokenRequest := &authv1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instasliceServiceAccount,
				Namespace: namespace,
			},
			Spec: authv1.TokenRequestSpec{
				ExpirationSeconds: new(int64),
			},
		}
		// Optionally Set expiration time to 1 hour (3600 seconds)
		*tokenRequest.Spec.ExpirationSeconds = 3600
		// Create the token for the service account
		token, err := clientSet.CoreV1().ServiceAccounts(namespace).CreateToken(context.Background(), instasliceServiceAccount, tokenRequest, metav1.CreateOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	options := corev1.PodLogOptions{}
	req := clientSet.CoreV1().Pods(namespace).GetLogs("curl-metrics", &options)
	podLogs, err := req.Stream(ctx)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl-metrics pod")
	defer func() {
		err := podLogs.Close()
		Expect(err).NotTo(HaveOccurred(), "Failed to close pod logs reader")
	}()
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl-metrics pod")
	metricsOutput := buf.String()
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}
