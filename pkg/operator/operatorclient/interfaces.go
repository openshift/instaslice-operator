package operatorclient

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"

	operatorv1 "github.com/openshift/api/operator/v1"
	operatorapplyconfigurationv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/library-go/pkg/apiserver/jsonpatch"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	instasliceoperatorapiv1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	instasliceoperatorv1alpha1 "github.com/openshift/instaslice-operator/pkg/generated/applyconfiguration/instasliceoperator/v1alpha1"
	instasliceoperatorinterface "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/typed/instasliceoperator/v1alpha1"
	instasliceoperatorlisterv1alpha1 "github.com/openshift/instaslice-operator/pkg/generated/listers/instasliceoperator/v1alpha1"
)

const OperatorConfigName = "cluster"

var _ v1helpers.OperatorClient = &InstasliceOperatorSetClient{}

type InstasliceOperatorSetClient struct {
	Ctx               context.Context
	SharedInformer    cache.SharedIndexInformer
	OperatorClient    instasliceoperatorinterface.OpenShiftOperatorV1alpha1Interface
	Lister            instasliceoperatorlisterv1alpha1.InstasliceOperatorLister
	OperatorNamespace string
}

func (l *InstasliceOperatorSetClient) Informer() cache.SharedIndexInformer {
	return l.SharedInformer
}

func (l *InstasliceOperatorSetClient) GetObjectMeta() (meta *metav1.ObjectMeta, err error) {
	var instance *instasliceoperatorapiv1alpha1.InstasliceOperator
	if l.SharedInformer.HasSynced() {
		instance, err = l.Lister.InstasliceOperators(l.OperatorNamespace).Get(OperatorConfigName)
		if err != nil {
			return nil, err
		}
	} else {
		instance, err = l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Get(l.Ctx, OperatorConfigName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
	}
	return &instance.ObjectMeta, nil
}

func (l *InstasliceOperatorSetClient) GetOperatorState() (spec *operatorv1.OperatorSpec, status *operatorv1.OperatorStatus, resourceVersion string, err error) {
	if !l.SharedInformer.HasSynced() {
		return l.GetOperatorStateWithQuorum(l.Ctx)
	}
	instance, err := l.Lister.InstasliceOperators(l.OperatorNamespace).Get(OperatorConfigName)
	if err != nil {
		return nil, nil, "", err
	}
	return &instance.Spec.OperatorSpec, &instance.Status.OperatorStatus, instance.ResourceVersion, nil
}

func (l *InstasliceOperatorSetClient) GetOperatorStateWithQuorum(ctx context.Context) (spec *operatorv1.OperatorSpec, status *operatorv1.OperatorStatus, resourceVersion string, err error) {
	instance, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Get(ctx, OperatorConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, "", err
	}
	return &instance.Spec.OperatorSpec, &instance.Status.OperatorStatus, instance.ResourceVersion, nil
}

func (l *InstasliceOperatorSetClient) UpdateOperatorSpec(ctx context.Context, resourceVersion string, in *operatorv1.OperatorSpec) (out *operatorv1.OperatorSpec, newResourceVersion string, err error) {
	original, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Get(ctx, OperatorConfigName, metav1.GetOptions{ResourceVersion: resourceVersion})
	if err != nil {
		return nil, "", err
	}
	original.Spec.OperatorSpec = *in

	ret, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Update(ctx, original, metav1.UpdateOptions{})
	if err != nil {
		return nil, "", err
	}

	return &ret.Spec.OperatorSpec, ret.ResourceVersion, nil
}

func (l *InstasliceOperatorSetClient) UpdateOperatorStatus(ctx context.Context, resourceVersion string, in *operatorv1.OperatorStatus) (out *operatorv1.OperatorStatus, err error) {
	original, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Get(ctx, OperatorConfigName, metav1.GetOptions{ResourceVersion: resourceVersion})
	if err != nil {
		return nil, err
	}
	original.Status.OperatorStatus = *in

	ret, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).UpdateStatus(ctx, original, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	return &ret.Status.OperatorStatus, nil
}

func (l *InstasliceOperatorSetClient) ApplyOperatorSpec(ctx context.Context, fieldManager string, applyConfiguration *operatorapplyconfigurationv1.OperatorSpecApplyConfiguration) (err error) {
	if applyConfiguration == nil {
		return fmt.Errorf("applyConfiguration must have a value")
	}

	desiredSpec := &instasliceoperatorv1alpha1.InstasliceOperatorSpecApplyConfiguration{
		OperatorSpecApplyConfiguration: *applyConfiguration,
	}
	desired := instasliceoperatorv1alpha1.InstasliceOperator(OperatorConfigName, l.OperatorNamespace)
	desired.WithSpec(desiredSpec)

	instance, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Get(ctx, OperatorConfigName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	// do nothing and proceed with the apply
	case err != nil:
		return fmt.Errorf("unable to get operator configuration: %w", err)
	default:
		original, err := instasliceoperatorv1alpha1.ExtractInstasliceOperator(instance, fieldManager)
		if err != nil {
			return fmt.Errorf("unable to extract operator configuration from spec: %w", err)
		}
		if equality.Semantic.DeepEqual(original, desired) {
			return nil
		}
	}

	_, err = l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Apply(ctx, desired, metav1.ApplyOptions{
		Force:        true,
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("unable to Apply for operator using fieldManager %q: %w", fieldManager, err)
	}

	return nil
}

func (l *InstasliceOperatorSetClient) ApplyOperatorStatus(ctx context.Context, fieldManager string, applyConfiguration *operatorapplyconfigurationv1.OperatorStatusApplyConfiguration) (err error) {
	if applyConfiguration == nil {
		return fmt.Errorf("applyConfiguration must have a value")
	}

	desiredStatus := &instasliceoperatorv1alpha1.InstasliceOperatorStatusApplyConfiguration{
		OperatorStatusApplyConfiguration: *applyConfiguration,
	}
	desired := instasliceoperatorv1alpha1.InstasliceOperator(OperatorConfigName, l.OperatorNamespace)
	desired.WithStatus(desiredStatus)

	instance, err := l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Get(ctx, OperatorConfigName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// do nothing and proceed with the apply
		v1helpers.SetApplyConditionsLastTransitionTime(clock.RealClock{}, &desired.Status.Conditions, nil)
	case err != nil:
		return fmt.Errorf("unable to get operator configuration: %w", err)
	default:
		original, err := instasliceoperatorv1alpha1.ExtractInstasliceOperatorStatus(instance, fieldManager)
		if err != nil {
			return fmt.Errorf("unable to extract operator configuration from status: %w", err)
		}
		if equality.Semantic.DeepEqual(original, desired) {
			return nil
		}
		if original.Status != nil {
			v1helpers.SetApplyConditionsLastTransitionTime(clock.RealClock{}, &desired.Status.Conditions, original.Status.Conditions)
		} else {
			v1helpers.SetApplyConditionsLastTransitionTime(clock.RealClock{}, &desired.Status.Conditions, nil)
		}
	}

	_, err = l.OperatorClient.InstasliceOperators(l.OperatorNamespace).ApplyStatus(ctx, desired, metav1.ApplyOptions{
		Force:        true,
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("unable to ApplyStatus for operator using fieldManager %q: %w", fieldManager, err)
	}

	return nil
}

func (l *InstasliceOperatorSetClient) PatchOperatorStatus(ctx context.Context, jsonPatch *jsonpatch.PatchSet) (err error) {
	jsonPatchBytes, err := jsonPatch.Marshal()
	if err != nil {
		return err
	}
	_, err = l.OperatorClient.InstasliceOperators(l.OperatorNamespace).Patch(ctx, OperatorConfigName, types.JSONPatchType, jsonPatchBytes, metav1.PatchOptions{}, "/status")
	return err
}
