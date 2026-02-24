package resourceapply

import (
	"context"
	"fmt"

	"github.com/openshift/library-go/pkg/operator/events"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	networkingclientv1 "k8s.io/client-go/kubernetes/typed/networking/v1"

	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourcehelper"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"

	"k8s.io/klog/v2"
)

// TODO: move to library-go
var (
	networkingScheme = runtime.NewScheme()
	networkingCodecs = serializer.NewCodecFactory(networkingScheme)
)

func init() {
	if err := networkingv1.AddToScheme(networkingScheme); err != nil {
		panic(fmt.Errorf("failed to add to scheme: %w", err))
	}
}

// ReadNetworkPolicyV1OrDie decodes raw bytes into a NetworkPolicy object or panics.
func ReadNetworkPolicyV1OrDie(bytes []byte) *networkingv1.NetworkPolicy {
	obj, err := runtime.Decode(networkingCodecs.UniversalDecoder(networkingv1.SchemeGroupVersion), bytes)
	if err != nil {
		panic(fmt.Errorf("failed to decode raw bytes into NetworkPolicy object: %w", err))
	}
	return obj.(*networkingv1.NetworkPolicy)
}

// ApplyNetworkPolicy applies the desired NetworkPolicy to the cluster.
func ApplyNetworkPolicy(ctx context.Context, getter networkingclientv1.NetworkPoliciesGetter, recorder events.Recorder, want *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, bool, error) {
	client := getter.NetworkPolicies(want.Namespace)
	current, err := client.Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, err
		}

		copy := want.DeepCopy()
		current, err := client.Create(ctx, resourcemerge.WithCleanLabelsAndAnnotations(copy).(*networkingv1.NetworkPolicy), metav1.CreateOptions{})
		resourcehelper.ReportCreateEvent(recorder, want, err)
		return current, false, err
	}

	diff := false
	copy := current.DeepCopy()
	resourcemerge.EnsureObjectMeta(&diff, &copy.ObjectMeta, want.ObjectMeta)
	if !diff && equality.Semantic.DeepEqual(current.Spec, want.Spec) {
		return current, false, nil
	}

	copy.Spec = *want.Spec.DeepCopy()
	if klog.V(2).Enabled() {
		klog.Infof("NetworkPolicy %q changes: %v", want.Namespace+"/"+want.Name, resourceapply.JSONPatchNoError(current, copy))
	}

	current, err = client.Update(ctx, copy, metav1.UpdateOptions{})
	resourcehelper.ReportUpdateEvent(recorder, want, err)
	return current, true, err
}
