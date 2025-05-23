package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

const (
	URI                  string = "/mutate-pod"
	ReadinessEndpointURI string = "/readyz"
	HealthzEndpointURI   string = "/healthz"
	WebhookName          string = "instaslice-webhook"

	OrgInstaslicePrefix = "instaslice.redhat.com/"
	GateName            = OrgInstaslicePrefix + "accelerator"
	QuotaResourceName   = OrgInstaslicePrefix + "accelerator-memory-quota"
	NvidiaMIGPrefix     = "nvidia.com/mig-"
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

	pod, err := s.renderPod(request)
	if err != nil {
		klog.Error(err, "couldn't render a Pod from the incoming request")
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.AdmissionRequest.UID
		return ret
	}

	mutatePod, err := s.mutatePod(pod)
	if err != nil {
		klog.Error(err, "could not mutate pod")
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.AdmissionRequest.UID
		return ret
	}

	ret = admissionctl.PatchResponseFromRaw(request.Object.Raw, mutatePod)
	ret.UID = request.AdmissionRequest.UID
	return ret
}

func (s *InstasliceWebhook) mutatePod(pod *corev1.Pod) ([]byte, error) {
	mutatedPod := pod.DeepCopy()

	if !s.hasMIGResource(mutatedPod) {
		klog.Info("No nvidia.com/mig-* resource found, skipping mutation.")
		return json.Marshal(mutatedPod)
	}

	s.performQuotaArithmetic(mutatedPod)

	// Transform resource requests from nvidia.com/mig-* to instaslice.redhat.com/mig-*
	s.transformResources(&mutatedPod.Spec.Containers[0].Resources)

	// Add scheduling gate
	schedulingGateName := GateName
	found := false
	for _, gate := range mutatedPod.Spec.SchedulingGates {
		if gate.Name == schedulingGateName {
			found = true
			break
		}
	}
	if !found {
		mutatedPod.Spec.SchedulingGates = append(mutatedPod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: schedulingGateName})
	}

	// Generate an extended resource name based on the pod name
	uuidStr := uuid.New().String()

	// Add envFrom with a unique ConfigMap name derived from the pod name
	configMapName := uuidStr
	// Support for only one pod workloads
	mutatedPod.Spec.Containers[0].EnvFrom = append(mutatedPod.Spec.Containers[0].EnvFrom, corev1.EnvFromSource{
		ConfigMapRef: &corev1.ConfigMapEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
		},
	})

	return json.Marshal(mutatedPod)
}

// renderPod decodes an *corev1.Pod from the incoming request.
// If the request includes an OldObject (from an update or deletion), it will be
// preferred, otherwise, the Object will be preferred.
func (s *InstasliceWebhook) renderPod(request admissionctl.Request) (*corev1.Pod, error) {
	var err error
	decoder := admissionctl.NewDecoder(scheme)
	pod := &corev1.Pod{}
	if len(request.OldObject.Raw) > 0 {
		err = decoder.DecodeRaw(request.OldObject, pod)
	} else {
		err = decoder.DecodeRaw(request.Object, pod)
	}

	return pod, err
}

// hasMIGResource checks if a pod has resource requests or limits with a key that matches `nvidia.com/mig-*`
func (s *InstasliceWebhook) hasMIGResource(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		// Check resource limits
		for resourceName := range container.Resources.Limits {
			if strings.HasPrefix(string(resourceName), NvidiaMIGPrefix) {
				return true
			}
		}
		// Check resource requests
		for resourceName := range container.Resources.Requests {
			if strings.HasPrefix(string(resourceName), NvidiaMIGPrefix) {
				return true
			}
		}
	}
	return false
}

func (s *InstasliceWebhook) performQuotaArithmetic(pod *corev1.Pod) {
	// assumption is that workloads will have 1 container where
	// MIG is requested.
	// TODO instead of only iterating over regular containers,
	// we should also consider other types of containers (such as init containers) in future
	for _, container := range pod.Spec.Containers {
		// dont bother checking requests section. Nvidia supports only limits
		// if requests is added by user, it should be equal to limits.
		for resourceName, quantity := range container.Resources.Limits {
			resourceParts := strings.Split(strings.TrimPrefix(string(resourceName), NvidiaMIGPrefix), ".")

			if len(resourceParts) == 2 {
				// gpuPart := resourceParts[0]
				memoryPart := resourceParts[1]
				memoryValue, err := strconv.Atoi(strings.TrimSuffix(memoryPart, "gb"))
				if err != nil {
					klog.ErrorS(err, "failed to parse memory value")
					continue
				}
				acceleratorMemory := memoryValue * int(quantity.Value())
				// assume 1 container workload
				// Convert the string to ResourceName
				resourceName := corev1.ResourceName(QuotaResourceName)
				pod.Spec.Containers[0].Resources.Limits[resourceName] = resource.MustParse(fmt.Sprintf("%dGi", acceleratorMemory))
			}
		}
	}
}

func (s *InstasliceWebhook) transformResources(resources *corev1.ResourceRequirements) {
	func(resourceLists ...*corev1.ResourceList) {
		for _, resourceList := range resourceLists {
			if *resourceList == nil {
				*resourceList = make(corev1.ResourceList)
			}
			for resourceName, quantity := range *resourceList {
				if strings.HasPrefix(string(resourceName), NvidiaMIGPrefix) {
					newResourceName := strings.Replace(string(resourceName), NvidiaMIGPrefix, fmt.Sprintf("%smig-", OrgInstaslicePrefix), 1)
					delete(*resourceList, resourceName)
					(*resourceList)[corev1.ResourceName(newResourceName)] = quantity
				}
			}
		}
	}(&resources.Limits, &resources.Requests)
}
