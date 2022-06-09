package eviction

import (
	"context"
	"fmt"

	"golang.org/x/sync/singleflight"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

var (
	evictVersion = ""
	initDone     = false
	sfg          = singleflight.Group{}
)

// Evict Pods using the best available method in Kubernetes.
//
// The available methods are, in order of preference:
// * v1 eviction API
// * v1beta1 eviction API
// * v1 Pod deletion API
// Note that eviction and deletion are basically the same thing in Kubernetes, but eviction may indicate intent better.
func Evict(ctx context.Context, kubernetes kubernetes.Interface, namespace, name string, opts metav1.DeleteOptions) error {
	evictVersion, err, _ := sfg.Do("init", func() (interface{}, error) {
		if initDone {
			return evictVersion, nil
		}

		result, err := findSupportedEvictVersion(kubernetes)
		if err != nil {
			return "", err
		}

		evictVersion = result
		initDone = true
		return result, nil
	})
	if err != nil {
		return err
	}

	if evictVersion == "v1" {
		return kubernetes.CoreV1().Pods(namespace).EvictV1(ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			DeleteOptions: &opts,
		})
	}

	if evictVersion == "v1beta1" {
		return kubernetes.CoreV1().Pods(namespace).EvictV1beta1(ctx, &policyv1beta1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			DeleteOptions: &opts,
		})
	}

	return kubernetes.CoreV1().Pods(namespace).Delete(ctx, name, opts)
}

func findSupportedEvictVersion(client kubernetes.Interface) (string, error) {
	resources, err := client.Discovery().ServerResourcesForGroupVersion("v1")
	if err != nil {
		return "", fmt.Errorf("failed to get resource list: %w", err)
	}

	for _, res := range resources.APIResources {
		if res.Name == "pods/eviction" {
			klog.V(2).Infof("API server supports pod eviction %s", res.Version)
			return res.Version, nil
		}
	}

	klog.V(2).Infof("API server does not support pod eviction")

	return "", nil
}
