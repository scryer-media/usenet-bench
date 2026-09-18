package clientadapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// The cgroup files are the only thing standing between a container and a
// number that gets published, so the parse is pinned to real file shapes:
// cgroup v2 names the anonymous total `anon`, cgroup v1 names it `rss`, and
// both files carry many other fields that must not be mistaken for it.
func TestCgroupMemoryFieldsAreReadByName(t *testing.T) {
	dir := t.TempDir()
	v2 := filepath.Join(dir, "memory.stat.v2")
	if err := os.WriteFile(v2, []byte("anon 1048576\nfile 536870912\nkernel_stack 65536\nslab 131072\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value, err := readCgroupMemoryField(v2, "anon")
	if err != nil {
		t.Fatal(err)
	}
	if value != 1048576 {
		t.Fatalf("cgroup v2 anon read as %d, want 1048576", value)
	}
	// The page cache sits in the same file and dwarfs the client's own memory
	// on this workload. Reading the wrong field is the failure this guards.
	if cache, err := readCgroupMemoryField(v2, "file"); err != nil || cache <= value {
		t.Fatalf("the fixture must keep a page cache larger than anon: %d, %v", cache, err)
	}

	v1 := filepath.Join(dir, "memory.stat.v1")
	if err := os.WriteFile(v1, []byte("cache 536870912\nrss 2097152\nrss_huge 0\nmapped_file 4096\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value, err = readCgroupMemoryField(v1, "rss")
	if err != nil {
		t.Fatal(err)
	}
	if value != 2097152 {
		t.Fatalf("cgroup v1 rss read as %d, want 2097152", value)
	}
}

func TestCgroupMemoryFieldAbsenceIsAnErrorNotZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.stat")
	if err := os.WriteFile(path, []byte("file 536870912\nslab 131072\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCgroupMemoryField(path, "anon"); err == nil {
		t.Fatal("a missing anon field must be an error, never a zero peak")
	}
	if _, err := readCgroupMemoryField(filepath.Join(dir, "absent"), "anon"); err == nil {
		t.Fatal("an unreadable cgroup file must be an error")
	}
}

// A sampler that never resolved a cgroup reports both counters unavailable
// with the reason, and must not report a peak of zero.
func TestUnstartedMemorySamplerReportsUnavailable(t *testing.T) {
	sampled, hint := unavailableMemorySampler("docker is not reachable").finish()
	for name, measurement := range map[string]benchmark.CounterMeasurement{
		"peak_rss_bytes":           sampled,
		"peak_rss_high_water_hint": hint,
	} {
		if measurement.Status != benchmark.CounterUnavailable || measurement.Value != nil {
			t.Fatalf("%s reported a value without collecting one: %#v", name, measurement)
		}
		if measurement.Reason == "" {
			t.Fatalf("%s was unavailable without saying why", name)
		}
	}
	// Whatever the sampler produces has to satisfy the results schema, or the
	// adapter fails the run at the very end instead of reporting an
	// unavailable counter.
	metrics := benchmark.ResourceMetrics{
		CPUTimeNanoseconds:   sampled,
		InstructionsRetired:  hint,
		PeakRSSBytes:         sampled,
		PeakRSSHighWaterHint: hint,
	}
	if err := metrics.Validate(); err != nil {
		t.Fatalf("an unavailable sampler produced metrics the schema rejects: %v", err)
	}
}
