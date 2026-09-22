package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// The real-provider leg is the one lane this harness repeats a client on, and
// this is what reads it. A shaped lane holds its link still, so one run per
// client is a fair run; the internet holds nothing still, so a leg that took
// four hours measured its first client against a different network than its
// last. The leg answers that by running the client list forward, then
// reversed, then forward again, and this summary reports each client's median
// over its passes with the min and the max beside it -- and every arm in the
// order it ran, so a reader can see the drift for themselves rather than take
// the median on trust.
//
// It is deliberately not a paired statistical comparison. Three passes over an
// unattested link do not support a confidence interval, and printing one would
// dress a reconnaissance number up as the headline result. The shaped lanes
// carry the claim; this leg says what the same shipped products did against a
// real provider on one evening.

// InterleavedMetric names what each arm measures: the same
// submission-to-verified-terminal wall clock the shaped lanes measure.
const InterleavedMetric = "submission_to_observed_terminal"

type interleavedReport struct {
	SchemaVersion int    `json:"schema_version"`
	Metric        string `json:"metric"`
	// Passes is the number of forward/reverse passes the plan declared.
	Passes  int                 `json:"passes"`
	Clients []interleavedClient `json:"clients"`
	// Arms lists every measured arm in the order it ran, which is the order
	// that makes a drift visible.
	Arms       []interleavedArm  `json:"arms"`
	Provenance *reportProvenance `json:"provenance,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// interleavedClient is one client on one fixture across its passes.
type interleavedClient struct {
	Client      benchmark.Client `json:"client"`
	FixtureID   string           `json:"fixture_id"`
	FixtureName string           `json:"fixture_name"`
	// FinishedArms is how many of this client's arms produced a verified
	// output; the wall clocks below describe those only.
	FinishedArms                 int    `json:"finished_arms"`
	DidNotFinishArms             int    `json:"did_not_finish_arms"`
	WallClockMedianNanoseconds   int64  `json:"wall_clock_median_nanoseconds,omitempty"`
	WallClockMinNanoseconds      int64  `json:"wall_clock_min_nanoseconds,omitempty"`
	WallClockMaxNanoseconds      int64  `json:"wall_clock_max_nanoseconds,omitempty"`
	ContaminatedArms             int    `json:"contaminated_arms,omitempty"`
	MedianWithheld               string `json:"median_withheld,omitempty"`
	WallClockSpreadOverMedianPct string `json:"wall_clock_spread_over_median_percent,omitempty"`
}

// interleavedArm is one client's one pass over one fixture.
type interleavedArm struct {
	Order     int              `json:"order"`
	Pass      int              `json:"pass"`
	Client    benchmark.Client `json:"client"`
	FixtureID string           `json:"fixture_id"`
	// StartedAtLocal is the arm's submission time in the measuring host's own
	// time zone. An arm that ran at 02:40 local and one that ran at 20:10 met
	// different providers, and a UTC stamp hides that from the reader who
	// knows the difference.
	StartedAtLocal       string    `json:"started_at_local"`
	StartedAt            time.Time `json:"started_at"`
	WallClockNanoseconds int64     `json:"wall_clock_nanoseconds,omitempty"`
	Outcome              string    `json:"outcome"`
	TerminalError        string    `json:"terminal_error,omitempty"`
	// NZBArticleBytes is what the NZB says the arm had to move, from the
	// fixture's own article attestation.
	NZBArticleBytes uint64 `json:"nzb_article_bytes,omitempty"`
	// HostNICReceiveBytes is what the host's interfaces actually received
	// while this arm, and nothing else the harness started, was running.
	// Unavailable says why when the platform has no counter this harness
	// reads.
	HostNICReceiveBytes uint64 `json:"host_nic_receive_bytes,omitempty"`
	NICUnavailable      string `json:"host_nic_unavailable,omitempty"`
	// NICOverPayload is the ratio of the two. A few percent over one is
	// protocol overhead; well over one is other traffic sharing the link, and
	// Contamination says so.
	NICOverPayload float64 `json:"host_nic_over_payload,omitempty"`
	Contamination  string  `json:"contamination,omitempty"`
}

// buildInterleavedReport turns an interleaved leg's artifacts into the report
// above. It refuses a root whose plan was not interleaved: a randomized
// schedule read as an ABBA one would report a median over passes that were
// never passes.
func buildInterleavedReport(artifacts []benchmark.QueueArtifact, passes int) (interleavedReport, error) {
	if passes < 1 {
		return interleavedReport{}, fmt.Errorf("this artifact root's plan is not interleaved; --mode interleaved reads the real-provider leg only")
	}
	report := interleavedReport{SchemaVersion: 1, Metric: InterleavedMetric, Passes: passes}
	type clientKey struct {
		Client    benchmark.Client
		FixtureID string
	}
	type clientAccount struct {
		name         string
		wallClocks   []int64
		didNotFinish int
		contaminated int
	}
	accounts := make(map[clientKey]*clientAccount)
	for _, artifact := range artifacts {
		if err := artifact.ValidateEvidence(); err != nil {
			return interleavedReport{}, fmt.Errorf("artifact %s: %w", artifact.SuiteID, err)
		}
		for _, job := range artifact.Jobs {
			arm := interleavedArm{
				Pass:           job.Run.Repetition,
				Client:         job.Run.Client,
				FixtureID:      job.Run.FixtureID,
				StartedAt:      job.AdapterResult.SubmissionStartedAt,
				StartedAtLocal: job.AdapterResult.SubmissionStartedAt.Local().Format(time.RFC3339),
				Outcome:        job.Outcome,
				TerminalError:  job.AdapterResult.TerminalError,
			}
			finished := job.Outcome == "completed" && job.Verification != nil
			if finished {
				arm.WallClockNanoseconds = job.AdapterResult.SubmissionToTerminalNanoseconds
			}
			if job.Workload != nil {
				var payload uint64
				for _, segment := range job.Workload.Articles.Segments {
					if segment.RawBytes > 0 {
						payload += uint64(segment.RawBytes)
					}
				}
				arm.NZBArticleBytes = payload
			}
			if delta := artifact.HostNICReceive; delta != nil {
				if delta.Available {
					arm.HostNICReceiveBytes = delta.DeltaBytes
					if ratio, exceeds := delta.ExceedsPayload(arm.NZBArticleBytes); ratio > 0 {
						arm.NICOverPayload = ratio
						if exceeds {
							arm.Contamination = fmt.Sprintf("the host received %.2fx the arm's payload, above the %.0f%% tolerance: other traffic shared this link and the arm's wall clock is worth less than it looks",
								ratio, benchmark.NICExcessTolerance*100)
						}
					}
				} else {
					arm.NICUnavailable = delta.Unavailable
				}
			} else {
				arm.NICUnavailable = "the adapter recorded no host NIC counter for this suite"
			}
			report.Arms = append(report.Arms, arm)

			key := clientKey{Client: job.Run.Client, FixtureID: job.Run.FixtureID}
			account := accounts[key]
			if account == nil {
				account = &clientAccount{}
				accounts[key] = account
			}
			if name := job.Workload.Manifest.Case.DisplayName(); name != "" {
				account.name = name
			}
			if finished {
				account.wallClocks = append(account.wallClocks, arm.WallClockNanoseconds)
			} else {
				account.didNotFinish++
			}
			if arm.Contamination != "" {
				account.contaminated++
			}
		}
	}
	if len(report.Arms) == 0 {
		return interleavedReport{}, fmt.Errorf("artifact root contains no interleaved arms")
	}
	sort.Slice(report.Arms, func(left, right int) bool {
		if !report.Arms[left].StartedAt.Equal(report.Arms[right].StartedAt) {
			return report.Arms[left].StartedAt.Before(report.Arms[right].StartedAt)
		}
		return report.Arms[left].FixtureID < report.Arms[right].FixtureID
	})
	for index := range report.Arms {
		report.Arms[index].Order = index + 1
	}

	keys := make([]clientKey, 0, len(accounts))
	for key := range accounts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].FixtureID != keys[right].FixtureID {
			return keys[left].FixtureID < keys[right].FixtureID
		}
		return keys[left].Client < keys[right].Client
	})
	for _, key := range keys {
		account := accounts[key]
		entry := interleavedClient{
			Client:           key.Client,
			FixtureID:        key.FixtureID,
			FixtureName:      account.name,
			FinishedArms:     len(account.wallClocks),
			DidNotFinishArms: account.didNotFinish,
			ContaminatedArms: account.contaminated,
		}
		if len(account.wallClocks) == 0 {
			entry.MedianWithheld = fmt.Sprintf("%s finished none of its %d arms on this fixture", key.Client, account.didNotFinish)
			report.Clients = append(report.Clients, entry)
			continue
		}
		sorted := append([]int64(nil), account.wallClocks...)
		sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
		entry.WallClockMinNanoseconds = sorted[0]
		entry.WallClockMaxNanoseconds = sorted[len(sorted)-1]
		// The upper median, as the shaped summary's transfer evidence uses:
		// an even count has no middle and this harness never invents one by
		// averaging two observations of different conditions.
		entry.WallClockMedianNanoseconds = sorted[len(sorted)/2]
		entry.WallClockSpreadOverMedianPct = fmt.Sprintf("%.1f",
			100*float64(entry.WallClockMaxNanoseconds-entry.WallClockMinNanoseconds)/float64(entry.WallClockMedianNanoseconds))
		if len(account.wallClocks)+account.didNotFinish != passes {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("%s ran %d arms on %s, not the %d passes the plan declared", key.Client, len(account.wallClocks)+account.didNotFinish, key.FixtureID, passes))
		}
		report.Clients = append(report.Clients, entry)
	}
	for _, arm := range report.Arms {
		if arm.Contamination != "" {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("arm %d (%s, pass %d) ran over a contaminated link: %s", arm.Order, arm.Client, arm.Pass, arm.Contamination))
		}
	}
	sort.Strings(report.Warnings)
	return report, nil
}
