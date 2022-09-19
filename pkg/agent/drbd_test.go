package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/piraeusdatastore/piraeus-ha-controller/pkg/agent"
)

func TestUnmarshalDrbdResourceState(t *testing.T) {
	source := `[{"peer-node-id":0,"name":"4b","connection-state":"Connecting","congested":false,"peer-role":"Unknown","ap-in-flight":18446744073709551608,"rs-in-flight":0,"peer_devices":[{"volume":0,"replication-state":"Off","peer-disk-state":"DUnknown","peer-client":false,"resync-suspended":"no","received":0,"sent":0,"out-of-sync":0,"pending":0,"unacked":0,"has-sync-details":false,"has-online-verify-details":false,"percent-in-sync":100.00}]}]`

	var result []agent.DrbdResourceState
	err := json.Unmarshal([]byte(source), &result)
	assert.NoError(t, err)
	assert.Equal(t, []agent.DrbdResourceState{
		{
			Name:            "4b",
			Role:            "",
			ForceIoFailures: false,
			Suspended:       false,
		},
	}, result)
}
