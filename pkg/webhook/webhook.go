package webhook

import (
	"encoding/json"
	"net/http"
	"strings"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
	utiluuid "k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

const (
	URI                       string = "/mutate-pod"
	ReadinessEndpointURI      string = "/readyz"
	HealthzEndpointURI        string = "/healthz"
	WebhookName               string = "das-webhook"
	secondaryScheduler        string = "das-scheduler"
	envConfigMapAnnotationKey string = "mig.das.com/env-configmap"
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
	klog.InfoS("Webhook Authorized response", "uid", request.UID, "patch", string(ret.Patch))
	return ret
}

func (s *InstasliceWebhook) mutatePod(pod *corev1.Pod) ([]byte, error) {
	klog.InfoS("Mutating Pod structure", "name", pod.Name, "namespace", pod.Namespace)
	mutatedPod := pod.DeepCopy()
	needsScheduler := false

	mutateResources := func(c *corev1.Container) {
		if c.Resources.Limits == nil {
			return
		}
		klog.InfoS("checking container resources", "container", c.Name)
		newLimits := corev1.ResourceList{}
		newRequests := corev1.ResourceList{}

		for name, qty := range c.Resources.Limits {
			key := string(name)
			switch {
			case strings.HasPrefix(key, "nvidia.com/mig-"):
				profile := strings.TrimPrefix(key, "nvidia.com/mig-")
				newKey := corev1.ResourceName("mig.das.com/" + profile)
				klog.InfoS("renaming GPU resource", "from", key, "to", newKey)
				newLimits[newKey] = qty
				newRequests[newKey] = qty
				needsScheduler = true
			case strings.HasPrefix(key, "nvidia.com/"):
				newKey := corev1.ResourceName(strings.Replace(key, "nvidia.com/", "mig.das.com/", 1))
				klog.InfoS("renaming GPU resource", "from", key, "to", newKey)
				newLimits[newKey] = qty
				newRequests[newKey] = qty
				needsScheduler = true
			default:
				newLimits[name] = qty
				if strings.HasPrefix(key, "mig.das.com/") {
					needsScheduler = true
				}
			}
		}

		for name, qty := range c.Resources.Requests {
			key := string(name)
			if !strings.HasPrefix(key, "nvidia.com/") && !strings.HasPrefix(key, "mig.das.com/") {
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

		cmName := string(utiluuid.NewUUID())
		if mutatedPod.Annotations == nil {
			mutatedPod.Annotations = map[string]string{}
		}
		mutatedPod.Annotations[envConfigMapAnnotationKey] = cmName

		// addEnv injects or overwrites ConfigMap-backed environment variables so
		// the scheduler can identify which GPU slice was allocated to the pod.
		addEnv := func(c *corev1.Container) {
			nvidiaVar := corev1.EnvVar{
				Name: "NVIDIA_VISIBLE_DEVICES",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
						Key:                  "NVIDIA_VISIBLE_DEVICES",
					},
				},
			}
			cudaVar := corev1.EnvVar{
				Name: "CUDA_VISIBLE_DEVICES",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
						Key:                  "CUDA_VISIBLE_DEVICES",
					},
				},
			}

			replacedNvidia := false
			replacedCuda := false
			for i := range c.Env {
				switch c.Env[i].Name {
				case "NVIDIA_VISIBLE_DEVICES":
					c.Env[i] = nvidiaVar
					replacedNvidia = true
				case "CUDA_VISIBLE_DEVICES":
					c.Env[i] = cudaVar
					replacedCuda = true
				}
			}
			if !replacedNvidia {
				klog.InfoS("setting NVIDIA_VISIBLE_DEVICES", "container", c.Name)
				c.Env = append(c.Env, nvidiaVar)
			} else {
				klog.InfoS("overwriting NVIDIA_VISIBLE_DEVICES", "container", c.Name)
			}
			if !replacedCuda {
				klog.InfoS("setting CUDA_VISIBLE_DEVICES", "container", c.Name)
				c.Env = append(c.Env, cudaVar)
			} else {
				klog.InfoS("overwriting CUDA_VISIBLE_DEVICES", "container", c.Name)
			}
		}

		for i := range mutatedPod.Spec.Containers {
			addEnv(&mutatedPod.Spec.Containers[i])
		}
		for i := range mutatedPod.Spec.InitContainers {
			addEnv(&mutatedPod.Spec.InitContainers[i])
		}
		for i := range mutatedPod.Spec.EphemeralContainers {
			c := (*corev1.Container)(&mutatedPod.Spec.EphemeralContainers[i].EphemeralContainerCommon)
			addEnv(c)
		}
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
