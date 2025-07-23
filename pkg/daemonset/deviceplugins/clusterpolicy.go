package deviceplugins

import (
	"context"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

func waitForClusterPolicy(ctx context.Context, dynClient dynamic.Interface) error {
	gvr := schema.GroupVersionResource{Group: "nvidia.com", Version: "v1", Resource: "clusterpolicies"}
	return wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		obj, err := dynClient.Resource(gvr).Get(ctx, "gpu-cluster-policy", metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.InfoS("Waiting for ClusterPolicy", "name", "gpu-cluster-policy")
				return false, nil
			}
			klog.ErrorS(err, "failed to get ClusterPolicy")
			return false, nil
		}

		state, _, _ := unstructured.NestedString(obj.Object, "status", "state")
		if strings.EqualFold(state, "ready") {
			return true, nil
		}

		if conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); found {
			for _, c := range conds {
				if m, ok := c.(map[string]interface{}); ok {
					t, _, _ := unstructured.NestedString(m, "type")
					st, _, _ := unstructured.NestedString(m, "status")
					if t == "Ready" && strings.EqualFold(st, "True") {
						return true, nil
					}
				}
			}
		}

		klog.InfoS("Nvidia GPU Operator ClusterPolicy not ready", "state", state)
		return false, nil
	})
}
