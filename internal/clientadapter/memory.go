package clientadapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// Resident memory for the containerized clients. One sampler produces both
// figures the results carry.
//
// The sampled figure is the high point of the container's *anonymous* memory,
// read on a fixed interval. Anonymous memory is the RSS analogue for a
// cgroup: memory.current and memory.peak both count the page cache, and a
// download benchmark pushes tens of gigabytes through that cache, so a peak
// taken from either would describe the kernel's willingness to cache this
// run's payload rather than anything the client allocated. Sampling on an
// interval is also what the native macOS and Windows lanes can do, so taking
// the same reading here is what makes the number comparable across hosts.
//
// The kernel's own cumulative high-water mark is reported alongside it as a
// hint. It is exact and catches every spike, but it counts that page cache,
// so it is an upper bound on a different quantity rather than a better
// version of the same one.
//
// Every process in the container is inside the cgroup, so an unpacker the
// client shells out to is counted without anything extra: a cgroup is a tree
// figure by construction.
const (
	memorySampleInterval = 100 * time.Millisecond
	memoryScope          = "client_container"
	memoryHintScope      = "client_container"
)

type cgroupMemorySource struct {
	statPath  string
	peakPath  string
	anonField string
	collector string
	hint      string
	version   string
}

type memorySampler struct {
	source  cgroupMemorySource
	reason  string
	started bool

	mu      sync.Mutex
	peak    uint64
	samples uint64
	failure string

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func unavailableMemorySampler(reason string) *memorySampler {
	return &memorySampler{reason: reason}
}

func startMemorySampler(ctx context.Context, docker dockerClient, containerName string) *memorySampler {
	source, err := resolveCgroupMemory(ctx, docker, containerName)
	if err != nil {
		return &memorySampler{reason: err.Error()}
	}
	sampler := &memorySampler{
		source:  source,
		started: true,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	// Take one reading before returning so a run that ends faster than the
	// interval still reports a sample rather than an empty peak.
	sampler.sample()
	go sampler.loop()
	return sampler
}

func resolveCgroupMemory(ctx context.Context, docker dockerClient, containerName string) (cgroupMemorySource, error) {
	group, err := docker.containerControllerCgroup(ctx, containerName, "memory")
	if err != nil {
		return cgroupMemorySource{}, fmt.Errorf("external cgroup accounting unavailable: %w", err)
	}
	if !filepath.IsLocal(group) {
		return cgroupMemorySource{}, fmt.Errorf("invalid cgroup path")
	}
	unified := cgroupMemorySource{
		statPath:  filepath.Join("/sys/fs/cgroup", group, "memory.stat"),
		peakPath:  filepath.Join("/sys/fs/cgroup", group, "memory.peak"),
		anonField: "anon",
		collector: "cgroup-v2-memory.stat-anon-sampled",
		hint:      "cgroup-v2-memory.peak",
		version:   cgroupMemoryVersion("cgroup-v2"),
	}
	if _, err := readCgroupMemoryField(unified.statPath, unified.anonField); err == nil {
		return unified, nil
	}
	legacy := cgroupMemorySource{
		statPath:  filepath.Join("/sys/fs/cgroup/memory", group, "memory.stat"),
		peakPath:  filepath.Join("/sys/fs/cgroup/memory", group, "memory.max_usage_in_bytes"),
		anonField: "rss",
		collector: "cgroup-v1-memory.stat-rss-sampled",
		hint:      "cgroup-v1-memory.max_usage_in_bytes",
		version:   cgroupMemoryVersion("cgroup-v1"),
	}
	if _, err := readCgroupMemoryField(legacy.statPath, legacy.anonField); err == nil {
		return legacy, nil
	}
	return cgroupMemorySource{}, fmt.Errorf("container resident memory unavailable: cgroup v2 and v1 probes failed")
}

// The interval belongs in the provenance: a sampled peak is a lower bound,
// and how much of one depends entirely on how often it was read.
func cgroupMemoryVersion(generation string) string {
	return fmt.Sprintf("%s@%dms", generation, memorySampleInterval.Milliseconds())
}

func (sampler *memorySampler) loop() {
	defer close(sampler.done)
	ticker := time.NewTicker(memorySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sampler.stop:
			sampler.sample()
			return
		case <-ticker.C:
			sampler.sample()
		}
	}
}

func (sampler *memorySampler) sample() {
	value, err := readCgroupMemoryField(sampler.source.statPath, sampler.source.anonField)
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	if err != nil {
		// The cgroup disappearing is how a container exiting looks from here.
		// Record it once; whether it invalidates the peak is decided at finish.
		if sampler.failure == "" {
			sampler.failure = err.Error()
		}
		return
	}
	sampler.samples++
	if value > sampler.peak {
		sampler.peak = value
	}
}

// finish stops sampling and returns the sampled peak and the kernel's
// high-water hint, in that order.
func (sampler *memorySampler) finish() (benchmark.CounterMeasurement, benchmark.CounterMeasurement) {
	if !sampler.started {
		reason := sampler.reason
		if reason == "" {
			reason = "container memory sampler was not started"
		}
		return benchmark.UnavailableMeasurement(memoryScope, "cgroup-memory-sampled", "unknown", reason),
			benchmark.UnavailableMeasurement(memoryHintScope, "cgroup-memory-peak", "unknown", reason)
	}
	sampler.stopOnce.Do(func() { close(sampler.stop) })
	<-sampler.done
	sampler.mu.Lock()
	peak, samples, failure := sampler.peak, sampler.samples, sampler.failure
	sampler.mu.Unlock()

	sampled := benchmark.MeasuredMeasurement(memoryScope, sampler.source.collector, sampler.source.version, peak)
	switch {
	case samples == 0:
		reason := failure
		if reason == "" {
			reason = "no container memory sample was taken"
		}
		sampled = benchmark.UnavailableMeasurement(memoryScope, sampler.source.collector, sampler.source.version, reason)
	case peak == 0:
		sampled = benchmark.UnavailableMeasurement(memoryScope, sampler.source.collector, sampler.source.version,
			"container anonymous memory sampled as zero for the whole run")
	}
	return sampled, sampler.highWater()
}

func (sampler *memorySampler) highWater() benchmark.CounterMeasurement {
	value, err := readCgroupMemoryValue(sampler.source.peakPath)
	if err != nil {
		return benchmark.UnavailableMeasurement(memoryHintScope, sampler.source.hint, sampler.source.version, err.Error())
	}
	return benchmark.MeasuredMeasurement(memoryHintScope, sampler.source.hint, sampler.source.version, value)
}

func readCgroupMemoryField(path, field string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == field {
			value, parseErr := strconv.ParseUint(fields[1], 10, 64)
			if parseErr != nil {
				return 0, fmt.Errorf("parse %s %s: %w", filepath.Base(path), field, parseErr)
			}
			return value, nil
		}
	}
	return 0, fmt.Errorf("%s has no %s field", filepath.Base(path), field)
}

func readCgroupMemoryValue(path string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("container memory high-water mark unavailable: %w", err)
	}
	value, parseErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("parse %s: %w", filepath.Base(path), parseErr)
	}
	return value, nil
}
