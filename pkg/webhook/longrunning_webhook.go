package webhook

import (
	"encoding/json"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

const (
	DeploymentURI      = "/mutate-deployment"
	StatefulSetURI     = "/mutate-statefulset"
	LeaderWorkerSetURI = "/mutate-leaderworkerset"
)

// DeploymentWebhook handles mutation of Deployment resources
type DeploymentWebhook struct{}

func NewDeploymentWebhook() *DeploymentWebhook { return &DeploymentWebhook{} }
func (w *DeploymentWebhook) GetURI() string    { return DeploymentURI }
func (w *DeploymentWebhook) Name() string      { return "das-deployment-webhook" }

func (w *DeploymentWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("Deployment Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	deployment := &appsv1.Deployment{}
	if err := json.Unmarshal(request.Object.Raw, deployment); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(deployment.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	totalMemGB, profiles, modified := TransformPodTemplateForDAS(&deployment.Spec.Template)

	if modified {
		klog.InfoS("Mutated Deployment for Kueue integration",
			"name", deployment.Name,
			"totalMemoryGB", totalMemGB,
			"profiles", profiles)
	}

	return patchResponse(request, deployment)
}

// StatefulSetWebhook handles mutation of StatefulSet resources
type StatefulSetWebhook struct{}

func NewStatefulSetWebhook() *StatefulSetWebhook { return &StatefulSetWebhook{} }
func (w *StatefulSetWebhook) GetURI() string     { return StatefulSetURI }
func (w *StatefulSetWebhook) Name() string       { return "das-statefulset-webhook" }

func (w *StatefulSetWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("StatefulSet Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	statefulset := &appsv1.StatefulSet{}
	if err := json.Unmarshal(request.Object.Raw, statefulset); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(statefulset.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	totalMemGB, profiles, modified := TransformPodTemplateForDAS(&statefulset.Spec.Template)

	if modified {
		klog.InfoS("Mutated StatefulSet for Kueue integration",
			"name", statefulset.Name,
			"totalMemoryGB", totalMemGB,
			"profiles", profiles)
	}

	return patchResponse(request, statefulset)
}

// LeaderWorkerSetWebhook handles mutation of LeaderWorkerSet resources
type LeaderWorkerSetWebhook struct{}

func NewLeaderWorkerSetWebhook() *LeaderWorkerSetWebhook { return &LeaderWorkerSetWebhook{} }
func (w *LeaderWorkerSetWebhook) GetURI() string         { return LeaderWorkerSetURI }
func (w *LeaderWorkerSetWebhook) Name() string           { return "das-leaderworkerset-webhook" }

func (w *LeaderWorkerSetWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("LeaderWorkerSet Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	lws := &lwsv1.LeaderWorkerSet{}
	if err := json.Unmarshal(request.Object.Raw, lws); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(lws.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false

	// Transform leader template
	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		template := &corev1.PodTemplateSpec{
			ObjectMeta: lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta,
			Spec:       lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta = template.ObjectMeta
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec = template.Spec
			klog.InfoS("Transformed LeaderWorkerSet leader template",
				"totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	// Transform worker template (WorkerTemplate is not a pointer, so always exists)
	totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(&lws.Spec.LeaderWorkerTemplate.WorkerTemplate)
	if wasModified {
		modified = true
		klog.InfoS("Transformed LeaderWorkerSet worker template",
			"totalMemoryGB", totalMemGB, "profiles", profiles)
	}

	if modified {
		klog.InfoS("Mutated LeaderWorkerSet for Kueue integration", "name", lws.Name)
	}

	return patchResponse(request, lws)
}
