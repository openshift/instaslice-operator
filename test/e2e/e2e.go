package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	clientset "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
)

func gpuSlicePodSpec(profile string, mode instav1.EmulatedMode) corev1.PodSpec {
	image := "quay.io/prometheus/busybox"
	command := []string{"sh", "-c", "env && sleep 3600"}
	if mode == instav1.EmulatedModeDisabled {
		image = "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8"
		command = []string{"sh", "-c", "/cuda-samples/vectorAdd && env && sleep 3600"}
	}
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:    "busy",
				Image:   image,
				Command: command,
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName(fmt.Sprintf("nvidia.com/mig-%s", profile)): resource.MustParse("1"),
					},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: pointer.Bool(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					// RunAsNonRoot:             pointer.Bool(true),
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
			},
		},
	}
}

func defaultGPUSlicePodSpec() corev1.PodSpec {
	return gpuSlicePodSpec("1g.5gb", emulatedMode)
}

func multiGPUSlicePodSpec(count int) corev1.PodSpec {
	spec := gpuSlicePodSpec("1g.5gb", emulatedMode)
	spec.Containers[0].Resources.Limits[corev1.ResourceName("nvidia.com/mig-1g.5gb")] = resource.MustParse(fmt.Sprintf("%d", count))
	return spec
}

var (
	kubeClient   *kubernetes.Clientset
	dasClient    *clientset.Clientset
	emulatedMode instav1.EmulatedMode
)

const (
	testNamespace          = "das-e2e"
	multiTestNamespace     = "das-e2e-multi"
	multiResourceNamespace = "das-e2e-multires"
)

var _ = BeforeSuite(func() {
	//(TODO) Remove the deadcode if this resolves rbac issue while executing e2e test suite in CI
	// cfg, err := rest.InClusterConfig()
	// if err != nil {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		Skip("kubernetes config not available: " + err.Error())
	}
	//}

	// TODO - Remove this warning suppression once the webhook starts working properly
	cfg.WarningHandler = rest.NoWarnings{}
	kubeClient, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		Skip("failed to create kubernetes client: " + err.Error())
	}
	dasClient, err = clientset.NewForConfig(cfg)
	if err != nil {
		Skip("failed to create das client: " + err.Error())
	}
	emulatedModeStr := os.Getenv("EMULATED_MODE")
	if emulatedModeStr == "" {
		emulatedMode = instav1.EmulatedModeDisabled
	} else {
		emulatedMode = instav1.EmulatedMode(emulatedModeStr)
	}
})

var _ = Describe("Test pods for requesting single type of extended resource", Ordered, func() {
	var (
		podNames  []string
		namespace string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = testNamespace

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		podSpec := defaultGPUSlicePodSpec()

		nodes, err := kubeClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())

		podsToCreate := 0
		for _, node := range nodes.Items {
			if q, ok := node.Status.Capacity[corev1.ResourceName("mig.das.com/1g.5gb")]; ok {
				podsToCreate += int(q.Value())
			}
		}

		var pods []*corev1.Pod
		for i := 1; i <= podsToCreate; i++ {
			pods = append(pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-das-%d", i),
					Namespace: namespace,
				},
				Spec: podSpec,
			})
		}

		By(fmt.Sprintf("creating %d test pods", len(pods)))
		Expect(createPods(context.Background(), namespace, pods)).To(Succeed())

		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 2*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (corev1.PodPhase, error) {
				p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				return p.Status.Phase, nil
			}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
		}
	})

	It("should create allocationclaims for each requested GPU slice", func(ctx SpecContext) {
		expected := 0
		for _, name := range podNames {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			for _, c := range p.Spec.Containers {
				if q, ok := c.Resources.Limits[corev1.ResourceName("mig.das.com/1g.5gb")]; ok {
					expected += int(q.Value())
				}
			}
		}

		Eventually(func() (int, error) {
			allocs, err := dasClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			return len(allocs.Items), nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(expected))
	})

	It("should set NVIDIA_VISIBLE_DEVICES env var in each pod", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (bool, error) {
				req := kubeClient.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
				out, err := req.Do(ctx).Raw()
				if err != nil {
					return false, err
				}
				return strings.Contains(string(out), "NVIDIA_VISIBLE_DEVICES="), nil
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		}
	})

	It("should set CUDA_VISIBLE_DEVICES env var in each pod", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (bool, error) {
				req := kubeClient.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
				out, err := req.Do(ctx).Raw()
				if err != nil {
					return false, err
				}
				return strings.Contains(string(out), "CUDA_VISIBLE_DEVICES="), nil
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		}
	})

	It("should keep a new pod pending when the resource is exhausted", func(ctx SpecContext) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-das-overcommit",
				Namespace: namespace,
			},
			Spec: defaultGPUSlicePodSpec(),
		}

		By("creating pod " + pod.Name)
		Expect(createPods(ctx, namespace, []*corev1.Pod{pod})).To(Succeed())

		Consistently(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 25*time.Second, 5*time.Second).Should(Equal(corev1.PodPending))
		By("deleting pod " + podNames[0])
		err := kubeClient.CoreV1().Pods(namespace).Delete(ctx, podNames[0], metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podNames[0], metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 2*time.Minute, time.Second).Should(BeTrue())

		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
	})
})

var _ = Describe("Test pods requesting multiple resources", Ordered, func() {
	var (
		podNames  []string
		namespace string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = multiResourceNamespace

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		podSpec := multiGPUSlicePodSpec(3)

		pods := []*corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-resource-1",
					Namespace: namespace,
				},
				Spec: podSpec,
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-resource-2",
					Namespace: namespace,
				},
				Spec: podSpec,
			},
		}

		By("creating test pods")
		Expect(createPods(context.Background(), namespace, pods)).To(Succeed())

		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 2*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (corev1.PodPhase, error) {
				p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				return p.Status.Phase, nil
			}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
		}
	})

	It("should set NVIDIA_VISIBLE_DEVICES to 3 comma separated values", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (int, error) {
				req := kubeClient.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
				out, err := req.Do(ctx).Raw()
				if err != nil {
					return 0, err
				}
				for _, line := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(line, "NVIDIA_VISIBLE_DEVICES=") {
						val := strings.TrimPrefix(line, "NVIDIA_VISIBLE_DEVICES=")
						return len(strings.Split(strings.TrimSpace(val), ",")), nil
					}
				}
				return 0, fmt.Errorf("env var not found")
			}, 2*time.Minute, 5*time.Second).Should(Equal(3))
		}
	})

	It("should set CUDA_VISIBLE_DEVICES to 3 comma separated values", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (int, error) {
				req := kubeClient.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
				out, err := req.Do(ctx).Raw()
				if err != nil {
					return 0, err
				}
				for _, line := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(line, "CUDA_VISIBLE_DEVICES=") {
						val := strings.TrimPrefix(line, "CUDA_VISIBLE_DEVICES=")
						return len(strings.Split(strings.TrimSpace(val), ",")), nil
					}
				}
				return 0, fmt.Errorf("env var not found")
			}, 2*time.Minute, 5*time.Second).Should(Equal(3))
		}
	})
})

var _ = Describe("Test pods for requesting multiple slice types", Ordered, func() {
	var (
		podNames  []string
		namespace string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = multiTestNamespace

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		insts, err := dasClient.OpenShiftOperatorV1alpha1().NodeAccelerators("das-operator").List(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())

		gpuCount := 0
		for _, inst := range insts.Items {
			var res instav1.DiscoveredNodeResources
			Expect(json.Unmarshal(inst.Status.NodeResources.Raw, &res)).To(Succeed())
			gpuCount += len(res.NodeGPUs)
		}

		var pods []*corev1.Pod

		for i := 1; i <= gpuCount; i++ {
			pods = append(pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("multi-1g-%d", i),
					Namespace: namespace,
				},
				Spec: gpuSlicePodSpec("1g.5gb", emulatedMode),
			})
		}

		for i := 1; i <= gpuCount; i++ {
			pods = append(pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("multi-2g-%d", i),
					Namespace: namespace,
				},
				Spec: gpuSlicePodSpec("2g.10gb", emulatedMode),
			})
		}

		By(fmt.Sprintf("creating %d test pods", len(pods)))
		Expect(createPods(context.Background(), namespace, pods)).To(Succeed())

		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 2*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (corev1.PodPhase, error) {
				p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				return p.Status.Phase, nil
			}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
		}
	})
})

func createPods(ctx context.Context, namespace string, pods []*corev1.Pod) error {
	for _, pod := range pods {
		pod.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
		if _, err := kubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
