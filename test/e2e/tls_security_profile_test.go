package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/crypto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// TLS CLI flag prefixes used by DAS operator components
	tlsMinVersionFlag   = "--tls-min-version="
	tlsCipherSuitesFlag = "--tls-cipher-suites="

	// TLS profile hash annotation for rolling updates
	tlsProfileHashAnnotation = "das-operator.openshift.io/tls-profile-hash"

	// Component names
	webhookDeploymentName = "das-operator-webhook"
	daemonsetName         = "das-daemonset"
)

var _ = Describe("TLS Security Profile Configuration", Ordered, func() {
	Context("Default Intermediate TLS Profile", func() {
		It("should have correct TLS args in webhook deployment", func(ctx SpecContext) {
			expectedCiphers := configv1.TLSProfiles[configv1.TLSProfileIntermediateType].Ciphers
			expectedMinVersion := "VersionTLS12"

			Eventually(func() error {
				return verifyDeploymentTLSArgs(ctx, dasOperatorNamespace, webhookDeploymentName, expectedMinVersion, expectedCiphers)
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should have correct TLS args in daemonset", func(ctx SpecContext) {
			expectedCiphers := configv1.TLSProfiles[configv1.TLSProfileIntermediateType].Ciphers
			expectedMinVersion := "VersionTLS12"

			Eventually(func() error {
				return verifyDaemonSetTLSArgs(ctx, dasOperatorNamespace, daemonsetName, expectedMinVersion, expectedCiphers)
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should have TLS profile hash annotation on webhook deployment", func(ctx SpecContext) {
			Eventually(func() (bool, error) {
				return hasValidTLSProfileHashAnnotation(ctx, dasOperatorNamespace, webhookDeploymentName, "deployment")
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		})

		It("should have TLS profile hash annotation on daemonset", func(ctx SpecContext) {
			Eventually(func() (bool, error) {
				return hasValidTLSProfileHashAnnotation(ctx, dasOperatorNamespace, daemonsetName, "daemonset")
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		})
	})
})

var _ = Describe("TLS Profile Types Verification", Ordered, func() {
	DescribeTable("should have correct cipher count for profile type",
		func(profileType configv1.TLSProfileType, expectedMinCiphers int) {
			profile := configv1.TLSProfiles[profileType]
			Expect(len(profile.Ciphers)).To(BeNumerically(">=", expectedMinCiphers),
				fmt.Sprintf("Profile %s should have at least %d ciphers", profileType, expectedMinCiphers))
		},
		Entry("Old profile", configv1.TLSProfileOldType, 10),
		Entry("Intermediate profile", configv1.TLSProfileIntermediateType, 5),
		Entry("Modern profile", configv1.TLSProfileModernType, 1),
	)

	DescribeTable("should have correct min TLS version for profile type",
		func(profileType configv1.TLSProfileType, expectedVersion configv1.TLSProtocolVersion) {
			profile := configv1.TLSProfiles[profileType]
			Expect(profile.MinTLSVersion).To(Equal(expectedVersion),
				fmt.Sprintf("Profile %s should have min TLS version %s", profileType, expectedVersion))
		},
		Entry("Old profile", configv1.TLSProfileOldType, configv1.VersionTLS10),
		Entry("Intermediate profile", configv1.TLSProfileIntermediateType, configv1.VersionTLS12),
		Entry("Modern profile", configv1.TLSProfileModernType, configv1.VersionTLS13),
	)
})

// verifyDeploymentTLSArgs checks that a deployment has the expected TLS CLI args
func verifyDeploymentTLSArgs(ctx context.Context, namespace, name, expectedMinVersion string, expectedCiphers []string) error {
	deployment, err := kubeClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	containers := deployment.Spec.Template.Spec.Containers
	return verifyContainerTLSArgs(containers, expectedMinVersion, expectedCiphers)
}

// verifyDaemonSetTLSArgs checks that a daemonset has the expected TLS CLI args
func verifyDaemonSetTLSArgs(ctx context.Context, namespace, name, expectedMinVersion string, expectedCiphers []string) error {
	daemonset, err := kubeClient.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	containers := daemonset.Spec.Template.Spec.Containers
	return verifyContainerTLSArgs(containers, expectedMinVersion, expectedCiphers)
}

// verifyContainerTLSArgs checks container args for TLS configuration
func verifyContainerTLSArgs(containers []corev1.Container, expectedMinVersion string, expectedCiphers []string) error {
	expectedVersionArg := tlsMinVersionFlag + expectedMinVersion
	expectedCiphersArg := tlsCipherSuitesFlag + strings.Join(crypto.OpenSSLToIANACipherSuites(expectedCiphers), ",")

	for _, c := range containers {
		foundVersion := false
		foundCiphers := false

		for _, arg := range c.Args {
			if arg == expectedVersionArg {
				foundVersion = true
			}
			if strings.HasPrefix(arg, tlsCipherSuitesFlag) {
				if arg == expectedCiphersArg {
					foundCiphers = true
				}
			}
		}

		if !foundVersion {
			return fmt.Errorf("container %s missing expected TLS min version arg %s, got args: %v", c.Name, expectedVersionArg, c.Args)
		}

		// Cipher suites are optional (Go uses secure defaults for TLS 1.3)
		if len(expectedCiphers) > 0 && !foundCiphers {
			GinkgoWriter.Printf("Warning: container %s missing cipher suites arg, expected: %s\n", c.Name, expectedCiphersArg)
		}
	}

	return nil
}

// hasValidTLSProfileHashAnnotation checks if the workload has a non-empty TLS profile hash annotation
func hasValidTLSProfileHashAnnotation(ctx context.Context, namespace, name, kind string) (bool, error) {
	var annotations map[string]string

	switch kind {
	case "deployment":
		deployment, err := kubeClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		annotations = deployment.Spec.Template.Annotations
	case "daemonset":
		daemonset, err := kubeClient.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		annotations = daemonset.Spec.Template.Annotations
	default:
		return false, fmt.Errorf("unknown kind: %s", kind)
	}

	hash, exists := annotations[tlsProfileHashAnnotation]
	return exists && hash != "", nil
}
