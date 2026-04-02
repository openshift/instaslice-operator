package webhook

import (
	"encoding/json"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	kubeflowv1 "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// Webhook URIs for each Kubeflow job type
	PyTorchJobURI = "/mutate-pytorchjob"
	TFJobURI      = "/mutate-tfjob"
	MPIJobURI     = "/mutate-mpijob"
	PaddleJobURI  = "/mutate-paddlejob"
	XGBoostJobURI = "/mutate-xgboostjob"
	JaxJobURI     = "/mutate-jaxjob"

	KubeflowWebhookName = "das-kubeflow-webhook"
)

// KubeflowWebhook interface for Kubeflow training job mutation
type KubeflowWebhook interface {
	Authorized(request admissionctl.Request) admissionctl.Response
	GetURI() string
	Name() string
}

// PyTorchJobWebhook handles mutation of PyTorchJob resources
type PyTorchJobWebhook struct{}

func NewPyTorchJobWebhook() *PyTorchJobWebhook { return &PyTorchJobWebhook{} }
func (w *PyTorchJobWebhook) GetURI() string    { return PyTorchJobURI }
func (w *PyTorchJobWebhook) Name() string      { return "das-pytorchjob-webhook" }

func (w *PyTorchJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("PyTorchJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &kubeflowv1.PyTorchJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false
	for replicaType, replicaSpec := range job.Spec.PyTorchReplicaSpecs {
		if replicaSpec.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: replicaSpec.Template.ObjectMeta,
			Spec:       replicaSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			replicaSpec.Template.ObjectMeta = template.ObjectMeta
			replicaSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed PyTorchJob replica", "replicaType", replicaType, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated PyTorchJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// MPIJobWebhook handles mutation of MPIJob resources
type MPIJobWebhook struct{}

func NewMPIJobWebhook() *MPIJobWebhook { return &MPIJobWebhook{} }
func (w *MPIJobWebhook) GetURI() string { return MPIJobURI }
func (w *MPIJobWebhook) Name() string   { return "das-mpijob-webhook" }

func (w *MPIJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("MPIJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &kubeflowv1.MPIJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false
	for replicaType, replicaSpec := range job.Spec.MPIReplicaSpecs {
		if replicaSpec.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: replicaSpec.Template.ObjectMeta,
			Spec:       replicaSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			replicaSpec.Template.ObjectMeta = template.ObjectMeta
			replicaSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed MPIJob replica", "replicaType", replicaType, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated MPIJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// TFJobWebhook handles mutation of TFJob resources
type TFJobWebhook struct{}

func NewTFJobWebhook() *TFJobWebhook { return &TFJobWebhook{} }
func (w *TFJobWebhook) GetURI() string { return TFJobURI }
func (w *TFJobWebhook) Name() string   { return "das-tfjob-webhook" }

func (w *TFJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("TFJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &kubeflowv1.TFJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false
	for replicaType, replicaSpec := range job.Spec.TFReplicaSpecs {
		if replicaSpec.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: replicaSpec.Template.ObjectMeta,
			Spec:       replicaSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			replicaSpec.Template.ObjectMeta = template.ObjectMeta
			replicaSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed TFJob replica", "replicaType", replicaType, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated TFJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// XGBoostJobWebhook handles mutation of XGBoostJob resources
type XGBoostJobWebhook struct{}

func NewXGBoostJobWebhook() *XGBoostJobWebhook { return &XGBoostJobWebhook{} }
func (w *XGBoostJobWebhook) GetURI() string    { return XGBoostJobURI }
func (w *XGBoostJobWebhook) Name() string      { return "das-xgboostjob-webhook" }

func (w *XGBoostJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("XGBoostJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &kubeflowv1.XGBoostJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false
	for replicaType, replicaSpec := range job.Spec.XGBReplicaSpecs {
		if replicaSpec.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: replicaSpec.Template.ObjectMeta,
			Spec:       replicaSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			replicaSpec.Template.ObjectMeta = template.ObjectMeta
			replicaSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed XGBoostJob replica", "replicaType", replicaType, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated XGBoostJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// PaddleJobWebhook handles mutation of PaddleJob resources
type PaddleJobWebhook struct{}

func NewPaddleJobWebhook() *PaddleJobWebhook { return &PaddleJobWebhook{} }
func (w *PaddleJobWebhook) GetURI() string   { return PaddleJobURI }
func (w *PaddleJobWebhook) Name() string     { return "das-paddlejob-webhook" }

func (w *PaddleJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("PaddleJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &kubeflowv1.PaddleJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false
	for replicaType, replicaSpec := range job.Spec.PaddleReplicaSpecs {
		if replicaSpec.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: replicaSpec.Template.ObjectMeta,
			Spec:       replicaSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			replicaSpec.Template.ObjectMeta = template.ObjectMeta
			replicaSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed PaddleJob replica", "replicaType", replicaType, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated PaddleJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// JaxJobWebhook handles mutation of JAXJob resources
type JaxJobWebhook struct{}

func NewJaxJobWebhook() *JaxJobWebhook { return &JaxJobWebhook{} }
func (w *JaxJobWebhook) GetURI() string { return JaxJobURI }
func (w *JaxJobWebhook) Name() string   { return "das-jaxjob-webhook" }

func (w *JaxJobWebhook) Authorized(request admissionctl.Request) admissionctl.Response {
	klog.InfoS("JAXJob Webhook called", "uid", request.UID, "name", request.Name, "namespace", request.Namespace)

	job := &kubeflowv1.JAXJob{}
	if err := json.Unmarshal(request.Object.Raw, job); err != nil {
		return errorResponse(request, err)
	}

	if !IsKueueManaged(job.Labels) {
		return admissionctl.PatchResponseFromRaw(request.Object.Raw, request.Object.Raw)
	}

	modified := false
	for replicaType, replicaSpec := range job.Spec.JAXReplicaSpecs {
		if replicaSpec.Template.Spec.Containers == nil {
			continue
		}
		template := &corev1.PodTemplateSpec{
			ObjectMeta: replicaSpec.Template.ObjectMeta,
			Spec:       replicaSpec.Template.Spec,
		}
		totalMemGB, profiles, wasModified := TransformPodTemplateForDAS(template)
		if wasModified {
			modified = true
			replicaSpec.Template.ObjectMeta = template.ObjectMeta
			replicaSpec.Template.Spec = template.Spec
			klog.InfoS("Transformed JAXJob replica", "replicaType", replicaType, "totalMemoryGB", totalMemGB, "profiles", profiles)
		}
	}

	if modified {
		klog.InfoS("Mutated JAXJob for Kueue integration", "name", job.Name)
	}

	return patchResponse(request, job)
}

// Helper functions
func errorResponse(request admissionctl.Request, err error) admissionctl.Response {
	klog.ErrorS(err, "Failed to process request",
		"uid", request.UID,
		"kind", request.Kind.Kind,
		"name", request.Name,
		"namespace", request.Namespace)
	ret := admissionctl.Errored(http.StatusBadRequest, err)
	ret.UID = request.UID
	return ret
}

func patchResponse(request admissionctl.Request, obj interface{}) admissionctl.Response {
	mutatedBytes, err := json.Marshal(obj)
	if err != nil {
		return errorResponse(request, err)
	}
	ret := admissionctl.PatchResponseFromRaw(request.Object.Raw, mutatedBytes)
	ret.UID = request.UID
	return ret
}
