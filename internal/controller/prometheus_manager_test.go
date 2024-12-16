package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Instaslice Controller", func() {
	var (
		fakeClient client.Client
		r          *InstasliceReconciler
	)

	BeforeEach(func() {
		scheme := runtime.NewScheme()
		Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(v1.AddToScheme(scheme)).To(Succeed())

		fakeClient = fake.NewClientBuilder().WithScheme(scheme).Build()

		r = &InstasliceReconciler{
			Client: fakeClient,
		}
	})

	// Test IncrementTotalProcessedGpuSliceMetrics
	It("should increment total processed GPU slice metrics", func() {
		err := r.IncrementTotalProcessedGpuSliceMetrics("node-1", "gpu-1", 4, 0, "1g.5gb")
		Expect(err).ToNot(HaveOccurred())
	})

	// Test UpdateGpuSliceMetrics
	It("should update GPU slice metrics", func() {
		err := r.UpdateGpuSliceMetrics("node-1", "gpu-1", 3, 5)
		Expect(err).ToNot(HaveOccurred())
	})

	// Test UpdateDeployedPodTotalMetrics
	It("should update deployed pod total metrics", func() {
		err := r.UpdateDeployedPodTotalMetrics("node-1", "gpu-1", "namespace-1", "pod-1", "profile-1", 2)
		Expect(err).ToNot(HaveOccurred())
	})

	// Test UpdateCompatibleProfilesMetrics
	It("should update compatible profiles metrics", func() {
		instaslice := inferencev1alpha1.Instaslice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "instaslice-1",
				Namespace: "default",
			},
		}
		err := r.UpdateCompatibleProfilesMetrics(instaslice, "node-1", map[string]int32{"gpu-1": 6})
		Expect(err).ToNot(HaveOccurred())
	})
})
