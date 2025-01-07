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

	instasliceObj inferencev1alpha1.Instaslice

	cfg       *rest.Config
	k8sClient client.Client
	clientSet *kubernetes.Clientset
)

const (
	instasliceMetricSvc      = "instaslice-operator-controller-manager-metrics-service"
	instasliceServiceAccount = "instaslice-operator-controller-manager"
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
	switch os.Getenv("EMULATOR_MODE") {
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
		if emulated {
			err := k8sClient.Create(ctx, resources.GenerateFakeCapacity(templateVars.NodeName))
			Expect(err).NotTo(HaveOccurred(), "Failed to create instaslice object")

			err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice resource")
		}
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
		const (
			originalName     = "cuda-vectoradd-1"
			numNewNames      = 10
			checkInterval    = 30 * time.Second
			longRunningCount = 15
		)
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
			By("creating a ClusterRole to access /metrics endpoint")
			clusterRole := resources.GetClusterRole()
			err := k8sClient.Create(ctx, clusterRole)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the ClusterRole")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, clusterRole)
				if err != nil {
					log.Printf("Error deleting the ClusterRole %+v: %+v", clusterRole, err)
				}
			})
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			clusterRoleBinding := resources.GetClusterRoleBinding()
			err = k8sClient.Create(ctx, clusterRoleBinding)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the ClusterRoleBinding")

			DeferCleanup(func() {
				err = k8sClient.Delete(ctx, clusterRoleBinding)
				if err != nil {
					log.Printf("Error deleting the ClusterRoleBinding %+v: %+v", clusterRole, err)
				}
			})

			By("validating that the metrics service is available")
			var svc corev1.Service
			err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instasliceMetricSvc}, &svc)
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
				propagationPolicy := metav1.DeletePropagationForeground
				err = k8sClient.Delete(ctx, deployment, &client.DeleteOptions{
					PropagationPolicy: &propagationPolicy,
				})
				if err != nil {
					log.Printf("Error deleting the deployment %+v: %+v", deployment, err)
				}
				if !emulated {
					Eventually(func() bool {
						err := k8sClient.Get(ctx, client.ObjectKey{
							Namespace: namespace,
							Name:      templateVars.NodeName,
						}, &instasliceObj)
						if err != nil {
							fmt.Printf("Failed to get Instaslice object: %v\n", err)
							return false
						}
						for _, allocation := range instasliceObj.Spec.Allocations {
							if allocation.Allocationstatus != inferencev1alpha1.AllocationStatusDeleted {
								return false
							}
						}
						return len(instasliceObj.Spec.Allocations) == 0
					}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "Allocations were not deleted in Instaslice object")
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
				allSlicesProvisioned := true

				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.Allocationstatus != inferencev1alpha1.AllocationStatusUngated && allocation.Allocationstatus != inferencev1alpha1.AllocationStatusCreated {
						return false
					}

					// Since we are requesting 7 slices of type mig-1g.5gb, all 7 pods must be
					// assigned to the same GPU in best case scenario.
					// previous allocations or workload may be in deleting phase this
					// will lead instaslice to allocate workloads to any available GPUs on node.
					if assignedGPUUUID == "" {
						assignedGPUUUID = allocation.GPUUUID
					} else if allocation.GPUUUID != assignedGPUUUID {
						log.Printf("partition assigned to different GPU")
					}
				}

				return allSlicesProvisioned
			}, 2*time.Minute, time.Millisecond*5).Should(BeTrue(), "Not all allocations are provisioned after the timeout")

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
				for _, allocation := range instasliceObj.Spec.Allocations {
					if allocation.Allocationstatus != inferencev1alpha1.AllocationStatusDeleted || len(instasliceObj.Spec.Allocations) == 0 {
						return false
					}
				}
				return true
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Expected Instaslice object Allocations to be empty or 'deleted' state")
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

				allocation := instasliceObj.Spec.Allocations[string(pod.UID)]
				currentStatus := string(allocation.Allocationstatus)
				if currentStatus != "" {
					if len(observedStatuses) == 0 || (observedStatuses[len(observedStatuses)-1] != currentStatus) {
						observedStatuses = append(observedStatuses, currentStatus)
					}
				}
				return currentStatus
			}, time.Minute, time.Millisecond*500).Should(Equal("ungated"))
			Expect(observedStatuses).To(Equal([]string{"creating", "created", "ungated"}))
		})
		// we are running real cuda vectoradd GPU workload on below tests to test
		// daemonset interaction with GPU on the node. the daemonset creates MIG (CI and GI) for pod
		// and the pod consumes the created MIG partition. Since this involves use of real GPU hardware
		// these tests should never be run in emulator mode and are skipped.
		It("should verify run to completion GPU workload when EmulatorMode is false", func() {
			if emulated {
				Skip("Skipping because EmulatorMode is true")
			}

			podTemplate := resources.GetTestGPURunToCompletionWorkload()

			DeferCleanup(func() {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace(podTemplate.Namespace))
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
			}, 2*time.Minute, checkInterval).Should(BeTrue(), "Not all pods completed successfully")
		})
		It("should verify all 1g profiles of GPUs are consumed when EmulatorMode is false", func() {
			if emulated {
				Skip("Skipping because EmulatorMode is true")
			}
			podTemplateLongRunning := resources.GetTestGPULongRunningWorkload()
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
				Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)).To(Succeed())
				expectedCountRunning := len(instasliceObj.Spec.MigGPUUUID) * 7
				log.Printf("expected count running is %v", expectedCountRunning)
				countRunning := 0
				for i := 1; i <= longRunningCount; i++ {
					podName := fmt.Sprintf("cuda-vectoradd-longrunning-%d", i+1)
					pod := &corev1.Pod{}
					err := k8sClient.Get(ctx, client.ObjectKey{Namespace: podTemplateLongRunning.Namespace, Name: podName}, pod)
					if err != nil && pod.Status.Phase == corev1.PodRunning {
						countRunning++
						if countRunning == expectedCountRunning {
							break
						}
					}
				}
				return true
			}, 2*time.Minute, checkInterval).Should(BeTrue(), "14 pods were running successfully")
		})
	})

	AfterEach(func() {

		if emulated {
			_ = k8sClient.Delete(ctx, &instasliceObj)
		} else {
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: templateVars.NodeName}, &instasliceObj)
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object")
			instasliceObj.Spec.Allocations = map[string]inferencev1alpha1.AllocationDetails{}
			err = k8sClient.Update(ctx, &instasliceObj)
			Expect(err).NotTo(HaveOccurred(), "Failed to reset Allocations in Instaslice object")

		}
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
