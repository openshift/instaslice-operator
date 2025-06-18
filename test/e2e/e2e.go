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
)

func gpuSlicePodSpec(profile string) corev1.PodSpec {
	return corev1.PodSpec{
		SchedulerName: "das-scheduler",
		Containers: []corev1.Container{
			{
				Name:    "busy",
				Image:   "quay.io/prometheus/busybox",
				Command: []string{"sh", "-c", "env && sleep 3600"},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName(fmt.Sprintf("mig.das.com/%s", profile)): resource.MustParse("1"),
					},
				},
			},
		},
	}
}

func defaultGPUSlicePodSpec() corev1.PodSpec {
	return gpuSlicePodSpec("1g.5gb")
}

var (
	kubeClient *kubernetes.Clientset
	dasClient  *clientset.Clientset
)

const (
	testNamespace      = "das-e2e"
	multiTestNamespace = "das-e2e-multi"
)

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
	dasClient, err = clientset.NewForConfig(cfg)
	if err != nil {
		Skip("failed to create das client: " + err.Error())
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

		for i := 1; i <= podsToCreate; i++ {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-das-%d", i),
					Namespace: namespace,
				},
				Spec: podSpec,
			}

			By("creating test pod " + pod.Name)
			_, err := kubeClient.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			podNames = append(podNames, pod.Name)
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
			}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
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

	It("should set MIG_UUID env var in each pod", func(ctx SpecContext) {
		for _, name := range podNames {
			Eventually(func() (bool, error) {
				req := kubeClient.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
				out, err := req.Do(ctx).Raw()
				if err != nil {
					return false, err
				}
				return strings.Contains(string(out), "MIG_UUID="), nil
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
		_, err := kubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Consistently(func() (corev1.PodPhase, error) {
			p, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return p.Status.Phase, nil
		}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodPending))
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

		pods1g := gpuCount * 2
		pods2g := gpuCount * 2

		var pods []*corev1.Pod

		for i := 1; i <= pods1g; i++ {
			pods = append(pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("multi-1g-%d", i),
					Namespace: namespace,
				},
				Spec: gpuSlicePodSpec("1g.5gb"),
			})
		}

		for i := 1; i <= pods2g; i++ {
			pods = append(pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("multi-2g-%d", i),
					Namespace: namespace,
				},
				Spec: gpuSlicePodSpec("2g.10gb"),
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
			}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodRunning))
		}
	})

	It("should keep new pods pending when all slice types are exhausted", func(ctx SpecContext) {
		pods := []*corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-over-2g",
					Namespace: namespace,
				},
				Spec: gpuSlicePodSpec("2g.10gb"),
			},
		}

		By("creating overcommit pods")
		Expect(createPods(ctx, namespace, pods)).To(Succeed())

		for _, p := range pods {
			Consistently(func() (corev1.PodPhase, error) {
				pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, p.Name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				return pod.Status.Phase, nil
			}, 2*time.Minute, 5*time.Second).Should(Equal(corev1.PodPending))
		}
	})
})

func createPods(ctx context.Context, namespace string, pods []*corev1.Pod) error {
	for _, pod := range pods {
		if _, err := kubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
