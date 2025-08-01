package operator

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	operatorsv1 "github.com/openshift/api/operator/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextclientv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	v1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"

	"github.com/openshift/instaslice-operator/bindata"
	slicev1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	instasliceoperatorv1alphaclientset "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/typed/dasoperator/v1alpha1"
	operatorclientv1alpha1informers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions/dasoperator/v1alpha1"

	"github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
)

const (
	WebhookCertificateSecretName  = "webhook-server-cert"
	WebhookCertificateName        = "das-serving-cert"
	CertManagerInjectCaAnnotation = "cert-manager.io/inject-ca-from"
)

type TargetConfigReconciler struct {
	apiextensionClient         *apiextclientv1.Clientset
	appsClient                 appsv1client.DaemonSetsGetter
	cache                      resourceapply.ResourceCache
	discoveryClient            discovery.DiscoveryInterface
	dynamicClient              dynamic.Interface
	eventRecorder              events.Recorder
	generations                []operatorsv1.GenerationStatus
	instasliceoperatorClient   *operatorclient.DASOperatorSetClient
	kubeClient                 kubernetes.Interface
	kubeInformersForNamespaces v1helpers.KubeInformersForNamespaces
	namespace                  string
	operatorClient             instasliceoperatorv1alphaclientset.DASOperatorInterface
	resourceCache              resourceapply.ResourceCache
	secretLister               v1.SecretLister
	targetDaemonsetImage       string
	targetWebhookImage         string
	targetSchedulerImage       string
	emulatedMode               slicev1alpha1.EmulatedMode
	nodeSelector               map[string]string
}

func NewTargetConfigReconciler(
	emulatedMode slicev1alpha1.EmulatedMode,
	targetDaemonsetImage string,
	targetWebhookImage string,
	targetSchedulerImage string,
	namespace string,
	operatorConfigClient instasliceoperatorv1alphaclientset.DASOperatorInterface,
	operatorClientInformer operatorclientv1alpha1informers.DASOperatorInformer,
	kubeInformersForNamespaces v1helpers.KubeInformersForNamespaces,
	appsClient appsv1client.DaemonSetsGetter,
	instasliceoperatorClient *operatorclient.DASOperatorSetClient,
	dynamicClient dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	kubeClient kubernetes.Interface,
	apiExtensionClient *apiextclientv1.Clientset,
	eventRecorder events.Recorder,
) factory.Controller {
	c := &TargetConfigReconciler{
		apiextensionClient:         apiExtensionClient,
		discoveryClient:            discoveryClient,
		dynamicClient:              dynamicClient,
		eventRecorder:              eventRecorder,
		instasliceoperatorClient:   instasliceoperatorClient,
		kubeClient:                 kubeClient,
		appsClient:                 appsClient,
		kubeInformersForNamespaces: kubeInformersForNamespaces,
		namespace:                  namespace,
		operatorClient:             operatorConfigClient,
		resourceCache:              resourceapply.NewResourceCache(),
		secretLister:               kubeInformersForNamespaces.SecretLister(),
		targetDaemonsetImage:       targetDaemonsetImage,
		targetWebhookImage:         targetWebhookImage,
		targetSchedulerImage:       targetSchedulerImage,
		cache:                      resourceapply.NewResourceCache(),
		emulatedMode:               emulatedMode,
	}

	return factory.New().WithInformers(
		// for the operator changes
		operatorClientInformer.Informer(),
		// for the deployment and its configmap, secret, daemonsets.
		kubeInformersForNamespaces.InformersFor(namespace).Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Apps().V1().DaemonSets().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Apps().V1().Deployments().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Core().V1().ConfigMaps().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Core().V1().Secrets().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Core().V1().ServiceAccounts().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Core().V1().Services().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Rbac().V1().ClusterRoleBindings().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Rbac().V1().ClusterRoles().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Rbac().V1().RoleBindings().Informer(),
		kubeInformersForNamespaces.InformersFor(namespace).Rbac().V1().Roles().Informer(),
	).ResyncEvery(time.Minute*5).
		WithSync(c.sync).
		ToController("DASOperatorController", eventRecorder)
}

func (c *TargetConfigReconciler) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	found, err := isResourceRegistered(c.discoveryClient, schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	if err != nil {
		return fmt.Errorf("unable to check cert-manager is installed: %w", err)
	}

	if !found {
		return fmt.Errorf("please make sure that cert-manager is installed on your cluster")
	}

	sliceOperator, err := c.operatorClient.Get(ctx, operatorclient.OperatorConfigName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("unable to get operator configuration %s/%s: %w", c.namespace, operatorclient.OperatorConfigName, err)
	}

	c.nodeSelector = sliceOperator.Spec.NodeSelector

	ownerReference := metav1.OwnerReference{
		APIVersion: "inference.redhat.com/v1alpha1",
		Kind:       "DASOperator",
		Name:       sliceOperator.Name,
		UID:        sliceOperator.UID,
	}

	klog.V(2).InfoS("Got operator config", "emulated_mode", c.emulatedMode)

	daemonset, _, err := c.manageDaemonset(ctx, ownerReference)
	if err != nil {
		return err
	}

	scheduler, _, err := c.manageScheduler(ctx, ownerReference)
	if err != nil {
		return err
	}

	webhook, _, err := c.manageMutatingWebhookDeployment(ctx, ownerReference)
	if err != nil {
		return err
	}

	if err := c.manageMutatingWebhookService(ctx, ownerReference); err != nil {
		return err
	}

	if err := c.manageMutatingWebhook(ctx, ownerReference); err != nil {
		return err
	}
	_, _, err = c.manageIssuerCR(ctx, ownerReference)
	if err != nil {
		return err
	}

	_, _, err = c.manageCertificateWebhookCR(ctx, ownerReference)
	if err != nil {
		return err
	}

	_, _, err = c.manageWebhookCertSecret()
	if err != nil {
		return err
	}

	// Get das-operator deployment status for readyReplicas
	operatorDeployment, err := c.kubeClient.AppsV1().Deployments(c.namespace).Get(ctx, "das-operator", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get das-operator deployment status: %w", err)
	}

	if err := c.updateOperatorStatus(ctx, operatorDeployment, daemonset, scheduler, webhook); err != nil {
		return fmt.Errorf("failed to update operator status: %w", err)
	}

	return nil
}

func (c *TargetConfigReconciler) manageWebhookCertSecret() (*corev1.Secret, bool, error) {
	secret, err := c.secretLister.Secrets(c.namespace).Get(WebhookCertificateSecretName)
	// secret should be generated by the cert manager
	if err != nil {
		return nil, false, err
	}
	if len(secret.Data["tls.crt"]) == 0 || len(secret.Data["tls.key"]) == 0 {
		return nil, false, fmt.Errorf("%s secret is not initialized", secret.Name)
	}
	return secret, true, nil
}

func (c *TargetConfigReconciler) manageMutatingWebhookDeployment(ctx context.Context, ownerReference metav1.OwnerReference) (*appsv1.Deployment, bool, error) {
	required := resourceread.ReadDeploymentV1OrDie(bindata.MustAsset("assets/instaslice-operator/webhook-deployment.yaml"))
	required.Namespace = c.namespace
	required.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	for i := range required.Spec.Template.Spec.Containers {
		if c.targetWebhookImage != "" {
			required.Spec.Template.Spec.Containers[i].Image = c.targetWebhookImage
		}
	}
	err := injectCertManagerCA(required, c.namespace)
	if err != nil {
		return nil, false, err
	}
	return resourceapply.ApplyDeployment(ctx, c.kubeClient.AppsV1(), c.eventRecorder, required, resourcemerge.ExpectedDeploymentGeneration(required, c.generations))
}

func (c *TargetConfigReconciler) manageMutatingWebhookService(ctx context.Context, ownerReference metav1.OwnerReference) error {
	required := resourceread.ReadServiceV1OrDie(bindata.MustAsset("assets/instaslice-operator/webhook-service.yaml"))
	required.Namespace = c.namespace
	required.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err := resourceapply.ApplyService(ctx, c.kubeClient.CoreV1(), c.eventRecorder, required)
	if err != nil {
		return err
	}
	return nil
}

func (c *TargetConfigReconciler) manageMutatingWebhook(ctx context.Context, ownerReference metav1.OwnerReference) error {
	required := resourceread.ReadMutatingWebhookConfigurationV1OrDie(bindata.MustAsset("assets/instaslice-operator/webhook.yaml"))
	required.Namespace = c.namespace
	required.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	for i := range required.Webhooks {
		required.Webhooks[i].ClientConfig.Service.Namespace = c.namespace
	}
	err := injectCertManagerCA(required, c.namespace)
	if err != nil {
		return err
	}
	mutatingWebhook, updated, err := resourceapply.ApplyMutatingWebhookConfigurationImproved(ctx, c.kubeClient.AdmissionregistrationV1(), c.eventRecorder, required, c.cache)
	if err != nil {
		return err
	}
	if updated {
		resourcemerge.SetMutatingWebhooksConfigurationGeneration(&c.generations, mutatingWebhook)
	}
	return nil
}

func (c *TargetConfigReconciler) manageScheduler(ctx context.Context, ownerReference metav1.OwnerReference) (*appsv1.Deployment, bool, error) {
	schedulerConfig := resourceread.ReadConfigMapV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_config.yaml"))
	schedulerConfig.Namespace = c.namespace
	schedulerConfig.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err := resourceapply.ApplyConfigMap(ctx, c.kubeClient.CoreV1(), c.eventRecorder, schedulerConfig)
	if err != nil {
		return nil, false, err
	}

	schedulerCR := resourceread.ReadClusterRoleV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_rbac.clusterrole.yaml"))
	schedulerCR.Namespace = c.namespace
	schedulerCR.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err = resourceapply.ApplyClusterRole(ctx, c.kubeClient.RbacV1(), c.eventRecorder, schedulerCR)
	if err != nil {
		return nil, false, err
	}

	schedulerCRbac := resourceread.ReadClusterRoleBindingV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_rbac.clusterrolebinding.yaml"))
	schedulerCRbac.Namespace = c.namespace
	schedulerCRbac.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err = resourceapply.ApplyClusterRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, schedulerCRbac)
	if err != nil {
		return nil, false, err
	}

	schedulerRole := resourceread.ReadRoleV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_rbac.role.yaml"))
	schedulerRole.Namespace = c.namespace
	schedulerRole.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err = resourceapply.ApplyRole(ctx, c.kubeClient.RbacV1(), c.eventRecorder, schedulerRole)
	if err != nil {
		return nil, false, err
	}

	schedulerRoleBinding := resourceread.ReadClusterRoleBindingV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_rbac.clusterrolebinding.yaml"))
	schedulerRoleBinding.Namespace = c.namespace
	schedulerRoleBinding.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err = resourceapply.ApplyClusterRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, schedulerRoleBinding)
	if err != nil {
		return nil, false, err
	}

	schedulerSA := resourceread.ReadServiceAccountV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_serviceaccount.yaml"))
	schedulerSA.Namespace = c.namespace
	schedulerSA.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	_, _, err = resourceapply.ApplyServiceAccount(ctx, c.kubeClient.CoreV1(), c.eventRecorder, schedulerSA)
	if err != nil {
		return nil, false, err
	}

	scheduler := resourceread.ReadDeploymentV1OrDie(bindata.MustAsset("assets/instaslice-operator/scheduler_deployment.yaml"))
	scheduler.Namespace = c.namespace
	scheduler.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	if c.targetSchedulerImage != "" {
		for i := range scheduler.Spec.Template.Spec.Containers {
			scheduler.Spec.Template.Spec.Containers[i].Image = c.targetSchedulerImage
		}
	}
	return resourceapply.ApplyDeployment(ctx, c.kubeClient.AppsV1(), c.eventRecorder, scheduler, resourcemerge.ExpectedDeploymentGeneration(scheduler, c.generations))
}

func (c *TargetConfigReconciler) manageDaemonset(ctx context.Context, ownerReference metav1.OwnerReference) (*appsv1.DaemonSet, bool, error) {
	required := resourceread.ReadDaemonSetV1OrDie(bindata.MustAsset("assets/instaslice-operator/daemonset.yaml"))
	required.Namespace = c.namespace
	required.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	// Merge user-specified nodeSelector labels (if any)
	if len(c.nodeSelector) > 0 {
		if required.Spec.Template.Spec.NodeSelector == nil {
			required.Spec.Template.Spec.NodeSelector = make(map[string]string)
		}
		maps.Copy(required.Spec.Template.Spec.NodeSelector, c.nodeSelector)
	}
	for i := range required.Spec.Template.Spec.Containers {
		if c.targetDaemonsetImage != "" {
			required.Spec.Template.Spec.Containers[i].Image = c.targetDaemonsetImage
		}
		if c.emulatedMode == slicev1alpha1.EmulatedModeEnabled {
			required.Spec.Template.Spec.Containers[i].Env = append(required.Spec.Template.Spec.Containers[i].Env, corev1.EnvVar{
				Name:  "EMULATED_MODE",
				Value: slicev1alpha1.EmulatedModeEnabled,
			})
		}
	}
	return resourceapply.ApplyDaemonSet(ctx,
		c.appsClient,
		c.eventRecorder,
		required,
		resourcemerge.ExpectedDaemonSetGeneration(required, c.generations))
}

func (c *TargetConfigReconciler) manageIssuerCR(ctx context.Context, ownerReference metav1.OwnerReference) (*unstructured.Unstructured, bool, error) {
	gvr := schema.GroupVersionResource{
		Group:    "cert-manager.io",
		Version:  "v1",
		Resource: "issuers",
	}

	issuer, err := resourceread.ReadGenericWithUnstructured(bindata.MustAsset("assets/instaslice-operator/cert-manager-self-signed-issuer.yaml"))
	if err != nil {
		return nil, false, err
	}
	issuerAsUnstructured, ok := issuer.(*unstructured.Unstructured)
	if !ok {
		return nil, false, fmt.Errorf("issuer is not an Unstructured")
	}
	issuerAsUnstructured.SetNamespace(c.namespace)
	ownerReferences := issuerAsUnstructured.GetOwnerReferences()
	ownerReferences = append(ownerReferences, ownerReference)
	issuerAsUnstructured.SetOwnerReferences(ownerReferences)

	return resourceapply.ApplyUnstructuredResourceImproved(ctx, c.dynamicClient, c.eventRecorder, issuerAsUnstructured, c.resourceCache, gvr, nil, nil)
}

func (c *TargetConfigReconciler) manageCertificateWebhookCR(ctx context.Context, ownerReference metav1.OwnerReference) (*unstructured.Unstructured, bool, error) {
	gvr := schema.GroupVersionResource{
		Group:    "cert-manager.io",
		Version:  "v1",
		Resource: "certificates",
	}

	service := resourceread.ReadServiceV1OrDie(bindata.MustAsset("assets/instaslice-operator/webhook-service.yaml"))
	issuer, err := resourceread.ReadGenericWithUnstructured(bindata.MustAsset("assets/instaslice-operator/cert-manager-serving-cert.yaml"))
	if err != nil {
		return nil, false, err
	}
	issuerAsUnstructured, ok := issuer.(*unstructured.Unstructured)
	if !ok {
		return nil, false, fmt.Errorf("issuer is not an Unstructured")
	}
	issuerAsUnstructured.SetNamespace(c.namespace)
	ownerReferences := issuerAsUnstructured.GetOwnerReferences()
	ownerReferences = append(ownerReferences, ownerReference)
	issuerAsUnstructured.SetOwnerReferences(ownerReferences)
	dnsNames, found, err := unstructured.NestedStringSlice(issuerAsUnstructured.Object, "spec", "dnsNames")
	if !found || err != nil {
		return nil, false, fmt.Errorf("%v: .spec.dnsNames not found: %v", issuerAsUnstructured.GetName(), err)
	}
	for i := range dnsNames {
		dnsNames[i] = strings.Replace(dnsNames[i], "SERVICE_NAME", service.Name, 1)
		dnsNames[i] = strings.Replace(dnsNames[i], "SERVICE_NAMESPACE", c.namespace, 1)
	}
	_ = unstructured.SetNestedStringSlice(issuerAsUnstructured.Object, dnsNames, "spec", "dnsNames")
	return resourceapply.ApplyUnstructuredResourceImproved(ctx, c.dynamicClient, c.eventRecorder, issuerAsUnstructured, c.resourceCache, gvr, nil, nil)
}

func isResourceRegistered(discoveryClient discovery.DiscoveryInterface, gvk schema.GroupVersionKind) (bool, error) {
	apiResourceLists, err := discoveryClient.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, apiResource := range apiResourceLists.APIResources {
		if apiResource.Kind == gvk.Kind {
			return true, nil
		}
	}
	return false, nil
}

// updateOperatorStatus calculates and updates the DAS operator status based on component health.
func (c *TargetConfigReconciler) updateOperatorStatus(ctx context.Context, operatorDeployment *appsv1.Deployment, daemonset *appsv1.DaemonSet, scheduler *appsv1.Deployment, webhook *appsv1.Deployment) error {
	operatorReadyReplicas := operatorDeployment.Status.ReadyReplicas

	// Check component health for conditions
	operatorReady := operatorDeployment.Status.ReadyReplicas > 0
	schedulerReady := scheduler.Status.ReadyReplicas > 0
	webhookReady := webhook.Status.ReadyReplicas > 0
	daemonsetHealthy := daemonset.Status.NumberReady > 0

	operatorDesired := int32(1)
	if operatorDeployment.Spec.Replicas != nil {
		operatorDesired = *operatorDeployment.Spec.Replicas
	}
	schedulerDesired := int32(1)
	if scheduler.Spec.Replicas != nil {
		schedulerDesired = *scheduler.Spec.Replicas
	}
	webhookDesired := int32(1)
	if webhook.Spec.Replicas != nil {
		webhookDesired = *webhook.Spec.Replicas
	}

	operatorMatches := operatorDeployment.Status.ReadyReplicas == operatorDesired
	schedulerMatches := scheduler.Status.ReadyReplicas == schedulerDesired
	webhookMatches := webhook.Status.ReadyReplicas == webhookDesired

	conditions := c.buildOperatorConditions(operatorReady, schedulerReady, webhookReady, daemonsetHealthy, operatorMatches, schedulerMatches, webhookMatches)

	// Update status using the operator client.
	_, _, err := v1helpers.UpdateStatus(ctx, c.instasliceoperatorClient, func(status *operatorsv1.OperatorStatus) error {
		status.ReadyReplicas = operatorReadyReplicas
		status.Conditions = conditions
		return nil
	})

	return err
}

func (c *TargetConfigReconciler) buildOperatorConditions(operatorReady, schedulerReady, webhookReady, daemonsetHealthy, operatorMatches, schedulerMatches, webhookMatches bool) []operatorsv1.OperatorCondition {
	// OperatorAvailable: true if ≥1 operators AND ≥1 schedulers AND ≥1 webhooks are available.
	availableStatus := operatorsv1.ConditionFalse
	availableReason := "ComponentsNotAvailable"
	availableMessage := "Not all required components have at least 1 replica available"

	if operatorReady && schedulerReady && webhookReady && daemonsetHealthy {
		availableStatus = operatorsv1.ConditionTrue
		availableReason = "AllRequiredComponentsAvailable"
		availableMessage = "All required components have at least 1 replica available"
	} else {
		var unavailableComponents []string
		if !operatorReady {
			unavailableComponents = append(unavailableComponents, "operator")
		}
		if !schedulerReady {
			unavailableComponents = append(unavailableComponents, "scheduler")
		}
		if !webhookReady {
			unavailableComponents = append(unavailableComponents, "webhook")
		}
		if !daemonsetHealthy {
			unavailableComponents = append(unavailableComponents, "daemonset")
		}
		availableMessage = fmt.Sprintf("Components without available replicas: %s", strings.Join(unavailableComponents, ", "))
	}

	// Progressing condition
	progressingStatus := operatorsv1.ConditionFalse
	progressingReason := "AllComponentsAtDesiredState"
	progressingMessage := "All components are at desired state"

	if !operatorMatches || !schedulerMatches || !webhookMatches || !daemonsetHealthy {
		progressingStatus = operatorsv1.ConditionTrue
		progressingReason = "Reconciling"
		progressingMessage = "Components are being reconciled to desired state"
	}

	degradedStatus := operatorsv1.ConditionFalse
	degradedReason := "AllComponentsHealthy"
	degradedMessage := ""

	var mismatchedComponents []string
	if !operatorMatches {
		mismatchedComponents = append(mismatchedComponents, "operator")
	}
	if !schedulerMatches {
		mismatchedComponents = append(mismatchedComponents, "scheduler")
	}
	if !webhookMatches {
		mismatchedComponents = append(mismatchedComponents, "webhook")
	}
	if !daemonsetHealthy {
		mismatchedComponents = append(mismatchedComponents, "daemonset")
	}

	if len(mismatchedComponents) > 0 {
		degradedStatus = operatorsv1.ConditionTrue
		degradedReason = "ComponentReplicaMismatch"
		degradedMessage = fmt.Sprintf("Components not matching configured replica counts: %s", strings.Join(mismatchedComponents, ", "))
	}

	return []operatorsv1.OperatorCondition{
		{
			Type:               "Available",
			Status:             availableStatus,
			Reason:             availableReason,
			Message:            availableMessage,
			LastTransitionTime: metav1.Now(),
		},
		{
			Type:               "Progressing",
			Status:             progressingStatus,
			Reason:             progressingReason,
			Message:            progressingMessage,
			LastTransitionTime: metav1.Now(),
		},
		{
			Type:               "Degraded",
			Status:             degradedStatus,
			Reason:             degradedReason,
			Message:            degradedMessage,
			LastTransitionTime: metav1.Now(),
		},
	}
}

func injectCertManagerCA(obj metav1.Object, namespace string) error {
	annotations := obj.GetAnnotations()
	if _, ok := annotations[CertManagerInjectCaAnnotation]; !ok {
		return fmt.Errorf("%s is missing %s annotation", obj.GetName(), CertManagerInjectCaAnnotation)
	}
	injectAnnotation := annotations[CertManagerInjectCaAnnotation]
	injectAnnotation = strings.Replace(injectAnnotation, "CERTIFICATE_NAMESPACE", namespace, 1)
	injectAnnotation = strings.Replace(injectAnnotation, "CERTIFICATE_NAME", WebhookCertificateName, 1)
	annotations[CertManagerInjectCaAnnotation] = injectAnnotation
	obj.SetAnnotations(annotations)
	return nil
}
