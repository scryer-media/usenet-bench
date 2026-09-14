package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

type pilotSizing struct {
	FixtureID                    string `json:"fixture_id"`
	DisplayName                  string `json:"display_name"`
	WorkloadSHA256               string `json:"workload_sha256"`
	PayloadBytes                 int64  `json:"pilot_payload_bytes"`
	SuccessfulRuns               int    `json:"successful_runs"`
	DidNotFinish                 int    `json:"did_not_finish"`
	FastestLowerBoundNanoseconds int64  `json:"fastest_duration_lower_bound_nanoseconds"`
	TargetDurationNanoseconds    int64  `json:"target_duration_nanoseconds"`
	RecommendedPayloadBytes      int64  `json:"recommended_payload_bytes,omitempty"`
	Status                       string `json:"status"`
}

type pilotSizingReport struct {
	SchemaVersion            int           `json:"schema_version"`
	SourceExecutionSHA256    string        `json:"source_execution_sha256"`
	SourceObservationsSHA256 string        `json:"source_observations_sha256"`
	MaximumPayloadBytes      int64         `json:"maximum_payload_bytes"`
	Policy                   string        `json:"policy"`
	Fixtures                 []pilotSizing `json:"fixtures"`
}

// size-pilot only reads reconciled artifacts. It neither changes the corpus
// nor launches clients, changes caches or operates the NNTP stack.
func sizePilot(args []string, out io.Writer) error {
	f := flag.NewFlagSet("size-pilot", flag.ContinueOnError)
	root := f.String("artifacts", "", "completed sequential pilot artifact root (never pooled with the final experiment)")
	target := f.Duration("target-duration", 30*time.Second, "minimum duration for the fastest client; at least 10s")
	maximum := f.Int64("maximum-payload-bytes", 32<<30, "operator-approved per-fixture sizing ceiling; advice only, no allocation")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *root == "" || f.NArg() != 0 {
		return fmt.Errorf("size-pilot requires --artifacts and accepts flags only")
	}
	artifacts, _, err := loadSequentialArtifacts(*root)
	if err != nil {
		return err
	}
	report, err := buildPilotSizing(artifacts, *target, *maximum)
	if err != nil {
		return err
	}
	report.SourceExecutionSHA256, err = sha256File(filepath.Join(*root, "execution-manifest.json"))
	if err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(report)
}

func buildPilotSizing(artifacts []benchmark.QueueArtifact, target time.Duration, maximum int64) (pilotSizingReport, error) {
	report := pilotSizingReport{SchemaVersion: 1, MaximumPayloadBytes: maximum, Policy: "exploratory linear estimate, rounded up to a 1 MiB power-of-two tier; preserve format/layout/repair/encryption axes and use the SAME selected size for every client; confirm with a second pilot, then freeze a fresh randomized plan in separate independently started sessions; never pool pilot measurements into confirmatory results"}
	if target < 10*time.Second || target > time.Hour || maximum < 1<<20 || maximum > 1<<40 {
		return report, fmt.Errorf("pilot target must be 10s..1h and payload ceiling 1 MiB..1 TiB")
	}
	rows := map[string]*pilotSizing{}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		if err := artifact.ValidateEvidence(); err != nil {
			return report, err
		}
		if artifact.SubmissionMode != benchmark.SubmissionModeSequential || len(artifact.Jobs) != 1 {
			return report, fmt.Errorf("sizing requires sequential one-fixture runs")
		}
		job := artifact.Jobs[0]
		if seen[job.Run.ID] {
			return report, fmt.Errorf("duplicate pilot run %s", job.Run.ID)
		}
		seen[job.Run.ID] = true
		var payload int64
		for _, file := range job.Workload.Manifest.ExpectedFiles {
			if file.Size < 0 || file.Size > math.MaxInt64-payload {
				return report, fmt.Errorf("invalid pilot payload size")
			}
			payload += file.Size
		}
		if payload <= 0 {
			return report, fmt.Errorf("pilot fixture has no payload")
		}
		row := rows[job.Run.FixtureID]
		if row == nil {
			row = &pilotSizing{FixtureID: job.Run.FixtureID, DisplayName: job.Workload.Manifest.Case.DisplayName(), WorkloadSHA256: job.WorkloadSHA256, PayloadBytes: payload, TargetDurationNanoseconds: int64(target), Status: "estimated_tier_requires_confirmation"}
			rows[row.FixtureID] = row
		}
		if row.WorkloadSHA256 != job.WorkloadSHA256 {
			return report, fmt.Errorf("pilot fixture mixes workload identities")
		}
		if job.Outcome == "dnf" {
			row.DidNotFinish++
			continue
		}
		if job.Outcome != "completed" {
			return report, fmt.Errorf("pilot contains a harness failure, not a client outcome")
		}
		lower := job.AdapterResult.SubmissionToTerminalNanoseconds - job.AdapterResult.TerminalObservationUncertainty
		if lower <= 0 {
			return report, fmt.Errorf("pilot duration lower bound is not positive")
		}
		row.SuccessfulRuns++
		if row.FastestLowerBoundNanoseconds == 0 || lower < row.FastestLowerBoundNanoseconds {
			row.FastestLowerBoundNanoseconds = lower
		}
		uncertainty := job.AdapterResult.TerminalObservationUncertainty
		if uncertainty > math.MaxInt64/100 {
			return report, fmt.Errorf("pilot uncertainty overflows target duration")
		}
		row.TargetDurationNanoseconds = max(row.TargetDurationNanoseconds, uncertainty*100)
	}
	if len(rows) == 0 {
		return report, fmt.Errorf("no pilot observations")
	}
	ordered := append([]benchmark.QueueArtifact(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SuiteID < ordered[j].SuiteID })
	report.SourceObservationsSHA256 = benchmark.EvidenceDigest(ordered)
	for _, row := range rows {
		switch {
		case row.DidNotFinish > 0:
			row.Status = "investigate_failures_before_sizing"
		case row.SuccessfulRuns < 2:
			row.Status = "insufficient_pilot_observations"
		default:
			factor := max(1, float64(row.TargetDurationNanoseconds)/float64(row.FastestLowerBoundNanoseconds))
			required := math.Ceil(float64(row.PayloadBytes) * factor)
			tier := int64(1 << 20)
			for float64(tier) < required && tier <= maximum/2 {
				tier *= 2
			}
			if float64(tier) < required || tier > maximum {
				row.Status = "required_size_exceeds_declared_ceiling"
			} else {
				row.RecommendedPayloadBytes = tier
			}
		}
		report.Fixtures = append(report.Fixtures, *row)
	}
	sort.Slice(report.Fixtures, func(i, j int) bool { return report.Fixtures[i].FixtureID < report.Fixtures[j].FixtureID })
	return report, nil
}
