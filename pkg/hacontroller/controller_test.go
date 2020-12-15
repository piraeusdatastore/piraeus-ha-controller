package hacontroller_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/hacontroller"
)

const (
	fakeVAName            = "fake-va"
	fakePVName            = "fake-pv"
	fakePVCName           = "fake-pvc"
	fakePodWithVolumeName = "fake-pod-with-volume"
	fakeNamespace         = "fake"
	fakeAttacher          = "csi.fake.k8s.io"
	fakeNode              = "node1.fake.k8s.io"
)

func initialKubeClient() kubernetes.Interface {
	podWithVolume := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakePodWithVolumeName,
			Namespace: fakeNamespace,
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "foo",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: fakePVCName,
						},
					},
				},
			},
		},
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakePVCName,
			Namespace: fakeNamespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: fakePVName,
		},
	}

	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: fakeVAName,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: fakeAttacher,
			NodeName: fakeNode,
			Source: storagev1.VolumeAttachmentSource{
				PersistentVolumeName: &pvc.Spec.VolumeName,
			},
		},
	}

	return fake.NewSimpleClientset(
		podWithVolume,
		pvc,
		va,
	)
}

func TestHaController_Run_Basic(t *testing.T) {
	kubeClient := initialKubeClient()

	lostResource := make(chan string, 1)
	lostResource <- fakePVName

	haController := hacontroller.NewHAController("test", kubeClient, lostResource, hacontroller.WithReconcileInterval(50*time.Millisecond))

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	err := haController.Run(reconcileCtx)
	assert.EqualError(t, err, "context deadline exceeded")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodWithVolumeName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "pod should be deleted")

	_, err = kubeClient.StorageV1().VolumeAttachments().Get(ctx, fakeVAName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "va should be deleted")
}

func TestHaController_Run_WithAttacherName(t *testing.T) {
	kubeClient := initialKubeClient()

	lostResource := make(chan string, 1)
	lostResource <- fakePVName

	haController := hacontroller.NewHAController("test", kubeClient, lostResource,
		hacontroller.WithReconcileInterval(50*time.Millisecond),
		hacontroller.WithAttacherName(fakeAttacher),
	)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	err := haController.Run(reconcileCtx)
	assert.EqualError(t, err, "context deadline exceeded")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodWithVolumeName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "pod should be deleted")

	_, err = kubeClient.StorageV1().VolumeAttachments().Get(ctx, fakeVAName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "va should be deleted")
}

func TestHaController_Run_WithWrongAttacherName(t *testing.T) {
	kubeClient := initialKubeClient()

	lostResource := make(chan string, 1)
	lostResource <- fakePVName

	haController := hacontroller.NewHAController("test", kubeClient, lostResource,
		hacontroller.WithReconcileInterval(50*time.Millisecond),
		hacontroller.WithAttacherName(fakeAttacher+"something wrong"),
	)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	err := haController.Run(reconcileCtx)
	assert.EqualError(t, err, "context deadline exceeded")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodWithVolumeName, metav1.GetOptions{})
	assert.NoError(t, err, "pod should not be deleted")

	_, err = kubeClient.StorageV1().VolumeAttachments().Get(ctx, fakeVAName, metav1.GetOptions{})
	assert.NoError(t, err, "va should not be deleted")
}

type neverLeaderElector struct{}

func (*neverLeaderElector) IsLeader() bool {
	return false
}

func TestHaController_Run_NoChangeIfNotLeader(t *testing.T) {
	kubeClient := initialKubeClient()

	lostResource := make(chan string, 1)
	lostResource <- fakePVName

	haController := hacontroller.NewHAController("test", kubeClient, lostResource,
		hacontroller.WithReconcileInterval(50*time.Millisecond),
		hacontroller.WithLeaderElector(&neverLeaderElector{}),
	)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	err := haController.Run(reconcileCtx)
	assert.EqualError(t, err, "context deadline exceeded")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodWithVolumeName, metav1.GetOptions{})
	assert.NoError(t, err, "pod should not be deleted")

	_, err = kubeClient.StorageV1().VolumeAttachments().Get(ctx, fakeVAName, metav1.GetOptions{})
	assert.NoError(t, err, "va should not be deleted")
}
