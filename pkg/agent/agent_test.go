package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata"
)

func drbdResourceWithQuorum(name string, quorum bool) *DrbdResource {
	res := &DrbdResource{
		Name:   name,
		Config: DrbdConfiguration{Resource: name},
		State: DrbdResourceState{
			Name:    name,
			Devices: []DrbdDevice{{Quorum: quorum}},
		},
	}
	res.Config.Options.Quorum = QuorumMajority
	return res
}

func TestManageOwnTaints(t *testing.T) {
	const nodeName = "node-a"
	const failOverTimeout = 5 * time.Second

	ctx := context.Background()
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}})
	nodeInformer := informers.NewSharedInformerFactory(client, 0).Core().V1().Nodes().Informer()

	a := &agent{
		Options: &Options{
			NodeName:        nodeName,
			FailOverTimeout: failOverTimeout,
		},
		client:        client,
		nodeInformer:  nodeInformer,
		noQuorumSince: make(map[string]time.Time),
	}

	// The informer is never started, so mirror the fake API state into its store by hand.
	syncedNode := func(t *testing.T) *corev1.Node {
		t.Helper()
		node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NoError(t, nodeInformer.GetStore().Add(node))
		return node
	}

	recorder := events.NewFakeRecorder(20)
	start := time.Now()

	// A resource that just appeared without quorum does not taint the node.
	syncedNode(t)
	err := a.ManageOwnTaints(ctx, map[string]*DrbdResource{"res": drbdResourceWithQuorum("res", false)}, start, recorder)
	assert.NoError(t, err)
	assert.Empty(t, syncedNode(t).Spec.Taints)

	// The same resource still without quorum after the fail-over timeout taints the node.
	err = a.ManageOwnTaints(ctx, map[string]*DrbdResource{"res": drbdResourceWithQuorum("res", false)}, start.Add(failOverTimeout), recorder)
	assert.NoError(t, err)
	taints := syncedNode(t).Spec.Taints
	if assert.Len(t, taints, 1) {
		assert.Equal(t, metadata.NodeLostQuorumTaint, taints[0].Key)
	}

	// Once the resource regains quorum, the taint is removed.
	err = a.ManageOwnTaints(ctx, map[string]*DrbdResource{"res": drbdResourceWithQuorum("res", true)}, start.Add(failOverTimeout+time.Second), recorder)
	assert.NoError(t, err)
	assert.Empty(t, syncedNode(t).Spec.Taints)

	// A new no-quorum episode starts the fail-over timeout from scratch.
	err = a.ManageOwnTaints(ctx, map[string]*DrbdResource{"res": drbdResourceWithQuorum("res", false)}, start.Add(failOverTimeout+2*time.Second), recorder)
	assert.NoError(t, err)
	assert.Empty(t, syncedNode(t).Spec.Taints)

	// A resource without the majority quorum option never taints the node.
	res := drbdResourceWithQuorum("plain", false)
	res.Config.Options.Quorum = ""
	err = a.ManageOwnTaints(ctx, map[string]*DrbdResource{"plain": res}, start.Add(time.Hour), recorder)
	assert.NoError(t, err)
	assert.Empty(t, syncedNode(t).Spec.Taints)
	assert.Empty(t, a.noQuorumSince, "vanished and quorate resources are dropped from tracking")
}
