package main

import (
	"encoding/json"
	"os"
	"testing"
)

// The published historical export is data, not an oracle for new performance
// claims. Check identity/presentation without modifying its measured values.
func TestPublishedResultsHaveNoDuplicateFixturesPerComparison(t *testing.T) {
	raw, err := os.ReadFile("../../results/weaver-0.11.2-client-sweep.json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Phases []struct {
			Phase       string
			Rig         string
			Comparisons map[string]struct{ Strata []struct{ Fixture string } }
		}
		Fixtures []struct {
			ID          string
			DisplayName string `json:"display_name"`
		}
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Phases) == 0 || len(result.Fixtures) == 0 {
		t.Fatal("empty export")
	}
	for _, p := range result.Phases {
		for baseline, c := range p.Comparisons {
			seen := map[string]bool{}
			for _, s := range c.Strata {
				if s.Fixture == "" || seen[s.Fixture] {
					t.Fatalf("duplicate/missing fixture identity in %s/%s/%s: %s", p.Rig, p.Phase, baseline, s.Fixture)
				}
				seen[s.Fixture] = true
			}
		}
	}
	names := map[string]string{}
	byID := map[string]string{}
	for _, f := range result.Fixtures {
		if _, ok := byID[f.ID]; ok {
			t.Fatalf("duplicate fixture catalog ID %s", f.ID)
		}
		byID[f.ID] = f.DisplayName
		if f.DisplayName != "" {
			if previous := names[f.DisplayName]; previous != "" {
				t.Fatalf("label collision %s / %s", previous, f.ID)
			}
			names[f.DisplayName] = f.ID
		}
	}
	a, b := byID["rar4-393-data-normal-solid-data-incompressible"], byID["rar4-data-normal-solid-data-incompressible"]
	if a == "" || b == "" || a == b {
		t.Fatal("RAR4 writer variants need distinct exported names")
	}
}
