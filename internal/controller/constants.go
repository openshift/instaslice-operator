/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import "time"

const (
	OrgInstaslicePrefix              = "instaslice.redhat.com/"
	GateName                         = OrgInstaslicePrefix + "accelerator"
	FinalizerName                    = GateName
	QuotaResourceName                = OrgInstaslicePrefix + "accelerator-memory-quota"
	LabelInstasliceMutated           = OrgInstaslicePrefix + "mutated"
	GPUMemoryLabelName               = "nvidia.com/gpu.memory"
	GPUCountLabelName                = "nvidia.com/gpu.count"
	EmulatorModeFalse                = "false"
	EmulatorModeTrue                 = "true"
	InstaslicePodMutatedTrue         = "true"
	AttributeMediaExtensions         = "me"
	InstaSliceOperatorNamespace      = "instaslice-system"
	NvidiaMIGPrefix                  = "nvidia.com/mig-"
	NodeLabel                        = "kubernetes.io/hostname"
	multipleContainersUnsupportedErr = "multiple containers per pod not supported"
	noContainerInsidePodErr          = "no containers present inside the pod"
	InstasliceDaemonsetName          = "instaslice-operator-controller-daemonset"
	daemonSetImageName               = "quay.io/amalvank/instaslicev2-daemonset:latest"
	daemonSetName                    = "daemonset"
	serviceAccountName               = "instaslice-operator-controller-manager"

	Requeue1sDelay  = 1 * time.Second
	Requeue2sDelay  = 2 * time.Second
	requeue10sDelay = 10 * time.Second
)
