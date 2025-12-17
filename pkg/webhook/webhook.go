package webhook

import (
	"encoding/json"
	"net/http"
	"strings"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/openshift/instaslice-operator/pkg/constants"
)

const (
	URI                  string = "/mutate-pod"
	ReadinessEndpointURI string = "/readyz"
	HealthzEndpointURI   string = "/healthz"
	WebhookName          string = "das-webhook"
)

// Webhook interface
type Webhook interface {
	// Authorized will determine if the request is allowed
	Authorized(request admissionctl.Request) admissionctl.Response
	// GetURI returns the URI for the webhook
	GetURI() string
	// GetReadiness URI returns the URI for the webhook
	GetReadinessURI() string
	// GetHealthzURI() returns the URI for the webhook
	GetHealthzURI() string
	// Name is the name of the webhook
	Name() string
}

type InstasliceWebhook struct{}

func NewWebhook() Webhook {
	return &InstasliceWebhook{}
}

// GetURI implements Webhook interface
func (s *InstasliceWebhook) GetURI() string { return URI }

// GetReadinessURI implements Webhook interface
func (s *InstasliceWebhook) GetReadinessURI() string { return ReadinessEndpointURI }

// GetHealthzURI() implements Webhook interface
func (s *InstasliceWebhook) GetHealthzURI() string { return HealthzEndpointURI }

// Name implements Webhook interface
func (s *InstasliceWebhook) Name() string { return WebhookName }

// Validate if the incoming request even valid
func (s *InstasliceWebhook) Validate(req admissionctl.Request) bool {
	return req.Kind.Kind == "Pod" || req.Kind.Kind == "SharedSecret" || req.Kind.Kind == "SharedConfigMap"
}

// Authorized implements Webhook interface
func (s *InstasliceWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	var ret admissionctl.Response
	// basic request metadata for operators
	klog.InfoS("Webhook Authorized called", "uid", request.UID, "kind", request.Kind.Kind)
	klog.V(1).InfoS("decoding object")

	pod, err := s.renderPod(request)
	if err != nil {
		klog.ErrorS(err, "Failed to render Pod from request", "uid", request.UID)
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.UID
		return ret
	}
	klog.InfoS("Rendering Pod successful", "name", pod.Name, "namespace", pod.Namespace, "uid", request.UID)

	mutatePod, err := s.mutatePod(pod)
	if err != nil {
		klog.ErrorS(err, "Pod mutation failed", "uid", request.UID)
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.UID
		return ret
	}
	klog.InfoS("Pod mutation successful", "name", pod.Name, "namespace", pod.Namespace, "uid", request.UID)

	klog.V(4).InfoS("Returning patch response for Pod", "uid", request.UID)
	ret = admissionctl.PatchResponseFromRaw(request.Object.Raw, mutatePod)
	ret.UID = request.UID
	klog.V(4).InfoS("Webhook Authorized response", "uid", request.UID, "patchLen", len(ret.Patch))
	return ret
}

func (s *InstasliceWebhook) mutatePod(pod *corev1.Pod) ([]byte, error) {
	klog.InfoS("Mutating Pod structure", "name", pod.Name, "namespace", pod.Namespace)
	mutatedPod := pod.DeepCopy()
	needsScheduler := false

	// Check if this Pod will be managed by Kueue (has queue label)
	isKueueManaged := false
	if mutatedPod.Labels != nil {
		if queueName, exists := mutatedPod.Labels[constants.KueueQueueLabel]; exists {
			isKueueManaged = true
			klog.InfoS("Pod has Kueue queue label - using annotation-based approach", "name", pod.Name, "queue", queueName)
		}
	}
	if !isKueueManaged {
		klog.InfoS("Pod not managed by Kueue - using standard resource injection", "name", pod.Name)
	}

	// Track MIG profiles for DAS scheduler via annotations (Kueue-managed Pods only).
	migProfiles := make(map[string]int64)

	// Check if the Workload webhook has already injected GPU memory resource.
	// This happens when the Pod comes from a Kueue-managed Job/Workload.
	gpuMemoryAlreadyInjected := false
	for _, c := range mutatedPod.Spec.Containers {
		if c.Resources.Limits != nil {
			if _, exists := c.Resources.Limits[corev1.ResourceName(constants.GPUMemoryResource)]; exists {
				gpuMemoryAlreadyInjected = true
				klog.V(4).InfoS("GPU memory already injected by Workload webhook, skipping re-injection", "name", pod.Name)
				break
			}
		}
	}

	mutateResources := func(c *corev1.Container) {
		if c.Resources.Limits == nil {
			return
		}
		klog.InfoS("checking container resources", "container", c.Name)
		newLimits := corev1.ResourceList{}
		newRequests := corev1.ResourceList{}
		totalGPUMemory := int64(0)

		for name, qty := range c.Resources.Limits {
			key := string(name)
			switch {
			case strings.HasPrefix(key, constants.NVIDIAMIGResourcePrefix):
				profile := strings.TrimPrefix(key, constants.NVIDIAMIGResourcePrefix)
				// For Kueue-managed Pods: Store original profile in annotations for DAS scheduler.
				if !isKueueManaged {
					newKey := corev1.ResourceName(constants.MIGResourcePrefix + profile)
					klog.InfoS("renaming GPU resource for non-Kueue Pod", "from", key, "to", newKey)
					newLimits[newKey] = qty
					newRequests[newKey] = qty
				} else {
					// Store original profile for DAS scheduler to read from annotations.
					migProfiles[profile] = qty.Value()
					klog.InfoS("storing original MIG profile for Kueue-managed Pod", "profile", profile, "quantity", qty.Value())
				}
				needsScheduler = true

				// Extract GPU memory from profile and accumulate (only if not already injected).
				if !gpuMemoryAlreadyInjected {
					memGB := extractGPUMemoryFromProfile(profile)
					if memGB > 0 {
						totalGPUMemory += memGB * qty.Value()
						klog.InfoS("extracted GPU memory from profile", "profile", profile, "memoryGB", memGB, "quantity", qty.Value(), "totalMemory", totalGPUMemory)
					}
				}
			case strings.HasPrefix(key, constants.NVIDIAResourcePrefix):
				newKey := corev1.ResourceName(strings.Replace(key, constants.NVIDIAResourcePrefix, constants.MIGResourcePrefix, 1))
				klog.InfoS("renaming GPU resource", "from", key, "to", newKey)
				newLimits[newKey] = qty
				newRequests[newKey] = qty
				needsScheduler = true
			default:
				newLimits[name] = qty
				if strings.HasPrefix(key, constants.MIGResourcePrefix) {
					needsScheduler = true
					// Extract GPU memory from existing MIG profile resource (only if not already injected).
					if !gpuMemoryAlreadyInjected {
						profile := strings.TrimPrefix(key, constants.MIGResourcePrefix)
						memGB := extractGPUMemoryFromProfile(profile)
						if memGB > 0 {
							totalGPUMemory += memGB * qty.Value()
							klog.InfoS("extracted GPU memory from existing profile", "profile", profile, "memoryGB", memGB, "quantity", qty.Value(), "totalMemory", totalGPUMemory)
						}
					}
				}
			}
		}

		// Inject GPU memory resource if we found any MIG profiles and it wasn't already injected.
		if totalGPUMemory > 0 && !gpuMemoryAlreadyInjected {
			gpuMemResource := corev1.ResourceName(constants.GPUMemoryResource)
			gpuMemQuantity := resource.NewQuantity(totalGPUMemory, resource.DecimalSI)
			newLimits[gpuMemResource] = *gpuMemQuantity
			newRequests[gpuMemResource] = *gpuMemQuantity
			klog.InfoS("injecting GPU memory resource", "resource", constants.GPUMemoryResource, "totalMemoryGB", totalGPUMemory)
		}

		for name, qty := range c.Resources.Requests {
			key := string(name)
			// Keep non-NVIDIA resources and gpu.das.openshift.io/mem (which may have been
			// injected by the Workload webhook and must be preserved in both limits and requests)
			if !strings.HasPrefix(key, constants.NVIDIAResourcePrefix) && !strings.HasPrefix(key, constants.MIGResourcePrefix) {
				newRequests[name] = qty
			}
		}

		// Kubernetes expects extended resources to have identical Requests and Limits.
		// In some flows (e.g. Workload webhook injection) gpu.das.openshift.io/mem may appear
		// in Limits but not Requests; ensure we keep them consistent.
		gpuMemResource := corev1.ResourceName(constants.GPUMemoryResource)
		if qty, ok := newLimits[gpuMemResource]; ok {
			if _, okReq := newRequests[gpuMemResource]; !okReq {
				newRequests[gpuMemResource] = qty
			}
		}

		if len(newLimits) > 0 {
			c.Resources.Limits = newLimits
		}
		if len(newRequests) > 0 {
			c.Resources.Requests = newRequests
		}
	}

	for i := range mutatedPod.Spec.Containers {
		mutateResources(&mutatedPod.Spec.Containers[i])
	}
	for i := range mutatedPod.Spec.InitContainers {
		mutateResources(&mutatedPod.Spec.InitContainers[i])
	}
	for i := range mutatedPod.Spec.EphemeralContainers {
		c := (*corev1.Container)(&mutatedPod.Spec.EphemeralContainers[i].EphemeralContainerCommon)
		mutateResources(c)
	}

	if needsScheduler {
		mutatedPod.Spec.SchedulerName = constants.DASSchedulerName
		klog.InfoS("using secondary scheduler", "name", mutatedPod.Name)
		// Set nvidia-legacy runtime for MIG workloads to avoid CDI resolution issues
		// with the nvidia runtime's CDI mode
		runtimeClass := constants.NvidiaLegacyRuntimeClass
		mutatedPod.Spec.RuntimeClassName = &runtimeClass
		klog.InfoS("setting runtimeClassName for MIG workload", "name", mutatedPod.Name, "runtimeClassName", runtimeClass)
	}

	if len(migProfiles) > 0 && isKueueManaged {
		if mutatedPod.Annotations == nil {
			mutatedPod.Annotations = make(map[string]string)
		}
		mutatedPod.Annotations[constants.MIGProfileAnnotation] = SerializeMIGProfiles(migProfiles)
		klog.InfoS("added MIG profiles to Pod annotations for Kueue-managed Pod", "annotation", mutatedPod.Annotations[constants.MIGProfileAnnotation])
	}

	klog.V(5).InfoS("finished pod mutation", "name", mutatedPod.Name, "namespace", mutatedPod.Namespace, "schedulerName", mutatedPod.Spec.SchedulerName, "runtimeClassName", mutatedPod.Spec.RuntimeClassName)
	return json.Marshal(mutatedPod)
}

func (s *InstasliceWebhook) renderPod(request admissionctl.Request) (*corev1.Pod, error) {
	klog.InfoS("Rendering Pod from request", "uid", request.UID)
	decoder := admissionctl.NewDecoder(scheme)
	pod := &corev1.Pod{}
	// Always use Object (the new/current state) for both CREATE and UPDATE operations.
	// Using OldObject on UPDATE would cause us to lose changes made by other controllers.
	err := decoder.DecodeRaw(request.Object, pod)
	return pod, err
}
