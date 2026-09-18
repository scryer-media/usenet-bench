package benchmark

import (
	"fmt"
	"strings"
)

type CounterStatus string

const (
	CounterMeasured    CounterStatus = "measured"
	CounterUnavailable CounterStatus = "unavailable"
)

// CounterValue makes unavailable hardware counters explicit. A missing value
// is never interpreted as zero.
type CounterValue struct {
	Status CounterStatus `json:"status"`
	Value  *uint64       `json:"value,omitempty"`
	Reason string        `json:"reason,omitempty"`
}

func MeasuredCounter(value uint64) CounterValue {
	return CounterValue{Status: CounterMeasured, Value: &value}
}

func UnavailableCounter(reason string) CounterValue {
	return CounterValue{Status: CounterUnavailable, Reason: reason}
}

func (c CounterValue) validate(name string) error {
	switch c.Status {
	case CounterMeasured:
		if c.Value == nil || strings.TrimSpace(c.Reason) != "" {
			return fmt.Errorf("%s measured counter must have a value and no unavailable reason", name)
		}
	case CounterUnavailable:
		if c.Value != nil || strings.TrimSpace(c.Reason) == "" {
			return fmt.Errorf("%s unavailable counter must have a reason and no value", name)
		}
	default:
		return fmt.Errorf("%s has unsupported counter status %q", name, c.Status)
	}
	return nil
}

// CounterMeasurement describes one resource counter. CPU time and retired
// instructions may come from different collectors and scopes, so their
// provenance is recorded independently rather than implied by the run.
type CounterMeasurement struct {
	Window           string `json:"window"`
	Scope            string `json:"scope"`
	Collector        string `json:"collector"`
	CollectorVersion string `json:"collector_version"`
	CounterValue
}

func MeasuredMeasurement(scope, collector, collectorVersion string, value uint64) CounterMeasurement {
	return CounterMeasurement{
		Window:           "collector_lifetime",
		Scope:            scope,
		Collector:        collector,
		CollectorVersion: collectorVersion,
		CounterValue:     MeasuredCounter(value),
	}
}

func UnavailableMeasurement(scope, collector, collectorVersion, reason string) CounterMeasurement {
	return CounterMeasurement{
		Window:           "collector_lifetime",
		Scope:            scope,
		Collector:        collector,
		CollectorVersion: collectorVersion,
		CounterValue:     UnavailableCounter(reason),
	}
}

func (m CounterMeasurement) validate(name string) error {
	switch m.Window {
	case "collector_lifetime", "pre_submission_to_post_terminal", "recorder_enabled_to_post_terminal":
	default:
		return fmt.Errorf("%s requires an explicit supported measurement window", name)
	}
	switch m.Scope {
	case "client_container", "client_process", "client_process_tree":
	default:
		return fmt.Errorf("%s must use client_container, client_process or client_process_tree scope", name)
	}
	if strings.TrimSpace(m.Collector) == "" || strings.TrimSpace(m.CollectorVersion) == "" {
		return fmt.Errorf("%s requires collector and collector version", name)
	}
	return m.CounterValue.validate(name)
}

// ResourceMetrics record CPU time, retired instructions and resident memory
// for the actual client workload, never the benchmark controller alone. Each
// counter carries its own provenance so reviewers can interpret availability
// correctly.
//
// The two memory counters answer different questions and are not
// interchangeable. PeakRSSBytes is the high point of the *sum* of resident
// memory over every process in the client's tree, sampled on a fixed
// interval; it is the figure that is comparable between clients and between
// hosts, because every platform computes it the same way. It is also a lower
// bound: a spike shorter than the sampling interval is not seen.
// PeakRSSHighWaterHint is whatever exact high-water mark the kernel keeps for
// free, which is cheaper and catches every spike but means something
// different on each platform -- the largest single process on macOS, the sum
// of per-process peaks on Windows, the whole container including page cache
// under cgroup. It is a hint for interpreting the sampled figure, never a
// cross-platform number.
type ResourceMetrics struct {
	CPUTimeNanoseconds   CounterMeasurement `json:"cpu_time_nanoseconds"`
	InstructionsRetired  CounterMeasurement `json:"instructions_retired"`
	PeakRSSBytes         CounterMeasurement `json:"peak_rss_bytes"`
	PeakRSSHighWaterHint CounterMeasurement `json:"peak_rss_high_water_hint"`
}

func (m ResourceMetrics) Validate() error {
	if err := m.CPUTimeNanoseconds.validate("cpu_time_nanoseconds"); err != nil {
		return err
	}
	if err := m.InstructionsRetired.validate("instructions_retired"); err != nil {
		return err
	}
	if err := m.PeakRSSBytes.validate("peak_rss_bytes"); err != nil {
		return err
	}
	return m.PeakRSSHighWaterHint.validate("peak_rss_high_water_hint")
}
