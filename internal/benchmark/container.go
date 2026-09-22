package benchmark

import (
	"fmt"
	"strings"
)

// ContainerRuntime is what the Docker daemon says a client container actually
// ran with, read back from the daemon after the container started rather than
// copied from the configuration the harness intended. The two are not the
// same claim: a daemon default, a compose override or a cgroup driver that
// cannot honour a limit all leave the intent intact while changing what ran,
// and a report that states the intent is stating something it did not
// observe.
//
// Every field is here because it changes a measured number. CPU quota and
// memory limit decide how much machine each client was given; an unequal pair
// is not a comparison at all. The pids limit decides how many unpackers a
// client may fan out to. The working directory's mount type and path decide
// whether the client's final move is a rename or a copy across a network.
// The network mode decides which stack the bytes crossed.
type ContainerRuntime struct {
	// Inspected is false when the daemon could not be asked; Unavailable then
	// carries the reason and every figure below is meaningless. A missing
	// readback is never reported as an unlimited container.
	Inspected   bool   `json:"inspected"`
	Unavailable string `json:"unavailable,omitempty"`

	ContainerID string `json:"container_id,omitempty"`
	// Image is the reference the run was started from, ImageDigest the
	// daemon's own resolution of it.
	Image       string `json:"image,omitempty"`
	ImageDigest string `json:"image_digest,omitempty"`

	// NanoCPUs is `--cpus` in billionths of a CPU; CPUQuotaMicros and
	// CPUPeriodMicros are the cgroup pair. Zero means unlimited in each case,
	// which is what an unconstrained benchmark container reports.
	NanoCPUs        int64 `json:"nano_cpus"`
	CPUQuotaMicros  int64 `json:"cpu_quota_micros"`
	CPUPeriodMicros int64 `json:"cpu_period_micros"`
	// MemoryLimitBytes is zero when unlimited. PidsLimit is zero or -1 when
	// unlimited, as the daemon reports it.
	MemoryLimitBytes int64 `json:"memory_limit_bytes"`
	PidsLimit        int64 `json:"pids_limit"`

	NetworkMode string `json:"network_mode,omitempty"`

	// WorkingDir* describe the mount the client's downloads landed on: the
	// completion directory, which is the one whose kind decides whether the
	// final move is a rename or a copy.
	WorkingDirMountType        string `json:"working_dir_mount_type,omitempty"`
	WorkingDirMountSource      string `json:"working_dir_mount_source,omitempty"`
	WorkingDirMountDestination string `json:"working_dir_mount_destination,omitempty"`
}

// CPUAllowance is the container's CPU ceiling in whole CPUs, and whether the
// daemon reported one at all. Two clients are comparable only if this matches.
func (c ContainerRuntime) CPUAllowance() (float64, bool) {
	if !c.Inspected {
		return 0, false
	}
	if c.NanoCPUs > 0 {
		return float64(c.NanoCPUs) / 1e9, true
	}
	if c.CPUQuotaMicros > 0 && c.CPUPeriodMicros > 0 {
		return float64(c.CPUQuotaMicros) / float64(c.CPUPeriodMicros), true
	}
	// An unconstrained container is a real, equal allowance: every client
	// getting the whole host is the harness's own default.
	return 0, true
}

// CPULabel renders the allowance for a report line.
func (c ContainerRuntime) CPULabel() string {
	allowance, ok := c.CPUAllowance()
	if !ok {
		return "unknown"
	}
	if allowance == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%.3g", allowance)
}

// MemoryLabel renders the memory ceiling for a report line.
func (c ContainerRuntime) MemoryLabel() string {
	if !c.Inspected {
		return "unknown"
	}
	if c.MemoryLimitBytes <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", c.MemoryLimitBytes)
}

// PidsLabel renders the pids ceiling for a report line.
func (c ContainerRuntime) PidsLabel() string {
	if !c.Inspected {
		return "unknown"
	}
	if c.PidsLimit <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", c.PidsLimit)
}

// MountLabel renders the working directory's mount for a report line.
func (c ContainerRuntime) MountLabel() string {
	if !c.Inspected {
		return "unknown"
	}
	kind := strings.TrimSpace(c.WorkingDirMountType)
	destination := strings.TrimSpace(c.WorkingDirMountDestination)
	if kind == "" && destination == "" {
		return "none"
	}
	if kind == "" {
		kind = "unknown"
	}
	if destination == "" {
		return kind
	}
	return kind + ":" + destination
}

// NetworkLabel renders the network mode for a report line.
func (c ContainerRuntime) NetworkLabel() string {
	if !c.Inspected {
		return "unknown"
	}
	if strings.TrimSpace(c.NetworkMode) == "" {
		return "unknown"
	}
	return c.NetworkMode
}

// Validate refuses a readback that claims to have happened without saying
// anything, and one that reports a failure while also reporting figures.
func (c ContainerRuntime) Validate() error {
	if c.Inspected {
		if strings.TrimSpace(c.Unavailable) != "" {
			return fmt.Errorf("container runtime readback cannot be both inspected and unavailable")
		}
		if strings.TrimSpace(c.ContainerID) == "" {
			return fmt.Errorf("container runtime readback lacks a container id")
		}
		return nil
	}
	if strings.TrimSpace(c.Unavailable) == "" {
		return fmt.Errorf("container runtime readback was not taken and gives no reason")
	}
	return nil
}
