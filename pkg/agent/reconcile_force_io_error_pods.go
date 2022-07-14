package agent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/eviction"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

type forceIoErrorReconciler struct {
	opt    *Options
	client kubernetes.Interface
}

var _ Reconciler = &forceIoErrorReconciler{}

// NewForceIoErrorReconciler creates a reconciler that evicts pods if a volume is reporting IO errors.
//
// If DRBD is in "force IO failures" mode, all opener processes will see IO errors. This lasts until all openers
// closed the DRBD device, at which point DRBD will start behaving normally again. In order for all openers to be
// closed we need to force all local Pods to be stopped. This is what this reconciler does:
// * Adding a taint on the node, causing new Pods to avoid the node.
// * Evicting all pods using that volume from the failed node, creating new pods to replace them.
func NewForceIoErrorReconciler(opt *Options, client kubernetes.Interface) Reconciler {
	return &forceIoErrorReconciler{
		opt:    opt,
		client: client,
	}
}

func (f *forceIoErrorReconciler) RunForResource(ctx context.Context, req *ReconcileRequest, recorder events.EventRecorder) error {
	if !req.Resource.ForceIoFailures {
		klog.V(4).Infof("resource '%s' is not forcing io-errors", req.Resource.Name)
		return nil
	}

	// NB: we use runtime.Object here, so we don't accidentally create a "nil interface value", which would
	// confuse the event-recorder into panicking.
	var matchingVA runtime.Object
	for _, va := range req.Attachments {
		if va.Spec.NodeName == f.opt.NodeName {
			matchingVA = va
			break
		}
	}

	localPods := f.LocalAttachedPods(req.Pods)

	if len(localPods) == 0 {
		klog.V(2).Infof("No pods to evict")
		return nil
	}

	ownNode := req.FindNode(f.opt.NodeName)
	taintTime := metav1.NewTime(req.RefTime)
	added, err := TaintNode(ctx, f.client, ownNode, corev1.Taint{
		Key:       metadata.NodeForceIoErrorTaint,
		Effect:    corev1.TaintEffectNoSchedule,
		TimeAdded: &taintTime,
	})
	if err != nil {
		return err
	}

	if added {
		recorder.Eventf(ownNode, nil, corev1.EventTypeWarning, metadata.ReasonVolumeForcingIoErrors, metadata.ActionTaintedNode, "Tainted node because some volumes are forcing IO errors")
	}

	for _, pod := range localPods {
		klog.V(1).Infof("Pod '%s/%s' is using a resource that reports io-errors, deleting", pod.Namespace, pod.Name)

		if len(pod.ObjectMeta.OwnerReferences) == 0 {
			return fmt.Errorf("pod '%s/%s' does not have an owner", pod.Namespace, pod.Name)
		}

		// NB: we only evict here, and never do a forceful deletion as in the failover case. In here we only evict
		// Pods that are running on the local node, so kubelet better be able to terminate Pods.

		err = eviction.Evict(ctx, f.client, pod.Namespace, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: &f.opt.DeletionGraceSec,
			Preconditions:      metav1.NewUIDPreconditions(string(pod.UID)),
		})
		if ignoreNotFound(err) != nil {
			return fmt.Errorf("failed to evict pod '%s/%s' on failing storage: %w", pod.Namespace, pod.Name, err)
		}

		recorder.Eventf(pod, matchingVA, corev1.EventTypeWarning, metadata.ReasonVolumeForcingIoErrors, metadata.ActionEvictedPod, "Pod was evicted because attached volume reports io-errors")
	}

	return nil
}

func (f *forceIoErrorReconciler) LocalAttachedPods(pods []*corev1.Pod) []*corev1.Pod {
	var localPods []*corev1.Pod

	for _, pod := range pods {
		if pod.Spec.NodeName != f.opt.NodeName {
			klog.V(4).Infof("Pod '%s/%s' not running on this node", pod.Namespace, pod.Name)
			continue
		}

		if len(pod.ObjectMeta.OwnerReferences) == 0 {
			klog.V(4).Infof("Pod '%s/%s' does not have an owner", pod.Namespace, pod.Name)
			continue
		}

		if _, exists := pod.ObjectMeta.Annotations[metadata.AnnotationIgnoreFailOver]; exists {
			klog.V(4).Infof("Pod '%s/%s' is exempt from automatic fail over", pod.Namespace, pod.Name)
			continue
		}

		localPods = append(localPods, pod)
	}

	return localPods
}
