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
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"codeflare.dev/instaslice/test/utils"
)

//TODO: add more test cases -
// 1. delete instaslice object, fill the object with dangling slices ie no capacity available and
// verify that allocation should not exists in instaslice object.
// 2. check size and index value based on different mig slice profiles requested.
// 3. submit 3 pods with 3g.20gb slice and verify that two allocations exists in instaslice object.
// 4. submit a test pod with 1g.20gb slice and later delete it. verify the allocation status to be
// in state deleting

var _ = Describe("controller", Ordered, func() {
	var namespace string = "instaslicev2-system"

	BeforeAll(func() {
		fmt.Println("Setting up Kind cluster")
		cmd := exec.Command("kind", "create", "cluster")
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create Kind cluster: %s", output))

		By("creating manager namespace")
		cmdNamespace := exec.Command("kubectl", "create", "ns", namespace)
		outputNs, err := cmdNamespace.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create namespace: %s", outputNs))
	})

	AfterAll(func() {
		fmt.Println("Deleting the cluster")
		cmd := exec.Command("kind", "delete", "cluster")
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create Kind cluster: %s", output))
	})

	Context("Operator", func() {
		It("should run successfully", func() {
			var controllerPodName string
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
			verifyControllerUp := func() error {
				cmd = exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(string(podOutput))
				if len(podNames) != 1 {
					return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
				}
				controllerPodName = podNames[0]
				ExpectWithOffset(2, controllerPodName).Should(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				status, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				if string(status) != "Running" {
					return fmt.Errorf("controller pod in %s status", status)
				}
				return nil
			}
			EventuallyWithOffset(1, verifyControllerUp, time.Minute, time.Second).Should(Succeed())
		})

		It("should apply the YAML and check if that instaslice resource exists", func() {

			cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/instaslice.yaml")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply YAML: %s", output))

			checkCmd := exec.Command("kubectl", "describe", "instaslice", "-n", "default")
			output, err = checkCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Resource not found: %s", output))
		})

		It("should apply the pod YAML and check the allocation in instaslice object", func() {
			cmdPod := exec.Command("kubectl", "apply", "-f", "test/e2e/resources/test-pod.yaml")
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
				fmt.Printf("the allocations found are %v", allocations)
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
	})
})
