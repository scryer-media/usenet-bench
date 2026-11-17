package nativeadapter

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// queueConfig is a native config whose NZBs exist on disk, on the target this
// host can validate, so Validate reaches the queue checks.
func queueConfig(t *testing.T, mode benchmark.SubmissionMode, jobs int) Config {
	t.Helper()
	cfg := testConfig(benchmark.Weaver)
	switch runtime.GOOS {
	case "darwin":
		cfg.ExecutionTarget = benchmark.MacOSNative
	case "windows":
		cfg.ExecutionTarget = benchmark.WindowsNative
	default:
		t.Skipf("no native execution target on %s", runtime.GOOS)
	}
	root := t.TempDir()
	cfg.NZBPath = filepath.Join(root, "fixture.nzb")
	if err := os.WriteFile(cfg.NZBPath, []byte("<nzb/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.StartupTimeout = time.Second
	cfg.PollInterval = time.Millisecond
	cfg.JobTimeout = time.Minute
	input := &benchmark.QueueInput{SchemaVersion: 3, SuiteID: "queue-transition-0001", SubmissionMode: mode}
	for index := range jobs {
		input.Jobs = append(input.Jobs, benchmark.QueueInputJob{
			RunID:          fmt.Sprintf("run-%04d", index+1),
			FixtureID:      "direct-mkv-200mb",
			NZBPath:        cfg.NZBPath,
			SubmissionName: fmt.Sprintf("queue-transition-%02d.nzb", index+1),
			ForceAccept:    true,
		})
	}
	cfg.QueueInput = input
	return cfg
}

func TestNativeQueueDrainAcceptsTenJobs(t *testing.T) {
	cfg := queueConfig(t, benchmark.SubmissionModeQueueDrain, 10)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a ten-job native queue drain was refused: %v", err)
	}
}

func TestNativeQueueRefusesAnUnknownSubmissionMode(t *testing.T) {
	cfg := queueConfig(t, benchmark.SubmissionMode("burst"), 2)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "burst") {
		t.Fatalf("err = %v, want an unknown submission mode refused", err)
	}
}

func TestNativeSequentialQueueStillRefusesTwoJobs(t *testing.T) {
	cfg := queueConfig(t, benchmark.SubmissionModeSequential, 2)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one job") {
		t.Fatalf("err = %v, want two sequential jobs refused", err)
	}
}

// The per-job checks still apply to a drain: a missing NZB is refused before
// any client is launched.
func TestNativeQueueDrainStillChecksEachJobsNZB(t *testing.T) {
	cfg := queueConfig(t, benchmark.SubmissionModeQueueDrain, 3)
	cfg.QueueInput.Jobs[2].NZBPath = filepath.Join(t.TempDir(), "missing.nzb")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "run-0003") {
		t.Fatalf("err = %v, want the missing NZB of run-0003 refused", err)
	}
}

func drainMetrics() benchmark.ResourceMetrics {
	return benchmark.ResourceMetrics{
		CPUTimeNanoseconds:   benchmark.MeasuredMeasurement("client_process", "go-os-process-state", "darwin", 123),
		InstructionsRetired:  nativeInstructionMeasurement(),
		PeakRSSBytes:         benchmark.MeasuredMeasurement("client_process_tree", "ps-process-tree-sampled", "darwin@250ms", 65536),
		PeakRSSHighWaterHint: benchmark.MeasuredMeasurement("client_process", "getrusage-ru_maxrss", "darwin", 65536),
		DeviceWriteBytes:     nativeDeviceWriteMeasurement(),
	}
}

func drainJob(runID string, submitted, completed time.Time, status string) benchmark.QueueJobResult {
	accepted := submitted.Add(time.Millisecond)
	lowerBound := completed.Add(-10 * time.Millisecond)
	job := benchmark.QueueJobResult{
		TimingClock:                     "monotonic",
		RunID:                           runID,
		JobID:                           "job-" + runID,
		SubmissionStartedAt:             submitted,
		AcceptedAt:                      accepted,
		QueuedAt:                        accepted,
		CompletionAt:                    completed,
		TerminalObservationLowerBound:   lowerBound,
		TerminalObservedAt:              completed,
		TerminalObservationUncertainty:  completed.Sub(lowerBound).Nanoseconds(),
		SubmissionToTerminalNanoseconds: completed.Sub(submitted).Nanoseconds(),
		FixtureWallClockNanoseconds:     completed.Sub(accepted).Nanoseconds(),
		TerminalStatus:                  status,
		ProcessingTimingError:           "terminal status observed before an active state",
	}
	if status != "succeeded" {
		job.TerminalError = "client reported failure"
	}
	return job
}

// Jobs finish out of submission order, and a refused copy is recorded after
// the accepted ones, so neither the first nor the last job bounds the suite.
func TestNativeQueueDrainResultBoundsTheSuiteOverEveryJob(t *testing.T) {
	cfg := testConfig(benchmark.Weaver)
	cfg.PollInterval = 100 * time.Millisecond
	cfg.QueueInput = &benchmark.QueueInput{SchemaVersion: 3, SuiteID: "queue-transition-0001", SubmissionMode: benchmark.SubmissionModeQueueDrain}
	base := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	earliest := base
	latest := base.Add(40 * time.Second)
	jobs := []benchmark.QueueJobResult{
		// Listed in the order the monitor and the refusal path return them,
		// which is neither submission nor completion order.
		drainJob("run-0002", base.Add(20*time.Millisecond), base.Add(12*time.Second), "succeeded"),
		drainJob("run-0003", base.Add(30*time.Millisecond), latest, "succeeded"),
		drainJob("run-0004", base.Add(40*time.Millisecond), base.Add(25*time.Second), "failed"),
		drainJob("run-0001", earliest, base.Add(30*time.Second), "succeeded"),
	}
	result := nativeQueueDrainResult(cfg, "sha256:client", "stock", strings.Repeat("a", 64), jobs, drainMetrics())
	if !result.QueueStartedAt.Equal(earliest) {
		t.Fatalf("suite started at %s, want the earliest submission %s", result.QueueStartedAt, earliest)
	}
	if !result.QueueCompletedAt.Equal(latest) {
		t.Fatalf("suite completed at %s, want the latest completion %s", result.QueueCompletedAt, latest)
	}
	if result.QueueElapsedNanoseconds != latest.Sub(earliest).Nanoseconds() {
		t.Fatalf("suite elapsed = %d, want %d", result.QueueElapsedNanoseconds, latest.Sub(earliest).Nanoseconds())
	}
	if result.SubmissionMode != benchmark.SubmissionModeQueueDrain || result.SuiteID != "queue-transition-0001" || len(result.Jobs) != 4 {
		t.Fatalf("result does not describe the drain: %#v", result)
	}
	if result.StatusPollIntervalNanos != int64(100*time.Millisecond) || result.ClientVersion != "test" || result.ArchiveToolchainIdentity != "stock" {
		t.Fatalf("result lost its provenance: %#v", result)
	}
	if err := validateNativeQueueDrainResult(result, 4); err != nil {
		t.Fatalf("a complete drain result was refused: %v", err)
	}
}

func TestNativeQueueDrainResultValidation(t *testing.T) {
	cfg := testConfig(benchmark.Weaver)
	cfg.PollInterval = 100 * time.Millisecond
	cfg.QueueInput = &benchmark.QueueInput{SchemaVersion: 3, SuiteID: "queue-transition-0001", SubmissionMode: benchmark.SubmissionModeQueueDrain}
	base := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	build := func() benchmark.QueueAdapterResult {
		return nativeQueueDrainResult(cfg, "sha256:client", "stock", strings.Repeat("a", 64), []benchmark.QueueJobResult{
			drainJob("run-0001", base, base.Add(10*time.Second), "succeeded"),
			drainJob("run-0002", base.Add(time.Millisecond), base.Add(9*time.Second), "succeeded"),
		}, drainMetrics())
	}
	for name, test := range map[string]struct {
		planned int
		mutate  func(*benchmark.QueueAdapterResult)
		want    string
	}{
		"missing job":       {planned: 3, mutate: func(*benchmark.QueueAdapterResult) {}, want: "expected 3"},
		"repeated run":      {planned: 2, mutate: func(r *benchmark.QueueAdapterResult) { r.Jobs[1].RunID = "run-0001" }, want: "repeats"},
		"per-job metrics":   {planned: 2, mutate: func(r *benchmark.QueueAdapterResult) { m := drainMetrics(); r.Jobs[0].ResourceMetrics = &m }, want: "suite-level"},
		"no failure reason": {planned: 2, mutate: func(r *benchmark.QueueAdapterResult) { r.Jobs[0].TerminalStatus = "failed" }, want: "reason"},
		"outside window":    {planned: 2, mutate: func(r *benchmark.QueueAdapterResult) { r.Jobs[1].CompletionAt = base.Add(time.Minute) }, want: "outside"},
		"sequential mode":   {planned: 2, mutate: func(r *benchmark.QueueAdapterResult) { r.SubmissionMode = benchmark.SubmissionModeSequential }, want: "submission mode"},
	} {
		t.Run(name, func(t *testing.T) {
			result := build()
			test.mutate(&result)
			if err := validateNativeQueueDrainResult(result, test.planned); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want it to mention %q", err, test.want)
			}
		})
	}
}
