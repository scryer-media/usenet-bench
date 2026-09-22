package benchmark

import (
	"encoding/json"
	"testing"
)

func TestResourceMetricsCarryIndependentCounterProvenance(t *testing.T) {
	metrics := ResourceMetrics{
		CPUTimeNanoseconds:   MeasuredMeasurement("client_container", "cgroup-v2-cpu.stat", "cgroup-v2", 42),
		InstructionsRetired:  UnavailableMeasurement("client_process", "linux-perf", "macos", "native Linux perf is unavailable"),
		PeakRSSBytes:         MeasuredMeasurement("client_process_tree", "ps-process-tree-sampled", "darwin@250ms", 4096),
		PeakRSSHighWaterHint: UnavailableMeasurement("client_process", "getrusage-ru_maxrss", "darwin", "the client did not exit"),
		DeviceWriteBytes:     UnavailableMeasurement("client_process_tree", "native-device-writes", "darwin", "not collected on the native lanes"),
	}
	if err := metrics.Validate(); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		CPU struct {
			Scope     string `json:"scope"`
			Collector string `json:"collector"`
			Status    string `json:"status"`
			Value     uint64 `json:"value"`
		} `json:"cpu_time_nanoseconds"`
		Instructions struct {
			Scope  string `json:"scope"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"instructions_retired"`
		PeakRSS struct {
			Scope     string `json:"scope"`
			Collector string `json:"collector"`
			Status    string `json:"status"`
			Value     uint64 `json:"value"`
		} `json:"peak_rss_bytes"`
		PeakRSSHint struct {
			Scope  string `json:"scope"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"peak_rss_high_water_hint"`
	}
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CPU.Scope != "client_container" || decoded.CPU.Collector != "cgroup-v2-cpu.stat" || decoded.CPU.Status != string(CounterMeasured) || decoded.CPU.Value != 42 {
		t.Fatalf("CPU provenance did not round-trip: %s", contents)
	}
	if decoded.Instructions.Scope != "client_process" || decoded.Instructions.Status != string(CounterUnavailable) || decoded.Instructions.Reason == "" {
		t.Fatalf("instruction provenance did not round-trip: %s", contents)
	}
	// The two memory counters answer different questions and must stay
	// separately attributable: a sampled tree figure that is comparable across
	// hosts, and a platform high-water hint that is not.
	if decoded.PeakRSS.Scope != "client_process_tree" || decoded.PeakRSS.Collector != "ps-process-tree-sampled" || decoded.PeakRSS.Status != string(CounterMeasured) || decoded.PeakRSS.Value != 4096 {
		t.Fatalf("peak RSS provenance did not round-trip: %s", contents)
	}
	if decoded.PeakRSSHint.Scope != "client_process" || decoded.PeakRSSHint.Status != string(CounterUnavailable) || decoded.PeakRSSHint.Reason == "" {
		t.Fatalf("peak RSS hint provenance did not round-trip: %s", contents)
	}
}
