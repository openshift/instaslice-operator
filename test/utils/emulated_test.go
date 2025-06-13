package utils

import (
	"encoding/json"
	"testing"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
)

type genCase struct {
	name    string
	fn      func(string) *instav1.NodeAccelerator
	gpuName string
	mem     string
	profile string
}

var generators = []genCase{
	{"a100-pcie-40", GenerateFakeCapacityA100PCIE40GB, "NVIDIA A100-PCIE-40GB", "40Gi", "1g.5gb"},
	{"a100-pcie-80", GenerateFakeCapacityA100PCIE80GB, "NVIDIA A100-PCIE-80GB", "80Gi", "1g.10gb"},
	{"a100-sxm4-40", GenerateFakeCapacityA100SXM440GB, "NVIDIA A100-SXM4-40GB", "40Gi", "1g.5gb"},
	{"a100-sxm4-80", GenerateFakeCapacityA100SXM480GB, "NVIDIA A100-SXM4-80GB", "80Gi", "1g.10gb"},
	{"h100-sxm5-80", GenerateFakeCapacityH100SXM580GB, "NVIDIA H100-SXM5-80GB", "80Gi", "1g.10gb"},
	{"h100-pcie-80", GenerateFakeCapacityH100PCIE80GB, "NVIDIA H100-PCIE-80GB", "80Gi", "1g.10gb"},
	{"h100-sxm5-94", GenerateFakeCapacityH100SXM594GB, "NVIDIA H100-SXM5-94GB", "94Gi", "1g.10gb"},
	{"h100-pcie-94", GenerateFakeCapacityH100PCIE94GB, "NVIDIA H100-PCIE-94GB", "94Gi", "1g.10gb"},
	{"h100-gh200-96", GenerateFakeCapacityH100GH20096GB, "NVIDIA H100-GH200-96GB", "96Gi", "1g.10gb"},
	{"h200-sxm5-141", GenerateFakeCapacityH200SXM5141GB, "NVIDIA H200-SXM5-141GB", "141Gi", "1g.18gb"},
	{"a30-24", GenerateFakeCapacityA30PCIE24GB, "NVIDIA A30-24GB", "24Gi", "1g.6gb"},
}

func TestGenerateFakeCapacityVariants(t *testing.T) {
	for _, tc := range generators {
		t.Run(tc.name, func(t *testing.T) {
			inst := tc.fn("node1")
			var res instav1.DiscoveredNodeResources
			if err := json.Unmarshal(inst.Status.NodeResources.Raw, &res); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(res.NodeGPUs) != 2 {
				t.Fatalf("expected 2 GPUs, got %d", len(res.NodeGPUs))
			}
			for _, g := range res.NodeGPUs {
				if g.GPUName != tc.gpuName {
					t.Errorf("expected GPUName %s, got %s", tc.gpuName, g.GPUName)
				}
				if g.GPUMemory.String() != tc.mem {
					t.Errorf("expected memory %s, got %s", tc.mem, g.GPUMemory.String())
				}
			}
			if _, ok := res.MigPlacement[tc.profile]; !ok {
				t.Errorf("expected profile %s in placement", tc.profile)
			}
		})
	}
}
