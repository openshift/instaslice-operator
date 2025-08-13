package constants

// Resource names used by the DAS operator
const (
	// GPUMemoryResource is the extended resource name for GPU memory used by Kueue
	GPUMemoryResource = "gpu.das.openshift.io/mem"

	// MIGResourcePrefix is the prefix for MIG profile resources managed by DAS
	MIGResourcePrefix = "mig.das.com/"

	// NVIDIAMIGResourcePrefix is the original NVIDIA MIG resource prefix that gets transformed
	NVIDIAMIGResourcePrefix = "nvidia.com/mig-"

	// NVIDIAResourcePrefix is the general NVIDIA resource prefix
	NVIDIAResourcePrefix = "nvidia.com/"

	// MIGProfileAnnotation is the annotation key for storing MIG profiles for Kueue-managed Pods
	MIGProfileAnnotation = "das.openshift.io/mig-profiles"
)

// Device plugin constants
const (
	// DevicePluginSocketDir is the directory where device plugin sockets are created
	DevicePluginSocketDir = "/var/lib/kubelet/device-plugins"
)
