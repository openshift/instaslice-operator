package webhook

import (
	"encoding/json"
	"net/http"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/klog/v2"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	JobURI         string = "/mutate-job"
	JobWebhookName string = "das-job-webhook"
)

// InstasliceJobWebhook handles mutation of Job resources
type InstasliceJobWebhook struct{}

// NewJobWebhook creates a new JobWebhook instance
func NewJobWebhook() *InstasliceJobWebhook {
	return &InstasliceJobWebhook{}
}

// GetURI implements GenericWebhook interface
func (w *InstasliceJobWebhook) GetURI() string { return JobURI }

// Name implements GenericWebhook interface
func (w *InstasliceJobWebhook) Name() string { return JobWebhookName }

// Authorized implements GenericWebhook interface
func (w *InstasliceJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	var ret admissionctl.Response

	klog.InfoS("Job Webhook called", "uid", request.UID, "kind", request.Kind.Kind,
		"name", request.Name, "namespace", request.Namespace)

	job, err := w.renderJob(request)
	if err != nil {
		klog.ErrorS(err, "Failed to render Job from request", "uid", request.UID)
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.UID
		return ret
	}

	mutatedJob, err := w.mutateJob(job)
	if err != nil {
		klog.ErrorS(err, "Job mutation failed", "uid", request.UID)
		ret = admissionctl.Errored(http.StatusBadRequest, err)
		ret.UID = request.UID
		return ret
	}

	ret = admissionctl.PatchResponseFromRaw(request.Object.Raw, mutatedJob)
	ret.UID = request.UID
	klog.V(4).InfoS("Returning patch response for Job", "uid", request.UID, "patch", string(ret.Patch))
	return ret
}

// mutateJob mutates the Job's pod template to transform MIG resources.
func (w *InstasliceJobWebhook) mutateJob(job *batchv1.Job) ([]byte, error) {
	// Only transform if Kueue-managed - non-Kueue Jobs are handled by Pod webhook
	if !IsKueueManaged(job.Labels) {
		klog.V(4).InfoS("Job not Kueue-managed, skipping transformation", "name", job.Name)
		return json.Marshal(job)
	}

	klog.InfoS("Mutating Kueue-managed Job", "name", job.Name, "namespace", job.Namespace)

	// Use shared transformation function
	totalMemGB, profiles, modified := TransformPodTemplateForDAS(&job.Spec.Template)

	if !modified {
		klog.V(4).InfoS("No MIG resources found in Job, allowing without modification", "name", job.Name)
		return json.Marshal(job)
	}

	klog.InfoS("Mutated Job for Kueue integration",
		"name", job.Name,
		"totalMemoryGB", totalMemGB,
		"profiles", profiles)

	return json.Marshal(job)
}

// renderJob decodes the Job from the admission request
func (w *InstasliceJobWebhook) renderJob(request admissionctl.Request) (*batchv1.Job, error) {
	klog.V(4).InfoS("Rendering Job from request", "uid", request.UID)

	job := &batchv1.Job{}
	// Always use Object.Raw (the new/current state) for both CREATE and UPDATE operations.
	// Using OldObject.Raw on UPDATE would cause us to lose changes like Kueue's suspend=false.
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return nil, err
	}

	return job, nil
}

// Ensure InstasliceJobWebhook implements GenericWebhook
var _ GenericWebhook = (*InstasliceJobWebhook)(nil)
