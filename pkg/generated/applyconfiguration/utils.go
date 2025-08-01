/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package applyconfiguration

import (
	v1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	dasoperatorv1alpha1 "github.com/openshift/instaslice-operator/pkg/generated/applyconfiguration/dasoperator/v1alpha1"
	internal "github.com/openshift/instaslice-operator/pkg/generated/applyconfiguration/internal"
	runtime "k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	testing "k8s.io/client-go/testing"
)

// ForKind returns an apply configuration type for the given GroupVersionKind, or nil if no
// apply configuration type exists for the given GroupVersionKind.
func ForKind(kind schema.GroupVersionKind) interface{} {
	switch kind {
	// Group=inference.redhat.com, Version=v1alpha1
	case v1alpha1.SchemeGroupVersion.WithKind("AllocationClaim"):
		return &dasoperatorv1alpha1.AllocationClaimApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("AllocationClaimStatus"):
		return &dasoperatorv1alpha1.AllocationClaimStatusApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("DASOperator"):
		return &dasoperatorv1alpha1.DASOperatorApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("DASOperatorSpec"):
		return &dasoperatorv1alpha1.DASOperatorSpecApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("DASOperatorStatus"):
		return &dasoperatorv1alpha1.DASOperatorStatusApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("NodeAccelerator"):
		return &dasoperatorv1alpha1.NodeAcceleratorApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("NodeAcceleratorSpec"):
		return &dasoperatorv1alpha1.NodeAcceleratorSpecApplyConfiguration{}
	case v1alpha1.SchemeGroupVersion.WithKind("NodeAcceleratorStatus"):
		return &dasoperatorv1alpha1.NodeAcceleratorStatusApplyConfiguration{}

	}
	return nil
}

func NewTypeConverter(scheme *runtime.Scheme) *testing.TypeConverter {
	return &testing.TypeConverter{Scheme: scheme, TypeResolver: internal.Parser()}
}
