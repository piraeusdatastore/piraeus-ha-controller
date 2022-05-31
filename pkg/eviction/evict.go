package eviction

import (
	"context"
	"sync"

	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	canEvictV1      = true
	canEvictV1beta1 = true
	lock            = sync.Mutex{}
)

// Evict Pods using the best available method in Kubernetes.
//
// The available methods are, in order of preference:
// * v1 eviction API
// * v1beta1 eviction API
// * v1 Pod deletion API
// Note that eviction and deletion are basically the same thing in Kubernetes, but eviction may indicate intent better.
// This function keeps track of which method worked, and uses that method in later invocations.
func Evict(ctx context.Context, kubernetes kubernetes.Interface, namespace, name string, opts metav1.DeleteOptions) error {
	if canEvictV1 {
		err := kubernetes.CoreV1().Pods(namespace).EvictV1(ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			DeleteOptions: &opts,
		})
		if !errors.IsNotFound(err) {
			return err
		}

		lock.Lock()
		canEvictV1 = false
		lock.Unlock()
	}

	if canEvictV1beta1 {
		err := kubernetes.CoreV1().Pods(namespace).EvictV1beta1(ctx, &policyv1beta1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			DeleteOptions: &opts,
		})
		if !errors.IsNotFound(err) {
			return err
		}

		lock.Lock()
		canEvictV1beta1 = false
		lock.Unlock()
	}

	return kubernetes.CoreV1().Pods(namespace).Delete(ctx, name, opts)
}
