package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/eviction"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

var _ Reconciler = &failoverReconciler{}

type failoverReconciler struct {
	opt          *Options
	client       kubernetes.Interface
	drbdFailedAt map[string]time.Time
	mu           sync.Mutex
}

// NewFailoverReconciler creates a reconciler that "fails over" pods that are on storage without quorum.
//
// The reconciler recognizes storage without quorum by:
// * Having the local copy be promotable
// * Having pods running
// * Have pods that are mounting the volume read-write (otherwise the promotable info is useless)
// * Have a connection to the peer node that is not connected
// If all of these are true, it waits for a short timeout, before starting the actual "fail over" process.
// The process involves:
// * Adding a taint on the node, causing new Pods to avoid the node.
// * Evicting all pods using that volume from the failed node, creating new pods to replace them.
// * Delete the volume attachment, informing Kubernetes that attaching the volume to a new node is fine.
func NewFailoverReconciler(opt *Options, client kubernetes.Interface) Reconciler {
	return &failoverReconciler{
		opt:          opt,
		client:       client,
		drbdFailedAt: make(map[string]time.Time),
	}
}

func (f *failoverReconciler) RunForResource(ctx context.Context, req *ReconcileRequest, recorder events.EventRecorder) error {
	klog.V(3).Infof("check if resource '%s' is promotable, triggering fail-over if required", req.Resource.Name)

	if !req.Resource.MayPromote() {
		klog.V(4).Infof("resource '%s' is not promotable", req.Resource.Name)

		f.mu.Lock()
		delete(f.drbdFailedAt, req.Resource.Name)
		f.mu.Unlock()
		return nil
	}

	if !hasPersistentVolumeClaimRef(req.Volume) {
		klog.V(4).Infof("resource '%s' has no referenced claim", req.Resource.Name)
		return nil
	}

	if allPodsMountReadOnly(req.Pods, req.Volume.Spec.ClaimRef.Name) {
		klog.V(4).Infof("resource '%s' is only mounted read-only, fail over not yet implemented", req.Resource.Name)
		return nil
	}

	for _, conn := range req.Resource.Connections {
		f.reconcileConnection(ctx, req, conn, recorder)
	}

	return nil
}

func (f *failoverReconciler) reconcileConnection(ctx context.Context, req *ReconcileRequest, conn DrbdConnection, recorder events.EventRecorder) {
	if conn.Name == f.opt.NodeName {
		return
	}

	if conn.ConnectionState == "Connected" {
		klog.V(3).Infof("resource '%s' on node '%s' connected", req.Resource.Name, conn.Name)
		return
	}

	node := req.FindNode(conn.Name)
	if node == nil {
		klog.V(3).Infof("Node triggering fail-over does not exist in cluster")
		return
	}

	var nodePods []*corev1.Pod
	for _, pod := range req.Pods {
		if pod.Spec.NodeName == conn.Name {
			nodePods = append(nodePods, pod)
		}
	}

	var va *storagev1.VolumeAttachment
	for _, v := range req.Attachments {
		if v.Spec.NodeName == conn.Name {
			va = v
			break
		}
	}

	if len(nodePods) == 0 && va == nil {
		klog.V(3).Infof("resource '%s' on node '%s' has failed, but nothing to evict", req.Resource.Name, conn.Name)
		return
	}

	f.mu.Lock()
	failedAt, ok := f.drbdFailedAt[req.Resource.Name]
	if !ok {
		f.drbdFailedAt[req.Resource.Name] = req.RefTime
		failedAt = req.RefTime
	}
	f.mu.Unlock()

	if failedAt.Add(f.opt.FailOverTimeout).After(req.RefTime) {
		klog.V(3).Infof("resource '%s' on node '%s' has not reached fail-over timeout", req.Resource.Name, conn.Name)
		return
	}

	klog.V(1).Infof("resource '%s' on node '%s' has failed, evicting", req.Resource.Name, conn.Name)

	err := f.evictPods(ctx, req.Resource, req.Volume, nodePods, va, node, req.RefTime, recorder)
	if err != nil {
		klog.V(1).ErrorS(err, "failed to fail-over resource")
	}
}

func (f *failoverReconciler) evictPods(ctx context.Context, res *DrbdResourceState, pv *corev1.PersistentVolume, pods []*corev1.Pod, attachment *storagev1.VolumeAttachment, node *corev1.Node, refTime time.Time, recorder events.EventRecorder) error {
	taintTime := metav1.NewTime(refTime)
	tainted, err := TaintNode(ctx, f.client, node, corev1.Taint{
		Key:       metadata.NodeLostQuorumTaint,
		Effect:    corev1.TaintEffectNoSchedule,
		TimeAdded: &taintTime,
	})
	if err != nil {
		return err
	}

	if tainted {
		// NB: don't accidentally create a "nil interface value", which would confuse the event-recorder into panicking.
		if attachment != nil {
			recorder.Eventf(node, attachment, corev1.EventTypeWarning, metadata.ReasonNodeStorageQuorumLost, metadata.ActionTaintedNode, "Tainted node because some volumes have lost quorum")
		} else {
			recorder.Eventf(node, nil, corev1.EventTypeWarning, metadata.ReasonNodeStorageQuorumLost, metadata.ActionTaintedNode, "Tainted node because some volumes have lost quorum")
		}

	}

	klog.V(2).Infof("resource '%s' requires eviction of %d pods", res.Name, len(pods))

	errs := make([]error, len(pods))
	wg := sync.WaitGroup{}
	wg.Add(len(pods))

	for i := range pods {
		go func(pod *corev1.Pod, errLoc *error) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, f.opt.Timeout())
			defer cancel()

			klog.V(2).Infof("Evicting pod '%s/%s'", pod.Namespace, pod.Name)

			if len(pod.ObjectMeta.OwnerReferences) == 0 {
				klog.V(2).Infof("Pod '%s/%s' does not have an owner", pod.Namespace, pod.Name)
				return
			}

			if _, exists := pod.ObjectMeta.Annotations[metadata.AnnotationIgnoreFailOver]; exists {
				klog.V(2).Infof("Pod '%s/%s' is exempt from eviction per annotation", pod.Namespace, pod.Name)
				return
			}

			if nodeReady(node) {
				klog.V(2).Infof("Node %s running Pod '%s/%s' appears ready, using graceful eviction", node.Name, pod.Namespace, pod.Name)

				err := eviction.Evict(ctx, f.client, pod.Namespace, pod.Name, metav1.DeleteOptions{
					GracePeriodSeconds: &f.opt.DeletionGraceSec,
					Preconditions:      metav1.NewUIDPreconditions(string(pod.UID)),
				})
				if ignoreNotFound(err) != nil {
					klog.V(2).ErrorS(err, "failed to evict pod", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
					*errLoc = err
					return
				}
			} else {
				klog.V(2).Infof("Node %s running Pod '%s/%s' appears not ready, using forceful deletion", node.Name, pod.Namespace, pod.Name)

				noGrace := int64(0)
				err := f.client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
					GracePeriodSeconds: &noGrace,
					Preconditions:      metav1.NewUIDPreconditions(string(pod.UID)),
				})
				if ignoreNotFound(err) != nil {
					klog.V(2).ErrorS(err, "failed to delete pod", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
					*errLoc = err
					return
				}
			}

			// NB: don't accidentally create a "nil interface value", which would confuse the event-recorder into panicking.
			if pv != nil {
				recorder.Eventf(pod, pv, corev1.EventTypeWarning, metadata.ReasonVolumeWithoutQuorum, metadata.ActionEvictedPod, "Pod was evicted because attached volume lost quorum")
			} else {
				recorder.Eventf(pod, nil, corev1.EventTypeWarning, metadata.ReasonVolumeWithoutQuorum, metadata.ActionEvictedPod, "Pod was evicted because attached volume lost quorum")
			}
		}(pods[i], &errs[i])
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return fmt.Errorf("failed to evict at least one pod: %w", err)
		}
	}

	if attachment != nil {
		klog.V(2).Infof("resource '%s' requires force detach of node %s", res.Name, attachment.Spec.NodeName)

		gracePeriod := f.opt.DeletionGraceSec
		if !nodeReady(node) {
			gracePeriod = 0
		}

		err = f.client.StorageV1().VolumeAttachments().Delete(ctx, attachment.Name, metav1.DeleteOptions{
			Preconditions:      metav1.NewUIDPreconditions(string(attachment.UID)),
			GracePeriodSeconds: &gracePeriod,
		})
		if err != nil {
			klog.V(2).ErrorS(err, "failed to force detach of node", "node", attachment.Spec.NodeName)
			return fmt.Errorf("failed force detach: %w", err)
		}

		recorder.Eventf(attachment, nil, corev1.EventTypeWarning, metadata.ReasonVolumeWithoutQuorum, metadata.ActionVolumeAttachmentDeleted, "Volume attachment was force-detached because node lost quorum")
	}

	return nil
}

func ignoreNotFound(err error) error {
	if errors.IsNotFound(err) {
		return nil
	}

	return err
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return false
}

func allPodsMountReadOnly(pods []*corev1.Pod, pvcName string) bool {
	for _, pod := range pods {
		for i := range pod.Spec.Volumes {
			pvc := pod.Spec.Volumes[i].PersistentVolumeClaim

			if pvc != nil && pvc.ClaimName == pvcName && !pvc.ReadOnly {
				return false
			}
		}
	}

	return true
}
