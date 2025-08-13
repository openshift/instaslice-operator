package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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
	secondaryScheduler   string = "das-scheduler"
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

// extractGPUMemoryFromProfile extracts the GPU memory in GB from a MIG profile string.
func extractGPUMemoryFromProfile(profile string) int64 {
	// Match patterns like "1g.5gb", "2g.10gb", "3g.20gb", etc.
	// Also handle compute instance patterns like "1c.1g.5gb"
	re := regexp.MustCompile(`(?:\d+c\.)?(\d+)g\.(\d+)gb`)
	matches := re.FindStringSubmatch(profile)
	if len(matches) >= 3 {
		if memGB, err := strconv.ParseInt(matches[2], 10, 64); err == nil {
			return memGB
		}
	}
	return 0
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
	klog.InfoS("Webhook Authorized response", "uid", request.UID, "patch", string(ret.Patch))
	return ret
}

func (s *InstasliceWebhook) mutatePod(pod *corev1.Pod) ([]byte, error) {
	klog.InfoS("Mutating Pod structure", "name", pod.Name, "namespace", pod.Namespace)
	mutatedPod := pod.DeepCopy()
	needsScheduler := false

	// Check if this Pod will be managed by Kueue (has queue label)
	isKueueManaged := false
	if mutatedPod.Labels != nil {
		if queueName, exists := mutatedPod.Labels["kueue.x-k8s.io/queue-name"]; exists {
			isKueueManaged = true
			klog.InfoS("Pod has Kueue queue label - using annotation-based approach", "name", pod.Name, "queue", queueName)
		}
	}
	if !isKueueManaged {
		klog.InfoS("Pod not managed by Kueue - using standard resource injection", "name", pod.Name)
	}

	// Track MIG profiles for DAS scheduler via annotations (Kueue-managed Pods only).
	migProfiles := make(map[string]int64)

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

				// Extract GPU memory from profile and accumulate.
				memGB := extractGPUMemoryFromProfile(profile)
				if memGB > 0 {
					totalGPUMemory += memGB * qty.Value()
					klog.InfoS("extracted GPU memory from profile", "profile", profile, "memoryGB", memGB, "quantity", qty.Value(), "totalMemory", totalGPUMemory)
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
					// Check if this is already a MIG profile and extract memory
					if strings.Contains(key, constants.MIGResourcePrefix) {
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

		// Inject GPU memory resource if we found any MIG profiles.
		if totalGPUMemory > 0 {
			gpuMemResource := corev1.ResourceName(constants.GPUMemoryResource)
			gpuMemQuantity := resource.NewQuantity(totalGPUMemory, resource.DecimalSI)
			newLimits[gpuMemResource] = *gpuMemQuantity
			newRequests[gpuMemResource] = *gpuMemQuantity
			klog.InfoS("injecting GPU memory resource", "resource", constants.GPUMemoryResource, "totalMemoryGB", totalGPUMemory)
		}

		for name, qty := range c.Resources.Requests {
			key := string(name)
			if !strings.HasPrefix(key, constants.NVIDIAResourcePrefix) && !strings.HasPrefix(key, constants.MIGResourcePrefix) && key != constants.GPUMemoryResource {
				newRequests[name] = qty
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
		mutatedPod.Spec.SchedulerName = secondaryScheduler
		klog.InfoS("using secondary scheduler", "name", mutatedPod.Name)
	}

	if len(migProfiles) > 0 && isKueueManaged {
		if mutatedPod.Annotations == nil {
			mutatedPod.Annotations = make(map[string]string)
		}

		// Serialize MIG profiles to annotation: "1g.5gb:1,2g.10gb:2"
		migData := make([]string, 0, len(migProfiles))
		for profile, quantity := range migProfiles {
			migData = append(migData, fmt.Sprintf("%s:%d", profile, quantity))
		}
		mutatedPod.Annotations[constants.MIGProfileAnnotation] = strings.Join(migData, ",")
		klog.InfoS("added MIG profiles to Pod annotations for Kueue-managed Pod", "annotation", mutatedPod.Annotations[constants.MIGProfileAnnotation])
	}

	klog.InfoS("finished pod mutation", "mutatedPod", mutatedPod)
	return json.Marshal(mutatedPod)
}

func (s *InstasliceWebhook) renderPod(request admissionctl.Request) (*corev1.Pod, error) {
	var err error
	klog.InfoS("Rendering Pod from request", "uid", request.UID)
	decoder := admissionctl.NewDecoder(scheme)
	pod := &corev1.Pod{}
	if len(request.OldObject.Raw) > 0 {
		err = decoder.DecodeRaw(request.OldObject, pod)
	} else {
		err = decoder.DecodeRaw(request.Object, pod)
	}

	return pod, err
}
