package clientadapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// Bytes written to device, for the containerized clients. It is read the same
// way CPU time is -- a bracketing pair of host-side cgroup reads, never a
// probe executed inside the measured container -- because it answers the same
// kind of question: what did this client spend, as opposed to how long did it
// take.
//
// The counter is the cgroup's own block-io accounting summed over every
// device, so an unpacker the client shells out to is charged to the client by
// construction, exactly as its CPU and its resident memory are. What it
// exposes is the client that writes the payload more than once: a completion
// move that fell back to a copy, an unpack that staged a second full copy, a
// repair pass rewriting what it had just written. None of that shows in wall
// clock on a fast disk, and all of it is a real cost on the operator's.
//
// It counts bytes submitted to the device, so it excludes writes still sitting
// in the page cache when the run ended and includes writeback the kernel did
// on this cgroup's behalf during it. That makes it a measurement of the run's
// device traffic rather than of the client's write() calls, which is the
// quantity worth comparing.
const (
	deviceWriteScope = "client_container"
)

type deviceWriteSnapshot struct {
	bytes     uint64
	collector string
	version   string
}

type deviceWriteSampler struct {
	docker dockerClient
	name   string
	start  *deviceWriteSnapshot
	reason string
}

func unavailableDeviceWriteSampler(reason string) deviceWriteSampler {
	return deviceWriteSampler{reason: reason}
}

func startDeviceWriteSampler(ctx context.Context, docker dockerClient, containerName string) deviceWriteSampler {
	sampler := deviceWriteSampler{docker: docker, name: containerName}
	snapshot, err := sampler.read(ctx)
	if err != nil {
		sampler.reason = err.Error()
		return sampler
	}
	sampler.start = &snapshot
	return sampler
}

func (sampler deviceWriteSampler) finish(ctx context.Context) benchmark.CounterMeasurement {
	if sampler.start == nil {
		reason := sampler.reason
		if reason == "" {
			reason = "container device-write collector was not started"
		}
		return benchmark.UnavailableMeasurement(deviceWriteScope, "cgroup-io", "unknown", reason)
	}
	return sampler.measureFrom(ctx, *sampler.start)
}

func (sampler deviceWriteSampler) measureFrom(ctx context.Context, start deviceWriteSnapshot) benchmark.CounterMeasurement {
	end, err := sampler.read(ctx)
	if err != nil {
		return benchmark.UnavailableMeasurement(deviceWriteScope, start.collector, start.version, err.Error())
	}
	if end.collector != start.collector || end.version != start.version {
		return benchmark.UnavailableMeasurement(deviceWriteScope, start.collector, start.version, "block-io accounting source changed during benchmark run")
	}
	if end.bytes < start.bytes {
		return benchmark.UnavailableMeasurement(deviceWriteScope, start.collector, start.version, "block-io write counter moved backwards during benchmark run")
	}
	return benchmark.MeasuredMeasurement(deviceWriteScope, end.collector, end.version, end.bytes-start.bytes)
}

func (sampler deviceWriteSampler) read(ctx context.Context) (deviceWriteSnapshot, error) {
	group, err := sampler.docker.containerControllerCgroup(ctx, sampler.name, "blkio")
	if err != nil {
		return deviceWriteSnapshot{}, fmt.Errorf("external cgroup accounting unavailable: %w", err)
	}
	if !filepath.IsLocal(group) {
		return deviceWriteSnapshot{}, fmt.Errorf("invalid cgroup path")
	}
	raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "io.stat"))
	if err == nil {
		written, parseErr := parseCgroupV2WriteBytes(string(raw))
		if parseErr == nil {
			return deviceWriteSnapshot{bytes: written, collector: "cgroup-v2-io.stat-wbytes", version: "cgroup-v2"}, nil
		}
	}
	// cgroup v1 keeps the same quantity under blkio, per device and
	// direction. The throttle file is the one every v1 kernel carries.
	for _, legacy := range []string{"blkio.throttle.io_service_bytes", "blkio.io_service_bytes"} {
		raw, legacyErr := os.ReadFile(filepath.Join("/sys/fs/cgroup/blkio", group, legacy))
		if legacyErr != nil {
			continue
		}
		written, parseErr := parseCgroupV1WriteBytes(string(raw))
		if parseErr != nil {
			continue
		}
		return deviceWriteSnapshot{bytes: written, collector: "cgroup-v1-" + legacy + "-write", version: "cgroup-v1"}, nil
	}
	return deviceWriteSnapshot{}, fmt.Errorf("container device-write bytes unavailable: cgroup v2 and v1 probes failed")
}

// parseCgroupV2WriteBytes sums wbytes over every device line of io.stat. An
// io.stat with no device line at all is a container that has not touched a
// block device yet, which is a real zero rather than a parse failure.
func parseCgroupV2WriteBytes(contents string) (uint64, error) {
	var total uint64
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// The first field is the device major:minor; the rest are key=value.
		if !strings.Contains(fields[0], ":") {
			return 0, fmt.Errorf("io.stat line does not start with a device")
		}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key != "wbytes" {
				continue
			}
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse io.stat wbytes: %w", err)
			}
			total += parsed
		}
	}
	return total, nil
}

// parseCgroupV1WriteBytes sums the Write rows of a blkio io_service_bytes
// file, skipping its per-device Total rows so nothing is counted twice.
func parseCgroupV1WriteBytes(contents string) (uint64, error) {
	var total uint64
	found := false
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[1] != "Write" {
			continue
		}
		parsed, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse blkio write bytes: %w", err)
		}
		total += parsed
		found = true
	}
	if !found {
		return 0, fmt.Errorf("blkio io_service_bytes has no Write row")
	}
	return total, nil
}
