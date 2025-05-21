package operator

import (
	"context"
	"fmt"
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
	slicev1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	instasliceoperatorv1alphaclientset "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/typed/instasliceoperator/v1alpha1"
	operatorclientv1alpha1informers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions/instasliceoperator/v1alpha1"

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
	WebhookCertificateName        = "instaslice-serving-cert"
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
	instasliceInformer         operatorclientv1alpha1informers.InstasliceInformer
	instasliceoperatorClient   *operatorclient.InstasliceOperatorSetClient
	kubeClient                 kubernetes.Interface
	kubeInformersForNamespaces v1helpers.KubeInformersForNamespaces
	namespace                  string
	operatorClient             instasliceoperatorv1alphaclientset.InstasliceOperatorInterface
	resourceCache              resourceapply.ResourceCache
	secretLister               v1.SecretLister
	targetDaemonsetImage       string
	targetWebhookImage         string
	emulatedMode               slicev1alpha1.EmulatedMode
}

func NewTargetConfigReconciler(
	targetDaemonsetImage string,
	targetWebhookImage string,
	namespace string,
	operatorConfigClient instasliceoperatorv1alphaclientset.InstasliceOperatorInterface,
	operatorClientInformer operatorclientv1alpha1informers.InstasliceOperatorInformer,
	kubeInformersForNamespaces v1helpers.KubeInformersForNamespaces,
	appsClient appsv1client.DaemonSetsGetter,
	instasliceoperatorClient *operatorclient.InstasliceOperatorSetClient,
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
		cache:                      resourceapply.NewResourceCache(),
		emulatedMode:               slicev1alpha1.EmulatedModeDisabled,
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
		kubeInformersForNamespaces.InformersFor(namespace).Core().V1().Services().Informer(),
	).ResyncEvery(time.Minute*5).
		WithSync(c.sync).
		WithSyncDegradedOnError(instasliceoperatorClient).
		ToController("InstasliceOperatorController", eventRecorder)
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

	c.emulatedMode = sliceOperator.Spec.EmulatedMode

	ownerReference := metav1.OwnerReference{
		APIVersion: "inference.redhat.com/v1alpha1",
		Kind:       "InstasliceOperator",
		Name:       sliceOperator.Name,
		UID:        sliceOperator.UID,
	}

	klog.V(2).InfoS("Got operator config", "emulated_mode", c.emulatedMode)

	if _, _, err := c.manageDaemonset(ctx, ownerReference); err != nil {
		return err
	}

	if err := c.manageMutatingWebhookDeployment(ctx, ownerReference); err != nil {
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
	return secret, false, nil
}

func (c *TargetConfigReconciler) manageMutatingWebhookDeployment(ctx context.Context, ownerReference metav1.OwnerReference) error {
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
		return err
	}
	_, _, err = resourceapply.ApplyDeployment(ctx, c.kubeClient.AppsV1(), c.eventRecorder, required, resourcemerge.ExpectedDeploymentGeneration(required, c.generations))
	if err != nil {
		return err
	}
	return nil
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

func (c *TargetConfigReconciler) manageDaemonset(ctx context.Context, ownerReference metav1.OwnerReference) (*appsv1.DaemonSet, bool, error) {
	required := resourceread.ReadDaemonSetV1OrDie(bindata.MustAsset("assets/instaslice-operator/daemonset.yaml"))
	required.Namespace = c.namespace
	required.OwnerReferences = []metav1.OwnerReference{
		ownerReference,
	}
	for i := range required.Spec.Template.Spec.Containers {
		if c.targetDaemonsetImage != "" {
			required.Spec.Template.Spec.Containers[i].Image = c.targetDaemonsetImage
		}
		for j := range required.Spec.Template.Spec.Containers[i].Env {
			if required.Spec.Template.Spec.Containers[i].Env[j].Name == "EMULATED_MODE" {
				required.Spec.Template.Spec.Containers[i].Env[j].Value = fmt.Sprintf("%v", c.emulatedMode)
			}
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
	unstructured.SetNestedStringSlice(issuerAsUnstructured.Object, dnsNames, "spec", "dnsNames")
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
