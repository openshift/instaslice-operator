package webhook

import (
	"encoding/json"

	"k8s.io/klog/v2"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
)

const (
	JobSetURI = "/mutate-jobset"
)

// JobSetWebhook handles mutation of JobSet resources
type JobSetWebhook struct{}

func NewJobSetWebhook() *JobSetWebhook { return &JobSetWebhook{} }
func (w *JobSetWebhook) GetURI() string { return JobSetURI }
func (w *JobSetWebhook) Name() string   { return "das-jobset-webhook" }

func (w *JobSetWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("JobSet Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	jobset := &jobsetv1alpha2.JobSet{}
	if err := json.Unmarshal(request.Object.Raw, jobset); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(jobset.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false

	// JobSet has ReplicatedJobs, each with a Template (JobTemplateSpec)
	// JobTemplateSpec.Spec is a JobSpec which has Template (PodTemplateSpec)
	for i := range jobset.Spec.ReplicatedJobs {
		replicatedJob := &jobset.Spec.ReplicatedJobs[i]
		template := &replicatedJob.Template.Spec.Template

		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			klog.InfoS("Transformed JobSet replicatedJob",
				"replicatedJobName", replicatedJob.Name,
				"totalMemoryGB", totalMemGB,
				"profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated JobSet for Kueue integration", "name", jobset.Name)
	}

	return patchResponse(request, jobset)
}
