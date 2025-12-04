package agent

import (
	"context"
	"fmt"
	"os/exec"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

type suspendedPodReconciler struct {
	opt *Options
}

var _ Reconciler = &suspendedPodReconciler{}

// NewSuspendedPodReconciler creates a reconciler that gets suspended Pods to resume termination.
//
// While DRBD is suspending IO, all processes (including Pods and filesystems) using the device are stuck. In order
// to resume, one can force DRBD to report IO errors instead. The reconciler does just that if a local Pod should
// be stopped while it is suspended by DRBD. This enables a (relatively) clean shutdown of the resource without
// node reboot.
func NewSuspendedPodReconciler(opt *Options) Reconciler {
	return &suspendedPodReconciler{
		opt: opt,
	}
}

func (s *suspendedPodReconciler) RunForResource(ctx context.Context, req *ReconcileRequest, recorder events.EventRecorder) error {
	klog.V(3).Infof("checking if resource needs to be forced to become secondary")

	if !req.Resource.State.Suspended {
		klog.V(4).Infof("resource '%s' not suspended", req.Resource.Name)
		return nil
	}

	if !req.Resource.State.Primary() {
		klog.V(4).Infof("resource '%s' not primary", req.Resource.Name)
		return nil
	}

	// NB: we use runtime.Object here, so we don't accidentally create a "nil interface value", which would
	// confuse the event-recorder into panicking.
	var matchingVA runtime.Object
	for _, va := range req.Attachments {
		if va.Spec.NodeName == s.opt.NodeName {
			matchingVA = va
			break
		}
	}

	for _, pod := range req.Pods {
		if pod.Spec.NodeName != s.opt.NodeName {
			klog.V(4).Infof("Pod '%s/%s' not running on this node", pod.Namespace, pod.Name)
			continue
		}

		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			klog.V(4).Infof("consumer pod '%s/%s' of resource '%s' is not terminating, no forced demotion", pod.Namespace, pod.Name, req.Resource.Name)
			return nil
		}
	}

	klog.V(1).Infof("force demotion of resource '%s'", req.Resource.Name)

	// Always run "--force" here: we know the resource is suspended, so without --force demotion will time out.
	cmd := []string{"drbdsetup", "secondary", "--force", req.Resource.Name}
	klog.V(4).Infof("running command: %v", cmd)

	_, err := exec.CommandContext(ctx, cmd[0], cmd[1:]...).Output()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			return fmt.Errorf("force demotion of resource '%s' failed: %w", req.Resource.Name, err)
		}

		return fmt.Errorf("force demotion of resource '%s' failed: %w, stderr: %s", req.Resource.Name, err, exitErr.Stderr)
	}

	recorder.Eventf(req.Volume, matchingVA, corev1.EventTypeWarning, metadata.ReasonVolumeWithoutQuorum, metadata.ActionForcedVolumeDemotion, "Volume is forcefully demoted as there are Pods stuck terminating")

	if req.Volume.Spec.ClaimRef != nil {
		recorder.Eventf(req.Volume.Spec.ClaimRef, matchingVA, corev1.EventTypeWarning, metadata.ReasonVolumeWithoutQuorum, metadata.ActionForcedVolumeDemotion, "Volume is forcefully demoted as there are Pods stuck terminating")
	}

	return nil
}
