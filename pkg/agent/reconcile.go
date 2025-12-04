package agent

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/client-go/tools/events"
)

type ReconcileRequest struct {
	RefTime     time.Time
	Resource    *DrbdResource
	Volume      *corev1.PersistentVolume
	Pods        []*corev1.Pod
	Attachments []*storagev1.VolumeAttachment
	Nodes       []*corev1.Node
}

type Reconciler interface {
	RunForResource(ctx context.Context, req *ReconcileRequest, recorder events.EventRecorder) error
}

func (r *ReconcileRequest) FindNode(name string) *corev1.Node {
	for _, node := range r.Nodes {
		if node.Name == name {
			return node
		}
	}

	return nil
}
