package main

import (
	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
	"testing"
)

func TestSubgroupsKeepFailureDenominatorsAndEncryptionSeparate(t *testing.T) {
	key := aggregateStratum{FixtureClass: fixture.HeadlineFixtureClass, Transport: benchmark.TLS}
	parents := map[aggregateStratum]*aggregateAccount{key: {samples: map[string][]benchmark.PairedSample{"clean": {{Baseline: 2, Candidate: 1}, {Baseline: 2, Candidate: 1}}}, withheld: []string{"repair"}}}
	cases := map[string]fixture.ArchiveCase{"clean": {Encryption: fixture.NoEncryption, RepairProfile: fixture.CleanRepairProfile}, "repair": {Encryption: fixture.HeaderEncryption, RepairProfile: fixture.PAR2HeavyRepairProfile}}
	report, err := buildClassAggregates(subgroupAccounts(parents, cases), 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 4 {
		t.Fatalf("expected two repair and two encryption groups, got %d", len(report))
	}
	for _, group := range report {
		if group.Stratum.Transport != benchmark.TLS || group.Stratum.FixtureClass != fixture.HeadlineFixtureClass {
			t.Fatal("lost parent stratum")
		}
		failed := group.Stratum.GroupValue == "repair_required" || group.Stratum.GroupValue == string(fixture.HeaderEncryption)
		if failed {
			if group.Summary != nil || len(group.FixturesWithheld) != 1 || group.FixturesWithheld[0] != "repair" {
				t.Fatal("DNF disappeared from subgroup")
			}
		} else if group.Summary == nil || len(group.FixturesCompared) != 1 {
			t.Fatal("clean subgroup should retain its independent estimate")
		}
	}
}
