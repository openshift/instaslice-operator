package utils

import (
	v1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GenerateFakeCapacity(nodeName string) *v1alpha1.Instaslice {
	return &v1alpha1.Instaslice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: "instaslice-system",
		},
		Spec: v1alpha1.InstasliceSpec{
			CpuOnNodeAtBoot:    72,
			MemoryOnNodeAtBoot: 1000000000,
			MigGPUUUID: map[string]string{
				"GPU-8d042338-e67f-9c48-92b4-5b55c7e5133c": "NVIDIA A100-PCIE-40GB",
				"GPU-31cfe05c-ed13-cd17-d7aa-c63db5108c24": "NVIDIA A100-PCIE-40GB",
			},
			Migplacement: []v1alpha1.Mig{
				{
					CIProfileID:    0,
					CIEngProfileID: 0,
					Giprofileid:    0,
					Placements: []v1alpha1.Placement{
						{Size: 1, Start: 0},
						{Size: 1, Start: 1},
						{Size: 1, Start: 2},
						{Size: 1, Start: 3},
						{Size: 1, Start: 4},
						{Size: 1, Start: 5},
						{Size: 1, Start: 6},
					},
					Profile: "1g.5gb",
				},
				{
					CIProfileID:    1,
					CIEngProfileID: 0,
					Giprofileid:    1,
					Placements: []v1alpha1.Placement{
						{Size: 2, Start: 0},
						{Size: 2, Start: 2},
						{Size: 2, Start: 4},
					},
					Profile: "2g.10gb",
				},
				{
					CIProfileID:    2,
					CIEngProfileID: 0,
					Giprofileid:    2,
					Placements: []v1alpha1.Placement{
						{Size: 4, Start: 0},
						{Size: 4, Start: 4},
					},
					Profile: "3g.20gb",
				},
				{
					CIProfileID:    3,
					CIEngProfileID: 0,
					Giprofileid:    3,
					Placements: []v1alpha1.Placement{
						{Size: 4, Start: 0},
					},
					Profile: "4g.20gb",
				},
				{
					CIProfileID:    4,
					CIEngProfileID: 0,
					Giprofileid:    4,
					Placements: []v1alpha1.Placement{
						{Size: 8, Start: 0},
					},
					Profile: "7g.40gb",
				},
				{
					CIProfileID:    7,
					CIEngProfileID: 0,
					Giprofileid:    7,
					Placements: []v1alpha1.Placement{
						{Size: 1, Start: 0},
						{Size: 1, Start: 1},
						{Size: 1, Start: 2},
						{Size: 1, Start: 3},
						{Size: 1, Start: 4},
						{Size: 1, Start: 5},
						{Size: 1, Start: 6},
					},
					Profile: "1g.5gb+me",
				},
				{
					CIProfileID:    9,
					CIEngProfileID: 0,
					Giprofileid:    9,
					Placements: []v1alpha1.Placement{
						{Size: 2, Start: 0},
						{Size: 2, Start: 2},
						{Size: 2, Start: 4},
						{Size: 2, Start: 6},
					},
					Profile: "1g.10gb",
				},
			},
		},
		Status: v1alpha1.InstasliceStatus{
			Processed: true,
		},
	}
}
