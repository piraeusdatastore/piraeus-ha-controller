// A HAController monitors Pods and their attached PersistentVolumes
// and removes Pods whose storage is "unhealthy".
//
// In general, the HAController uses two inputs: The Kubernetes API,
// to get information about Pods and their volumes, and a user provided
// stream of "failing" volumes. Once it is notified of a failing volume,
// it will try to delete the currently attached Pod and VolumeAttachment,
// which triggers Kubernetes to recreate the Pod with healthy volumes.
//
// "Failing" in this context means that the current resource user is not
// able to write to the volume, i.e. it is safe to attach the volume from
// another node in ReadWriteOnce scenarios.
package hacontroller

import (
	"context"
	"errors"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

const (
	// Reason given in case a Pod was deleted because a volume failed (machine readable).
	PodDeleteEventReason = "ForceDeleted"
	// Reason given in case a VolumeAttachment was deleted because a volume failed (machine readable).
	VADeleteEventReason = "ForceDetached"

	defaultReconcileInterval = 10 * time.Second
	podDeleteEventMessage    = "pod deleted because a used volume is marked as failing"
	vaDeleteEventMessage     = "volume detached because it is marked as failing"
)

// Options to pass when creating a new HAController
type Option func(hac *haController)

// A type that supports leader election
type LeaderElector interface {
	IsLeader() bool
}

// Set the maximum duration between two runs of the failing volume attachment cleaner
func WithReconcileInterval(d time.Duration) Option {
	return func(hac *haController) {
		hac.reconcileInterval = d
	}
}

// Set an EventRecorder that will be notified about Pod and VolumeAttachment deletions
func WithEventRecorder(ev record.EventRecorder) Option {
	return func(hac *haController) {
		hac.evRecorder = ev
	}
}

// Only consider Pods found with the given ListOptions
// Can be used to set the LabelSelector and FieldSelector for all Pods.
func WithPodSelector(opts metav1.ListOptions) Option {
	return func(hac *haController) {
		hac.podListOpts = opts
	}
}

// Only consider VolumeAttachments with the given attacher name.
func WithAttacherName(name string) Option {
	return func(hac *haController) {
		hac.attacherName = name
	}
}

// Use this leader elector to determine if deletions should be submitted to Kubernetes
// Only the current leader will submit deletions.
func WithLeaderElector(el LeaderElector) Option {
	return func(hac *haController) {
		hac.elector = el
	}
}

// Create a new HAController with the given Name, Kubernetes client and channel with "failing" PVs.
// Can be customized by adding Option(s).
func NewHAController(name string, kubeClient kubernetes.Interface, pvWithFailingVAUpdates <-chan string, opts ...Option) *haController {
	haController := &haController{
		name:                   name,
		kubeClient:             kubeClient,
		pvWithFailingVAUpdates: pvWithFailingVAUpdates,
		reconcileInterval:      defaultReconcileInterval,
		pvcToPod:               cache.NewIndexer(cache.MetaNamespaceKeyFunc, map[string]cache.IndexFunc{}),
		pvToPVC:                cache.NewIndexer(cache.MetaNamespaceKeyFunc, map[string]cache.IndexFunc{}),
		pvToVolumeAttachment:   cache.NewIndexer(cache.MetaNamespaceKeyFunc, map[string]cache.IndexFunc{}),
		pvWithFailingVA:        make(map[string]struct{}),
	}

	for _, opt := range opts {
		opt(haController)
	}

	// Applied after controller options, as the indexers might depend on values on the haController instance
	_ = haController.pvcToPod.AddIndexers(map[string]cache.IndexFunc{
		"pvc": func(obj interface{}) ([]string, error) {
			log.WithField("obj", obj).Trace("checking change in Pod")
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return nil, errors.New(fmt.Sprintf("expected Pod, got object type: %T", obj))
			}

			var pvcs []string
			for _, vol := range pod.Spec.Volumes {
				if vol.PersistentVolumeClaim == nil {
					continue
				}

				pvc := fmt.Sprintf("%s/%s", pod.Namespace, vol.PersistentVolumeClaim.ClaimName)
				pvcs = append(pvcs, pvc)
			}

			log.WithFields(log.Fields{"name": pod.Name, "namespace": pod.Namespace, "keys": pvcs}).Debug("processed Pod change")
			return pvcs, nil
		},
	})

	_ = haController.pvToPVC.AddIndexers(map[string]cache.IndexFunc{
		"pv": func(obj interface{}) ([]string, error) {
			log.WithField("obj", obj).Trace("checking change in PersistentVolumeClaim")
			pvc, ok := obj.(*corev1.PersistentVolumeClaim)
			if !ok {
				return nil, errors.New(fmt.Sprintf("expected PersistentVolumeClaim, got object type: %T", obj))
			}

			log.WithFields(log.Fields{"name": pvc.Name, "namespace": pvc.Namespace, "pv": pvc.Spec.VolumeName}).Debug("processed PersistentVolumeClaim change")
			return []string{pvc.Spec.VolumeName}, nil
		},
	})

	_ = haController.pvToVolumeAttachment.AddIndexers(map[string]cache.IndexFunc{
		"pv": func(obj interface{}) ([]string, error) {
			log.WithField("obj", obj).Trace("checking change in VolumeAttachment")
			va, ok := obj.(*storagev1.VolumeAttachment)
			if !ok {
				return nil, errors.New(fmt.Sprintf("expected VolumeAttachment, got object type: %T", obj))
			}

			if haController.attacherName != "" && va.Spec.Attacher != haController.attacherName {
				return nil, nil
			}

			if va.Spec.Source.PersistentVolumeName == nil {
				return nil, nil
			}

			log.WithFields(log.Fields{"name": va.Name, "pv": *va.Spec.Source.PersistentVolumeName}).Debug("processed VolumeAttachment change")
			return []string{*va.Spec.Source.PersistentVolumeName}, nil
		},
	})

	return haController
}

// Start monitoring volumes and kill pods that use failing volumes.
//
// This will listen for any updates on: Pods, PVCs, VolumeAttachments (VA) to keep
// it's own "state of the world" up-to-date, without requiring a full re-query of all resources.
func (hac *haController) Run(ctx context.Context) error {
	hac.startPodWatch(ctx)
	hac.startPVCWatch(ctx)
	hac.startVAWatch(ctx)

	ticker := time.NewTicker(hac.reconcileInterval)
	defer ticker.Stop()

	// Here we update our state-of-the-world and check if we we need to remove any failing volume attachments and pods
	for {
		var err error
		select {
		case <-ctx.Done():
			log.Debug("context done")
			return ctx.Err()
		case lost, ok := <-hac.pvWithFailingVAUpdates:
			if !ok {
				return &unexpectedChannelClose{"lost pv updates"}
			}
			log.WithField("lostPV", lost).Trace("lost pv")
			hac.pvWithFailingVA[lost] = struct{}{}

			hac.reconcile(ctx)
		case <-ticker.C:
			hac.reconcile(ctx)
		}

		if err != nil {
			return fmt.Errorf("error processing event: %w", err)
		}
	}
}

type haController struct {
	// Required settings
	name                   string
	kubeClient             kubernetes.Interface
	pvWithFailingVAUpdates <-chan string

	// optional settings
	reconcileInterval time.Duration
	attacherName      string
	podListOpts       metav1.ListOptions
	evRecorder        record.EventRecorder
	elector           LeaderElector

	// runtime values == basically our "state-of-the-world"
	pvcToPod             cache.Indexer
	pvToPVC              cache.Indexer
	pvToVolumeAttachment cache.Indexer
	pvWithFailingVA      map[string]struct{}
}

func (hac *haController) startPodWatch(ctx context.Context) {
	podListWatch := cache.NewFilteredListWatchFromClient(hac.kubeClient.CoreV1().RESTClient(), "pods", metav1.NamespaceAll, func(options *metav1.ListOptions) {
		options.LabelSelector = hac.podListOpts.LabelSelector
		options.FieldSelector = hac.podListOpts.FieldSelector
	})

	podReflector := cache.NewNamedReflector("pod-watcher", podListWatch, &corev1.Pod{}, hac.pvcToPod, 0)

	go podReflector.Run(ctx.Done())
}

func (hac *haController) startPVCWatch(ctx context.Context) {
	pvcListWatch := cache.NewListWatchFromClient(hac.kubeClient.CoreV1().RESTClient(), "persistentvolumeclaims", metav1.NamespaceAll, fields.Everything())

	pvcReflector := cache.NewNamedReflector("pvc-watcher", pvcListWatch, &corev1.PersistentVolumeClaim{}, hac.pvToPVC, 0)

	go pvcReflector.Run(ctx.Done())
}

func (hac *haController) startVAWatch(ctx context.Context) {
	vaListWatch := cache.NewListWatchFromClient(hac.kubeClient.StorageV1().RESTClient(), "volumeattachments", metav1.NamespaceNone, fields.Everything())

	vaReflector := cache.NewNamedReflector("va-watcher", vaListWatch, &storagev1.VolumeAttachment{}, hac.pvToVolumeAttachment, 0)

	go vaReflector.Run(ctx.Done())
}

func (hac *haController) reconcile(ctx context.Context) {
	log.Trace("start reconciling failing volume attachments")
	errs := hac.removeFailingVolumeAttachments(ctx)
	if len(errs) != 0 {
		// These are "expected errors", in the sense that we can retry the operation again at a later time.
		// No reason to stop the loop here...
		log.WithField("errs", errs).Info("failed to clean up lost resources")
	}
	log.Trace("finished reconciling failing volume attachments")
}

// Try to remove all pods and volume attachments that are known to be failing.
//
// This takes the current "state-of-the-world" and the set of "failing PVs" and tries to delete the associated
// resources (if any).
func (hac *haController) removeFailingVolumeAttachments(ctx context.Context) []error {
	var reconcileErrors []error
	var reconciledVAs []string
	defer func() {
		// Remove any successfully detached VAs from our to-do list
		for _, resource := range reconciledVAs {
			delete(hac.pvWithFailingVA, resource)
		}
	}()

	for pvName := range hac.pvWithFailingVA {
		pvlog := log.WithField("pv", pvName)

		pvlog.Info("processing failing pv")
		// Assumption: resource name == PV name
		vaObj, err := hac.pvToVolumeAttachment.ByIndex("pv", pvName)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}

		if len(vaObj) == 0 {
			pvlog.Debug("PV without volume attachment, nothing to do")
			reconciledVAs = append(reconciledVAs, pvName)
			continue
		}

		attachment, ok := vaObj[0].(*storagev1.VolumeAttachment)
		if !ok {
			reconcileErrors = append(reconcileErrors, errors.New(fmt.Sprintf("expected VolumeAttachment, got object type: %T", vaObj)))
			continue
		}

		pvcObj, err := hac.pvToPVC.ByIndex("pv", pvName)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}

		if len(pvcObj) == 0 {
			pvlog.Debug("PV not associated to a PVC, nothing to do")
			reconciledVAs = append(reconciledVAs, pvName)
			continue
		}

		pvc, ok := pvcObj[0].(*corev1.PersistentVolumeClaim)
		if !ok {
			reconcileErrors = append(reconcileErrors, errors.New(fmt.Sprintf("expected PersistentVolumeClaim, got object type: %T", pvcObj)))
			continue
		}

		if hac.elector != nil && !hac.elector.IsLeader() {
			pvlog.Info("skipping actual deletion, not the leader")
			continue
		}

		podObj, err := hac.pvcToPod.ByIndex("pvc", fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name))
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}

		if len(podObj) == 1 {
			pod, ok := podObj[0].(*corev1.Pod)
			if !ok {
				reconcileErrors = append(reconcileErrors, errors.New(fmt.Sprintf("expected Pod, got object type: %T", podObj)))
				continue
			}

			pvlog.WithField("pod", pod.ObjectMeta).Info("found pod associated with dead storage, will force delete")
			err := hac.killPod(ctx, pod)
			if err != nil {
				reconcileErrors = append(reconcileErrors, err)
				continue
			}
		}

		err = hac.forceDetach(ctx, attachment)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}

		reconciledVAs = append(reconciledVAs, pvName)
	}

	return reconcileErrors
}

// Force a pod to be removed immediately.
//
// Force removal of a pod will remove the API object without waiting for actual termination.
// This is secure in our case, as the pod's storage is already isolated.
func (hac *haController) killPod(ctx context.Context, pod *corev1.Pod) error {
	noGracePeriod := int64(0)
	err := hac.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &noGracePeriod})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	hac.eventf(pod, corev1.EventTypeWarning, PodDeleteEventReason, podDeleteEventMessage)

	return nil
}

// Delete a VolumeAttachment.
func (hac *haController) forceDetach(ctx context.Context, va *storagev1.VolumeAttachment) error {
	err := hac.kubeClient.StorageV1().VolumeAttachments().Delete(ctx, va.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	hac.eventf(va, corev1.EventTypeWarning, VADeleteEventReason, vaDeleteEventMessage)

	return nil
}

// submit a new event originating from this controller to the k8s API, if configured.
func (hac *haController) eventf(obj runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	if hac.evRecorder == nil {
		return
	}

	hac.evRecorder.Eventf(obj, eventtype, reason, messageFmt, args...)
}

// a channel was closed unexpectedly
type unexpectedChannelClose struct {
	channelName string
}

func (u *unexpectedChannelClose) Error() string {
	return fmt.Sprintf("%s closed unexpectedly", u.channelName)
}
