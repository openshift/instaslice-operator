package webhook

import (
	"encoding/json"
	"net/http"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	URI                  string = "/mutate-pod"
	ReadinessEndpointURI string = "/readyz"
	HealthzEndpointURI   string = "/healthz"
	WebhookName          string = "instaslice-webhook"
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
   klog.InfoS("Webhook Authorized called", "uid", request.AdmissionRequest.UID, "kind", request.Kind.Kind)

	pod, err := s.renderPod(request)
   if err != nil {
       klog.ErrorS(err, "Failed to render Pod from request", "uid", request.AdmissionRequest.UID)
       ret = admissionctl.Errored(http.StatusBadRequest, err)
       ret.UID = request.AdmissionRequest.UID
       return ret
   }

	mutatePod, err := s.mutatePod(pod)
   if err != nil {
       klog.ErrorS(err, "Pod mutation failed", "uid", request.AdmissionRequest.UID)
       ret = admissionctl.Errored(http.StatusBadRequest, err)
       ret.UID = request.AdmissionRequest.UID
       return ret
   }

   klog.V(4).InfoS("Returning patch response for Pod", "uid", request.AdmissionRequest.UID)
   ret = admissionctl.PatchResponseFromRaw(request.Object.Raw, mutatePod)
   ret.UID = request.AdmissionRequest.UID
   return ret
}

func (s *InstasliceWebhook) mutatePod(pod *corev1.Pod) ([]byte, error) {
   klog.V(4).InfoS("Mutating Pod structure", "name", pod.Name, "namespace", pod.Namespace)
   mutatedPod := pod.DeepCopy()
	// TODO mutate pod
	return json.Marshal(mutatedPod)
}

// renderPod decodes an *corev1.Pod from the incoming request.
// If the request includes an OldObject (from an update or deletion), it will be
// preferred, otherwise, the Object will be preferred.
func (s *InstasliceWebhook) renderPod(request admissionctl.Request) (*corev1.Pod, error) {
   var err error
   klog.V(4).InfoS("Rendering Pod from request", "uid", request.AdmissionRequest.UID)
	decoder := admissionctl.NewDecoder(scheme)
	pod := &corev1.Pod{}
	if len(request.OldObject.Raw) > 0 {
		err = decoder.DecodeRaw(request.OldObject, pod)
	} else {
		err = decoder.DecodeRaw(request.Object, pod)
	}

	return pod, err
}
