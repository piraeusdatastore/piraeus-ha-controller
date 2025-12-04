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
	Get() map[string]*DrbdResource
}

type drbdResources struct {
	interval time.Duration

	lock    sync.Mutex
	states  []DrbdResourceState
	configs []DrbdConfiguration
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

		configs, states, err := execDrbdSetup(ctx)
		if err != nil {
			return err
		}

		d.lock.Lock()
		d.configs = configs
		d.states = states
		d.lock.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
		}
	}
}

func execDrbdSetup(ctx context.Context) ([]DrbdConfiguration, []DrbdResourceState, error) {
	klog.V(4).Infof("Checking if DRBD is loaded")

	// We check for the presence of DRBD here. We don't care about the specific DRBD version, for those cases
	// linstor-csi will already fail, and without linstor-csi this agent also doesn't do anything.
	_, err := os.Stat("/proc/drbd")
	if os.IsNotExist(err) {
		klog.V(3).Infof("DRBD not ready, no resources to monitor")
		return nil, nil, nil
	}

	klog.V(4).Info("Command: drbdsetup status --json")

	out, err := exec.CommandContext(ctx, "drbdsetup", "status", "--json").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to execute drbdsetup status --json: %w", err)
	}

	klog.V(5).Infof("Command result: %s", string(out))

	var states []DrbdResourceState
	err = json.Unmarshal(out, &states)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse drbdsetup status --json: %w", err)
	}

	klog.V(4).Info("Command: drbdsetup show --json")

	out, err = exec.CommandContext(ctx, "drbdsetup", "show", "--json").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to execute drbdsetup show --json: %w", err)
	}

	klog.V(5).Infof("Command result: %s", string(out))

	var configs []DrbdConfiguration
	err = json.Unmarshal(out, &configs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse drbdsetup show --json: %w", err)
	}

	return configs, states, nil
}

func (d *drbdResources) Get() map[string]*DrbdResource {
	d.lock.Lock()
	defer d.lock.Unlock()

	result := make(map[string]*DrbdResource)
	for _, config := range d.configs {
		result[config.Resource] = &DrbdResource{
			Name:   config.Resource,
			Config: config,
		}
	}

	for _, state := range d.states {
		if _, ok := result[state.Name]; !ok {
			klog.V(4).Infof("Got DRBD state without config %s, skipping", state.Name)
			continue
		}

		result[state.Name].State = state
	}

	return result
}

type DrbdResource struct {
	Name   string
	Config DrbdConfiguration
	State  DrbdResourceState
}

type DrbdConnection struct {
	Name            string `json:"name"`
	PeerRole        string `json:"peer-role"`
	ConnectionState string `json:"connection-state"`
}

type DrbdDevice struct {
	Quorum bool `json:"quorum"`
}

// DrbdResourceState is the parsed output of "drbdsetup status --json".
type DrbdResourceState struct {
	Name            string           `json:"name"`
	Role            string           `json:"role"`
	Suspended       bool             `json:"suspended"`
	ForceIoFailures bool             `json:"force-io-failures"`
	Devices         []DrbdDevice     `json:"devices"`
	Connections     []DrbdConnection `json:"connections"`
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

// DrbdConfiguration is the parsed output of "drbdsetup show --json".
type DrbdConfiguration struct {
	Resource string `json:"resource"`
	Options  struct {
		Quorum string `json:"quorum,omitempty"`
	} `json:"options"`
}

const (
	QuorumMajority = "majority"
)
