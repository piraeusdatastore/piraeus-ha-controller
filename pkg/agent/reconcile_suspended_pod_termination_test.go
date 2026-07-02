package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/agent"
)

// runningPod returns a pod that is running (and not terminating) on the given node.
func runningPod(node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestSuspendedPodReconciler(t *testing.T) {
	const node = "node-a"

	for _, tc := range []struct {
		name  string
		state agent.DrbdResourceState
		pods  []*corev1.Pod
	}{
		{
			name:  "user suspend is ignored",
			state: agent.DrbdResourceState{Name: "res", Role: "Primary", Suspended: true, SuspendedUser: true},
			// A terminating consumer pod would normally trigger the forced demotion, but a user-initiated
			// suspend (e.g. a snapshot) must be left alone.
			pods: nil,
		},
		{
			name:  "not suspended",
			state: agent.DrbdResourceState{Name: "res", Role: "Primary", Suspended: false},
		},
		{
			name:  "suspended secondary",
			state: agent.DrbdResourceState{Name: "res", Role: "Secondary", Suspended: true},
		},
		{
			name:  "suspended primary with running consumer",
			state: agent.DrbdResourceState{Name: "res", Role: "Primary", Suspended: true},
			pods:  []*corev1.Pod{runningPod(node)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reconciler := agent.NewSuspendedPodReconciler(&agent.Options{NodeName: node})
			recorder := events.NewFakeRecorder(10)

			req := &agent.ReconcileRequest{
				Resource: &agent.DrbdResource{Name: tc.state.Name, State: tc.state},
				Pods:     tc.pods,
			}

			err := reconciler.RunForResource(context.Background(), req, recorder)
			assert.NoError(t, err)
			assert.Empty(t, recorder.Events, "no forced demotion event expected")
		})
	}
}
