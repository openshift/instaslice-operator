package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/instaslice-operator/test/utils"
)

var _ = Describe("Sample pod", Ordered, func() {
	var podPath string

	BeforeAll(func() {
		dir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		podPath = filepath.Join(dir, "deploy-k8s", "07_test_pod.yaml")

		By("creating sample pod")
		_, err = utils.Run(exec.Command("kubectl", "apply", "-f", podPath))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting sample pod")
		_, err := utils.Run(exec.Command("kubectl", "delete", "-f", podPath, "--ignore-not-found=true"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("should be running", func(ctx SpecContext) {
		Eventually(func() (string, error) {
			out, err := utils.Run(exec.Command("kubectl", "get", "pod", "test-instaslice", "-o", "jsonpath={.status.phase}"))
			return strings.TrimSpace(string(out)), err
		}, 2*time.Minute, 5*time.Second).Should(Equal("Running"))
	})
})
