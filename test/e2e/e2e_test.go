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
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/instaslice-operator/test/utils"
)

//TODO: add more test cases -
// 1. delete instaslice object, fill the object with dangling slices ie no capacity available and
// verify that allocation should not exists in instaslice object.
// 2. check size and index value based on different mig slice profiles requested.
// 3. submit 3 pods with 3g.20gb slice and verify that two allocations exists in instaslice object.
// 4. submit a test pod with 1g.20gb slice and later delete it. verify the allocation status to be
// in state deleting

var _ = Describe("controller", Ordered, func() {
	var namespace string = "instaslice-operator-system"

	BeforeAll(func() {
		fmt.Println("Setting up Kind cluster")
		cmd := exec.Command("kind", "create", "cluster")
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create Kind cluster: %s", output))

		By("creating manager namespace")
		cmdNamespace := exec.Command("kubectl", "create", "ns", namespace)
		outputNs, err := cmdNamespace.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create namespace: %s", outputNs))

		By("installing cert manager")
		cmdCm := exec.Command("kubectl", "apply", "-f", "https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml")
		outputCm, err := cmdCm.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to deploy cert manager: %s", outputCm))

		By("validating the cert-manager-webhook pod to be ready as expected")
		EventuallyWithOffset(1, isResourceReady, 2*time.Minute, time.Second).WithArguments("pod", "app=webhook", "cert-manager").Should(BeTrue())
	})

	AfterAll(func() {
		fmt.Println("Deleting the cluster")
		cmd := exec.Command("kind", "delete", "cluster")
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create Kind cluster: %s", output))
	})

	Context("Operator", func() {
		It("should run successfully", func() {
			var err error

			var projectimage = "quay.io/amalvank/instaslicev2-controller:latest"

			By("building the manager(Operator) image")
			cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("loading the manager(Operator) image on Kind")
			err = utils.LoadImageToKindClusterWithName(projectimage)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("installing CRDs")
			cmd = exec.Command("make", "install")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("deploying the controller-manager")
			cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("validating that the controller-manager pod is running as expected")
			EventuallyWithOffset(1, isResourceReady, 2*time.Minute, time.Second).WithArguments("pod", "control-plane=controller-manager", namespace).Should(BeTrue())
			// We don't have a reliable source to depend on to verify if the service is up and ready
			// Issue Ref: https://github.com/kubernetes/kubernetes/issues/80828
			// (TODO) Wait for the instaslice-operator-webhook-service to be available and Ready
			/*
				By("verifying that the instaslice-operator-webhook service to be ready as expected")
				EventuallyWithOffset(1, isResourceReady, 2*time.Minute, time.Second).WithArguments("service", "app.kubernetes.io/component=webhook", namespace).Should(BeTrue())
			*/
			// Until then, adding a sleep of 1 Minute should mitigate the intermittent failures running the e2e tests.
			time.Sleep(time.Minute)
		})

		It("should apply the YAML and check if that instaslice resource exists", func() {

			cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/instaslice-fake-capacity.yaml")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", output))

			checkCmd := exec.Command("kubectl", "describe", "instaslice", "-n", "default")
			output, err = checkCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Resource not found: %s", output))
		})

		It("should apply the pod YAML with no requests and check if finalizer exists", func() {
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-pod-no-requests.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmdGetPod := exec.Command("kubectl", "get", "pod", "vectoradd-no-req", "-o", "json")
			outputGetPod, err := cmdGetPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to get pod: %s", outputGetPod))

			var podObj map[string]interface{}
			err = json.Unmarshal(outputGetPod, &podObj)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse pod JSON")

			finalizers, found := podObj["metadata"].(map[string]interface{})["finalizers"].([]interface{})
			Expect(found).To(BeTrue(), "Pod does not have finalizers")
			Expect(finalizers).To(ContainElement("org.instaslice/accelarator"), "Finalizer org.instaslice/accelarator not found on pod")
		})

		It("should apply the pod YAML with no requests and check the allocation in instaslice object", func() {
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-pod-no-requests.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				spec, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")
				allocations, found := spec["allocations"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec.Allocations not found in Instaslice object")
				for _, data := range allocations {
					if allocation, ok := data.(map[string]interface{}); ok {
						if status, ok := allocation["allocationStatus"].(string); ok {
							Expect(ok).To(BeTrue(), "allocationStatus not found in Instaslice object")
							Expect(status).To(Equal("creating"), "Spec.Allocations not found in Instaslice object")
						}
					}
				}

			} else {
				Fail("No Instaslice objects found")
			}
		})

		It("should apply the pod YAML with small requests and check the allocation in instaslice object", func() {
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-pod-with-small-requests.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				spec, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")
				allocations, found := spec["allocations"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec.Allocations not found in Instaslice object")
				for _, data := range allocations {
					if allocation, ok := data.(map[string]interface{}); ok {
						if status, ok := allocation["allocationStatus"].(string); ok {
							Expect(ok).To(BeTrue(), "allocationStatus not found in Instaslice object")
							Expect(status).To(Equal("creating"), "Spec.Allocations not found in Instaslice object")
						}
					}
				}

			} else {
				Fail("No Instaslice objects found")
			}
		})

		It("should apply the pod YAML with pod large memory requests and check the allocation in instaslice object", func() {
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-pod-with-large-memory-requests.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				spec, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")
				allocations, _ := spec["allocations"].(map[string]interface{})
				_, notCreated := findPodName(allocations, "cuda-vectoradd-large-memory")
				Expect(notCreated).To(BeTrue(), "Spec.Allocations found in Instaslice object")

			} else {
				Fail("No Instaslice objects found")
			}
		})

		It("should apply the pod YAML with pod large cpu requests and check the allocation in instaslice object", func() {
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-pod-with-large-cpu-requests.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				spec, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")
				allocations, _ := spec["allocations"].(map[string]interface{})
				_, notCreated := findPodName(allocations, "cuda-vectoradd-large-cpu")
				Expect(notCreated).To(BeTrue(), "Spec.Allocations found in Instaslice object")

			} else {
				Fail("No Instaslice objects found")
			}
		})

		It("should apply the deployment YAML and check if pod exists", func() {
			ctx := context.TODO()
			//deploymentName := "sleep-deployment"
			namespace := "default"
			labelSelector := "app=sleep-app"
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-sleep-deployment.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				_, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")

				Eventually(func() bool {
					cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", namespace, "-l", labelSelector, "--no-headers")
					output, err := cmd.CombinedOutput()
					if err != nil {
						fmt.Printf("Failed to execute kubectl: %v\n", err)
						return false
					}

					outputStr := string(output)
					podLines := strings.Split(strings.TrimSpace(outputStr), "\n")
					return len(podLines) > 0 && podLines[0] != ""
				}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "No pods spawned by the deployment")

			} else {
				Fail("No Instaslice objects found")
			}
		})

		It("should apply the statefulset YAML and check if pod exists", func() {
			ctx := context.TODO()
			//statefulSetName := "sleep-statefulset"
			namespace := "default"
			labelSelector := "app=sleep-statefulset"
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-sleep-statefulset.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				_, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")

				Eventually(func() bool {
					cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", namespace, "-l", labelSelector, "--no-headers")
					output, err := cmd.CombinedOutput()
					if err != nil {
						fmt.Printf("Failed to execute kubectl: %v\n", err)
						return false
					}

					outputStr := string(output)
					podLines := strings.Split(strings.TrimSpace(outputStr), "\n")
					return len(podLines) > 0 && podLines[0] != ""
				}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "No pods spawned by the statefulset")

			} else {
				Fail("No Instaslice objects found")
			}
		})

		It("should apply the job YAML and check if pod exists", func() {
			ctx := context.TODO()
			//statefulSetName := "sleep-statefulset"
			namespace := "default"
			labelSelector := "app=sleep-job"
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-sleep-job.yaml")
			outputPod, err := cmdPod.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", outputPod))

			cmd := exec.Command("kubectl", "get", "instaslice", "-n", "default", "-o", "json")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to get Instaslice object: "+string(output))

			// Parse the JSON output
			var result struct {
				Items []map[string]interface{} `json:"items"`
			}
			err = json.Unmarshal(output, &result)
			Expect(err).NotTo(HaveOccurred(), "Failed to parse JSON output")

			// Assume we want to check the first Instaslice object if it exists
			if len(result.Items) > 0 {
				instaslice := result.Items[0]
				_, found := instaslice["spec"].(map[string]interface{})
				Expect(found).To(BeTrue(), "Spec not found in Instaslice object")

				Eventually(func() bool {
					cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", namespace, "-l", labelSelector, "--no-headers")
					output, err := cmd.CombinedOutput()
					if err != nil {
						fmt.Printf("Failed to execute kubectl: %v\n", err)
						return false
					}

					outputStr := string(output)
					podLines := strings.Split(strings.TrimSpace(outputStr), "\n")
					return len(podLines) > 0 && podLines[0] != ""
				}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "No pods spawned by the job")

			} else {
				Fail("No Instaslice objects found")
			}
		})
	})
})

func findPodName(allocationMap map[string]interface{}, targetPodName string) (string, bool) {
	// Iterate over the outer map
	for uuid, allocationData := range allocationMap {
		// Assert that the allocationData is of type map[string]interface{}
		allocation, ok := allocationData.(map[string]interface{})
		if !ok {
			continue // Skip if it's not the expected type
		}
		if podName, exists := allocation["podName"]; exists {
			if podNameStr, ok := podName.(string); ok && podNameStr == targetPodName {
				return uuid, false
			}
		}
	}
	return "", true
}

func isResourceReady(resource, label, namespace string) bool {
	var cmd = new(exec.Cmd)
	switch resource {
	case "pod", "pods", "po":
		cmd = exec.Command("kubectl", "wait", "--for=condition=ready", resource, "-l", label,
			"-n", namespace, "--timeout=2m",
		)
	case "service", "svc", "services":
		cmd = exec.Command("kubectl", "wait", "--for=jsonpath=spec.type=ClusterIP", resource, "-l", label,
			"-n", namespace, "--timeout=2m",
		)
	default:
		fmt.Errorf("unsupported resource : %s\n", resource)
		return false
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Waiting for the %s to be ready, err: %s\noutput: %s\nRetrying...\n", resource, err, output)
		return false
	}
	return true
}
