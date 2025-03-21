package controller

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Instaslice Controller Metrics", func() {
	var (
		ctx        context.Context
		fakeClient client.Client
		r          *InstasliceReconciler
		instaslice *inferencev1alpha1.Instaslice
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme := runtime.NewScheme()
		Expect(inferencev1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		fakeClient = fake.NewClientBuilder().WithScheme(scheme).Build()
		r = &InstasliceReconciler{Client: fakeClient}

		// Reset metrics before each test to ensure clean state
		instasliceMetrics.processedSlices.Reset()
		instasliceMetrics.deployedPodTotal.Reset()
		instasliceMetrics.compatibleProfiles.Reset()

		instaslice = &inferencev1alpha1.Instaslice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-instaslice",
				Namespace: InstaSliceOperatorNamespace,
			},
			Status: inferencev1alpha1.InstasliceStatus{
				NodeResources: inferencev1alpha1.DiscoveredNodeResources{
					MigPlacement: map[string]inferencev1alpha1.Mig{
						"3g.20gb": {Placements: []inferencev1alpha1.Placement{{Size: 3, Start: 0}}},
						"4g.20gb": {Placements: []inferencev1alpha1.Placement{{Size: 4, Start: 0}}},
						"7g.40gb": {Placements: []inferencev1alpha1.Placement{{Size: 7, Start: 0}}},
					},
				},
			},
		}
		Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
	})

	It("should record processed slices for larger profiles", func() {
		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "GPU-123", "3g.20gb", 3)
		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "GPU-123", "4g.20gb", 4)
		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "GPU-123", "7g.40gb", 7)

		expect := `
# HELP instaslice_total_processed_gpu_slices Number of total processed GPU slices since instaslice controller start time.
# TYPE instaslice_total_processed_gpu_slices gauge
instaslice_total_processed_gpu_slices{gpu_id="GPU-123",node="node-1"} 14
`
		actual := testutil.CollectAndCompare(instasliceMetrics.processedSlices, strings.NewReader(expect), "instaslice_total_processed_gpu_slices")
		Expect(actual).To(Succeed())
	})

	It("should record deployed pod slice count per profile", func() {
		r.UpdateDeployedPodTotalMetrics("node-1", "GPU-123", "default", "test-pod", "4g.20gb", 4)
		expect := `
# HELP instaslice_pod_processed_slices Pods that are processed with their slice/s allocation.
# TYPE instaslice_pod_processed_slices gauge
instaslice_pod_processed_slices{gpu_id="GPU-123",namespace="default",node="node-1",podname="test-pod",profile="4g.20gb"} 4
`
		actual := testutil.CollectAndCompare(instasliceMetrics.deployedPodTotal, strings.NewReader(expect), "instaslice_pod_processed_slices")
		Expect(actual).To(Succeed())
	})

	It("should update compatible profiles metric", func() {
		r.UpdateCompatibleProfilesMetrics(*instaslice, "node-1")
		expect := `
# HELP instaslice_compatible_profiles Profiles compatible with remaining GPU slices in a node and their counts.
# TYPE instaslice_compatible_profiles gauge
instaslice_compatible_profiles{node="node-1",profile="3g.20gb"} 0
instaslice_compatible_profiles{node="node-1",profile="4g.20gb"} 0
instaslice_compatible_profiles{node="node-1",profile="7g.40gb"} 0
`
		actual := testutil.CollectAndCompare(instasliceMetrics.compatibleProfiles, strings.NewReader(expect), "instaslice_compatible_profiles")
		Expect(actual).To(Succeed())
	})
})
