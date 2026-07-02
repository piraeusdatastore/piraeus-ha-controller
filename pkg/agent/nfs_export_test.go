package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/agent"
)

func csiVolume(attrs map[string]string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:           "linstor.csi.linbit.com",
					VolumeHandle:     "pvc-123",
					VolumeAttributes: attrs,
				},
			},
		},
	}
}

func TestIsNfsExportVolume(t *testing.T) {
	assert.True(t, agent.IsNfsExportVolume(csiVolume(map[string]string{
		agent.NfsExportVolumeAttribute: "nfs://10.0.0.1/export",
	})), "volume with nfs-export attribute is an NFS export")

	assert.False(t, agent.IsNfsExportVolume(csiVolume(map[string]string{
		"linstor.csi.linbit.com/uses-volume-context": "true",
	})), "regular linstor volume is not an NFS export")

	assert.False(t, agent.IsNfsExportVolume(csiVolume(nil)), "volume without attributes is not an NFS export")

	assert.False(t, agent.IsNfsExportVolume(&corev1.PersistentVolume{}), "non-CSI volume is not an NFS export")

	assert.False(t, agent.IsNfsExportVolume(nil), "nil volume is not an NFS export")
}
