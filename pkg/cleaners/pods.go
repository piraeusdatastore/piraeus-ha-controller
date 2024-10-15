package cleaners

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

func CleanPod(pod *corev1.Pod, labelRequirements labels.Requirements) *corev1.Pod {
	neededLabels := map[string]string{}
	for i := range labelRequirements {
		k := labelRequirements[i].Key()
		if v, ok := pod.Labels[k]; ok {
			neededLabels[k] = v
		}
	}

	neededAnnotations := map[string]string{}
	if v, ok := pod.Annotations[metadata.AnnotationIgnoreFailOver]; ok {
		neededAnnotations[metadata.AnnotationIgnoreFailOver] = v
	}

	return &corev1.Pod{
		TypeMeta: pod.TypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Name:              pod.Name,
			Namespace:         pod.Namespace,
			UID:               pod.UID,
			ResourceVersion:   pod.ResourceVersion,
			Labels:            neededLabels,
			Annotations:       neededAnnotations,
			OwnerReferences:   pod.OwnerReferences,
			DeletionTimestamp: pod.DeletionTimestamp,
		},
		Spec: corev1.PodSpec{
			// Used in "agent", "failover", "force io error pods" and "suspeded pod termination".
			NodeName: pod.Spec.NodeName,
			// Used in "agent" and "failover".
			Volumes: pod.Spec.Volumes,
		},
		Status: corev1.PodStatus{
			// Used in "failover" and "suspended pod termination".
			Phase: pod.Status.Phase,
		},
	}
}
