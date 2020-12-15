package main

import "time"

const (
	Version = "0.0.0-unknown"
	Name    = "piraeus-ha-controller"

	// These defaults line up with Kubernetes' "node.kubernetes.io/unreachable: NoExecute" label so that a removed
	// Pod will not be scheduled on an unreachable node
	defaultNewResourceGracePeriod   = 45 * time.Second
	defaultKnownResourceGracePeriod = 45 * time.Second

	defaultReconcileInterval = 10 * time.Second
	defaultPodLabelSelector  = "linstor.csi.linbit.com/on-storage-lost=remove"
	defaultAttacherName      = "linstor.csi.linbit.com"
)
