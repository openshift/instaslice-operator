package webhook

import (
	"encoding/json"

	"k8s.io/klog/v2"

	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	AppWrapperURI = "/mutate-appwrapper"
)

// AppWrapperWebhook handles mutation of AppWrapper resources.
// Note: AppWrapper components (Jobs, Deployments, etc.) are mutated by their
// respective webhooks. This webhook adds DAS annotations to the AppWrapper itself.
type AppWrapperWebhook struct{}

func NewAppWrapperWebhook() *AppWrapperWebhook { return &AppWrapperWebhook{} }
func (w *AppWrapperWebhook) GetURI() string    { return AppWrapperURI }
func (w *AppWrapperWebhook) Name() string      { return "das-appwrapper-webhook" }

func (w *AppWrapperWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("AppWrapper Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	aw := &awv1beta2.AppWrapper{}
	if err := json.Unmarshal(request.Object.Raw, aw); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(aw.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	// AppWrapper components are embedded as runtime.RawExtension templates.
	// The individual resources (Jobs, Deployments, etc.) will be mutated by
	// their respective webhooks when they are created.
	//
	// Here we just log that we've seen a Kueue-managed AppWrapper.
	// The DAS scheduler and resource transformation will happen when the
	// AppWrapper controller creates the actual workload resources.

	klog.InfoS("Kueue-managed AppWrapper detected - components will be mutated by their respective webhooks",
		"name", aw.Name,
		"namespace", aw.Namespace,
		"componentCount", len(aw.Spec.Components))

	return patchResponse(request, aw)
}
