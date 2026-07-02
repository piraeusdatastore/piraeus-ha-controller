package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestPersistentVolumeResourceDefinitions(t *testing.T) {
	linstorPV := func(handle string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:       "linstor.csi.linbit.com",
						VolumeHandle: handle,
					},
				},
			},
		}
	}

	for _, tc := range []struct {
		name     string
		pv       *corev1.PersistentVolume
		expected []string
	}{
		{
			name:     "plain resource name",
			pv:       linstorPV("pvc-1234"),
			expected: []string{"pvc-1234"},
		},
		{
			name:     "volume within a resource",
			pv:       linstorPV("pvc-1234/1"),
			expected: []string{"pvc-1234"},
		},
		{
			name:     "volume zero within a resource",
			pv:       linstorPV("pvc-1234/0"),
			expected: []string{"pvc-1234"},
		},
		{
			name:     "invalid handle is skipped",
			pv:       linstorPV(""),
			expected: nil,
		},
		{
			name: "non-linstor driver is skipped",
			pv: &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{Driver: "other.csi.example.com", VolumeHandle: "vol"},
				},
			}},
			expected: nil,
		},
		{
			name:     "non-CSI volume is skipped",
			pv:       &corev1.PersistentVolume{},
			expected: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := persistentVolumeResourceDefinitions(tc.pv)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}
