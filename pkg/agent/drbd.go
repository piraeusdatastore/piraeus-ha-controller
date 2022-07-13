package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// DrbdResources keeps track of DRBD resources.
type DrbdResources interface {
	// StartUpdates starts the process of updating the current state of DRBD resources.
	StartUpdates(ctx context.Context) error
	// Get returns the resource state at the time the last update was made.
	Get() []DrbdResourceState
}

type drbdResources struct {
	interval time.Duration

	lock      sync.Mutex
	resources []DrbdResourceState
}

func NewDrbdResources(resync time.Duration) DrbdResources {
	return &drbdResources{
		interval: resync,
	}
}

func (d *drbdResources) StartUpdates(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	for {
		klog.V(3).Info("updating drbd state")

		current, err := execDrbdSetup(ctx)
		if err != nil {
			return err
		}

		d.lock.Lock()
		d.resources = current
		d.lock.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
		}
	}
}

func execDrbdSetup(ctx context.Context) ([]DrbdResourceState, error) {
	klog.V(4).Infof("Checking if DRBD is loaded")

	// We check for the presence of DRBD here. We don't care about the specific DRBD version, for those cases
	// linstor-csi will already fail, and without linstor-csi this agent also doesn't do anything.
	_, err := os.Stat("/proc/drbd")
	if os.IsNotExist(err) {
		klog.V(3).Infof("DRBD not ready, no resources to monitor")
		return nil, nil
	}

	klog.V(4).Info("Command: drbdsetup status --json")

	out, err := exec.CommandContext(ctx, "drbdsetup", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to execute drbdsetup status --json: %w", err)
	}

	klog.V(5).Infof("Command result: %s", string(out))

	var result []DrbdResourceState
	err = json.Unmarshal(out, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to parse drbdsetup json: %w", err)
	}

	return result, nil
}

func (d *drbdResources) Get() []DrbdResourceState {
	d.lock.Lock()
	defer d.lock.Unlock()

	result := make([]DrbdResourceState, len(d.resources))
	copy(result, d.resources)

	return result
}

type DrbdConnection struct {
	PeerNodeId      int    `json:"peer-node-id"`
	Name            string `json:"name"`
	ConnectionState string `json:"connection-state"`
	Congested       bool   `json:"congested"`
	PeerRole        string `json:"peer-role"`
	ApInFlight      int    `json:"ap-in-flight"`
	RsInFlight      int    `json:"rs-in-flight"`
	PeerDevices     []struct {
		Volume                 int     `json:"volume"`
		ReplicationState       string  `json:"replication-state"`
		PeerDiskState          string  `json:"peer-disk-state"`
		PeerClient             bool    `json:"peer-client"`
		ResyncSuspended        string  `json:"resync-suspended"`
		Received               int     `json:"received"`
		Sent                   int     `json:"sent"`
		OutOfSync              int     `json:"out-of-sync"`
		Pending                int     `json:"pending"`
		Unacked                int     `json:"unacked"`
		HasSyncDetails         bool    `json:"has-sync-details"`
		HasOnlineVerifyDetails bool    `json:"has-online-verify-details"`
		PercentInSync          float64 `json:"percent-in-sync"`
	} `json:"peer_devices"`
}

// DrbdResourceState is the parsed output of "drbdsetup status --json".
type DrbdResourceState struct {
	Name             string `json:"name"`
	NodeId           int    `json:"node-id"`
	Role             string `json:"role"`
	Suspended        bool   `json:"suspended"`
	SuspendedUser    bool   `json:"suspended-user"`
	SuspendedNoData  bool   `json:"suspended-no-data"`
	SuspendedFencing bool   `json:"suspended-fencing"`
	SuspendedQuorum  bool   `json:"suspended-quorum"`
	ForceIoFailures  bool   `json:"force-io-failures"`
	WriteOrdering    string `json:"write-ordering"`
	Devices          []struct {
		Volume       int    `json:"volume"`
		Minor        int    `json:"minor"`
		DiskState    string `json:"disk-state"`
		Client       bool   `json:"client"`
		Quorum       bool   `json:"quorum"`
		Size         int    `json:"size"`
		Read         int    `json:"read"`
		Written      int    `json:"written"`
		AlWrites     int    `json:"al-writes"`
		BmWrites     int    `json:"bm-writes"`
		UpperPending int    `json:"upper-pending"`
		LowerPending int    `json:"lower-pending"`
	} `json:"devices"`
	Connections []DrbdConnection `json:"connections"`
}

// MayPromote returns the best local approximation of the may promote flag from "drbdsetup events2".
func (d *DrbdResourceState) MayPromote() bool {
	for i := range d.Devices {
		if !d.Devices[i].Quorum {
			return false
		}
	}

	for i := range d.Connections {
		if d.Connections[i].PeerRole == "Primary" {
			return false
		}
	}

	return true
}

// Primary returns true if the local resource is primary.
func (d *DrbdResourceState) Primary() bool {
	return d.Role == "Primary"
}

// HasQuorum returns true if all local devices have quorum.
func (d *DrbdResourceState) HasQuorum() bool {
	for i := range d.Devices {
		if !d.Devices[i].Quorum {
			return false
		}
	}

	return true
}
