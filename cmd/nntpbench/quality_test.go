package main

import (
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestSummaryRevalidatesTimingOracleAndWorkloadEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*benchmark.QueueArtifact){
		"negative timing bound": func(a *benchmark.QueueArtifact) {
			a.Jobs[0].AdapterResult.TerminalObservationUncertainty = -1
			a.AdapterResult.Jobs[0] = a.Jobs[0].AdapterResult
		},
		"empty output verification": func(a *benchmark.QueueArtifact) { a.Jobs[0].Verification.Files = nil },
		"different output bytes":    func(a *benchmark.QueueArtifact) { a.Jobs[0].Verification.Files[0].BLAKE3 = strings.Repeat("c", 64) },
		"missing workload":          func(a *benchmark.QueueArtifact) { a.Jobs[0].Workload = nil },
		"changed seed size": func(a *benchmark.QueueArtifact) {
			a.Jobs[0].Workload.Articles.RawBytes = 1
			a.Jobs[0].WorkloadSHA256 = benchmark.EvidenceDigest(a.Jobs[0].Workload)
		},
		"changed workload digest": func(a *benchmark.QueueArtifact) { a.Jobs[0].Workload.NZBSHA256 = strings.Repeat("c", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var artifacts []benchmark.QueueArtifact
			for r := 1; r <= 2; r++ {
				for _, c := range []benchmark.Client{benchmark.Weaver, benchmark.SABnzbd} {
					a := summaryTestArtifact(c, r, 1e9)
					if c == benchmark.Weaver {
						mutate(&a)
					}
					artifacts = append(artifacts, a)
				}
			}
			if _, err := buildSummaryReport(artifacts, nil, benchmark.SABnzbd, benchmark.Weaver, 2, 1, 100); err == nil {
				t.Fatal("invalid evidence passed summary")
			}
		})
	}
}

func TestSummaryRefusesDifferentShapersWithinPair(t *testing.T) {
	var artifacts []benchmark.QueueArtifact
	for r := 1; r <= 2; r++ {
		for _, c := range []benchmark.Client{benchmark.Weaver, benchmark.SABnzbd} {
			a := summaryTestShapedArtifact(c, r, 1e9, 123456, nil)
			if c == benchmark.Weaver {
				a.ShaperBefore.Build.ExecutableSHA256 = strings.Repeat("1", 64)
				a.ShaperAfter.Build.ExecutableSHA256 = strings.Repeat("1", 64)
			}
			artifacts = append(artifacts, a)
		}
	}
	if _, err := buildSummaryReport(artifacts, nil, benchmark.SABnzbd, benchmark.Weaver, 2, 1, 100); err == nil {
		t.Fatal("mixed shaper identity passed")
	}
}

func TestSummaryRefusesProductBuildDriftAcrossFixtures(t *testing.T) {
	var artifacts []benchmark.QueueArtifact
	for _, id := range []string{"fixture-a", "fixture-b"} {
		for rep := 1; rep <= 2; rep++ {
			for _, client := range []benchmark.Client{benchmark.Weaver, benchmark.SABnzbd} {
				a := summaryTestFixtureArtifact(id, fixture.HeadlineFixtureClass, client, rep, 30e9)
				if id == "fixture-b" && client == benchmark.Weaver {
					a.AdapterResult.ClientVersion = "changed-build"
				}
				artifacts = append(artifacts, a)
			}
		}
	}
	if _, err := buildSummaryReport(artifacts, nil, benchmark.SABnzbd, benchmark.Weaver, 2, 1, 100); err == nil || !strings.Contains(err.Error(), "across fixtures") {
		t.Fatalf("mixed-build aggregate accepted: %v", err)
	}
}
