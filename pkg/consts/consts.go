package consts

import "time"

var (
	Version = "0.0.0-unknown"
	Name = "piraeus-ha-controller"
)

const (
	// These defaults line up with Kubernetes' "node.kubernetes.io/unreachable: NoExecute" label so that a removed
	// Pod will not be scheduled on an unreachable node
	DefaultNewResourceGracePeriod   = 45 * time.Second
	DefaultKnownResourceGracePeriod = 45 * time.Second

	DefaultReconcileInterval = 10 * time.Second
	DefaultPodLabelSelector  = "linstor.csi.linbit.com/on-storage-lost=remove"
	DefaultAttacherName      = "linstor.csi.linbit.com"
)
