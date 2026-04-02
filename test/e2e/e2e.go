package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	operatorv1 "github.com/openshift/api/operator/v1"
	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	clientset "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"

	appsv1 "k8s.io/api/apps/v1"
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
		image = "quay.io/dasoperator/cuda-sample:vectoradd-cuda12.5.0-ubi"
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
	dasOperatorNamespace    = "das-operator"
	testNamespace           = "das-e2e"
	multiTestNamespace      = "das-e2e-multi"
	multiResourceNamespace  = "das-e2e-multires"
	deploymentTestNamespace = "das-e2e-deploy"
	webhookServiceName      = "das-operator-webhook"
	webhookPort             = int32(8443)
)

var _ = BeforeSuite(func() {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		Skip("kubernetes config not available: " + err.Error())
	}

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

		Expect(waitForServiceReady(context.Background(), dasOperatorNamespace, webhookServiceName, 2*time.Minute)).To(Succeed())
		By(fmt.Sprintf("creating %d test pods", len(pods)))
		Expect(createPods(context.Background(), namespace, pods)).To(Succeed())
		Expect(waitForPodsReady(context.Background(), namespace, pods, 2*time.Minute)).To(Succeed())

		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
		Expect(podNames).NotTo(BeEmpty())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
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
		Expect(podNames).NotTo(BeEmpty())
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
			allocs, err := dasClient.OpenShiftOperatorV1alpha1().AllocationClaims(dasOperatorNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			return len(allocs.Items), nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(expected))
	})

	It("should set NVIDIA_VISIBLE_DEVICES and CUDA_VISIBLE_DEVICES env vars in each pod", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
		for _, name := range podNames {
			Eventually(func() (bool, error) {
				return verifyVisibleDevicesEnv(ctx, namespace, name, 0)
			}, 5*time.Minute, 5*time.Second).Should(BeTrue())
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
		Expect(podNames).NotTo(BeEmpty())
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

var _ = Describe("Test deployment requesting single type of extended resource", Ordered, func() {
	var (
		podNames   []string
		namespace  string
		deployName string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = deploymentTestNamespace
		deployName = "cuda-vectoradd"

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: namespace},
			Spec: appsv1.DeploymentSpec{
				Replicas: pointer.Int32(2),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": deployName}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": deployName}},
					Spec: func() corev1.PodSpec {
						spec := defaultGPUSlicePodSpec()
						spec.RestartPolicy = corev1.RestartPolicyAlways
						return spec
					}(),
				},
			},
		}

		By("creating deployment")
		_, err = kubeClient.AppsV1().Deployments(namespace).Create(context.Background(), dep, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() ([]string, error) {
			pl, err := kubeClient.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: "app=" + deployName})
			if err != nil {
				return nil, err
			}
			if len(pl.Items) < 2 {
				return nil, fmt.Errorf("waiting")
			}
			names := make([]string, len(pl.Items))
			for i := range pl.Items {
				names[i] = pl.Items[i].Name
			}
			podNames = names
			return names, nil
		}, 2*time.Minute, 5*time.Second).Should(HaveLen(2))
		Expect(podNames).NotTo(BeEmpty())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
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
		Expect(podNames).NotTo(BeEmpty())
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
			allocs, err := dasClient.OpenShiftOperatorV1alpha1().AllocationClaims(dasOperatorNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}
			return len(allocs.Items), nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(expected))
	})

	It("should set NVIDIA_VISIBLE_DEVICES and CUDA_VISIBLE_DEVICES env vars in each pod", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
		for _, name := range podNames {
			Eventually(func() (bool, error) {
				return verifyVisibleDevicesEnv(ctx, namespace, name, 0)
			}, 5*time.Minute, 5*time.Second).Should(BeTrue())
		}
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
		Expect(podNames).NotTo(BeEmpty())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
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

	It("should set NVIDIA_VISIBLE_DEVICES and CUDA_VISIBLE_DEVICES to 3 comma separated values", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
		for _, name := range podNames {
			Eventually(func() (bool, error) {
				return verifyVisibleDevicesEnv(ctx, namespace, name, 3)
			}, 5*time.Minute, 5*time.Second).Should(BeTrue())
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

		insts, err := dasClient.OpenShiftOperatorV1alpha1().NodeAccelerators(dasOperatorNamespace).List(context.Background(), metav1.ListOptions{})
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
		Expect(waitForServiceReady(context.Background(), dasOperatorNamespace, webhookServiceName, 2*time.Minute)).To(Succeed())
		By(fmt.Sprintf("creating %d test pods", len(pods)))
		Expect(createPods(context.Background(), namespace, pods)).To(Succeed())
		Expect(waitForPodsReady(context.Background(), namespace, pods, 2*time.Minute)).To(Succeed())

		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}
		Expect(podNames).NotTo(BeEmpty())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Expect(podNames).NotTo(BeEmpty())
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

// waitForPodsReady waits until all specified Pods are in Running and Ready state
func waitForPodsReady(ctx context.Context, namespace string, pods []*corev1.Pod, timeout time.Duration) error {
	for _, pod := range pods {
		podName := pod.Name
		var lastErr error
		Eventually(func() bool {
			currentPod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				lastErr = err
				return false
			}
			if currentPod.Status.Phase != corev1.PodRunning {
				return false
			}
			for _, cond := range currentPod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, timeout, 2*time.Second).Should(BeTrue(), fmt.Sprintf("pod %s did not become ready: %v", podName, lastErr))
	}
	return nil
}

// waitForServiceReady waits until the specified service(related endpoints) is Ready
// (TODO) This is not required if in future, the operator has a correct, updated status to rely on
func waitForServiceReady(ctx context.Context, namespace string, svc string, timeout time.Duration) error {
	var lastErr error
	Eventually(func() bool {
		endpoints, err := kubeClient.CoreV1().Endpoints(namespace).Get(context.Background(), svc, metav1.GetOptions{})
		if err != nil {
			lastErr = err
			return false
		}
		if len(endpoints.Subsets) == 0 {
			return false
		}
		readyAddressesCount := 0
		foundDesiredPort := false
		for _, subset := range endpoints.Subsets {
			readyAddressesCount += len(subset.Addresses)
			for _, port := range subset.Ports {
				if port.Port == webhookPort && port.Protocol == corev1.ProtocolTCP {
					foundDesiredPort = true
				}
			}
		}
		if readyAddressesCount > 0 && foundDesiredPort {
			return true
		}
		return false
	}, timeout, 2*time.Second).Should(BeTrue(), fmt.Sprintf("Webhook service %s not yet ready: %v", svc, lastErr))
	return nil
}

func verifyVisibleDevicesEnv(ctx context.Context, namespace, podName string, expectedCount int) (bool, error) {
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	var nvidia, cuda string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "NVIDIA_VISIBLE_DEVICES=") {
			nvidia = strings.TrimPrefix(line, "NVIDIA_VISIBLE_DEVICES=")
		}
		if strings.HasPrefix(line, "CUDA_VISIBLE_DEVICES=") {
			cuda = strings.TrimPrefix(line, "CUDA_VISIBLE_DEVICES=")
		}
	}

	if nvidia == "" || cuda == "" {
		return false, nil
	}

	// CUDA_VISIBLE_DEVICES should be indexed values (0,1,2...) while NVIDIA_VISIBLE_DEVICES contains UUIDs
	nvidiaDevices := strings.Split(strings.TrimSpace(nvidia), ",")
	cudaDevices := strings.Split(strings.TrimSpace(cuda), ",")

	// Both should have the same number of devices
	if len(nvidiaDevices) != len(cudaDevices) {
		return false, nil
	}

	// CUDA_VISIBLE_DEVICES should contain indexed values (0, 1, 2, ...)
	for i, cudaDevice := range cudaDevices {
		expectedIndex := strconv.Itoa(i)
		if strings.TrimSpace(cudaDevice) != expectedIndex {
			return false, nil
		}
	}

	if expectedCount > 0 {
		if len(nvidiaDevices) != expectedCount {
			return false, nil
		}
	}

	return true, nil
}

var _ = Describe("MIG placement start index", Ordered, func() {
	var (
		namespace string
		podNames  []string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = "das-e2e-start0"

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

		if gpuCount < 2 {
			Skip(fmt.Sprintf("need at least 2 GPUs, found %d", gpuCount))
		}

		pods := []*corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "start0-1", Namespace: namespace},
				Spec:       gpuSlicePodSpec("1g.5gb", emulatedMode),
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "start0-2", Namespace: namespace},
				Spec:       gpuSlicePodSpec("1g.5gb", emulatedMode),
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
		}, 5*time.Minute, time.Second).Should(BeTrue())
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

	It("should have valid MIG placements in AllocationClaims", func(ctx SpecContext) {
		// This test verifies that each pod gets a valid MIG placement.
		// When pods are scheduled on different GPUs (parallel scheduling with staged claims),
		// both should have start=0. When pods are scheduled on the same GPU (sequential
		// scheduling), they should have unique start indices (e.g., 0 and 1).
		Eventually(func() (bool, error) {
			allocs, err := dasClient.OpenShiftOperatorV1alpha1().AllocationClaims("das-operator").List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err
			}

			gpuPlacements := make(map[string][]int32)
			found := 0
			for _, alloc := range allocs.Items {
				var spec instav1.AllocationClaimSpec
				if err := json.Unmarshal(alloc.Spec.Raw, &spec); err != nil {
					continue
				}

				gpuPlacements[spec.GPUUUID] = append(gpuPlacements[spec.GPUUUID], spec.MigPlacement.Start)
				found++
			}

			if found != len(podNames) {
				return false, nil
			}

			// Validate: start indices must be unique per GPU.
			for _, starts := range gpuPlacements {
				seen := make(map[int32]bool)
				for _, start := range starts {
					if seen[start] {
						return false, nil
					}
					seen[start] = true
				}
			}

			return true, nil
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})
})

var _ = Describe("MIG UUID verification", Ordered, func() {
	var (
		namespace string
		podName   string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = "das-e2e-mig-uuid"
		podName = "mig-uuid-test-pod"

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
			},
			Spec: migUuidPodSpec(),
		}

		By("creating MIG UUID test pod")
		Expect(createPods(context.Background(), namespace, []*corev1.Pod{pod})).To(Succeed())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
	})

	It("should have NVIDIA_VISIBLE_DEVICES matching MIG_SLICE_UUID", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyMigUuidMatches(ctx, namespace, podName)
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	It("should successfully run vector addition", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyVectorAddSuccess(ctx, namespace, podName, "")
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})
})

var _ = Describe("MIG UUID dual slice verification", Ordered, func() {
	var (
		namespace string
		podName   string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = "das-e2e-mig-uuid-dual"
		podName = "mig-uuid-dual-test-pod"

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
			},
			Spec: migUuidDualPodSpec(),
		}

		By("creating MIG UUID dual slice test pod")
		Expect(createPods(context.Background(), namespace, []*corev1.Pod{pod})).To(Succeed())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
	})

	It("should report 2 CUDA devices", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyDualSliceCudaDeviceCount(ctx, namespace, podName)
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	It("should have NVIDIA_VISIBLE_DEVICES matching MIG device UUIDs", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyDualSliceMigUuidMatches(ctx, namespace, podName)
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	It("should successfully run vector addition on both devices", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyDualSliceVectorAddSuccess(ctx, namespace, podName)
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})
})

var _ = Describe("MIG UUID multi-container verification", Ordered, func() {
	var (
		namespace string
		podName   string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = "das-e2e-mig-uuid-multicontainer"
		podName = "mig-uuid-multicontainer-test-pod"

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
			},
			Spec: migUuidMulticontainerPodSpec(),
		}

		By("creating MIG UUID multi-container test pod")
		Expect(createPods(context.Background(), namespace, []*corev1.Pod{pod})).To(Succeed())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should be running", func(ctx SpecContext) {
		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
	})

	It("should have NVIDIA_VISIBLE_DEVICES matching MIG_SLICE_UUID in gpu-a container", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyMulticontainerMigUuidMatches(ctx, namespace, podName, "gpu-a")
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	It("should have NVIDIA_VISIBLE_DEVICES matching MIG_SLICE_UUID in gpu-b container", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyMulticontainerMigUuidMatches(ctx, namespace, podName, "gpu-b")
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	It("should successfully run vector addition in gpu-a container", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyVectorAddSuccess(ctx, namespace, podName, "gpu-a")
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	It("should successfully run vector addition in gpu-b container", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			return verifyVectorAddSuccess(ctx, namespace, podName, "gpu-b")
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})
})

var _ = Describe("Operator status", Ordered, func() {
	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	It("should report correct readyReplicas", func(ctx SpecContext) {
		By("getting the DAS operator deployment")
		operatorDeployment, err := kubeClient.AppsV1().Deployments(dasOperatorNamespace).Get(ctx, "das-operator", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(operatorDeployment).NotTo(BeNil())

		expectedReadyReplicas := operatorDeployment.Status.ReadyReplicas
		By(fmt.Sprintf("expecting %d ready replicas from deployment status", expectedReadyReplicas))

		By("checking DAS operator CR status")
		Eventually(func() (int32, error) {
			dasOperator, err := dasClient.OpenShiftOperatorV1alpha1().DASOperators(dasOperatorNamespace).Get(ctx, "cluster", metav1.GetOptions{})
			if err != nil {
				return 0, err
			}
			return dasOperator.Status.ReadyReplicas, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(expectedReadyReplicas))

		By("verifying operator CR status conditions are healthy")
		Eventually(func() (bool, error) {
			dasOperator, err := dasClient.OpenShiftOperatorV1alpha1().DASOperators(dasOperatorNamespace).Get(ctx, "cluster", metav1.GetOptions{})
			if err != nil {
				return false, err
			}

			availableCondition := findCondition(dasOperator.Status.Conditions, "Available")
			if availableCondition == nil || availableCondition.Status != "True" {
				return false, fmt.Errorf("Available condition not True: %v", availableCondition)
			}

			progressingCondition := findCondition(dasOperator.Status.Conditions, "Progressing")
			if progressingCondition == nil || progressingCondition.Status != "False" {
				return false, fmt.Errorf("Progressing condition not False: %v", progressingCondition)
			}

			degradedCondition := findCondition(dasOperator.Status.Conditions, "Degraded")
			if degradedCondition == nil || degradedCondition.Status != "False" {
				return false, fmt.Errorf("Degraded condition not False: %v", degradedCondition)
			}

			return true, nil
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})
})

var _ = Describe("Operator NetworkPolicies", Ordered, func() {
	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	It("should create all expected NetworkPolicy resources", func(ctx SpecContext) {
		expectedPolicies := []string{
			"das-deny-all",
			"das-allow-egress-kube-apiserver",
			"das-allow-egress-cluster-dns",
			"das-allow-ingress-webhook",
		}

		By("listing NetworkPolicies in das-operator namespace")
		Eventually(func() ([]string, error) {
			netPolicies, err := kubeClient.NetworkingV1().NetworkPolicies(dasOperatorNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			var names []string
			for _, np := range netPolicies.Items {
				names = append(names, np.Name)
			}
			return names, nil
		}, 2*time.Minute, 5*time.Second).Should(ContainElements(expectedPolicies))
	})

	It("should reconcile deleted NetworkPolicy", func(ctx SpecContext) {
		By("deleting the deny-all NetworkPolicy")
		err := kubeClient.NetworkingV1().NetworkPolicies(dasOperatorNamespace).Delete(ctx, "das-deny-all", metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the operator to recreate it")
		Eventually(func() error {
			_, err := kubeClient.NetworkingV1().NetworkPolicies(dasOperatorNamespace).Get(ctx, "das-deny-all", metav1.GetOptions{})
			return err
		}, 5*time.Minute, 5*time.Second).Should(Succeed())
	})
})

func verifyMigUuidMatches(ctx context.Context, namespace, podName string) (bool, error) {
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	var nvidia, migUuid string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "NVIDIA_VISIBLE_DEVICES=") {
			nvidia = strings.TrimPrefix(line, "NVIDIA_VISIBLE_DEVICES=")
		}
		if strings.HasPrefix(line, "MIG_SLICE_UUID=") {
			migUuid = strings.TrimPrefix(line, "MIG_SLICE_UUID=")
		}
	}

	if nvidia == "" || migUuid == "" {
		return false, nil
	}

	// NVIDIA_VISIBLE_DEVICES should match the MIG_SLICE_UUID
	return strings.TrimSpace(nvidia) == strings.TrimSpace(migUuid), nil
}

func verifyDualSliceCudaDeviceCount(ctx context.Context, namespace, podName string) (bool, error) {
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "CUDA_DEVICE_COUNT=") {
			countStr := strings.TrimPrefix(line, "CUDA_DEVICE_COUNT=")
			count := strings.TrimSpace(countStr)
			return count == "2", nil
		}
	}
	return false, nil
}

func verifyDualSliceMigUuidMatches(ctx context.Context, namespace, podName string) (bool, error) {
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	var nvidia string
	var migDevice0UUID, migDevice1UUID string

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "NVIDIA_VISIBLE_DEVICES=") {
			nvidia = strings.TrimPrefix(line, "NVIDIA_VISIBLE_DEVICES=")
		}
		if strings.HasPrefix(line, "MIG_DEVICE_0_UUID=") {
			migDevice0UUID = strings.TrimPrefix(line, "MIG_DEVICE_0_UUID=")
		}
		if strings.HasPrefix(line, "MIG_DEVICE_1_UUID=") {
			migDevice1UUID = strings.TrimPrefix(line, "MIG_DEVICE_1_UUID=")
		}
	}

	if nvidia == "" || migDevice0UUID == "" || migDevice1UUID == "" {
		return false, nil
	}

	// NVIDIA_VISIBLE_DEVICES should contain both MIG device UUIDs, comma-separated
	nvidiaDevices := strings.Split(strings.TrimSpace(nvidia), ",")
	if len(nvidiaDevices) != 2 {
		return false, nil
	}

	// Check that both UUIDs are present in NVIDIA_VISIBLE_DEVICES
	foundDevice0 := false
	foundDevice1 := false
	for _, device := range nvidiaDevices {
		trimmedDevice := strings.TrimSpace(device)
		if trimmedDevice == strings.TrimSpace(migDevice0UUID) {
			foundDevice0 = true
		}
		if trimmedDevice == strings.TrimSpace(migDevice1UUID) {
			foundDevice1 = true
		}
	}

	return foundDevice0 && foundDevice1, nil
}

func verifyDualSliceVectorAddSuccess(ctx context.Context, namespace, podName string) (bool, error) {
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	// Check that both devices (0 and 1) have successful vector addition
	dev0Success := false
	dev1Success := false

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "VECTOR_ADD_STATUS_DEV_0=") {
			status := strings.TrimPrefix(line, "VECTOR_ADD_STATUS_DEV_0=")
			dev0Success = strings.TrimSpace(status) == "OK"
		}
		if strings.HasPrefix(line, "VECTOR_ADD_STATUS_DEV_1=") {
			status := strings.TrimPrefix(line, "VECTOR_ADD_STATUS_DEV_1=")
			dev1Success = strings.TrimSpace(status) == "OK"
		}
	}

	return dev0Success && dev1Success, nil
}

func verifyMulticontainerMigUuidMatches(ctx context.Context, namespace, podName, containerName string) (bool, error) {
	req := kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName})
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	var nvidia, migUuid string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "NVIDIA_VISIBLE_DEVICES=") {
			nvidia = strings.TrimPrefix(line, "NVIDIA_VISIBLE_DEVICES=")
		}
		if strings.HasPrefix(line, "MIG_SLICE_UUID=") {
			migUuid = strings.TrimPrefix(line, "MIG_SLICE_UUID=")
		}
	}

	if nvidia == "" || migUuid == "" {
		return false, nil
	}

	// NVIDIA_VISIBLE_DEVICES should match the MIG_SLICE_UUID for each container
	return strings.TrimSpace(nvidia) == strings.TrimSpace(migUuid), nil
}

// findCondition finds a condition in a list of conditions by type.
func findCondition(conditions []operatorv1.OperatorCondition, conditionType string) *operatorv1.OperatorCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

var _ = Describe("GPU memory resource capacity", Ordered, func() {
	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	It("should advertise gpu.das.openshift.io/mem capacity on nodes with GPUs", func(ctx SpecContext) {
		Eventually(func() (int, error) {
			nodes, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}

			gpuMemResourceName := corev1.ResourceName("gpu.das.openshift.io/mem")
			nodesWithGPUMem := 0

			for _, node := range nodes.Items {
				if q, ok := node.Status.Capacity[gpuMemResourceName]; ok {
					By(fmt.Sprintf("node %s has gpu.das.openshift.io/mem capacity: %s", node.Name, q.String()))
					if q.Value() > 0 {
						nodesWithGPUMem++
					}
				}
			}
			return nodesWithGPUMem, nil
		}, 2*time.Minute, 5*time.Second).Should(BeNumerically(">", 0), "at least one node should advertise gpu.das.openshift.io/mem")
	})

	It("should have NodeAccelerator resources with GPU information", func(ctx SpecContext) {
		Eventually(func() (int, error) {
			nodeAccels, err := dasClient.OpenShiftOperatorV1alpha1().NodeAccelerators(dasOperatorNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return 0, err
			}

			validAccelerators := 0
			for _, accel := range nodeAccels.Items {
				if len(accel.Status.NodeResources.Raw) == 0 {
					continue
				}

				var discovered instav1.DiscoveredNodeResources
				if err := json.Unmarshal(accel.Status.NodeResources.Raw, &discovered); err != nil {
					continue
				}

				if len(discovered.NodeGPUs) > 0 && len(discovered.MigPlacement) > 0 {
					By(fmt.Sprintf("NodeAccelerator %s has %d GPUs and %d MIG profiles",
						accel.Name, len(discovered.NodeGPUs), len(discovered.MigPlacement)))
					validAccelerators++
				}
			}
			return validAccelerators, nil
		}, 2*time.Minute, 5*time.Second).Should(BeNumerically(">", 0), "at least one NodeAccelerator should have GPUs")
	})
})

var _ = Describe("MIG profiles annotation scheduling", Ordered, func() {
	var (
		namespace string
		podName   string
	)

	BeforeAll(func() {
		if os.Getenv("KUBECONFIG") == "" {
			Skip("KUBECONFIG is not set; skipping e2e test")
		}
	})

	BeforeAll(func() {
		namespace = "das-e2e-mig-annotation"
		podName = "mig-annotation-pod"

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		By("creating namespace " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}

		// Create a pod that uses the das.openshift.io/mig-profiles annotation
		// instead of nvidia.com/mig-* resource requests (simulating Kueue-transformed pod)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: namespace,
				Annotations: map[string]string{
					"das.openshift.io/mig-profiles": "1g.5gb:1",
				},
			},
			Spec: corev1.PodSpec{
				SchedulerName: "das-scheduler",
				Containers: []corev1.Container{
					{
						Name:    "test",
						Image:   "quay.io/prometheus/busybox",
						Command: []string{"sh", "-c", "env && sleep 3600"},
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceName("gpu.das.openshift.io/mem"): resource.MustParse("5"),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: pointer.Bool(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyOnFailure,
			},
		}

		Expect(waitForServiceReady(context.Background(), dasOperatorNamespace, webhookServiceName, 2*time.Minute)).To(Succeed())
		By("creating test pod with mig-profiles annotation")
		Expect(createPods(context.Background(), namespace, []*corev1.Pod{pod})).To(Succeed())
	})

	AfterAll(func() {
		By("deleting namespace " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, time.Second).Should(BeTrue())
	})

	It("should schedule pod using mig-profiles annotation", func(ctx SpecContext) {
		Eventually(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 60*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
	})

	It("should create AllocationClaim for pod with mig-profiles annotation", func(ctx SpecContext) {
		Eventually(func() (bool, error) {
			allocs, err := dasClient.OpenShiftOperatorV1alpha1().AllocationClaims(dasOperatorNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err
			}

			for _, alloc := range allocs.Items {
				var spec instav1.AllocationClaimSpec
				if err := json.Unmarshal(alloc.Spec.Raw, &spec); err != nil {
					continue
				}

				if spec.PodRef.Name == podName && spec.PodRef.Namespace == namespace {
					By(fmt.Sprintf("found AllocationClaim %s with profile %s", alloc.Name, spec.Profile))
					// Verify the profile matches what was in the annotation
					if spec.Profile == "1g.5gb" {
						return true, nil
					}
				}
			}
			return false, nil
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})
})
