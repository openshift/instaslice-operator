package scheduler

import (
   "testing"

   corev1 "k8s.io/api/core/v1"
   metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodToleratesTaints(t *testing.T) {
   tests := []struct{
       name      string
       taints    []corev1.Taint
       tolerations []corev1.Toleration
       want      bool
   }{
       {
           name: "no taints",
           taints: nil,
           tolerations: nil,
           want: true,
       },
       {
           name: "taint without toleration",
           taints: []corev1.Taint{{Key: "foo", Value: "bar", Effect: corev1.TaintEffectNoSchedule}},
           tolerations: nil,
           want: false,
       },
       {
           name: "toleration exists",
           taints: []corev1.Taint{{Key: "foo", Value: "bar", Effect: corev1.TaintEffectNoSchedule}},
           tolerations: []corev1.Toleration{{Key: "foo", Operator: corev1.TolerationOpEqual, Value: "bar", Effect: corev1.TaintEffectNoSchedule}},
           want: true,
       },
       {
           name: "toleration exists but wrong value",
           taints: []corev1.Taint{{Key: "foo", Value: "bar", Effect: corev1.TaintEffectNoSchedule}},
           tolerations: []corev1.Toleration{{Key: "foo", Operator: corev1.TolerationOpEqual, Value: "baz", Effect: corev1.TaintEffectNoSchedule}},
           want: false,
       },
       {
           name: "exists operator only",
           taints: []corev1.Taint{{Key: "foo", Value: "", Effect: corev1.TaintEffectNoSchedule}},
           tolerations: []corev1.Toleration{{Key: "foo", Operator: corev1.TolerationOpExists}},
           want: true,
       },
   }
   for _, tc := range tests {
       pod := &corev1.Pod{
           ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"},
           Spec: corev1.PodSpec{Tolerations: tc.tolerations},
       }
       ok := podToleratesTaints(pod, tc.taints)
       if ok != tc.want {
           t.Errorf("%s: got %v, want %v", tc.name, ok, tc.want)
       }
   }
}