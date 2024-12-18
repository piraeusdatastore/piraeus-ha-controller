// Package agent deals with High Availability tasks in a cluster
//
// Tasks include:
// * Marking nodes that have lost quorum as tainted to repel new pods
// * Force deletion of Pods and VolumeAttachments on a node with lost quorum, triggering failover
// * Reconfigure for IO errors instead of IO suspension when pods should be stopped
// * Stop Pods if running on force-io-error resources.
package agent

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	csilinstor "github.com/piraeusdatastore/linstor-csi/pkg/linstor"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/cleaners"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/indexers"
	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

type Options struct {
	// NodeName is the name of the local node, as used by DRBD and Kubernetes.
	NodeName string
	// RestConfig is the config used to connect to Kubernetes.
	RestConfig *rest.Config
	// DeletionGraceSec is the number of seconds to wait for graceful pod termination in eviction/deletion requests.
	DeletionGraceSec int64
	// ReconcileInterval is the maximum interval between reconcilation runs.
	ReconcileInterval time.Duration
	// ResyncInterval is the maximum interval between resyncing internal caches with Kubernetes.
	ResyncInterval time.Duration
	// DrbdStatusInterval is the maxmimum interval between drbd state updates.
	DrbdStatusInterval time.Duration
	// OperationTimeout is the timeout used for reconcile operations.
	OperationTimeout time.Duration
	// FailOverTimeout is minimum wait between noticing quorum loss and starting the fail-over process.
	FailOverTimeout time.Duration
	// FailOverUnsafePods indicates if Pods with unknown other volume types should be failed over as well.
	FailOverUnsafePods bool
	// SatellitePodSelector selects the Pods that should be considered LINSTOR Satellites.
	// If the DRBD connection name matches on of these Pods, the Kubernetes Node name is taken from these Pods.
	SatellitePodSelector labels.Selector
	// DisableNodeTaints prevents the nodes in a cluster from being tainted.
	DisableNodeTaints bool
}

// Timeout returns the operations timeout.
func (o *Options) Timeout() time.Duration {
	if o.OperationTimeout != 0 {
		return o.OperationTimeout
	}

	if o.DeletionGraceSec != 0 {
		return 2 * time.Duration(o.DeletionGraceSec) * time.Second
	}

	return 1 * time.Minute
}

type agent struct {
	*Options
	reconcilers []Reconciler

	informerFactory informers.SharedInformerFactory
	drbdResources   DrbdResources
	pvInformer      cache.SharedIndexInformer
	podInformer     cache.SharedIndexInformer
	vaInformer      cache.SharedIndexInformer
	nodeInformer    cache.SharedIndexInformer
	client          kubernetes.Interface
	broadcaster     events.EventBroadcaster

	mu                 sync.Mutex
	lastReconcileStart time.Time
}

const (
	PersistentVolumeByResourceDefinitionIndex    = "pv-by-rd"
	PersistentVolumeByPersistentVolumeClaimIndex = "pv-by-pvc"
	PodByPersistentVolumeClaimIndex              = "pod-by-pvc"
	SatellitePodIndex                            = "satellite-pod"
	VolumeAttachmentByPersistentVolumeIndex      = "va-by-pv"
)

func NewAgent(opt *Options) (*agent, error) {
	client, err := kubernetes.NewForConfig(opt.RestConfig)
	if err != nil {
		return nil, err
	}

	informerFactory := informers.NewSharedInformerFactory(client, opt.ResyncInterval)

	klog.V(2).Info("setting up PersistentVolume informer")

	pvInformer := informerFactory.Core().V1().PersistentVolumes().Informer()

	err = pvInformer.AddIndexers(map[string]cache.IndexFunc{
		PersistentVolumeByResourceDefinitionIndex: indexers.Gen(func(obj *corev1.PersistentVolume) ([]string, error) {
			if obj.Spec.CSI == nil {
				return nil, nil
			}

			if obj.Spec.CSI.Driver != csilinstor.DriverName {
				return nil, nil
			}

			return []string{obj.Spec.CSI.VolumeHandle}, nil
		}),
		PersistentVolumeByPersistentVolumeClaimIndex: indexers.Gen(func(obj *corev1.PersistentVolume) ([]string, error) {
			if !hasPersistentVolumeClaimRef(obj) {
				return nil, nil
			}

			return []string{fmt.Sprintf("%s/%s", obj.Spec.ClaimRef.Namespace, obj.Spec.ClaimRef.Name)}, nil
		}),
	})
	if err != nil {
		return nil, err
	}

	klog.V(2).Info("setting up Pod informer")

	requirements, _ := opt.SatellitePodSelector.Requirements()
	podInformer := informerFactory.Core().V1().Pods().Informer()
	err = podInformer.SetTransform(func(o any) (any, error) {
		return cleaners.CleanPod(o.(*corev1.Pod), requirements), nil
	})
	if err != nil {
		return nil, err
	}

	err = podInformer.AddIndexers(map[string]cache.IndexFunc{
		PodByPersistentVolumeClaimIndex: indexers.Gen(func(obj *corev1.Pod) ([]string, error) {
			var pvcs []string
			for i := range obj.Spec.Volumes {
				if obj.Spec.Volumes[i].PersistentVolumeClaim != nil {
					pvcs = append(pvcs, fmt.Sprintf("%s/%s", obj.Namespace, obj.Spec.Volumes[i].PersistentVolumeClaim.ClaimName))
				}
			}
			return pvcs, nil
		}),
		SatellitePodIndex: indexers.Gen(func(obj *corev1.Pod) ([]string, error) {
			if opt.SatellitePodSelector.Matches(labels.Set(obj.Labels)) {
				return []string{obj.Name}, nil
			}

			return nil, nil
		}),
	})
	if err != nil {
		return nil, err
	}

	klog.V(2).Infof("setting up VolumeAttachment informer")

	vaInformer := informerFactory.Storage().V1().VolumeAttachments().Informer()

	err = vaInformer.AddIndexers(map[string]cache.IndexFunc{
		VolumeAttachmentByPersistentVolumeIndex: indexers.Gen(func(obj *storagev1.VolumeAttachment) ([]string, error) {
			if obj.Spec.Source.PersistentVolumeName != nil {
				return []string{*obj.Spec.Source.PersistentVolumeName}, nil
			}
			return nil, nil
		}),
	})
	if err != nil {
		return nil, err
	}

	nodeInformer := informerFactory.Core().V1().Nodes().Informer()

	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: client.EventsV1()})

	return &agent{
		informerFactory: informerFactory,
		pvInformer:      pvInformer,
		podInformer:     podInformer,
		vaInformer:      vaInformer,
		nodeInformer:    nodeInformer,
		client:          client,
		broadcaster:     broadcaster,
		Options:         opt,
		reconcilers: []Reconciler{
			NewFailoverReconciler(opt, client, pvInformer.GetIndexer()),
			NewSuspendedPodReconciler(opt),
			NewForceIoErrorReconciler(opt, client),
		},
	}, nil
}

func (a *agent) Run(ctx context.Context) error {
	// To help debugging, immediately log version
	klog.Infof("version: %+v", metadata.Version)
	klog.Infof("node: %+v", a.Options.NodeName)

	klog.V(2).Info("setting up event broadcaster")

	recorder := a.broadcaster.NewRecorder(scheme.Scheme, metadata.EventsReporter)

	err := a.broadcaster.StartRecordingToSinkWithContext(ctx)
	if err != nil {
		return err
	}
	defer a.broadcaster.Shutdown()

	klog.V(2).Info("setting up periodic reconciliation ticker")

	ticker := time.NewTicker(a.ReconcileInterval)
	defer ticker.Stop()

	drbdUpdateResult := make(chan error)
	a.drbdResources = NewDrbdResources(a.DrbdStatusInterval)
	go func() {
		defer close(drbdUpdateResult)
		drbdUpdateResult <- a.drbdResources.StartUpdates(ctx)
	}()

	a.informerFactory.Start(ctx.Done())

	klog.V(1).Info("waiting for caches to sync")
	a.informerFactory.WaitForCacheSync(ctx.Done())
	klog.V(1).Info("caches synced")

	for {
		err := a.reconcile(ctx, recorder)
		if err != nil {
			klog.V(1).ErrorS(err, "error during reconciliation")
		}

		select {
		case <-ctx.Done():
			klog.V(2).Info("context done")
			return ctx.Err()
		case err := <-drbdUpdateResult:
			klog.V(2).Info("drbd syncer done")
			return err
		case <-ticker.C:
			klog.V(2).Info("starting periodic reconciliation")
		}
	}
}

// reconcile the expected cluster state with the known cluster state.
func (a *agent) reconcile(ctx context.Context, recorder events.EventRecorder) error {
	klog.V(1).Info("starting reconciliation")

	now := time.Now()

	a.mu.Lock()
	a.lastReconcileStart = now
	a.mu.Unlock()

	resourceState := a.drbdResources.Get()

	var wg sync.WaitGroup

	// Manage own node taints

	wg.Add(1)
	taintsCtx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	go func() {
		defer wg.Done()

		err := a.ManageOwnTaints(taintsCtx, resourceState, now, recorder)
		if err != nil {
			klog.V(1).Infof("managing node taints failed: %s", err)
		}

		klog.V(2).Infof("Own node taints synced")
	}()

	for i := range resourceState {
		resource := &resourceState[i]

		klog.V(2).Infof("checking on resource %s", resource.Name)

		a.replaceConnectionNames(resource)

		req, err := a.getRequestForDrbdResource(resource, now)
		if err != nil {
			klog.V(2).Infof("ignoring resource: %s", err)
			continue
		}

		wg.Add(len(a.reconcilers))

		for i := range a.reconcilers {
			r := a.reconcilers[i]
			go func() {
				defer wg.Done()

				klog.V(3).Infof("reconcile '%T' for resource '%s'", r, req.Resource.Name)

				ctx, cancel := context.WithTimeout(ctx, a.Timeout())
				defer cancel()

				err := r.RunForResource(ctx, req, recorder)
				if err != nil {
					klog.V(1).Infof("reconciliation failed: %v", err)
				}

				klog.V(3).Infof("reconcile '%T' for resource '%s' ended", r, req.Resource.Name)
			}()
		}
	}

	wg.Wait()

	return nil
}

func (a *agent) Healthz(writer http.ResponseWriter) {
	a.mu.Lock()
	sinceLastReconcile := time.Since(a.lastReconcileStart)
	a.mu.Unlock()

	if sinceLastReconcile > a.Timeout()+a.ReconcileInterval {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(fmt.Sprintf("too long since last update: %fs", sinceLastReconcile.Seconds())))
		return
	}

	if !a.pvInformer.HasSynced() || !a.podInformer.HasSynced() || !a.vaInformer.HasSynced() || !a.nodeInformer.HasSynced() {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("informers not synced"))
		return
	}

	_, exists, err := indexers.GetByKey[corev1.Node](a.nodeInformer.GetStore(), a.NodeName)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(fmt.Sprintf("failed to check own node: %s", err)))
		return
	}

	if !exists {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(fmt.Sprintf("node %s does not exist", a.NodeName)))
		return
	}

	_, _ = writer.Write([]byte("ok"))
}

// ManageOwnTaints ensures that the taints on the local node are up to date:
// * If all resources are in a good state, remove all taints.
// * Add taints for DRBD resources that are forcing IO errors.
func (a *agent) ManageOwnTaints(ctx context.Context, resourceState []DrbdResourceState, refTime time.Time, recorder events.EventRecorder) error {
	allQuorum := true
	anyForceIOError := false
	for i := range resourceState {
		if resourceState[i].ForceIoFailures {
			anyForceIOError = true
		}

		for j := range resourceState[i].Devices {
			if !resourceState[i].Devices[j].Quorum {
				allQuorum = false
			}
		}
	}

	ownNode, exists, err := indexers.GetByKey[corev1.Node](a.nodeInformer.GetStore(), a.NodeName)
	if err != nil {
		return fmt.Errorf("failed to check own node: %w", err)
	}

	if !exists {
		return fmt.Errorf("own node does not exist")
	}

	var eventUpdates []func()

	hasNoQuorumTaint := false
	hasForceIoErrorTaint := false
	updatedTaints := make([]corev1.Taint, 0, len(ownNode.Spec.Taints))
	for _, taint := range ownNode.Spec.Taints {
		switch taint.Key {
		case metadata.NodeLostQuorumTaint:
			hasNoQuorumTaint = true

			if allQuorum {
				eventUpdates = append(eventUpdates, func() {
					recorder.Eventf(ownNode, nil, corev1.EventTypeNormal, metadata.ReasonNodeStorageQuorumRegained, metadata.ActionUntaintedNode, "All DRBD resources have quorum")
				})
			} else {
				updatedTaints = append(updatedTaints, taint)
			}
		case metadata.NodeForceIoErrorTaint:
			hasForceIoErrorTaint = true

			if !anyForceIOError {
				eventUpdates = append(eventUpdates, func() {
					recorder.Eventf(ownNode, nil, corev1.EventTypeNormal, metadata.ReasonNoVolumeForcingIoErrors, metadata.ActionUntaintedNode, "No DRBD resources forces IO errors")
				})
			} else {
				updatedTaints = append(updatedTaints, taint)
			}
		default:
			updatedTaints = append(updatedTaints, taint)
		}

	}

	if !allQuorum && !hasNoQuorumTaint {
		t := metav1.NewTime(refTime)
		updatedTaints = append(updatedTaints, corev1.Taint{
			Key:       metadata.NodeLostQuorumTaint,
			Effect:    corev1.TaintEffectNoSchedule,
			TimeAdded: &t,
		})
		eventUpdates = append(eventUpdates, func() {
			recorder.Eventf(ownNode, nil, corev1.EventTypeWarning, metadata.ReasonNodeStorageQuorumLost, metadata.ActionTaintedNode, "DRBD resource without quorum")
		})
	}

	if anyForceIOError && !hasForceIoErrorTaint {
		t := metav1.NewTime(refTime)
		updatedTaints = append(updatedTaints, corev1.Taint{
			Key:       metadata.NodeForceIoErrorTaint,
			Effect:    corev1.TaintEffectNoSchedule,
			TimeAdded: &t,
		})
		eventUpdates = append(eventUpdates, func() {
			recorder.Eventf(ownNode, nil, corev1.EventTypeWarning, metadata.ReasonVolumeForcingIoErrors, metadata.ActionTaintedNode, "DRBD resource forcing IO errors")
		})
	}

	if len(eventUpdates) != 0 {
		klog.V(1).Info("updating node taints")

		if a.Options.DisableNodeTaints {
			updatedTaints = []corev1.Taint{}
		}

		// server-side apply updates would be nicer, but don't work in all situations.
		// Adding taints works as expected, but removing taints controlled by this agent would also
		// remove _all_ other existing taints. So we use a plain update instead.
		update := ownNode.DeepCopy()
		update.Spec.Taints = updatedTaints
		_, err := a.client.CoreV1().Nodes().Update(ctx, update, metav1.UpdateOptions{FieldManager: metadata.FieldManager})
		if err != nil {
			return fmt.Errorf("failed to update node taints: %w", err)
		}

		for i := range eventUpdates {
			eventUpdates[i]()
		}
	}

	return nil
}

func (a *agent) replaceConnectionNames(res *DrbdResourceState) {
	klog.V(4).InfoS("replacing connection names", "resource", res.Name)

	for i := range res.Connections {
		con := &res.Connections[i]

		klog.V(4).InfoS("replacing connection name", "resource", res.Name, "connection", con.Name)
		pods, err := indexers.List[corev1.Pod](a.podInformer.GetIndexer().ByIndex(SatellitePodIndex, con.Name))
		if err != nil {
			klog.V(2).ErrorS(err, "failed to fetch satellite Pods", "resource", res.Name, "connection", con.Name)
			continue
		}

		switch len(pods) {
		case 1:
			if pods[0].Spec.NodeName == "" {
				klog.V(2).InfoS("satellite pod has matching name but no node name (weird!)", "resource", res.Name, "connection", con.Name)
				continue
			}

			con.Name = pods[0].Spec.NodeName
		case 0:
			klog.V(4).InfoS("no matching satellite pod, assume connection name is node name", "resource", res.Name, "connection", con.Name)
		default:
			klog.V(2).InfoS("multiple satellite pods are candidates (weird!)", "resource", res.Name, "connection", con.Name)
		}

		klog.V(4).InfoS("replaced connection name", "resource", res.Name, "connection", con.Name)
	}

	klog.V(4).InfoS("replaced connection names", "resource", res.Name)
}

// getRequestForDrbdResource creates a reconcile request for a specific DRBD resource, collecting all information:
// * Matching PersistentVolume.
// * Matching VolumeAttachments.
// * Involved Nodes.
// * Involved Pods.
func (a *agent) getRequestForDrbdResource(res *DrbdResourceState, refTime time.Time) (*ReconcileRequest, error) {
	pvs, err := indexers.List[corev1.PersistentVolume](a.pvInformer.GetIndexer().ByIndex(PersistentVolumeByResourceDefinitionIndex, res.Name))
	if err != nil {
		return nil, fmt.Errorf("error fetching persistent volume from store: %w", err)
	}

	var pv *corev1.PersistentVolume
	switch len(pvs) {
	case 0:
	case 1:
		pv = pvs[0]
	default:
		return nil, fmt.Errorf("multiple PVs found for resource '%s'", res.Name)
	}

	attachments, err := indexers.List[storagev1.VolumeAttachment](a.vaInformer.GetIndexer().ByIndex(VolumeAttachmentByPersistentVolumeIndex, res.Name))
	if err != nil {
		return nil, fmt.Errorf("failed to access volume attachment information: %v", err)
	}

	nodes, err := indexers.List[corev1.Node](a.nodeInformer.GetStore().List(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch node information: %w", err)
	}

	var attachedPods []*corev1.Pod
	if hasPersistentVolumeClaimRef(pv) {
		pvcName := fmt.Sprintf("%s/%s", pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
		attachedPods, err = indexers.List[corev1.Pod](a.podInformer.GetIndexer().ByIndex(PodByPersistentVolumeClaimIndex, pvcName))
		if err != nil {
			return nil, fmt.Errorf("failed to access pod information: %v", err)
		}
	}

	return &ReconcileRequest{
		RefTime:     refTime,
		Resource:    res,
		Volume:      pv,
		Pods:        attachedPods,
		Attachments: attachments,
		Nodes:       nodes,
	}, nil
}

func hasPersistentVolumeClaimRef(pv *corev1.PersistentVolume) bool {
	if pv == nil {
		return false
	}

	if pv.Spec.ClaimRef == nil {
		return false
	}

	switch {
	case pv.Spec.ClaimRef.APIVersion == "v1" && pv.Spec.ClaimRef.Kind == "PersistentVolumeClaim":
		return true
	// When a volume is created with volume populators, they might not have APIVersion and Kind set.
	// Assume empty fields still refer to a PersistentVolumeClaim.
	case pv.Spec.ClaimRef.APIVersion == "" && pv.Spec.ClaimRef.Kind == "":
		return true
	default:
		return false
	}
}

// TaintNode adds the specific taint to the node.
//
// Returns false, nil if the taint was already present or applying node taints had been disabled.
func TaintNode(ctx context.Context, client kubernetes.Interface, node *corev1.Node, taint corev1.Taint, disableNodeTaints bool) (bool, error) {
	if disableNodeTaints || node == nil {
		return false, nil
	}

	for i := range node.Spec.Taints {
		if node.Spec.Taints[i].MatchTaint(&taint) {
			return false, nil
		}
	}

	node.Spec.Taints = append(node.Spec.Taints, taint)
	_, err := client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{FieldManager: metadata.FieldManager})
	if err != nil {
		return false, fmt.Errorf("failed to apply node taint: %w", err)
	}

	return true, nil
}
