package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"encoding/json"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	clientset "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"

	"github.com/openshift/instaslice-operator/test/utils"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "End-to-End Suite")
}

var _ = Describe("MIG scheduler dynamic combination", Ordered, func() {
	var (
		nodeName string
		podUIDs  []string
		pods     []*corev1.Pod
	)

	BeforeAll(func() {
		nodes, err := kubeClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(nodes.Items)).To(BeNumerically(">", 0))
		nodeName = nodes.Items[0].Name

		node := nodes.Items[0]
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels["nvidia.com/mig.capable"] = "true"
		_, err = kubeClient.CoreV1().Nodes().Update(context.Background(), &node, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		inst := utils.GenerateFakeCapacity(nodeName)
		inst.TypeMeta = metav1.TypeMeta{APIVersion: "inference.redhat.com/v1alpha1", Kind: "NodeAccelerator"}
		_, err = instaClient.OpenShiftOperatorV1alpha1().NodeAccelerators(inst.Namespace).Create(context.Background(), inst, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		var res instav1.DiscoveredNodeResources
		Expect(json.Unmarshal(inst.Status.NodeResources.Raw, &res)).To(Succeed())
		combos := combosForGPU(res.NodeGPUs[0].GPUName)
		profiles := parseCombo(combos[0])

		for i, prof := range profiles {
			name := fmt.Sprintf("combo-pod-%d", i+1)
			manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  schedulerName: das-scheduler
  restartPolicy: Never
  containers:
  - name: main
    image: ubuntu:20.04
    command: ["sh", "-c", "sleep 120"]
    resources:
      limits:
        nvidia.com/mig-%s: 1
`, name, prof)
			pod := &corev1.Pod{}
			Expect(yaml.Unmarshal([]byte(manifest), pod)).To(Succeed())
			pod.Namespace = "default"
			_, err = kubeClient.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			pods = append(pods, pod)
		}
	})

	AfterAll(func() {
		for _, p := range pods {
			_ = kubeClient.CoreV1().Pods(p.Namespace).Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		}
		_ = instaClient.OpenShiftOperatorV1alpha1().NodeAccelerators("das-operator").Delete(context.Background(), nodeName, metav1.DeleteOptions{})

		node, err := kubeClient.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err == nil {
			delete(node.Labels, "nvidia.com/mig.capable")
			_, _ = kubeClient.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
		}
	})

	It("should schedule valid combination pods", func(ctx SpecContext) {
		for _, p := range pods {
			Eventually(func() (corev1.PodPhase, error) {
				pod, err := kubeClient.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				return pod.Status.Phase, nil
			}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))

			pod, err := kubeClient.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			podUIDs = append(podUIDs, string(pod.UID))
			Expect(pod.Spec.NodeName).To(Equal(nodeName))
		}

		Eventually(func() (int, error) {
			list, err := instaClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			count := 0
			for _, c := range list.Items {
				for _, uid := range podUIDs {
					if string(c.Spec.PodRef.UID) == uid {
						count++
					}
				}
			}
			return count, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(len(pods)))
	})
})

var (
	kubeClient  *kubernetes.Clientset
	instaClient *clientset.Clientset
)

func parseCombo(s string) []string {
	var profiles []string
	for _, part := range strings.Split(s, " + ") {
		fields := strings.SplitN(part, "x", 2)
		if len(fields) != 2 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		for i := 0; i < n; i++ {
			profiles = append(profiles, fields[1])
		}
	}
	return profiles
}

func combosForGPU(gpu string) []string {
	base := []string{
		"1x1g.10gb + 1x1g.5gb+me + 5x1g.5gb",
		"1x1g.10gb + 6x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x2g.10gb + 1x1g.10gb + 4x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x2g.10gb + 2x1g.10gb + 2x1g.5gb",
		"1x2g.10gb + 3x1g.10gb",
		"1x3g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x3g.20gb + 1x1g.5gb+me + 3x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x3g.20gb + 1x2g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x3g.20gb + 1x2g.10gb + 2x1g.5gb",
		"1x3g.20gb + 2x1g.10gb",
		"1x3g.20gb + 2x2g.10gb",
		"1x3g.20gb + 4x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"1x4g.20gb + 1x1g.10gb + 2x1g.5gb",
		"1x4g.20gb + 1x2g.10gb + 1x1g.10gb",
		"1x4g.20gb + 1x3g.20gb",
		"1x4g.20gb + 2x1g.10gb",
		"1x7g.40gb",
		"2x1g.10gb + 1x1g.5gb+me + 3x1g.5gb",
		"2x1g.10gb + 4x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"2x2g.10gb + 1x1g.10gb + 2x1g.5gb",
		"2x2g.10gb + 2x1g.10gb",
		"2x3g.20gb",
		"3x1g.10gb + 1x1g.5gb+me + 1x1g.5gb",
		"3x1g.10gb + 2x1g.5gb",
		"3x2g.10gb + 1x1g.10gb",
		"4x1g.10gb",
	}

	if strings.Contains(gpu, "H100") {
		r := strings.NewReplacer(
			"1g.5gb", "1g.10gb",
			"1g.5gb+me", "1g.10gb+me",
			"1g.10gb", "1g.20gb",
			"2g.10gb", "2g.20gb",
			"3g.20gb", "3g.40gb",
			"4g.20gb", "4g.40gb",
			"7g.40gb", "7g.80gb",
		)
		for i, c := range base {
			base[i] = r.Replace(c)
		}
	} else if strings.Contains(gpu, "H200") {
		r := strings.NewReplacer(
			"1g.5gb", "1g.18gb",
			"1g.5gb+me", "1g.18gb+me",
			"1g.10gb", "1g.35gb",
			"2g.10gb", "2g.35gb",
			"3g.20gb", "3g.71gb",
			"4g.20gb", "4g.71gb",
			"7g.40gb", "7g.141gb",
		)
		for i, c := range base {
			base[i] = r.Replace(c)
		}
	}
	return base
}

var _ = BeforeSuite(func() {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			Skip("kubernetes config not available: " + err.Error())
		}
	}
	kubeClient, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		Skip("failed to create kubernetes client: " + err.Error())
	}
	instaClient, err = clientset.NewForConfig(cfg)
	if err != nil {
		Skip("failed to create instaslice client: " + err.Error())
	}
})

var _ = Describe("Sample pod", Ordered, func() {
	var (
		podPath   string
		podName   string
		namespace string
	)

	BeforeAll(func() {
		dir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		podPath = filepath.Join(dir, "deploy-k8s", "07_test_pod.yaml")

		data, err := os.ReadFile(podPath)
		Expect(err).NotTo(HaveOccurred())
		pod := &corev1.Pod{}
		Expect(yaml.Unmarshal(data, pod)).To(Succeed())
		if pod.Namespace == "" {
			pod.Namespace = "default"
		}
		namespace = pod.Namespace
		podName = pod.Name

		By("creating sample pod")
		_, err = kubeClient.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting sample pod")
		_ = kubeClient.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	})

	It("should be running", func(ctx SpecContext) {
		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
	})
})

var _ = Describe("Webhook pod mutation", Ordered, func() {
	var (
		podPath   string
		podName   = "emulator-pod-1"
		namespace string
	)

	BeforeAll(func() {
		dir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		podPath = filepath.Join(dir, "samples", "emulator-pod.yaml")

		data, err := os.ReadFile(podPath)
		Expect(err).NotTo(HaveOccurred())
		pod := &corev1.Pod{}
		Expect(yaml.Unmarshal(data, pod)).To(Succeed())
		if pod.Namespace == "" {
			pod.Namespace = "default"
		}
		namespace = pod.Namespace

		By("creating emulator pod")
		_, err = kubeClient.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting emulator pod")
		_ = kubeClient.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	})

	It("should mutate pod resources and scheduler", func(ctx SpecContext) {
		Eventually(func() error {
			pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if pod.Spec.SchedulerName != "das-scheduler" {
				return fmt.Errorf("unexpected scheduler %s", pod.Spec.SchedulerName)
			}
			limits := pod.Spec.Containers[0].Resources.Limits
			if _, ok := limits[corev1.ResourceName("nvidia.com/mig-1g.5gb")]; ok {
				return fmt.Errorf("nvidia resource still present")
			}
			if q, ok := limits[corev1.ResourceName("mig.das.com/1g.5gb")]; !ok || q.Value() != 1 {
				return fmt.Errorf("instaslice resource missing")
			}
			return nil
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})

var _ = Describe("AllocationClaim lifecycle", Ordered, func() {
	var (
		podPath   string
		podName   = "minimal-pod-1"
		namespace string
		podUID    string
	)

	BeforeAll(func() {
		dir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		podPath = filepath.Join(dir, "samples", "minimal-pod.yaml")

		data, err := os.ReadFile(podPath)
		Expect(err).NotTo(HaveOccurred())
		pod := &corev1.Pod{}
		Expect(yaml.Unmarshal(data, pod)).To(Succeed())
		if pod.Namespace == "" {
			pod.Namespace = "default"
		}
		namespace = pod.Namespace

		By("creating minimal pod")
		_, err = kubeClient.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting minimal pod")
		_ = kubeClient.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	})

	It("should create an AllocationClaim when pod is running", func(ctx SpecContext) {
		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))

		p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		podUID = string(p.UID)

		Eventually(func() (int, error) {
			list, err := instaClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			cnt := 0
			for _, item := range list.Items {
				if string(item.Spec.PodRef.UID) == podUID {
					cnt++
				}
			}
			return cnt, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(1))
	})

	It("should remove the AllocationClaim after pod deletion", func(ctx SpecContext) {
		_ = kubeClient.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})

		Eventually(func() (int, error) {
			list, err := instaClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			cnt := 0
			for _, item := range list.Items {
				if string(item.Spec.PodRef.UID) == podUID {
					cnt++
				}
			}
			return cnt, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(0))
	})
})

var _ = Describe("MIG scheduler placement", Ordered, func() {
	const podCount = 3
	var nodeName string
	var podUIDs []string
	var pods []*corev1.Pod

	BeforeAll(func() {
		nodes, err := kubeClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(nodes.Items)).To(BeNumerically(">", 0))
		nodeName = nodes.Items[0].Name

		node := nodes.Items[0]
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels["nvidia.com/mig.capable"] = "true"
		_, err = kubeClient.CoreV1().Nodes().Update(context.Background(), &node, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		inst := utils.GenerateFakeCapacity(nodeName)
		inst.TypeMeta = metav1.TypeMeta{APIVersion: "inference.redhat.com/v1alpha1", Kind: "NodeAccelerator"}
		_, err = instaClient.OpenShiftOperatorV1alpha1().NodeAccelerators(inst.Namespace).Create(context.Background(), inst, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		for i := 1; i <= podCount; i++ {
			name := fmt.Sprintf("mig-pod-%d", i)
			manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  restartPolicy: Never
  containers:
  - name: main
    image: ubuntu:20.04
    command: ["sh", "-c", "sleep 120"]
    resources:
      limits:
        nvidia.com/mig-1g.5gb: 1
`, name)
			pod := &corev1.Pod{}
			Expect(yaml.Unmarshal([]byte(manifest), pod)).To(Succeed())
			pod.Namespace = "default"
			_, err = kubeClient.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			pods = append(pods, pod)
		}
	})

	AfterAll(func() {
		for _, p := range pods {
			_ = kubeClient.CoreV1().Pods(p.Namespace).Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		}
		_ = instaClient.OpenShiftOperatorV1alpha1().NodeAccelerators("das-operator").Delete(context.Background(), nodeName, metav1.DeleteOptions{})

		node, err := kubeClient.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err == nil {
			delete(node.Labels, "nvidia.com/mig.capable")
			_, _ = kubeClient.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
		}
	})

	It("should schedule all pods and create claims", func(ctx SpecContext) {
		for i := 1; i <= podCount; i++ {
			name := fmt.Sprintf("mig-pod-%d", i)
			Eventually(func() (corev1.PodPhase, error) {
				p, err := kubeClient.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				return p.Status.Phase, nil
			}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))

			p, err := kubeClient.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			pods = append(pods, p)
			podUIDs = append(podUIDs, string(p.UID))

			Expect(p.Spec.NodeName).To(Equal(nodeName))
		}

		Eventually(func() (int, error) {
			list, err := instaClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			count := 0
			for _, c := range list.Items {
				for _, uid := range podUIDs {
					if string(c.Spec.PodRef.UID) == uid {
						count++
					}
				}
			}
			return count, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(podCount))
	})
})
