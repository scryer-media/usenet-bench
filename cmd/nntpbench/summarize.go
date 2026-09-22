package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
)

type summaryReport struct {
	SchemaVersion int                    `json:"schema_version"`
	Metric        string                 `json:"metric"`
	Baseline      benchmark.Client       `json:"baseline_client"`
	Candidate     benchmark.Client       `json:"candidate_client"`
	MinimumBlocks int                    `json:"minimum_complete_blocks"`
	Comparisons   []stratifiedComparison `json:"comparisons"`
	// Aggregates pools the per-fixture comparisons by fixture class, one
	// figure per class and non-fixture stratum. The headline aggregate is the
	// number a reader may quote for the common case; the breadth aggregate
	// is the compatibility figure. They are never pooled with each other.
	Aggregates []classAggregate `json:"aggregates"`
	Subgroups  []classAggregate `json:"subgroups"`
	// Provenance states the conditions the numbers above were taken under:
	// which harness, which build of each client, which corpus, which host and
	// which link. It is absent only when a summary is built from artifacts
	// alone, without the execution manifest that binds them.
	Provenance *reportProvenance `json:"provenance,omitempty"`
}

// aggregateStratum is comparisonStratum with the fixture replaced by its
// class: the key under which per-fixture results may be pooled.
type aggregateStratum struct {
	GroupAxis        string                     `json:"group_axis,omitempty"`
	GroupValue       string                     `json:"group_value,omitempty"`
	FixtureClass     fixture.FixtureClass       `json:"fixture_class"`
	Profile          string                     `json:"profile"`
	ExecutionTarget  benchmark.ExecutionTarget  `json:"execution_target"`
	Transport        benchmark.Transport        `json:"transport"`
	ArchiveToolchain benchmark.ArchiveToolchain `json:"archive_toolchain"`
	ServerLinkID     string                     `json:"server_link_id"`
	ServerEgressBPS  uint64                     `json:"server_egress_bits_per_second"`
	ServerBurstBytes uint64                     `json:"server_burst_bytes"`
	ServerRTTMicros  uint64                     `json:"server_rtt_micros"`
	StorageProfileID string                     `json:"storage_profile_id"`
	StorageNFSLinkID string                     `json:"storage_nfs_link_id"`
	StorageLinkBPS   uint64                     `json:"storage_link_bits_per_second"`
	StorageRTTMicros uint64                     `json:"storage_rtt_micros"`
	ArticleProfileID string                     `json:"article_profile_id"`
	ArticleRawBytes  int                        `json:"article_raw_bytes"`
}

func (s comparisonStratum) aggregateKey(class fixture.FixtureClass) aggregateStratum {
	return aggregateStratum{
		FixtureClass:     class,
		Profile:          s.Profile,
		ExecutionTarget:  s.ExecutionTarget,
		Transport:        s.Transport,
		ArchiveToolchain: s.ArchiveToolchain,
		ServerLinkID:     s.ServerLinkID,
		ServerEgressBPS:  s.ServerEgressBPS,
		ServerBurstBytes: s.ServerBurstBytes,
		ServerRTTMicros:  s.ServerRTTMicros,
		StorageProfileID: s.StorageProfileID,
		StorageNFSLinkID: s.StorageNFSLinkID,
		StorageLinkBPS:   s.StorageLinkBPS,
		StorageRTTMicros: s.StorageRTTMicros,
		ArticleProfileID: s.ArticleProfileID,
		ArticleRawBytes:  s.ArticleRawBytes,
	}
}

// classAggregate is one class's pooled figure. It is withheld, with the
// fixtures named, whenever any fixture of the class had its own comparison
// withheld: a client that could not finish a fixture of the class does not
// get a class figure computed over the fixtures it did finish.
type classAggregate struct {
	Weighting         string                         `json:"weighting"`
	Stratum           aggregateStratum               `json:"stratum"`
	FixturesCompared  []string                       `json:"fixtures_compared"`
	FixturesWithheld  []string                       `json:"fixtures_withheld,omitempty"`
	Summary           *benchmark.CrossFixtureSummary `json:"summary,omitempty"`
	AggregateWithheld string                         `json:"aggregate_withheld,omitempty"`
}

// aggregateAccount collects a class stratum's inputs while the per-fixture
// comparisons are built.
type aggregateAccount struct {
	samples  map[string][]benchmark.PairedSample
	withheld []string
}

// comparisonStratum is the pairing key. Transport is part of it; how each
// client validated TLS is not, because that is a per-client property of the
// run (SABnzbd cannot verify the harness CA and is labelled tls-unverified)
// and keying on it would leave every SABnzbd TLS block unpaired. Each
// client's validation and label are carried on the comparison instead, so a
// reader sees what was compared without the pairing depending on it.
type comparisonStratum struct {
	FixtureID        string                     `json:"fixture_id"`
	Profile          string                     `json:"profile"`
	ExecutionTarget  benchmark.ExecutionTarget  `json:"execution_target"`
	Transport        benchmark.Transport        `json:"transport"`
	ArchiveToolchain benchmark.ArchiveToolchain `json:"archive_toolchain"`
	ServerLinkID     string                     `json:"server_link_id"`
	ServerEgressBPS  uint64                     `json:"server_egress_bits_per_second"`
	ServerBurstBytes uint64                     `json:"server_burst_bytes"`
	ServerRTTMicros  uint64                     `json:"server_rtt_micros"`
	// StorageProfileID and its link join the stratum key. A local run and an
	// NFS run measure different questions, so they are never pooled — the same
	// rule that keeps transports and toolchains apart.
	StorageProfileID string `json:"storage_profile_id"`
	StorageNFSLinkID string `json:"storage_nfs_link_id"`
	StorageLinkBPS   uint64 `json:"storage_link_bits_per_second"`
	StorageRTTMicros uint64 `json:"storage_rtt_micros"`
	// ArticleProfileID and its byte count join the stratum key. The same
	// fixture downloaded at 384 KiB and at 750 KiB articles is the same bytes
	// split into a different number of round trips and a different amount of
	// per-article work, so the two are never pooled — the same rule that keeps
	// two server RTTs apart.
	ArticleProfileID string `json:"article_profile_id"`
	ArticleRawBytes  int    `json:"article_raw_bytes"`
}

type stratifiedComparison struct {
	DisplayName          string            `json:"display_name"`
	FixtureName          string            `json:"fixture_name"`
	TimingPrecision      timingPrecision   `json:"timing_precision"`
	WorkloadSHA256       string            `json:"workload_sha256"`
	InfrastructureSHA256 string            `json:"infrastructure_sha256"`
	Stratum              comparisonStratum `json:"stratum"`
	// FixtureClass is the class the fixture's manifest declared, and the key
	// under which this comparison is pooled in Aggregates.
	FixtureClass fixture.FixtureClass `json:"fixture_class"`
	// Encoding is how this fixture's articles were encoded: yenc for every
	// fixture but the uuencode lane. It is reported, not keyed on, because a
	// fixture has exactly one encoding — but a did-not-finish is much easier
	// to read with it in view.
	Encoding fixture.PostEncoding `json:"encoding"`
	// TransportPolicies records, per observed client, how it validated TLS in
	// this stratum. Plaintext strata carry not_applicable. A client the plan
	// excluded on this fixture has no observation and so no entry here; its
	// exclusion appears under ClientExclusions.
	TransportPolicies []clientTransportPolicy `json:"transport_policies"`
	// ClientExclusions lists the plan's exclusions of the baseline or the
	// candidate on this fixture: blocks the plan chose not to run, counted
	// under Completion as that client not finishing, with the reason why.
	ClientExclusions []benchmark.ClientExclusion `json:"client_exclusions,omitempty"`
	Completion       completionCounts            `json:"completion"`
	// Summary is absent when the stratum has fewer complete pairs than the
	// minimum because one client did not finish; ComparisonWithheld then says
	// so. A client that cannot finish a fixture is a result in itself, and
	// hiding the whole run behind it would hide that result.
	Summary            *benchmark.PairedSummary `json:"summary,omitempty"`
	ComparisonWithheld string                   `json:"comparison_withheld,omitempty"`
	// CPUTime is the secondary comparison: each client's CPU time over the
	// same paired blocks, so a client that finishes in the same wall clock by
	// spending more cores — or by delegating the work to child processes —
	// shows the difference. It is never pooled with the timing summary and
	// never fails the report closed; a counter the lane could not collect is
	// reported as such and the comparison withheld.
	CPUTime counterComparison `json:"cpu_time"`
	// PeakRSS is the other secondary comparison: the high point of each
	// client's resident memory over the same paired blocks. A client that
	// matches another's wall clock by holding far more of the job in memory
	// shows the difference here. Like CPUTime it is never pooled with the
	// timing summary and never fails the report closed.
	PeakRSS counterComparison `json:"peak_rss"`
	// DeviceWrites is the third secondary comparison: bytes written to block
	// devices over the same paired blocks. It is the cost a wall clock on a
	// fast disk hides -- a completion move that fell back to a copy, an
	// unpack staging a second full copy, a repair pass rewriting what it just
	// wrote -- and on the operator's slower disk it is the difference. Like
	// the other two it is never pooled with the timing summary, and a lane
	// with no block-io accounting (both native lanes today) reports it
	// unavailable with a reason rather than as zero.
	DeviceWrites counterComparison `json:"device_write_bytes"`
	// Transfer is evidence, not a comparison: what each client pulled through
	// the shaper on its finished blocks, next to how many articles it asked
	// for. A client that finishes fast by fetching more than the NZB carries
	// is visible here beside its wall clock, and the census says whether the
	// excess was asked for twice or simply read past. Absent for clients
	// without a shaped run in the stratum.
	Transfer []clientTransferEvidence `json:"transfer,omitempty"`
}

// clientTransferEvidence summarises one client's shaped, finished runs in a
// stratum. Downstream bytes are what the shaper wrote into the client's
// sockets — application bytes, so kernel retransmits never inflate them; a
// deterministic client lands within a few bytes of itself block to block, and
// a spread between min and max is itself a finding.
type clientTransferEvidence struct {
	Client                benchmark.Client `json:"client"`
	FinishedBlocks        int              `json:"finished_blocks"`
	DownstreamBytesMin    uint64           `json:"shaper_downstream_bytes_min"`
	DownstreamBytesMedian uint64           `json:"shaper_downstream_bytes_median"`
	DownstreamBytesMax    uint64           `json:"shaper_downstream_bytes_max"`
	// ArticleCensus is present when the shaper counted the client's command
	// lines (attestation schema 3); it sums over the blocks it covers.
	ArticleCensus *transferArticleCensus `json:"article_census,omitempty"`
}

type transferArticleCensus struct {
	Blocks                  int    `json:"blocks"`
	ArticleRequests         uint64 `json:"article_requests"`
	DistinctArticleRequests uint64 `json:"distinct_article_requests"`
	RepeatedArticleRequests uint64 `json:"repeated_article_requests"`
}

// transferAccount accumulates one client's shaped finished runs in a stratum.
type transferAccount struct {
	bytes    []uint64
	census   int
	requests uint64
	distinct uint64
	repeated uint64
}

// counterComparison pairs one secondary resource counter inside the stratum's
// blocks. Two of them are built per stratum: `cpu_time_nanoseconds` and
// `peak_rss_bytes`.
//
// A counter's scope is whole-container in the Docker lane (the container
// cgroup, so every helper the client spawns — unrar, par2, 7z — is charged to
// it) and the process tree in the native lanes. Those are different
// quantities: the comparison is withheld unless both clients were measured at
// the same scope, and the scope, collector and collector version each client's
// numbers came from are stated beside the result.
type counterComparison struct {
	Metric     string                    `json:"metric"`
	Accounting []clientCounterAccounting `json:"accounting"`
	// PairedBlocks counts the blocks both clients finished *and* both
	// counters were measured in; a block either counter is unavailable in is
	// dropped from this comparison and counted under its client's accounting.
	PairedBlocks int `json:"paired_blocks"`
	// Caveats carries the storage attestation's CPU accounting caveat when the
	// stratum ran on an NFS profile (the counter excludes the NFS client
	// kernel time the host spends outside the container's cgroup) and a note
	// when fewer blocks than the run's minimum were paired. The ratio is
	// candidate over baseline like the timing summary: below 1 means the
	// candidate spent less of whatever the counter counts.
	Caveats            []string                 `json:"caveats,omitempty"`
	Summary            *benchmark.PairedSummary `json:"summary,omitempty"`
	ComparisonWithheld string                   `json:"comparison_withheld,omitempty"`
}

// clientCounterAccounting says where one client's numbers for a counter in a
// stratum came from and how many blocks had none.
type clientCounterAccounting struct {
	Client             benchmark.Client `json:"client"`
	Scope              string           `json:"scope,omitempty"`
	Collector          string           `json:"collector,omitempty"`
	CollectorVersion   string           `json:"collector_version,omitempty"`
	MeasuredBlocks     int              `json:"measured_blocks"`
	UnavailableBlocks  int              `json:"unavailable_blocks"`
	UnavailableReasons []string         `json:"unavailable_reasons,omitempty"`
}

const (
	cpuTimeMetric     = "cpu_time_nanoseconds"
	peakRSSMetric     = "peak_rss_bytes"
	deviceWriteMetric = "device_write_bytes"
)

// completionCounts records, per stratum, how many randomized blocks each
// client finished. Blocks where either client did not finish (a terminal
// failure or an output that failed neutral verification) are excluded from the
// paired timing summary and counted here instead.
type completionCounts struct {
	BlocksObserved        int `json:"blocks_observed"`
	PairedBlocks          int `json:"paired_blocks"`
	BaselineDidNotFinish  int `json:"baseline_did_not_finish"`
	CandidateDidNotFinish int `json:"candidate_did_not_finish"`
	// BaselineExcluded and CandidateExcluded are the part of the did-not-finish
	// counts that the plan recorded instead of running: a client excluded on
	// the fixture is counted as not finishing every block, by declaration.
	BaselineExcluded  int `json:"baseline_excluded"`
	CandidateExcluded int `json:"candidate_excluded"`
}

type clientTransportPolicy struct {
	Client         benchmark.Client        `json:"client"`
	TLSValidation  benchmark.TLSValidation `json:"tls_validation"`
	TransportLabel string                  `json:"transport_label"`
}

type comparisonBlock struct {
	baselineUncertainty  float64
	candidateUncertainty float64
	baseline             *float64
	candidate            *float64
	baselineDNF          bool
	candidateDNF         bool
	// baselineCPU and candidateCPU are the measured `cpu_time_nanoseconds`
	// of the same two runs, nil when the lane recorded the counter as
	// unavailable. baselinePeakRSS and candidatePeakRSS are `peak_rss_bytes`
	// of those same runs, on the same terms.
	baselineCPU      *float64
	candidateCPU     *float64
	baselinePeakRSS  *float64
	candidatePeakRSS *float64
	// baselineDeviceWrite and candidateDeviceWrite are `device_write_bytes`
	// of the same two runs, on the same terms: the bytes each client put on a
	// block device while doing the job.
	baselineDeviceWrite  *float64
	candidateDeviceWrite *float64
}

// counterProvenance is one client's CPU counter source inside a stratum.
type counterProvenance struct {
	Window           string
	Scope            string
	Collector        string
	CollectorVersion string
}

// counterAccount accumulates one client's CPU counter evidence over a stratum.
type counterAccount struct {
	measured    map[counterProvenance]int
	unavailable int
	reasons     map[string]bool
}

func newCounterAccount() *counterAccount {
	return &counterAccount{measured: make(map[counterProvenance]int), reasons: make(map[string]bool)}
}

// counterObservation reads one of a finished run's secondary counters. A run
// whose lane recorded no resource metrics at all, or a measured counter of
// zero (which the paired ratio cannot take a logarithm of), is an unavailable
// observation with a stated reason, never a zero.
func counterObservation(metrics *benchmark.ResourceMetrics, metric string) (*float64, counterProvenance, string) {
	if metrics == nil {
		return nil, counterProvenance{}, "resource metrics not recorded for this run"
	}
	var counter benchmark.CounterMeasurement
	switch metric {
	case cpuTimeMetric:
		counter = metrics.CPUTimeNanoseconds
	case peakRSSMetric:
		counter = metrics.PeakRSSBytes
	case deviceWriteMetric:
		counter = metrics.DeviceWriteBytes
	default:
		return nil, counterProvenance{}, fmt.Sprintf("%s is not a counter this summary knows how to read", metric)
	}
	provenance := counterProvenance{Window: counter.Window, Scope: counter.Scope, Collector: counter.Collector, CollectorVersion: counter.CollectorVersion}
	if counter.Status != benchmark.CounterMeasured || counter.Value == nil {
		reason := strings.TrimSpace(counter.Reason)
		if reason == "" {
			reason = metric + " unavailable without a recorded reason"
		}
		return nil, provenance, reason
	}
	if *counter.Value == 0 {
		return nil, provenance, metric + " measured as zero"
	}
	value := float64(*counter.Value)
	return &value, provenance, ""
}

type summaryProductKey struct {
	Stratum comparisonStratum
	Client  benchmark.Client
}

type summaryProductIdentity struct {
	ClientIdentity           string
	ClientVersion            string
	ArchiveToolchainIdentity string
	RenderedConfigSHA256     string
	TLSValidation            benchmark.TLSValidation
	TransportLabel           string
}

// summaryExecutionContext is what the summarizer takes from an artifact
// root's immutable execution manifest and snapshotted plan.
type summaryExecutionContext struct {
	Command string
	// Manifest is the immutable manifest itself, kept so a report can state
	// the conditions the run was taken under rather than re-derive them.
	Manifest executionManifest
	// InterleavedPasses is the snapshotted plan's pass count, zero for the
	// randomized schedule every shaped lane uses.
	InterleavedPasses int
	PlannedRuns       map[string]benchmark.Run
	Exclusions        []benchmark.ClientExclusion
}

func summarize(args []string) error {
	flags := flag.NewFlagSet("summarize", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var artifactRoot, baselineName, candidateName, mode string
	var minimumBlocks, resamples int
	var seed int64
	flags.StringVar(&artifactRoot, "artifacts", "", "benchmark artifact root containing sequential queue.json files")
	flags.StringVar(&mode, "mode", "sequential", "sequential (paired per-fixture comparison), queue-drain (per-lane drain wall clock of a queue-transition root) or interleaved (per-client median over the real-provider leg's forward/reverse passes); --baseline and --candidate are used by sequential only")
	flags.StringVar(&baselineName, "baseline", "", "baseline client: weaver, sabnzbd, or nzbget")
	flags.StringVar(&candidateName, "candidate", "", "candidate client: weaver, sabnzbd, or nzbget")
	flags.IntVar(&minimumBlocks, "minimum-blocks", 20, "minimum complete paired randomized blocks per stratum")
	flags.IntVar(&resamples, "bootstrap-resamples", benchmark.DefaultBootstrapResamples, "fixed-seed paired bootstrap resamples")
	flags.Int64Var(&seed, "bootstrap-seed", 20260802, "deterministic paired bootstrap seed")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if mode == "queue-drain" {
		if artifactRoot == "" {
			return fmt.Errorf("--artifacts is required")
		}
		report, err := loadQueueDrainReport(artifactRoot)
		if err != nil {
			return err
		}
		return printJSON(report)
	}
	if mode == "interleaved" {
		if artifactRoot == "" {
			return fmt.Errorf("--artifacts is required")
		}
		artifacts, _, err := loadSequentialArtifacts(artifactRoot)
		if err != nil {
			return err
		}
		execution, err := loadSummaryExecutionContext(artifactRoot, "sequential")
		if err != nil {
			return err
		}
		report, err := buildInterleavedReport(artifacts, execution.InterleavedPasses)
		if err != nil {
			return err
		}
		provenance := buildReportProvenance(provenanceInputs{Artifacts: artifacts, Manifest: execution.Manifest})
		report.Provenance = &provenance
		return printJSON(report)
	}
	if mode != "sequential" {
		return fmt.Errorf("--mode must be sequential, queue-drain or interleaved, got %q", mode)
	}
	if artifactRoot == "" || baselineName == "" || candidateName == "" {
		return fmt.Errorf("--artifacts, --baseline, and --candidate are required")
	}

	if minimumBlocks < 2 {
		return fmt.Errorf("--minimum-blocks must be at least 2")
	}
	baseline, err := parseSingleClient(baselineName)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	candidate, err := parseSingleClient(candidateName)
	if err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	if baseline == candidate {
		return fmt.Errorf("baseline and candidate clients must differ")
	}
	artifacts, exclusions, err := loadSequentialArtifacts(artifactRoot)
	if err != nil {
		return err
	}
	report, err := buildSummaryReport(artifacts, exclusions, baseline, candidate, minimumBlocks, seed, resamples)
	if err != nil {
		return err
	}
	execution, err := loadSummaryExecutionContext(artifactRoot, "sequential")
	if err != nil {
		return err
	}
	provenance := buildReportProvenance(provenanceInputs{Artifacts: artifacts, Manifest: execution.Manifest})
	report.Provenance = &provenance
	return printJSON(report)
}

// validateSummaryShaperEvidence fails a summary closed when a run on a shaped
// server link cannot prove, from the shaper's own before/after counters, that
// the link was in force and carried the bytes the artifact claims.
func validateSummaryShaperEvidence(artifact benchmark.QueueArtifact, link benchmark.ServerLinkProfile) error {
	if !link.Shaped() {
		return nil
	}
	if artifact.ShaperBefore == nil || artifact.ShaperAfter == nil {
		return fmt.Errorf("shaped artifact %s lacks shaper attestations", artifact.SuiteID)
	}
	if err := artifact.ShaperBefore.ValidateFor(link); err != nil {
		return fmt.Errorf("shaped artifact %s before snapshot: %w", artifact.SuiteID, err)
	}
	if err := artifact.ShaperAfter.ValidateFor(link); err != nil {
		return fmt.Errorf("shaped artifact %s after snapshot: %w", artifact.SuiteID, err)
	}
	delivered, err := benchmark.ValidateShaperSnapshotPair(*artifact.ShaperBefore, *artifact.ShaperAfter)
	if err != nil {
		return fmt.Errorf("shaped artifact %s snapshot pair: %w", artifact.SuiteID, err)
	}
	if delivered == 0 || delivered != artifact.ShaperDownstreamBytes {
		return fmt.Errorf("shaped artifact %s has invalid shaper byte evidence", artifact.SuiteID)
	}
	return nil
}

// validateSummaryStorageEvidence fails a summary closed when a published NFS
// run cannot prove its shaped link, and when a local run carries storage
// evidence it should never have had.
func validateSummaryStorageEvidence(artifact benchmark.QueueArtifact, profile benchmark.StorageProfile) error {
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("sequential artifact %s has an invalid storage profile: %w", artifact.SuiteID, err)
	}
	if profile.Kind == benchmark.StorageLocal {
		if artifact.StorageAttestation != nil {
			return fmt.Errorf("local-storage artifact %s unexpectedly carries an NFS attestation", artifact.SuiteID)
		}
		return nil
	}
	if artifact.StorageAttestation == nil {
		return fmt.Errorf("storage-shaped artifact %s lacks its NFS attestation", artifact.SuiteID)
	}
	if artifact.StorageAttestation.Profile != profile {
		return fmt.Errorf("storage attestation for %s does not describe the planned storage profile", artifact.SuiteID)
	}
	if err := artifact.StorageAttestation.Validate(); err != nil {
		return fmt.Errorf("storage-shaped artifact %s: %w", artifact.SuiteID, err)
	}
	return nil
}

func parseSingleClient(value string) (benchmark.Client, error) {
	clients, err := parseClients(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if len(clients) != 1 {
		return "", fmt.Errorf("expected exactly one client")
	}
	return clients[0], nil
}

func loadSequentialArtifacts(root string) ([]benchmark.QueueArtifact, []benchmark.ClientExclusion, error) {
	execution, err := loadSummaryExecutionContext(root, "sequential")
	if err != nil {
		return nil, nil, err
	}
	plannedRuns := execution.PlannedRuns
	seenRuns := make(map[string]bool, len(plannedRuns))
	var artifacts []benchmark.QueueArtifact
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "queue.json" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read summary artifact %s: %w", path, err)
		}
		var artifact benchmark.QueueArtifact
		if err := json.Unmarshal(contents, &artifact); err != nil {
			return fmt.Errorf("decode summary artifact %s: %w", path, err)
		}
		if artifact.SubmissionMode == benchmark.SubmissionModeSequential {
			for _, run := range artifact.Runs {
				if seenRuns[run.ID] {
					return fmt.Errorf("duplicate planned run %s", run.ID)
				}
				seenRuns[run.ID] = true
				planned, ok := plannedRuns[run.ID]
				if !ok || planned != run {
					return fmt.Errorf("sequential artifact %s is not bound to the snapshotted plan", path)
				}
			}
			if !summarizableSequentialStatus(artifact.Status) {
				return fmt.Errorf("sequential artifact %s is not publishable: status=%s error=%s", path, artifact.Status, artifact.Error)
			}
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(artifacts) == 0 {
		return nil, nil, fmt.Errorf("artifact root %s contains no passed sequential queue artifacts", root)
	}
	if len(seenRuns) != len(plannedRuns) {
		return nil, nil, fmt.Errorf("incomplete execution: found %d of %d planned runs; missing runs are not DNFs and full-corpus comparisons are withheld", len(seenRuns), len(plannedRuns))
	}
	return artifacts, execution.Exclusions, nil
}

// summarizableSequentialStatus admits the two statuses that describe a client
// outcome: "passed" (verified output) and "completed_with_dnf" (the client
// reached a terminal failure or its output failed verification). "failed"
// means the harness itself could not run the suite and stays inadmissible.
func summarizableSequentialStatus(status string) bool {
	return status == "passed" || status == "completed_with_dnf"
}

// loadSummaryExecutionContext binds an artifact root to the command that
// produced it, its snapshotted plan and its adapter catalog, refusing a root
// whose snapshots no longer match the immutable manifest.
func loadSummaryExecutionContext(root, command string) (summaryExecutionContext, error) {
	manifestPath := filepath.Join(root, "execution-manifest.json")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		return summaryExecutionContext{}, fmt.Errorf("read summary execution manifest: %w", err)
	}
	var manifest executionManifest
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return summaryExecutionContext{}, fmt.Errorf("decode summary execution manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.ExecutionTarget == "" || manifest.Profile == "" || len(manifest.ExecutableSHA256) != 64 {
		return summaryExecutionContext{}, fmt.Errorf("summary execution manifest has unsupported or incomplete provenance")
	}
	if manifest.Command != command {
		return summaryExecutionContext{}, fmt.Errorf("summary execution manifest was written by %q, want %q", manifest.Command, command)
	}
	if manifest.PlanSnapshotPath != "plan.snapshot.json" || manifest.AdapterSnapshot != "adapter-catalog.snapshot.json" {
		return summaryExecutionContext{}, fmt.Errorf("summary execution manifest uses unexpected snapshot paths")
	}

	planSnapshot := filepath.Join(root, manifest.PlanSnapshotPath)
	adapterSnapshot := filepath.Join(root, manifest.AdapterSnapshot)
	planDigest, err := sha256File(planSnapshot)
	if err != nil {
		return summaryExecutionContext{}, fmt.Errorf("hash snapshotted plan: %w", err)
	}
	adapterDigest, err := sha256File(adapterSnapshot)
	if err != nil {
		return summaryExecutionContext{}, fmt.Errorf("hash snapshotted adapter catalog: %w", err)
	}
	if planDigest != manifest.PlanSHA256 || adapterDigest != manifest.AdapterSHA256 {
		return summaryExecutionContext{}, fmt.Errorf("summary execution snapshot digest does not match immutable manifest")
	}
	planned, err := benchmark.LoadPlan(planSnapshot)
	if err != nil {
		return summaryExecutionContext{}, fmt.Errorf("load snapshotted plan: %w", err)
	}
	target := benchmark.ExecutionTarget(manifest.ExecutionTarget)
	if planned.Profile != manifest.Profile {
		return summaryExecutionContext{}, fmt.Errorf("execution manifest profile %q does not match snapshotted plan %q", manifest.Profile, planned.Profile)
	}
	catalog, err := benchmark.LoadAdapterCatalog(adapterSnapshot)
	if err != nil {
		return summaryExecutionContext{}, fmt.Errorf("load snapshotted adapter catalog: %w", err)
	}
	if err := catalog.ValidateFor(planned, target); err != nil {
		return summaryExecutionContext{}, fmt.Errorf("validate snapshotted adapter catalog: %w", err)
	}
	plannedRuns := make(map[string]benchmark.Run)
	for _, run := range planned.Runs {
		if run.ExecutionTarget == target {
			plannedRuns[run.ID] = run
		}
	}
	if len(plannedRuns) == 0 {
		return summaryExecutionContext{}, fmt.Errorf("snapshotted plan has no runs for execution target %q", target)
	}
	return summaryExecutionContext{Command: manifest.Command, Manifest: manifest, InterleavedPasses: planned.InterleavedPasses, PlannedRuns: plannedRuns, Exclusions: planned.ClientExclusions}, nil
}

func buildSummaryReport(artifacts []benchmark.QueueArtifact, exclusions []benchmark.ClientExclusion, baseline, candidate benchmark.Client, minimumBlocks int, seed int64, resamples int) (summaryReport, error) {
	groups := make(map[comparisonStratum]map[int]*comparisonBlock)
	workloads := make(map[comparisonStratum]string)
	infrastructures := make(map[comparisonStratum]string)
	identities := make(map[summaryProductKey]summaryProductIdentity)
	type aggregateProductKey struct {
		Stratum aggregateStratum
		Client  benchmark.Client
	}
	aggregateProducts := make(map[aggregateProductKey]summaryProductIdentity)
	cpuAccounts := make(map[summaryProductKey]*counterAccount)
	rssAccounts := make(map[summaryProductKey]*counterAccount)
	deviceWriteAccounts := make(map[summaryProductKey]*counterAccount)
	cpuCaveats := make(map[comparisonStratum]map[string]bool)
	transfers := make(map[summaryProductKey]*transferAccount)
	classes := make(map[string]fixture.FixtureClass)
	cases := make(map[string]fixture.ArchiveCase)
	names := make(map[string]string)
	encodings := make(map[string]fixture.PostEncoding)
	// One summary describes one storage stratum. Local and NFS runs answer
	// different questions, so a directory holding both is an operator mistake
	// and is refused rather than silently split into two comparisons that look
	// like one report.
	var storageProfile *benchmark.StorageProfile
	for _, artifact := range artifacts {
		if err := artifact.ValidateEvidence(); err != nil {
			return summaryReport{}, fmt.Errorf("artifact %s: %w", artifact.SuiteID, err)
		}
		if artifact.SchemaVersion != 8 {
			return summaryReport{}, fmt.Errorf("summary input %s uses queue artifact schema %d, want 8", artifact.SuiteID, artifact.SchemaVersion)
		}
		if !summarizableSequentialStatus(artifact.Status) || artifact.SubmissionMode != benchmark.SubmissionModeSequential {
			return summaryReport{}, fmt.Errorf("summary input contains a non-passed sequential artifact %s", artifact.SuiteID)
		}
		if artifact.AdapterResult == nil || artifact.AdapterResult.SchemaVersion != 7 {
			return summaryReport{}, fmt.Errorf("sequential artifact %s lacks queue adapter result schema 7", artifact.SuiteID)
		}
		if len(artifact.Jobs) != 1 {
			return summaryReport{}, fmt.Errorf("sequential artifact %s contains %d jobs, want exactly one", artifact.SuiteID, len(artifact.Jobs))
		}
		job := artifact.Jobs[0]
		if len(artifact.Runs) != 1 || artifact.Runs[0] != job.Run || artifact.AdapterResult.SuiteID != artifact.SuiteID || len(artifact.AdapterResult.Jobs) != 1 || artifact.AdapterResult.Jobs[0].RunID != job.Run.ID || !reflect.DeepEqual(artifact.AdapterResult.Jobs[0], job.AdapterResult) {
			return summaryReport{}, fmt.Errorf("sequential artifact %s has inconsistent run or adapter-result identity", artifact.SuiteID)
		}
		if artifact.AdapterResult.Client != job.Run.Client || artifact.AdapterResult.ArchiveToolchain != job.Run.ArchiveToolchain || artifact.AdapterResult.ExecutionTarget != job.Run.ExecutionTarget || artifact.AdapterResult.Transport != job.Run.Transport || artifact.AdapterResult.TLSValidation != job.Run.TLSValidation || artifact.AdapterResult.TransportLabel != job.Run.TransportLabel || artifact.AdapterResult.ServerLink != job.Run.ServerLink || artifact.AdapterResult.StorageProfile != job.Run.StorageProfile || artifact.AdapterResult.ArticleProfile != job.Run.ArticleProfile {
			return summaryReport{}, fmt.Errorf("sequential artifact %s has adapter metadata inconsistent with its planned run", artifact.SuiteID)
		}
		if err := validateSummaryShaperEvidence(artifact, job.Run.ServerLink); err != nil {
			return summaryReport{}, err
		}
		if err := validateSummaryStorageEvidence(artifact, job.Run.StorageProfile); err != nil {
			return summaryReport{}, err
		}
		if storageProfile == nil {
			profile := job.Run.StorageProfile
			storageProfile = &profile
		} else if *storageProfile != job.Run.StorageProfile {
			return summaryReport{}, fmt.Errorf("summary inputs mix storage profiles %q and %q; summarize each storage profile separately",
				storageProfile.ID, job.Run.StorageProfile.ID)
		}
		if job.Run.Client != baseline && job.Run.Client != candidate {
			continue
		}
		didNotFinish := artifact.Status == "completed_with_dnf"
		if didNotFinish {
			if job.Outcome != "dnf" || job.Error == "" {
				return summaryReport{}, fmt.Errorf("sequential artifact %s reports did-not-finish without a recorded job failure", artifact.SuiteID)
			}
		} else {
			if job.Outcome != "completed" || job.Verification == nil || job.AdapterResult.SubmissionToTerminalNanoseconds <= 0 {
				return summaryReport{}, fmt.Errorf("sequential artifact %s contains an unverified or invalid measurement", artifact.SuiteID)
			}
			if !benchmark.ObservationUncertaintyAcceptable(job.AdapterResult.TerminalObservationUncertainty, job.AdapterResult.SubmissionToTerminalNanoseconds) {
				return summaryReport{}, fmt.Errorf("sequential artifact %s exceeds the terminal-observation uncertainty limit (%s)", artifact.SuiteID, benchmark.ObservationUncertaintyRule)
			}
		}
		stratum := comparisonStratum{
			FixtureID:        job.Run.FixtureID,
			Profile:          job.Run.Profile,
			ExecutionTarget:  job.Run.ExecutionTarget,
			Transport:        job.Run.Transport,
			ArchiveToolchain: job.Run.ArchiveToolchain,
			ServerLinkID:     job.Run.ServerLink.ID,
			ServerEgressBPS:  job.Run.ServerLink.EgressBitsPerSecond,
			ServerBurstBytes: job.Run.ServerLink.BurstBytes,
			ServerRTTMicros:  job.Run.ServerLink.RTTMicros,
			StorageProfileID: job.Run.StorageProfile.ID,
			StorageNFSLinkID: job.Run.StorageProfile.NFSLinkID,
			StorageLinkBPS:   job.Run.StorageProfile.LinkBitsPerSecond,
			StorageRTTMicros: job.Run.StorageProfile.RTTMicros,
			ArticleProfileID: job.Run.ArticleProfile.ID,
			ArticleRawBytes:  job.Run.ArticleProfile.RawBytes,
		}
		if previous, ok := workloads[stratum]; ok && previous != job.WorkloadSHA256 {
			return summaryReport{}, fmt.Errorf("fixture %s mixes immutable workload identities", job.Run.FixtureID)
		}
		workloads[stratum] = job.WorkloadSHA256
		infrastructure := artifact.InfrastructureDigest()
		if previous, ok := infrastructures[stratum]; ok && previous != infrastructure {
			return summaryReport{}, fmt.Errorf("fixture %s mixes infrastructure identities", job.Run.FixtureID)
		}
		infrastructures[stratum] = infrastructure
		if len(artifact.AdapterResult.RenderedConfigSHA256) != 64 {
			return summaryReport{}, fmt.Errorf("sequential artifact %s lacks a rendered-config SHA-256", artifact.SuiteID)
		}
		// The class comes from the fixture manifest through the artifact; an
		// artifact without one was run over a corpus that predates fixture
		// classes and cannot be placed in either aggregate.
		if !job.FixtureClass.Valid() {
			return summaryReport{}, fmt.Errorf("sequential artifact %s carries fixture class %q for %s, want %q or %q; regenerate the corpus and rerun", artifact.SuiteID, job.FixtureClass, job.Run.FixtureID, fixture.HeadlineFixtureClass, fixture.BreadthFixtureClass)
		}
		if previous, ok := classes[job.Run.FixtureID]; ok && previous != job.FixtureClass {
			return summaryReport{}, fmt.Errorf("fixture %s is recorded as both %q and %q across artifacts", job.Run.FixtureID, previous, job.FixtureClass)
		}
		classes[job.Run.FixtureID] = job.FixtureClass
		if previous, ok := cases[job.Run.FixtureID]; ok && previous != job.Workload.Manifest.Case {
			return summaryReport{}, fmt.Errorf("fixture %s changed its declared workload axes", job.Run.FixtureID)
		}
		cases[job.Run.FixtureID] = job.Workload.Manifest.Case
		names[job.Run.FixtureID] = job.Workload.Manifest.Case.DisplayName()
		// Encoding is a label, not a key: a uuencoded fixture is its own
		// fixture and so already has its own stratum. Carrying it here is what
		// lets a reader see "this client did not finish, and the post was
		// uuencoded" without going back to the corpus.
		encoding := job.Encoding
		if encoding == "" {
			encoding = fixture.YEncEncoding
		}
		if previous, ok := encodings[job.Run.FixtureID]; ok && previous != encoding {
			return summaryReport{}, fmt.Errorf("fixture %s is recorded as both %q and %q encoded across artifacts", job.Run.FixtureID, previous, encoding)
		}
		encodings[job.Run.FixtureID] = encoding
		productKey := summaryProductKey{Stratum: stratum, Client: job.Run.Client}
		identity := summaryProductIdentity{
			ClientIdentity:           artifact.AdapterResult.ClientIdentity,
			ClientVersion:            artifact.AdapterResult.ClientVersion,
			ArchiveToolchainIdentity: artifact.AdapterResult.ArchiveToolchainIdentity,
			RenderedConfigSHA256:     artifact.AdapterResult.RenderedConfigSHA256,
			TLSValidation:            job.Run.TLSValidation,
			TransportLabel:           job.Run.TransportLabel,
		}
		if identity.ClientIdentity == "" || identity.ClientVersion == "" || identity.ArchiveToolchainIdentity == "" {
			return summaryReport{}, fmt.Errorf("sequential artifact %s lacks product identity evidence", artifact.SuiteID)
		}
		if previous, ok := identities[productKey]; ok && previous != identity {
			return summaryReport{}, fmt.Errorf("product identity or TLS policy changed within stratum %+v for client %s", stratum, job.Run.Client)
		}
		identities[productKey] = identity
		// Fixture-specific paths/passwords legitimately alter rendered config,
		// but changing the actual client/helper build between fixtures cannot
		// form one class-level comparison of those products.
		aggregateIdentity := identity
		aggregateIdentity.RenderedConfigSHA256 = ""
		aggregateProduct := aggregateProductKey{stratum.aggregateKey(job.FixtureClass), job.Run.Client}
		if previous, ok := aggregateProducts[aggregateProduct]; ok && previous != aggregateIdentity {
			return summaryReport{}, fmt.Errorf("product build or TLS policy changed across fixtures in aggregate for client %s", job.Run.Client)
		}
		aggregateProducts[aggregateProduct] = aggregateIdentity
		blocks := groups[stratum]
		if blocks == nil {
			blocks = make(map[int]*comparisonBlock)
			groups[stratum] = blocks
		}
		block := blocks[job.Run.Repetition]
		if block == nil {
			block = &comparisonBlock{}
			blocks[job.Run.Repetition] = block
		}
		measurement := float64(job.AdapterResult.SubmissionToTerminalNanoseconds)
		// The secondary counters ride along with a finished run only: a run
		// that did not finish has no wall clock to pair either.
		var cpuValue, peakRSSValue, deviceWriteValue *float64
		if !didNotFinish {
			value, provenance, reason := counterObservation(job.AdapterResult.ResourceMetrics, cpuTimeMetric)
			account := cpuAccounts[productKey]
			if account == nil {
				account = newCounterAccount()
				cpuAccounts[productKey] = account
			}
			if value != nil {
				account.measured[provenance]++
			} else {
				account.unavailable++
				account.reasons[reason] = true
			}
			cpuValue = value

			rssValue, rssProvenance, rssReason := counterObservation(job.AdapterResult.ResourceMetrics, peakRSSMetric)
			rssAccount := rssAccounts[productKey]
			if rssAccount == nil {
				rssAccount = newCounterAccount()
				rssAccounts[productKey] = rssAccount
			}
			if rssValue != nil {
				rssAccount.measured[rssProvenance]++
			} else {
				rssAccount.unavailable++
				rssAccount.reasons[rssReason] = true
			}
			peakRSSValue = rssValue

			writeValue, writeProvenance, writeReason := counterObservation(job.AdapterResult.ResourceMetrics, deviceWriteMetric)
			writeAccount := deviceWriteAccounts[productKey]
			if writeAccount == nil {
				writeAccount = newCounterAccount()
				deviceWriteAccounts[productKey] = writeAccount
			}
			if writeValue != nil {
				writeAccount.measured[writeProvenance]++
			} else {
				writeAccount.unavailable++
				writeAccount.reasons[writeReason] = true
			}
			deviceWriteValue = writeValue

			if artifact.StorageAttestation != nil && artifact.StorageAttestation.CPUAccountingCaveat != "" {
				caveats := cpuCaveats[stratum]
				if caveats == nil {
					caveats = make(map[string]bool)
					cpuCaveats[stratum] = caveats
				}
				caveats[artifact.StorageAttestation.CPUAccountingCaveat] = true
			}
			// Bytes ride along with finished shaped runs only: a run that did
			// not finish pulled some unknown fraction of the job.
			if artifact.ShaperDownstreamBytes > 0 {
				account := transfers[productKey]
				if account == nil {
					account = &transferAccount{}
					transfers[productKey] = account
				}
				account.bytes = append(account.bytes, artifact.ShaperDownstreamBytes)
				if census := artifact.ShaperArticleCensus; census != nil {
					account.census++
					account.requests += census.ArticleRequests
					account.distinct += census.DistinctArticleRequests
					account.repeated += census.RepeatedArticleRequests
				}
			}
		}
		if job.Run.Client == baseline {
			if block.baseline != nil || block.baselineDNF {
				return summaryReport{}, fmt.Errorf("duplicate baseline observation for %+v repetition %d", stratum, job.Run.Repetition)
			}
			if didNotFinish {
				block.baselineDNF = true
			} else {
				block.baseline = &measurement
				block.baselineUncertainty = float64(job.AdapterResult.TerminalObservationUncertainty)
				block.baselineCPU = cpuValue
				block.baselinePeakRSS = peakRSSValue
				block.baselineDeviceWrite = deviceWriteValue
			}
		} else {
			if block.candidate != nil || block.candidateDNF {
				return summaryReport{}, fmt.Errorf("duplicate candidate observation for %+v repetition %d", stratum, job.Run.Repetition)
			}
			if didNotFinish {
				block.candidateDNF = true
			} else {
				block.candidate = &measurement
				block.candidateUncertainty = float64(job.AdapterResult.TerminalObservationUncertainty)
				block.candidateCPU = cpuValue
				block.candidatePeakRSS = peakRSSValue
				block.candidateDeviceWrite = deviceWriteValue
			}
		}
	}

	strata := make([]comparisonStratum, 0, len(groups))
	for stratum := range groups {
		strata = append(strata, stratum)
	}
	sort.Slice(strata, func(left, right int) bool { return fmt.Sprint(strata[left]) < fmt.Sprint(strata[right]) })
	report := summaryReport{
		SchemaVersion: 7,
		Metric:        benchmark.PrimaryMetric,
		Baseline:      baseline,
		Candidate:     candidate,
		MinimumBlocks: minimumBlocks,
		Comparisons:   make([]stratifiedComparison, 0, len(strata)),
	}
	aggregates := make(map[aggregateStratum]*aggregateAccount)
	for _, stratum := range strata {
		class := classes[stratum.FixtureID]
		account := aggregates[stratum.aggregateKey(class)]
		if account == nil {
			account = &aggregateAccount{samples: make(map[string][]benchmark.PairedSample)}
			aggregates[stratum.aggregateKey(class)] = account
		}
		blocks := groups[stratum]
		repetitions := make([]int, 0, len(blocks))
		for repetition := range blocks {
			repetitions = append(repetitions, repetition)
		}
		sort.Ints(repetitions)
		samples := make([]benchmark.PairedSample, 0, len(repetitions))
		completion := completionCounts{BlocksObserved: len(repetitions)}
		// A client the plan excluded on this fixture has no artifact in any
		// block. Every block then counts as that client not finishing, by the
		// plan's own record rather than by observation.
		baselineExclusion, baselineExcluded := benchmark.ClientExclusionFor(exclusions, baseline, stratum.FixtureID)
		candidateExclusion, candidateExcluded := benchmark.ClientExclusionFor(exclusions, candidate, stratum.FixtureID)
		for _, repetition := range repetitions {
			block := blocks[repetition]
			if block.baseline == nil && !block.baselineDNF && baselineExcluded {
				block.baselineDNF = true
				completion.BaselineExcluded++
			}
			if block.candidate == nil && !block.candidateDNF && candidateExcluded {
				block.candidateDNF = true
				completion.CandidateExcluded++
			}
			if block.baselineDNF {
				completion.BaselineDidNotFinish++
			}
			if block.candidateDNF {
				completion.CandidateDidNotFinish++
			}
			if (block.baseline == nil && !block.baselineDNF) || (block.candidate == nil && !block.candidateDNF) {
				// A block with neither a measurement nor a recorded failure for a
				// client is a run that never happened: an aborted pass, not a
				// client outcome.
				return summaryReport{}, fmt.Errorf("incomplete client pair for %+v repetition %d", stratum, repetition)
			}
			if block.baseline == nil || block.candidate == nil {
				continue
			}
			samples = append(samples, benchmark.PairedSample{Baseline: *block.baseline, Candidate: *block.candidate})
		}
		completion.PairedBlocks = len(samples)
		comparison := stratifiedComparison{Stratum: stratum, FixtureClass: class, Encoding: encodings[stratum.FixtureID], Completion: completion}
		comparison.FixtureName = names[stratum.FixtureID]
		comparison.DisplayName = comparisonDisplayName(comparison.FixtureName, stratum)
		comparison.WorkloadSHA256 = workloads[stratum]
		comparison.InfrastructureSHA256 = infrastructures[stratum]
		comparison.TimingPrecision = assessTiming(blocks, repetitions, seed, resamples, completion)
		for _, client := range []benchmark.Client{baseline, candidate} {
			if identity, ok := identities[summaryProductKey{Stratum: stratum, Client: client}]; ok {
				comparison.TransportPolicies = append(comparison.TransportPolicies, clientTransportPolicy{Client: client, TLSValidation: identity.TLSValidation, TransportLabel: identity.TransportLabel})
			}
		}
		if baselineExcluded {
			comparison.ClientExclusions = append(comparison.ClientExclusions, baselineExclusion)
		}
		if candidateExcluded {
			comparison.ClientExclusions = append(comparison.ClientExclusions, candidateExclusion)
		}
		cpuTime, err := buildCPUTimeComparison(stratum, blocks, repetitions, cpuAccounts, cpuCaveats[stratum], baseline, candidate, minimumBlocks, seed, resamples)
		if err != nil {
			return summaryReport{}, fmt.Errorf("summarize CPU time for stratum %+v: %w", stratum, err)
		}
		comparison.CPUTime = cpuTime
		// Peak RSS carries no storage caveat of its own: the NFS caveat is
		// about kernel CPU spent outside the container, which does not change
		// what the client had resident.
		peakRSS, err := buildPeakRSSComparison(stratum, blocks, repetitions, rssAccounts, nil, baseline, candidate, minimumBlocks, seed, resamples)
		if err != nil {
			return summaryReport{}, fmt.Errorf("summarize peak RSS for stratum %+v: %w", stratum, err)
		}
		comparison.PeakRSS = peakRSS
		// Device writes carry the storage caveat for the same reason CPU time
		// does not carry it here: on an NFS profile the bytes leave the
		// container as network traffic and the cgroup's block-io counter sees
		// none of them, so the figure means something different and says so.
		deviceWrites, err := buildDeviceWriteComparison(stratum, blocks, repetitions, deviceWriteAccounts, deviceWriteCaveats(storageProfile), baseline, candidate, minimumBlocks, seed, resamples)
		if err != nil {
			return summaryReport{}, fmt.Errorf("summarize device writes for stratum %+v: %w", stratum, err)
		}
		comparison.DeviceWrites = deviceWrites
		comparison.Transfer = buildTransferEvidence(stratum, transfers, baseline, candidate)
		if len(samples) < minimumBlocks {
			if completion.BaselineDidNotFinish == 0 && completion.CandidateDidNotFinish == 0 {
				return summaryReport{}, fmt.Errorf("stratum %+v has %d complete blocks, want at least %d", stratum, len(samples), minimumBlocks)
			}
			comparison.ComparisonWithheld = fmt.Sprintf("%d paired blocks, want at least %d: %s did not finish %d of %d blocks (%d excluded by the plan), %s did not finish %d of %d (%d excluded by the plan)",
				len(samples), minimumBlocks, baseline, completion.BaselineDidNotFinish, completion.BlocksObserved, completion.BaselineExcluded, candidate, completion.CandidateDidNotFinish, completion.BlocksObserved, completion.CandidateExcluded)
			report.Comparisons = append(report.Comparisons, comparison)
			account.withheld = append(account.withheld, stratum.FixtureID)
			continue
		}

		summary, err := benchmark.SummarizePaired(samples, seed, resamples)
		if err != nil {
			return summaryReport{}, fmt.Errorf("summarize stratum %+v: %w", stratum, err)
		}
		comparison.Summary = &summary
		report.Comparisons = append(report.Comparisons, comparison)
		if completion.BaselineDidNotFinish > 0 || completion.CandidateDidNotFinish > 0 {
			account.withheld = append(account.withheld, stratum.FixtureID)
			continue
		}
		account.samples[stratum.FixtureID] = samples
	}
	if len(report.Comparisons) == 0 {
		return summaryReport{}, fmt.Errorf("no strata contain either requested client")
	}
	classAggregates, err := buildClassAggregates(aggregates, seed, resamples)
	if err != nil {
		return summaryReport{}, err
	}
	report.Aggregates = classAggregates
	report.Subgroups, err = buildClassAggregates(subgroupAccounts(aggregates, cases), seed, resamples)
	if err != nil {
		return summaryReport{}, err
	}
	return report, nil
}

// buildClassAggregates pools each class stratum's per-fixture samples into one
// equal-weight figure, or withholds it naming the fixtures that had no
// comparison of their own.
func buildClassAggregates(accounts map[aggregateStratum]*aggregateAccount, seed int64, resamples int) ([]classAggregate, error) {
	keys := make([]aggregateStratum, 0, len(accounts))
	for key := range accounts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return fmt.Sprint(keys[left]) < fmt.Sprint(keys[right]) })
	result := make([]classAggregate, 0, len(keys))
	for _, key := range keys {
		account := accounts[key]
		aggregate := classAggregate{Weighting: "coverage_balanced_equal_fixture_weight_not_market_representative", Stratum: key, FixturesCompared: make([]string, 0, len(account.samples))}
		for fixtureID := range account.samples {
			aggregate.FixturesCompared = append(aggregate.FixturesCompared, fixtureID)
		}
		sort.Strings(aggregate.FixturesCompared)
		if len(account.withheld) > 0 {
			sort.Strings(account.withheld)
			aggregate.FixturesWithheld = account.withheld
			aggregate.AggregateWithheld = fmt.Sprintf("%d of %d %s fixtures had their comparison withheld (%s); a class figure over the remaining fixtures would hide that result",
				len(account.withheld), len(account.withheld)+len(account.samples), key.FixtureClass, strings.Join(account.withheld, ", "))
			result = append(result, aggregate)
			continue
		}
		summary, err := benchmark.SummarizeAcrossFixtures(account.samples, seed, resamples)
		if err != nil {
			return nil, fmt.Errorf("aggregate %s fixtures for %+v: %w", key.FixtureClass, key, err)
		}
		aggregate.Summary = &summary
		result = append(result, aggregate)
	}
	return result, nil
}

// buildTransferEvidence reports each client's shaped downstream bytes and
// article census over its finished blocks, baseline first.
func buildTransferEvidence(stratum comparisonStratum, transfers map[summaryProductKey]*transferAccount, baseline, candidate benchmark.Client) []clientTransferEvidence {
	var evidence []clientTransferEvidence
	for _, client := range []benchmark.Client{baseline, candidate} {
		account := transfers[summaryProductKey{Stratum: stratum, Client: client}]
		if account == nil || len(account.bytes) == 0 {
			continue
		}
		sorted := append([]uint64(nil), account.bytes...)
		sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
		entry := clientTransferEvidence{
			Client:                client,
			FinishedBlocks:        len(sorted),
			DownstreamBytesMin:    sorted[0],
			DownstreamBytesMedian: sorted[len(sorted)/2],
			DownstreamBytesMax:    sorted[len(sorted)-1],
		}
		if account.census > 0 {
			entry.ArticleCensus = &transferArticleCensus{
				Blocks:                  account.census,
				ArticleRequests:         account.requests,
				DistinctArticleRequests: account.distinct,
				RepeatedArticleRequests: account.repeated,
			}
		}
		evidence = append(evidence, entry)
	}
	return evidence
}

// buildCPUTimeComparison pairs the two clients' CPU counters over a stratum's
// blocks.
func buildCPUTimeComparison(stratum comparisonStratum, blocks map[int]*comparisonBlock, repetitions []int, accounts map[summaryProductKey]*counterAccount, caveats map[string]bool, baseline, candidate benchmark.Client, minimumBlocks int, seed int64, resamples int) (counterComparison, error) {
	return buildCounterComparison(cpuTimeMetric, "CPU", "CPU time", stratum, blocks, repetitions, accounts, caveats,
		func(block *comparisonBlock) (*float64, *float64) { return block.baselineCPU, block.candidateCPU },
		baseline, candidate, minimumBlocks, seed, resamples)
}

// buildPeakRSSComparison pairs the two clients' peak resident memory over a
// stratum's blocks. The sampled counter is the one compared, never the
// platform high-water hint beside it: the hint means a different thing on
// each host, so pairing two of them would pair two different quantities.
// buildDeviceWriteComparison pairs the two clients' device-write counters over
// a stratum's blocks, on the same terms as the other two secondary counters.
func buildDeviceWriteComparison(stratum comparisonStratum, blocks map[int]*comparisonBlock, repetitions []int, accounts map[summaryProductKey]*counterAccount, caveats map[string]bool, baseline, candidate benchmark.Client, minimumBlocks int, seed int64, resamples int) (counterComparison, error) {
	return buildCounterComparison(deviceWriteMetric, "device writes", "bytes written to device", stratum, blocks, repetitions, accounts, caveats,
		func(block *comparisonBlock) (*float64, *float64) {
			return block.baselineDeviceWrite, block.candidateDeviceWrite
		}, baseline, candidate, minimumBlocks, seed, resamples)
}

// deviceWriteCaveats states what an NFS storage profile does to the counter:
// the payload leaves the container over the network, so the cgroup's block-io
// accounting does not see it and the figure is not comparable with a local
// profile's.
func deviceWriteCaveats(profile *benchmark.StorageProfile) map[string]bool {
	if profile == nil || profile.Kind != benchmark.StorageNFS {
		return nil
	}
	return map[string]bool{
		"storage profile is NFS: the payload leaves the container as network traffic, so the container's block-io counter does not account for it and this figure is not comparable with a local-storage lane": true,
	}
}

func buildPeakRSSComparison(stratum comparisonStratum, blocks map[int]*comparisonBlock, repetitions []int, accounts map[summaryProductKey]*counterAccount, caveats map[string]bool, baseline, candidate benchmark.Client, minimumBlocks int, seed int64, resamples int) (counterComparison, error) {
	return buildCounterComparison(peakRSSMetric, "peak RSS", "peak RSS", stratum, blocks, repetitions, accounts, caveats,
		func(block *comparisonBlock) (*float64, *float64) {
			return block.baselinePeakRSS, block.candidatePeakRSS
		},
		baseline, candidate, minimumBlocks, seed, resamples)
}

// buildCounterComparison pairs one secondary counter over a stratum's blocks.
// It withholds rather than fails: these counters are secondary evidence, and a
// lane that could not collect one has already said so in the artifact.
func buildCounterComparison(metric, label, longLabel string, stratum comparisonStratum, blocks map[int]*comparisonBlock, repetitions []int, accounts map[summaryProductKey]*counterAccount, caveats map[string]bool, sample func(*comparisonBlock) (*float64, *float64), baseline, candidate benchmark.Client, minimumBlocks int, seed int64, resamples int) (counterComparison, error) {
	comparison := counterComparison{Metric: metric}
	for caveat := range caveats {
		comparison.Caveats = append(comparison.Caveats, caveat)
	}
	sort.Strings(comparison.Caveats)

	var withheld []string
	var compatible *counterProvenance
	scopes := make(map[benchmark.Client]string)
	for _, client := range []benchmark.Client{baseline, candidate} {
		accounting := clientCounterAccounting{Client: client}
		if account := accounts[summaryProductKey{Stratum: stratum, Client: client}]; account != nil {
			accounting.UnavailableBlocks = account.unavailable
			for reason := range account.reasons {
				accounting.UnavailableReasons = append(accounting.UnavailableReasons, reason)
			}
			sort.Strings(accounting.UnavailableReasons)
			provenances := make([]counterProvenance, 0, len(account.measured))
			for provenance, count := range account.measured {
				provenances = append(provenances, provenance)
				accounting.MeasuredBlocks += count
			}
			sort.Slice(provenances, func(left, right int) bool { return fmt.Sprint(provenances[left]) < fmt.Sprint(provenances[right]) })
			if len(provenances) > 0 {
				first := provenances[0]
				if compatible != nil && *compatible != first {
					withheld = append(withheld, "resource collection windows or collectors differ")
				}
				copy := first
				compatible = &copy
				accounting.Scope, accounting.Collector, accounting.CollectorVersion = first.Scope, first.Collector, first.CollectorVersion
			}
			// One source per client per stratum, like the product identity:
			// two collectors inside one stratum are two measurements wearing
			// one label.
			if len(provenances) > 1 {
				withheld = append(withheld, fmt.Sprintf("%s %s accounting source changed within the stratum", client, label))
			}
			if accounting.MeasuredBlocks == 0 {
				withheld = append(withheld, fmt.Sprintf("%s has no measured %s in this stratum", client, longLabel))
			}
		} else {
			withheld = append(withheld, fmt.Sprintf("%s finished no block in this stratum", client))
		}
		scopes[client] = accounting.Scope
		comparison.Accounting = append(comparison.Accounting, accounting)
	}
	if scopes[baseline] != "" && scopes[candidate] != "" && scopes[baseline] != scopes[candidate] {
		withheld = append(withheld, fmt.Sprintf("scopes differ: %s measured %s, %s measured %s; a process counter and a container counter are different quantities",
			baseline, scopes[baseline], candidate, scopes[candidate]))
	}

	samples := make([]benchmark.PairedSample, 0, len(repetitions))
	for _, repetition := range repetitions {
		baselineValue, candidateValue := sample(blocks[repetition])
		if baselineValue == nil || candidateValue == nil {
			continue
		}
		samples = append(samples, benchmark.PairedSample{Baseline: *baselineValue, Candidate: *candidateValue})
	}
	comparison.PairedBlocks = len(samples)
	if len(withheld) > 0 {
		comparison.ComparisonWithheld = strings.Join(withheld, "; ")
		return comparison, nil
	}
	// Secondary counters must meet the same predeclared minimum pair count;
	// missing telemetry is not permission to lower the evidence threshold.
	if len(samples) < max(2, minimumBlocks) {
		comparison.ComparisonWithheld = fmt.Sprintf("%d paired %s blocks, need at least %d: %s measured %d and had %d unavailable, %s measured %d and had %d unavailable",
			len(samples),
			label,
			max(2, minimumBlocks),
			baseline, comparison.Accounting[0].MeasuredBlocks, comparison.Accounting[0].UnavailableBlocks,
			candidate, comparison.Accounting[1].MeasuredBlocks, comparison.Accounting[1].UnavailableBlocks)
		return comparison, nil
	}
	summary, err := benchmark.SummarizePaired(samples, seed, resamples)
	if err != nil {
		return counterComparison{}, err
	}
	comparison.Summary = &summary
	return comparison, nil
}
