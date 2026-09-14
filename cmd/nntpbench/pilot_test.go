package main

import (
	"testing"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

func TestPilotSizingUsesFastestClientAndNeverShrinks(t *testing.T) {
	artifacts := []benchmark.QueueArtifact{summaryTestArtifact(benchmark.Weaver, 1, 1e9), summaryTestArtifact(benchmark.SABnzbd, 1, 30e9)}
	report, err := buildPilotSizing(artifacts, 30*time.Second, 32<<30)
	if err != nil {
		t.Fatal(err)
	}
	row := report.Fixtures[0]
	if row.FastestLowerBoundNanoseconds != 995e6 || row.RecommendedPayloadBytes < row.PayloadBytes || row.Status != "estimated_tier_requires_confirmation" {
		t.Fatal(row)
	}
	if row.TargetDurationNanoseconds != 30e9 || row.SuccessfulRuns != 2 {
		t.Fatal(row)
	}
}

func TestPilotSizingDoesNotTreatFailuresAsSlowSamples(t *testing.T) {
	artifacts := []benchmark.QueueArtifact{summaryTestArtifact(benchmark.Weaver, 1, 1e9), summaryTestDidNotFinishArtifact(benchmark.SABnzbd, 1)}
	report, err := buildPilotSizing(artifacts, 30*time.Second, 32<<30)
	if err != nil {
		t.Fatal(err)
	}
	if row := report.Fixtures[0]; row.Status != "investigate_failures_before_sizing" || row.RecommendedPayloadBytes != 0 || row.DidNotFinish != 1 {
		t.Fatal(row)
	}
}

func TestPilotSizingRejectsDuplicatesAndInvalidPolicy(t *testing.T) {
	a := summaryTestArtifact(benchmark.Weaver, 1, 1e9)
	if _, err := buildPilotSizing([]benchmark.QueueArtifact{a, a}, 30*time.Second, 32<<30); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, duration := range []time.Duration{0, time.Second, 2 * time.Hour} {
		if _, err := buildPilotSizing([]benchmark.QueueArtifact{a}, duration, 32<<30); err == nil {
			t.Fatal(duration)
		}
	}
}

func TestPilotSizingHonorsCeilingAndOutputIsDeterministic(t *testing.T) {
	a := summaryTestArtifact(benchmark.Weaver, 1, 100)
	b := summaryTestArtifact(benchmark.SABnzbd, 1, 100)
	report, err := buildPilotSizing([]benchmark.QueueArtifact{a, b}, time.Hour, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if row := report.Fixtures[0]; row.Status != "required_size_exceeds_declared_ceiling" || row.RecommendedPayloadBytes != 0 {
		t.Fatal(row)
	}
	reversed, err := buildPilotSizing([]benchmark.QueueArtifact{b, a}, time.Hour, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if benchmark.EvidenceDigest(report) != benchmark.EvidenceDigest(reversed) {
		t.Fatal("order-dependent sizing")
	}
}
