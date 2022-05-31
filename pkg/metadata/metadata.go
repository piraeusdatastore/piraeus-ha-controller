package metadata

var (
	NodeLostQuorumTaint             = "drbd.linbit.com/lost-quorum"
	NodeForceIoErrorTaint           = "drbd.linbit.com/force-io-error"
	AnnotationIgnoreFailOver        = "drbd.linbit.com/ignore-fail-over"
	FieldManager                    = "linstor.linbit.com/high-availability-controller/v2"
	EventsReporter                  = "linstor.linbit.com/HighAvailabilityController"
	ReasonVolumeWithoutQuorum       = "VolumeWithoutQuorum"
	ReasonVolumeForcingIoErrors     = "VolumeForcingIoErrors"
	ReasonNoVolumeForcingIoErrors   = "NoVolumeForcingIoErrors"
	ReasonNodeStorageQuorumLost     = "NodeStorageQuorumLost"
	ReasonNodeStorageQuorumRegained = "NodeStorageQuorumRegained"
	ActionEvictedPod                = "EvictedPod"
	ActionVolumeAttachmentDeleted   = "VolumeAttachmentDeleted"
	ActionForcedVolumeDemotion      = "ForcedVolumeDemotion"
	ActionTaintedNode               = "TaintedNode"
	ActionUntaintedNode             = "UntaintedNode"
	Version                         = "UNKNOWN"
)
