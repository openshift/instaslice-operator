package webhook

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	RayJobURI     = "/mutate-rayjob"
	RayClusterURI = "/mutate-raycluster"
)

// RayJobWebhook handles mutation of RayJob resources
type RayJobWebhook struct{}

func NewRayJobWebhook() *RayJobWebhook { return &RayJobWebhook{} }
func (w *RayJobWebhook) GetURI() string { return RayJobURI }
func (w *RayJobWebhook) Name() string   { return "das-rayjob-webhook" }

func (w *RayJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("RayJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &rayv1.RayJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := transformRayClusterSpec(job.Spec.RayClusterSpec)

	if modified {
		klog.InfoS("Mutated RayJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// RayClusterWebhook handles mutation of RayCluster resources
type RayClusterWebhook struct{}

func NewRayClusterWebhook() *RayClusterWebhook { return &RayClusterWebhook{} }
func (w *RayClusterWebhook) GetURI() string    { return RayClusterURI }
func (w *RayClusterWebhook) Name() string      { return "das-raycluster-webhook" }

func (w *RayClusterWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("RayCluster Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	cluster := &rayv1.RayCluster{}
	if err := json.Unmarshal(request.Object.Raw, cluster); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(cluster.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := transformRayClusterSpec(&cluster.Spec)

	if modified {
		klog.InfoS("Mutated RayCluster for Kueue integration", "name", cluster.Name)
	}

	return patchResponse(request, cluster)
}

// transformRayClusterSpec transforms the RayClusterSpec (shared between RayJob and RayCluster)
func transformRayClusterSpec(spec *rayv1.RayClusterSpec) bool {
	modified := false

	// Transform head group
	if spec.HeadGroupSpec.Template.Spec.Containers != nil {
		template := &corev1.PodTemplateSpec{
			ObjectMeta: spec.HeadGroupSpec.Template.ObjectMeta,
			Spec:       spec.HeadGroupSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			spec.HeadGroupSpec.Template.ObjectMeta = template.ObjectMeta
			spec.HeadGroupSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed Ray head group", "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	// Transform worker groups
	for i := range spec.WorkerGroupSpecs {
		workerGroup := &spec.WorkerGroupSpecs[i]
		if workerGroup.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: workerGroup.Template.ObjectMeta,
			Spec:       workerGroup.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			workerGroup.Template.ObjectMeta = template.ObjectMeta
			workerGroup.Template.Spec = template.Spec
			klog.InfoS("Transformed Ray worker group", "groupName", workerGroup.GroupName, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	return modified
}
