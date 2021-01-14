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
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/clock"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
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
		pvcToPod:               make(map[types.NamespacedName]*corev1.Pod),
		pvToPVC:                make(map[string]*corev1.PersistentVolumeClaim),
		pvToVolumeAttachment:   make(map[string]*storagev1.VolumeAttachment),
		pvWithFailingVA:        make(map[string]struct{}),
		watchBackoff:           wait.NewExponentialBackoffManager(100*time.Millisecond, 20*time.Second, 1*time.Minute, 2.0, 1.0, &clock.RealClock{}),
	}

	for _, opt := range opts {
		opt(haController)
	}

	return haController
}

// Start monitoring volumes and kill pods that use failing volumes.
//
// This will listen for any updates on: Pods, PVCs, VolumeAttachments (VA) to keep
// it's own "state of the world" up-to-date, without requiring a full re-query of all resources.
func (hac *haController) Run(ctx context.Context) error {
	podUpdates, err := hac.watchPods(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up pod watch: %w", err)
	}

	pvcUpdates, err := hac.watchPVCs(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up pvc watch: %w", err)
	}

	vaUpdates, err := hac.watchVAs(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up va watch: %w", err)
	}

	ticker := time.NewTicker(hac.reconcileInterval)
	defer ticker.Stop()

	// Here we update our state-of-the-world and check if we we need to remove any failing volume attachments and pods
	for {
		var err error
		select {
		case <-ctx.Done():
			log.Debug("context done")
			return ctx.Err()
		case podEv, ok := <-podUpdates:
			if !ok {
				return &unexpectedChannelClose{"pod updates"}
			}
			err = hac.handlePodWatchEvent(podEv)
		case pvcEv, ok := <-pvcUpdates:
			if !ok {
				return &unexpectedChannelClose{"pvc updates"}
			}
			err = hac.handlePVCWatchEvent(pvcEv)
		case vaEv, ok := <-vaUpdates:
			if !ok {
				return &unexpectedChannelClose{"va updates"}
			}
			err = hac.handleVAWatchEvent(vaEv)
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
	watchBackoff      wait.BackoffManager

	// runtime values == basically our "state-of-the-world"
	pvcToPod             map[types.NamespacedName]*corev1.Pod
	pvToPVC              map[string]*corev1.PersistentVolumeClaim
	pvToVolumeAttachment map[string]*storagev1.VolumeAttachment
	pvWithFailingVA      map[string]struct{}
}

func (hac *haController) watchPods(ctx context.Context) (<-chan watch.Event, error) {
	initialPods, err := hac.kubeClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, hac.podListOpts)
	if err != nil {
		return nil, err
	}

	for i := range initialPods.Items {
		hac.updateFromPod(&initialPods.Items[i])
	}

	watchOpts := *hac.podListOpts.DeepCopy()
	watchOpts.ResourceVersion = initialPods.ResourceVersion
	watchOpts.AllowWatchBookmarks = true

	watchChan := make(chan watch.Event)
	podWatch := func() {
		log.WithField("resource-version", watchOpts.ResourceVersion).Debug("(re-)starting Pod watch")
		w, err := hac.kubeClient.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx, watchOpts)
		if err != nil {
			log.WithError(err).Info("Pod watch failed, restarting...")
			return
		}

		defer w.Stop()

		for item := range w.ResultChan() {
			if item.Type == watch.Bookmark {
				watchOpts.ResourceVersion = item.Object.(*corev1.Pod).ResourceVersion
				log.WithField("resource-version", watchOpts.ResourceVersion).Trace("Pod watch resource version updated")
				continue
			}

			watchChan <- item
		}
	}

	go wait.BackoffUntil(podWatch, hac.watchBackoff, true, ctx.Done())

	return watchChan, nil
}

func (hac *haController) handlePodWatchEvent(ev watch.Event) error {
	switch ev.Type {
	case watch.Error:
		return fmt.Errorf("watch error: %v", ev.Object)
	case watch.Added:
		hac.updateFromPod(ev.Object.(*corev1.Pod))
	case watch.Modified:
		hac.updateFromPod(ev.Object.(*corev1.Pod))
	case watch.Deleted:
		hac.removeFromPod(ev.Object.(*corev1.Pod))
	}

	return nil
}

func (hac *haController) updateFromPod(pod *corev1.Pod) {
	if pod.Status.Phase == corev1.PodPending {
		// A pending pod is not yet bound and does not block volume detachment
		return
	}

	log.WithFields(log.Fields{"resource": "Pod", "name": pod.Name, "namespace": pod.Name}).Trace("update")

	for i := range pod.Spec.Volumes {
		pvcRef := pod.Spec.Volumes[i].PersistentVolumeClaim
		if pvcRef == nil {
			continue
		}

		pvcName := types.NamespacedName{Name: pvcRef.ClaimName, Namespace: pod.Namespace}
		hac.pvcToPod[pvcName] = pod
	}
}

func (hac *haController) removeFromPod(pod *corev1.Pod) {
	log.WithFields(log.Fields{"resource": "Pod", "name": pod.Name, "namespace": pod.Name}).Trace("remove")

	for i := range pod.Spec.Volumes {
		pvcRef := pod.Spec.Volumes[i].PersistentVolumeClaim
		if pvcRef == nil {
			continue
		}

		pvcName := types.NamespacedName{Name: pvcRef.ClaimName, Namespace: pod.Namespace}
		currentPod, ok := hac.pvcToPod[pvcName]
		if !ok {
			continue
		}

		if currentPod.Name != pod.Name {
			continue
		}

		delete(hac.pvcToPod, pvcName)
	}
}

func (hac *haController) watchPVCs(ctx context.Context) (<-chan watch.Event, error) {
	opts := metav1.ListOptions{}
	initialPVCs, err := hac.kubeClient.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(ctx, opts)
	if err != nil {
		return nil, err
	}

	for i := range initialPVCs.Items {
		hac.updateFromPVC(&initialPVCs.Items[i])
	}

	watchOpts := metav1.ListOptions{}
	watchOpts.ResourceVersion = initialPVCs.ResourceVersion
	watchOpts.AllowWatchBookmarks = true

	watchChan := make(chan watch.Event)
	pvcWatch := func() {
		log.WithField("resource-version", watchOpts.ResourceVersion).Debug("(re-)starting PVC watch")
		w, err := hac.kubeClient.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).Watch(ctx, watchOpts)
		if err != nil {
			log.WithError(err).Info("PVC watch failed, restarting...")
			return
		}

		defer w.Stop()

		for item := range w.ResultChan() {
			if item.Type == watch.Bookmark {
				watchOpts.ResourceVersion = item.Object.(*corev1.PersistentVolumeClaim).ResourceVersion
				log.WithField("resource-version", watchOpts.ResourceVersion).Trace("PVC watch resource version updated")
				continue
			}

			watchChan <- item
		}
	}

	go wait.BackoffUntil(pvcWatch, hac.watchBackoff, true, ctx.Done())

	return watchChan, nil
}

func (hac *haController) handlePVCWatchEvent(ev watch.Event) error {
	switch ev.Type {
	case watch.Error:
		return fmt.Errorf("watch error: %v", ev.Object)
	case watch.Added:
		hac.updateFromPVC(ev.Object.(*corev1.PersistentVolumeClaim))
	case watch.Modified:
		hac.updateFromPVC(ev.Object.(*corev1.PersistentVolumeClaim))
	case watch.Deleted:
		hac.removeFromPVC(ev.Object.(*corev1.PersistentVolumeClaim))
	}

	return nil
}

func (hac *haController) updateFromPVC(pvc *corev1.PersistentVolumeClaim) {
	log.WithFields(log.Fields{"resource": "PVC", "name": pvc.Name, "namespace": pvc.Name}).Trace("update")

	hac.pvToPVC[pvc.Spec.VolumeName] = pvc
}

func (hac *haController) removeFromPVC(pvc *corev1.PersistentVolumeClaim) {
	log.WithFields(log.Fields{"resource": "PVC", "name": pvc.Name, "namespace": pvc.Name}).Trace("remove")

	delete(hac.pvToPVC, pvc.Spec.VolumeName)
}

func (hac *haController) watchVAs(ctx context.Context) (<-chan watch.Event, error) {
	opts := metav1.ListOptions{}
	initialVAs, err := hac.kubeClient.StorageV1().VolumeAttachments().List(ctx, opts)
	if err != nil {
		return nil, err
	}

	for i := range initialVAs.Items {
		hac.updateFromVA(&initialVAs.Items[i])
	}

	watchOpts := metav1.ListOptions{}
	watchOpts.ResourceVersion = initialVAs.ResourceVersion
	watchOpts.AllowWatchBookmarks = true

	watchChan := make(chan watch.Event)
	vaWatch := func() {
		log.WithField("resource-version", watchOpts.ResourceVersion).Debug("(re-)starting VA watch")
		w, err := hac.kubeClient.StorageV1().VolumeAttachments().Watch(ctx, watchOpts)
		if err != nil {
			log.WithError(err).Info("VA watch failed, restarting...")
			return
		}

		defer w.Stop()

		for item := range w.ResultChan() {
			if item.Type == watch.Bookmark {
				watchOpts.ResourceVersion = item.Object.(*storagev1.VolumeAttachment).ResourceVersion
				log.WithField("resource-version", watchOpts.ResourceVersion).Trace("VA watch resource version updated")
				continue
			}

			watchChan <- item
		}
	}

	go wait.BackoffUntil(vaWatch, hac.watchBackoff, true, ctx.Done())

	return watchChan, nil
}

func (hac *haController) handleVAWatchEvent(ev watch.Event) error {
	switch ev.Type {
	case watch.Error:
		return fmt.Errorf("watch error: %v", ev.Object)
	case watch.Added:
		hac.updateFromVA(ev.Object.(*storagev1.VolumeAttachment))
	case watch.Modified:
		hac.updateFromVA(ev.Object.(*storagev1.VolumeAttachment))
	case watch.Deleted:
		hac.removeFromVA(ev.Object.(*storagev1.VolumeAttachment))
	}

	return nil
}

func (hac *haController) updateFromVA(va *storagev1.VolumeAttachment) {
	if va.Spec.Source.PersistentVolumeName == nil {
		return
	}

	// Skip if attacherName is given and does not match
	if hac.attacherName != "" && va.Spec.Attacher != hac.attacherName {
		return
	}

	log.WithFields(log.Fields{"resource": "VA", "name": va.Name}).Trace("update")

	hac.pvToVolumeAttachment[*va.Spec.Source.PersistentVolumeName] = va
}

func (hac *haController) removeFromVA(va *storagev1.VolumeAttachment) {
	if va.Spec.Source.PersistentVolumeName == nil {
		return
	}

	currentVA, ok := hac.pvToVolumeAttachment[*va.Spec.Source.PersistentVolumeName]
	if !ok {
		return
	}

	if currentVA.Name != va.Name {
		return
	}

	log.WithFields(log.Fields{"resource": "VA", "name": va.Name}).Trace("remove")

	delete(hac.pvWithFailingVA, *va.Spec.Source.PersistentVolumeName)
	delete(hac.pvToVolumeAttachment, *va.Spec.Source.PersistentVolumeName)
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
		attachment, ok := hac.pvToVolumeAttachment[pvName]
		if !ok {
			pvlog.Debug("PV without volume attachment, nothing to do")
			reconciledVAs = append(reconciledVAs, pvName)
			continue
		}

		pvc, ok := hac.pvToPVC[pvName]
		if !ok {
			pvlog.Debug("PV not associated to a PVC, nothing to do")
			reconciledVAs = append(reconciledVAs, pvName)
			continue
		}

		if hac.elector != nil && !hac.elector.IsLeader() {
			pvlog.Info("skipping actual deletion, not the leader")
			continue
		}

		pod, ok := hac.pvcToPod[types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}]
		if ok {
			pvlog.WithField("pod", pod.ObjectMeta).Info("found pod associated with dead storage, will force delete")
			err := hac.killPod(ctx, pod)
			if err != nil {
				reconcileErrors = append(reconcileErrors, err)
				continue
			}
		}
		hac.removeFromPVC(pvc)

		err := hac.forceDetach(ctx, attachment)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		hac.removeFromVA(attachment)

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
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	hac.eventf(pod, corev1.EventTypeWarning, PodDeleteEventReason, podDeleteEventMessage)

	return nil
}

// Delete a VolumeAttachment.
func (hac *haController) forceDetach(ctx context.Context, va *storagev1.VolumeAttachment) error {
	err := hac.kubeClient.StorageV1().VolumeAttachments().Delete(ctx, va.Name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
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
