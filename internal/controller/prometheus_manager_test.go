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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Instaslice Controller Metrics", func() {
	var (
		ctx        context.Context
		fakeClient client.Client
		r          *InstasliceReconciler
		instaslice *inferencev1alpha1.Instaslice
		pod        *v1.Pod
		podUUID    string
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

		podUUID = "test-pod-uuid-1"

		pod = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: InstaSliceOperatorNamespace,
				UID:       types.UID(podUUID),
				Finalizers: []string{
					FinalizerName,
				},
			},
		}

		Expect(fakeClient.Create(ctx, pod)).To(Succeed())
		instaslice = &inferencev1alpha1.Instaslice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-instaslice",
				Namespace: InstaSliceOperatorNamespace,
			},
			Spec: inferencev1alpha1.InstasliceSpec{
				PodAllocationRequests: map[types.UID]inferencev1alpha1.AllocationRequest{
					types.UID(podUUID): {
						Profile: "1g.5gb",
						PodRef: v1.ObjectReference{
							Name:      pod.Name,
							Namespace: InstaSliceOperatorNamespace,
							UID:       pod.UID,
						},
						Resources: v1.ResourceRequirements{},
					},
				},
			},
			Status: inferencev1alpha1.InstasliceStatus{
				NodeResources: inferencev1alpha1.DiscoveredNodeResources{
					MigPlacement: map[string]inferencev1alpha1.Mig{
						"1g.5gb": {Placements: []inferencev1alpha1.Placement{
							{Size: 1, Start: 0}, {Size: 1, Start: 1}, {Size: 1, Start: 2}, {Size: 1, Start: 3},
							{Size: 1, Start: 4}, {Size: 1, Start: 5}, {Size: 1, Start: 6},
						}},
						"1g.10gb": {Placements: []inferencev1alpha1.Placement{
							{Size: 2, Start: 0}, {Size: 2, Start: 2}, {Size: 2, Start: 4}, {Size: 2, Start: 6},
						}},
						"1g.5gb+me": {Placements: []inferencev1alpha1.Placement{
							{Size: 1, Start: 0}, {Size: 1, Start: 1}, {Size: 1, Start: 2}, {Size: 1, Start: 3},
							{Size: 1, Start: 4}, {Size: 1, Start: 5}, {Size: 1, Start: 6},
						}},
						"2g.10gb": {Placements: []inferencev1alpha1.Placement{
							{Size: 2, Start: 0}, {Size: 2, Start: 2}, {Size: 2, Start: 4},
						}},
						"3g.20gb": {Placements: []inferencev1alpha1.Placement{
							{Size: 4, Start: 0}, {Size: 4, Start: 4},
						}},
						"4g.20gb": {Placements: []inferencev1alpha1.Placement{{Size: 4, Start: 0}}},
						"7g.40gb": {Placements: []inferencev1alpha1.Placement{{Size: 8, Start: 0}}},
					},
					NodeGPUs: []inferencev1alpha1.DiscoveredGPU{
						{GPUUUID: "gpu1", GPUName: "uuid1"},
						{GPUUUID: "gpu2", GPUName: "uuid2"},
					},
				},
				PodAllocationResults: map[types.UID]inferencev1alpha1.AllocationResult{
					types.UID(podUUID): {
						AllocationStatus: inferencev1alpha1.AllocationStatus{
							AllocationStatusController: inferencev1alpha1.AllocationStatusUngated,
							AllocationStatusDaemonset:  inferencev1alpha1.AllocationStatusCreated},
						GPUUUID:                     "GPU-1",
						Nodename:                    "node-1",
						ConfigMapResourceIdentifier: "fake-configmap-uid",
						MigPlacement:                inferencev1alpha1.Placement{Start: 0, Size: 1},
					},
				},
			},
		}
		Expect(fakeClient.Create(ctx, instaslice)).To(Succeed())
	})

	It("should record processed slices for larger profiles", func() {
		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "GPU-1", "3g.20gb", 3)
		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "GPU-1", "4g.20gb", 4)
		r.IncrementTotalProcessedGpuSliceMetrics("node-1", "GPU-1", "7g.40gb", 7)

		expect := `
# HELP instaslice_total_processed_gpu_slices Number of total processed GPU slices since instaslice controller start time.
# TYPE instaslice_total_processed_gpu_slices gauge
instaslice_total_processed_gpu_slices{gpu_id="GPU-1",node="node-1"} 14
`
		actual := testutil.CollectAndCompare(instasliceMetrics.processedSlices, strings.NewReader(expect), "instaslice_total_processed_gpu_slices")
		Expect(actual).To(Succeed())
	})

	It("should record deployed pod slice count per profile", func() {
		r.UpdateDeployedPodTotalMetrics("node-1", "GPU-1", "default", "test-pod", "4g.20gb", 4)
		expect := `
# HELP instaslice_pod_processed_slices Pods that are processed with their slice/s allocation.
# TYPE instaslice_pod_processed_slices gauge
instaslice_pod_processed_slices{gpu_id="GPU-1",namespace="default",node="node-1",podname="test-pod",profile="4g.20gb"} 4
`
		actual := testutil.CollectAndCompare(instasliceMetrics.deployedPodTotal, strings.NewReader(expect), "instaslice_pod_processed_slices")
		Expect(actual).To(Succeed())
	})

	// for clean slate node with 2 gpus
	It("should reflect reduced compatibility after 1g.5gb pod allocation", func() {
		// simulate pod allocation in memory
		r.allocationCache = map[types.UID]inferencev1alpha1.AllocationResult{
			types.UID(podUUID): {
				GPUUUID:      "gpu1",
				Nodename:     "node-1",
				MigPlacement: inferencev1alpha1.Placement{Start: 0, Size: 1},
				AllocationStatus: inferencev1alpha1.AllocationStatus{
					AllocationStatusDaemonset:  inferencev1alpha1.AllocationStatusCreated,
					AllocationStatusController: inferencev1alpha1.AllocationStatusUngated,
				},
			},
		}
		r.UpdateCompatibleProfilesMetrics(*instaslice, "node-1")
		expect := `
# HELP instaslice_compatible_profiles Profiles compatible with remaining GPU slices in a node and their counts.
# TYPE instaslice_compatible_profiles gauge
instaslice_compatible_profiles{node="node-1",profile="1g.10gb"} 7
instaslice_compatible_profiles{node="node-1",profile="1g.5gb"} 13
instaslice_compatible_profiles{node="node-1",profile="1g.5gb+me"} 13
instaslice_compatible_profiles{node="node-1",profile="2g.10gb"} 5
instaslice_compatible_profiles{node="node-1",profile="3g.20gb"} 3
instaslice_compatible_profiles{node="node-1",profile="4g.20gb"} 1
instaslice_compatible_profiles{node="node-1",profile="7g.40gb"} 1
`
		actual := testutil.CollectAndCompare(instasliceMetrics.compatibleProfiles, strings.NewReader(expect), "instaslice_compatible_profiles")
		Expect(actual).To(Succeed())
	})
})
