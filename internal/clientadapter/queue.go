package clientadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// runQueue keeps one product process alive while every planned NZB is queued.
// Jobs are submitted before any terminal-state wait begins; this intentionally
// retains each product's own queueing, scheduling, and transition behaviour.
func runQueue(ctx context.Context, cfg Config) error {
	input := cfg.QueueInput
	if input == nil {
		return fmt.Errorf("queue adapter requires queue input")
	}
	spec, err := cfg.RenderProductConfig()
	if err != nil {
		return err
	}
	if !spec.ExposeAPI {
		return fmt.Errorf("%s queue adapter requires a public control API", cfg.Client)
	}
	if err := writeProductFiles(cfg, spec); err != nil {
		return err
	}
	container, err := startContainer(ctx, cfg, spec)
	if err != nil {
		return err
	}
	defer container.cleanup()

	cpu := cpuSampler{docker: container.docker, name: container.name, reason: "suite-level telemetry is not reported for this submission mode"}
	memory := unavailableMemorySampler("suite-level telemetry is not reported for this submission mode")
	deviceWrites := unavailableDeviceWriteSampler("suite-level telemetry is not reported for this submission mode")
	instructions := unavailableInstructionRecorder("suite-level retired instructions are not reported for this submission mode")
	if input.SubmissionMode == benchmark.SubmissionModeQueued || input.SubmissionMode == benchmark.SubmissionModeQueueDrain {
		cpu = startCPUSampler(ctx, container.docker, container.name)
		memory = startMemorySampler(ctx, container.docker, container.name)
		deviceWrites = startDeviceWriteSampler(ctx, container.docker, container.name)
	}
	// What the daemon says the container actually got, read back while it is
	// running rather than assembled from the flags the harness passed.
	containerRuntime := container.docker.inspectRuntime(ctx, container.name, containerCompletionDir)
	if input.SubmissionMode == benchmark.SubmissionModeQueued {
		instructions = startInstructionRecorder(ctx, cfg, container)
	}
	metricsCollected := false
	defer func() {
		if !metricsCollected {
			_ = instructions.finish()
			_, _ = memory.finish()
		}
	}()

	if err := container.resolveEndpoint(ctx, spec.APIPort); err != nil {
		return fmt.Errorf("resolve %s client API endpoint: %w", cfg.Client, err)
	}
	api, err := newProductAPI(cfg, container.endpoint)
	if err != nil {
		return err
	}
	clientVersion := container.docker.imageVersion(ctx, cfg.Image)
	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.StartupTimeout)
	readyVersion, err := waitUntilContainerReady(startupCtx, cfg.PollInterval, api, container)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("wait for %s readiness: %w", cfg.Client, err)
	}
	if readyVersion != "" {
		clientVersion = readyVersion
	}

	var jobs []benchmark.QueueJobResult
	if input.SubmissionMode == benchmark.SubmissionModeSequential {
		jobs, err = runSequentialSubmission(ctx, api, cfg.PollInterval, input.Jobs,
			cpuSampler{docker: container.docker, name: container.name},
			deviceWriteSampler{docker: container.docker, name: container.name}, cfg, container)
	} else {
		jobs, err = runQueuedSubmission(ctx, api, cfg.PollInterval, cfg.JobTimeout, input.Jobs)
	}
	if err != nil {
		return err
	}
	// The suite's window has to contain every job's, and a refused copy is
	// recorded out of submission order, so both bounds are taken over all of
	// them rather than assuming the first job opened the queue.
	queueStartedAt := jobs[0].SubmissionStartedAt
	queueCompletedAt := jobs[0].CompletionAt
	for _, job := range jobs[1:] {
		if job.SubmissionStartedAt.Before(queueStartedAt) {
			queueStartedAt = job.SubmissionStartedAt
		}
		if job.CompletionAt.After(queueCompletedAt) {
			queueCompletedAt = job.CompletionAt
		}
	}

	telemetryCtx, cancelTelemetry := context.WithTimeout(context.Background(), 15*time.Second)
	cpuMeasurement := cpu.finish(telemetryCtx)
	deviceWriteMeasurement := deviceWrites.finish(telemetryCtx)
	peakRSSMeasurement, peakRSSHint := memory.finish()
	cancelTelemetry()
	instructionMeasurement := instructions.finish()
	if raw := strings.TrimSpace(instructions.output.String()); raw != "" {
		if err := writeNewFile(filepath.Join(cfg.ConfigDir, "perf-instructions.txt"), []byte(raw+"\n")); err != nil {
			return fmt.Errorf("write perf instruction artifact: %w", err)
		}
	}
	metricsCollected = true
	result := benchmark.QueueAdapterResult{
		SchemaVersion:            7,
		SuiteID:                  input.SuiteID,
		SubmissionMode:           input.SubmissionMode,
		Client:                   cfg.Client,
		ArchiveToolchain:         cfg.ArchiveToolchain,
		ArchiveToolchainIdentity: cfg.archiveToolchainIdentity(),
		ExecutionTarget:          cfg.ExecutionTarget,
		Transport:                cfg.Transport,
		TLSValidation:            cfg.TLSValidation,
		TransportLabel:           cfg.TransportLabel,
		ServerLink:               cfg.ServerLink,
		ArticleProfile:           cfg.ArticleProfile,
		StorageProfile:           cfg.StorageProfile,
		QueueStartedAt:           queueStartedAt,
		QueueCompletedAt:         queueCompletedAt,
		QueueElapsedNanoseconds:  queueCompletedAt.Sub(queueStartedAt).Nanoseconds(),
		StatusPollIntervalNanos:  cfg.PollInterval.Nanoseconds(),
		Jobs:                     jobs,
		ClientIdentity:           cfg.Image,
		ClientVersion:            clientVersion,
		RenderedConfigSHA256:     spec.ConfigSHA256,
		Connections:              cfg.Connections,
		ResourceMetrics: benchmark.ResourceMetrics{
			CPUTimeNanoseconds:   cpuMeasurement,
			InstructionsRetired:  instructionMeasurement,
			PeakRSSBytes:         peakRSSMeasurement,
			PeakRSSHighWaterHint: peakRSSHint,
			DeviceWriteBytes:     deviceWriteMeasurement,
		},
		ContainerRuntime: &containerRuntime,
	}
	if err := result.ResourceMetrics.Validate(); err != nil {
		return fmt.Errorf("validate queue resource metrics: %w", err)
	}
	if err := writeQueueResult(cfg.ResultPath, result); err != nil {
		return err
	}
	return nil
}

func runQueuedSubmission(ctx context.Context, api productAPI, interval, jobTimeout time.Duration, inputJobs []benchmark.QueueInputJob) ([]benchmark.QueueJobResult, error) {
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	registrations := make(chan queuedJob, len(inputJobs))
	monitorResult := make(chan queueMonitorResult, 1)
	var refusals []benchmark.QueueJobResult
	accepted := 0
	go func() {
		jobs, err := monitorQueue(monitorCtx, api, interval, jobTimeout, registrations)
		monitorResult <- queueMonitorResult{jobs: jobs, err: err}
	}()
	for _, inputJob := range inputJobs {
		// Elapsed measurements retain Go's monotonic clock; JSON timestamps are annotations.
		submissionStartedAt := time.Now()
		jobID, err := api.queue(ctx, inputJob.NZBPath, inputJob.ArchivePassword, queueOptions{
			submissionName: inputJob.SubmissionName,
			forceAccept:    inputJob.ForceAccept,
		})
		if err != nil {
			var refused *SubmissionRefusedError
			if !errors.As(err, &refused) {
				cancelMonitor()
				return nil, fmt.Errorf("queue %s: %w", inputJob.RunID, err)
			}
			// A refusal is this client's outcome for this copy. The other
			// copies keep running and the drain still reports; the refused
			// copy is a recorded did-not-finish. Suite-level counters cover
			// the whole queue, so a refused copy carries none of its own.
			refusedJob, refusedErr := refusedSubmissionJob(inputJob.RunID, refused, submissionStartedAt, benchmark.ResourceMetrics{})
			if refusedErr != nil {
				cancelMonitor()
				return nil, fmt.Errorf("record %s refusal of %s: %w", refused.Client, inputJob.RunID, refusedErr)
			}
			refusedJob.ResourceMetrics = nil
			refusals = append(refusals, refusedJob)
			continue
		}
		acceptedAt := time.Now()
		registrations <- queuedJob{result: benchmark.QueueJobResult{
			RunID:               inputJob.RunID,
			JobID:               jobID,
			SubmissionStartedAt: submissionStartedAt,
			AcceptedAt:          acceptedAt,
			QueuedAt:            acceptedAt,
		}}
		accepted++
	}
	close(registrations)
	if accepted == 0 {
		// The monitor refuses an empty registration set, and rightly: nothing
		// was queued. Every copy was refused, which is a complete client
		// outcome on its own.
		cancelMonitor()
		<-monitorResult
		return refusals, nil
	}
	monitored := <-monitorResult
	if monitored.err != nil {
		return nil, fmt.Errorf("monitor queue lifecycle: %w", monitored.err)
	}
	return append(monitored.jobs, refusals...), nil
}

func runSequentialSubmission(ctx context.Context, api productAPI, interval time.Duration, inputJobs []benchmark.QueueInputJob, cpu cpuSampler, deviceWrites deviceWriteSampler, cfg Config, container *runningContainer) ([]benchmark.QueueJobResult, error) {
	jobs := make([]benchmark.QueueJobResult, 0, len(inputJobs))
	for jobIndex, inputJob := range inputJobs {
		instructions := startInstructionRecorder(ctx, cfg, container)
		// A peak cannot be differenced the way a counter can, so each fixture
		// gets its own sampling window rather than a reading taken across the
		// whole drain.
		memory := startMemorySampler(ctx, container.docker, container.name)
		cpuStart, cpuStartErr := cpu.read(ctx)
		deviceWriteStart, deviceWriteStartErr := deviceWrites.read(ctx)
		monitorCtx, cancelMonitor := context.WithCancel(ctx)
		registrations := make(chan queuedJob, 1)
		monitorResult := make(chan queueMonitorResult, 1)
		go func() {
			observed, err := monitorQueue(monitorCtx, api, interval, cfg.JobTimeout, registrations)
			monitorResult <- queueMonitorResult{jobs: observed, err: err}
		}()
		var cpuMeasurement, deviceWriteMeasurement benchmark.CounterMeasurement
		submissionStartedAt := time.Now()
		jobID, err := api.queue(ctx, inputJob.NZBPath, inputJob.ArchivePassword, queueOptions{
			submissionName: inputJob.SubmissionName,
			forceAccept:    inputJob.ForceAccept,
		})
		if err != nil {
			cancelMonitor()
			instructionMeasurement := instructions.finish()
			peakRSSMeasurement, _ := memory.finish()
			var refused *SubmissionRefusedError
			if !errors.As(err, &refused) {
				return nil, fmt.Errorf("queue %s: %w", inputJob.RunID, err)
			}
			// The client took the submission and declined to run it. That is
			// this client's outcome on this fixture, so it is recorded as a
			// did-not-finish carrying the client's own reason rather than
			// abandoning the suite -- and every other client's numbers in the
			// phase with it.
			telemetryCtx, cancelTelemetry := context.WithTimeout(context.Background(), 15*time.Second)
			if cpuStartErr != nil {
				cpuMeasurement = benchmark.UnavailableMeasurement("client_container", "cgroup-cpu", "unknown", cpuStartErr.Error())
			} else {
				cpuMeasurement = cpu.measureFrom(telemetryCtx, cpuStart)
			}
			if deviceWriteStartErr != nil {
				deviceWriteMeasurement = benchmark.UnavailableMeasurement(deviceWriteScope, "cgroup-io", "unknown", deviceWriteStartErr.Error())
			} else {
				deviceWriteMeasurement = deviceWrites.measureFrom(telemetryCtx, deviceWriteStart)
			}
			cancelTelemetry()
			refusedJob, refusedErr := refusedSubmissionJob(inputJob.RunID, refused, submissionStartedAt, benchmark.ResourceMetrics{
				CPUTimeNanoseconds:   windowedCounter(cpuMeasurement, "pre_submission_to_post_terminal"),
				InstructionsRetired:  windowedCounter(instructionMeasurement, "recorder_enabled_to_post_terminal"),
				PeakRSSBytes:         windowedCounter(peakRSSMeasurement, "pre_submission_to_post_terminal"),
				PeakRSSHighWaterHint: refusedPeakRSSHint(),
				DeviceWriteBytes:     windowedCounter(deviceWriteMeasurement, "pre_submission_to_post_terminal"),
			})
			if refusedErr != nil {
				return nil, fmt.Errorf("record %s refusal of %s: %w", refused.Client, inputJob.RunID, refusedErr)
			}
			jobs = append(jobs, refusedJob)
			continue
		}
		acceptedAt := time.Now()
		registrations <- queuedJob{result: benchmark.QueueJobResult{
			RunID:               inputJob.RunID,
			JobID:               jobID,
			SubmissionStartedAt: submissionStartedAt,
			AcceptedAt:          acceptedAt,
			QueuedAt:            acceptedAt,
		}}
		close(registrations)
		monitored := <-monitorResult
		cancelMonitor()
		telemetryCtx, cancelTelemetry := context.WithTimeout(context.Background(), 15*time.Second)
		if cpuStartErr != nil {
			cpuMeasurement = benchmark.UnavailableMeasurement("client_container", "cgroup-cpu", "unknown", cpuStartErr.Error())
		} else {
			cpuMeasurement = cpu.measureFrom(telemetryCtx, cpuStart)
		}
		if deviceWriteStartErr != nil {
			deviceWriteMeasurement = benchmark.UnavailableMeasurement(deviceWriteScope, "cgroup-io", "unknown", deviceWriteStartErr.Error())
		} else {
			deviceWriteMeasurement = deviceWrites.measureFrom(telemetryCtx, deviceWriteStart)
		}
		cancelTelemetry()
		instructionMeasurement := instructions.finish()
		peakRSSMeasurement, _ := memory.finish()
		// The kernel's own high-water mark accumulates over the container,
		// which outlives any one fixture here, so it cannot be attributed to
		// this one. Only the sampled window figure is reportable per fixture.
		peakRSSHint := benchmark.UnavailableMeasurement(memoryHintScope, "cgroup-memory-peak", "unknown",
			"the container's cumulative memory high-water mark cannot be attributed to a single fixture in a sequential drain")
		if raw := strings.TrimSpace(instructions.output.String()); raw != "" {
			if err := writeNewFile(filepath.Join(cfg.ConfigDir, fmt.Sprintf("perf-job-%03d.txt", jobIndex+1)), []byte(raw+"\n")); err != nil {
				return nil, err
			}
		}
		cpuMeasurement.Window = "pre_submission_to_post_terminal"
		deviceWriteMeasurement.Window = "pre_submission_to_post_terminal"
		instructionMeasurement.Window = "recorder_enabled_to_post_terminal"
		peakRSSMeasurement.Window = "pre_submission_to_post_terminal"
		if monitored.err != nil {
			return nil, fmt.Errorf("monitor fixture %s lifecycle: %w", inputJob.RunID, monitored.err)
		}
		if len(monitored.jobs) != 1 {
			return nil, fmt.Errorf("monitor fixture %s returned %d jobs", inputJob.RunID, len(monitored.jobs))
		}
		metrics := benchmark.ResourceMetrics{
			CPUTimeNanoseconds:   cpuMeasurement,
			InstructionsRetired:  instructionMeasurement,
			PeakRSSBytes:         peakRSSMeasurement,
			PeakRSSHighWaterHint: peakRSSHint,
			DeviceWriteBytes:     deviceWriteMeasurement,
		}
		if err := metrics.Validate(); err != nil {
			return nil, fmt.Errorf("validate fixture %s resource metrics: %w", inputJob.RunID, err)
		}
		job := monitored.jobs[0]
		job.ResourceMetrics = &metrics
		jobs = append(jobs, job)
	}
	return jobs, nil
}

type queuedJob struct {
	result benchmark.QueueJobResult
}

type queueMonitorResult struct {
	jobs []benchmark.QueueJobResult
	err  error
}

type trackedQueueJob struct {
	result         benchmark.QueueJobResult
	lastObservedAt time.Time
	lastStatus     string
	complete       bool
}

// monitorQueue polls the client for every registered job until each reaches a
// terminal state or exceeds jobTimeout after acceptance. A job that exceeds
// the bound is recorded with terminal status "timed_out": the client's own
// last reported status is kept as the reason, and the controller treats it as
// did-not-finish exactly like a client-reported failure.
func monitorQueue(
	ctx context.Context,
	api productAPI,
	interval time.Duration,
	jobTimeout time.Duration,
	registrations <-chan queuedJob,
) ([]benchmark.QueueJobResult, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var jobs []*trackedQueueJob
	seenIDs := make(map[string]bool)
	registrationsOpen := true
	completed := 0

	for registrationsOpen || completed < len(jobs) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case registration, ok := <-registrations:
			if !ok {
				registrationsOpen = false
				registrations = nil
				if len(jobs) == 0 {
					return nil, fmt.Errorf("queue monitor received no jobs")
				}
				continue
			}
			if seenIDs[registration.result.JobID] {
				return nil, fmt.Errorf("queue returned duplicate job id %s", registration.result.JobID)
			}
			seenIDs[registration.result.JobID] = true
			registration.result.TimingClock = "monotonic"
			jobs = append(jobs, &trackedQueueJob{result: registration.result, lastObservedAt: registration.result.SubmissionStartedAt})
		case <-ticker.C:
			pendingIDs := make([]string, 0, len(jobs)-completed)
			for _, job := range jobs {
				if !job.complete {
					pendingIDs = append(pendingIDs, job.result.JobID)
				}
			}
			if len(pendingIDs) == 0 {
				continue
			}
			requestStartedAt := time.Now()
			observations, err := api.observe(ctx, pendingIDs)
			if err != nil {
				return nil, err
			}
			observedAt := time.Now()
			for _, job := range jobs {
				if job.complete {
					continue
				}
				observation, ok := observations[job.result.JobID]
				if !ok {
					continue
				}
				switch observation.state {
				case jobUnknown:
					continue
				case jobQueued:
					job.lastObservedAt = observation.pendingSince(requestStartedAt)
					job.lastStatus = observation.status
				case jobActive:
					if job.result.ProcessingStartedAt.IsZero() {
						job.result.ProcessingStartedAt = observedAt
					}
					job.lastObservedAt = observation.pendingSince(requestStartedAt)
					job.lastStatus = observation.status
				case jobComplete:
					job.lastObservedAt = observation.terminalLowerBound(job.lastObservedAt)
					job.result.TerminalStatus = "succeeded"
					job.result.CompletionAt = observedAt
					job.result.TerminalObservationLowerBound = job.lastObservedAt
					job.result.TerminalObservedAt = observedAt
					job.result.TerminalObservationUncertainty = observedAt.Sub(job.lastObservedAt).Nanoseconds()
					job.result.SubmissionToTerminalNanoseconds = observedAt.Sub(job.result.SubmissionStartedAt).Nanoseconds()
					job.result.FixtureWallClockNanoseconds = observedAt.Sub(job.result.QueuedAt).Nanoseconds()
					finishProcessingTiming(&job.result, observedAt, observation.status)
					job.complete = true
					completed++
				case jobFailed:
					job.lastObservedAt = observation.terminalLowerBound(job.lastObservedAt)
					job.result.TerminalStatus = "failed"
					job.result.TerminalError = observation.status
					job.result.CompletionAt = observedAt
					job.result.TerminalObservationLowerBound = job.lastObservedAt
					job.result.TerminalObservedAt = observedAt
					job.result.TerminalObservationUncertainty = observedAt.Sub(job.lastObservedAt).Nanoseconds()
					job.result.SubmissionToTerminalNanoseconds = observedAt.Sub(job.result.SubmissionStartedAt).Nanoseconds()
					job.result.FixtureWallClockNanoseconds = observedAt.Sub(job.result.QueuedAt).Nanoseconds()
					finishProcessingTiming(&job.result, observedAt, observation.status)
					job.complete = true
					completed++
				}
			}
			for _, job := range jobs {
				// QueuedAt is the acceptance instant (the two are equal by
				// construction) and is what the fixture wall clock is measured
				// from, so the bound is measured from it as well.
				if job.complete || observedAt.Before(job.result.QueuedAt.Add(jobTimeout)) {
					continue
				}
				job.result.TerminalStatus = "timed_out"
				job.result.TerminalError = fmt.Sprintf("no terminal state within %s of acceptance; last reported status %q", jobTimeout, job.lastStatus)
				job.result.CompletionAt = observedAt
				job.result.TerminalObservationLowerBound = job.lastObservedAt
				job.result.TerminalObservedAt = observedAt
				job.result.TerminalObservationUncertainty = observedAt.Sub(job.lastObservedAt).Nanoseconds()
				job.result.SubmissionToTerminalNanoseconds = observedAt.Sub(job.result.SubmissionStartedAt).Nanoseconds()
				job.result.FixtureWallClockNanoseconds = observedAt.Sub(job.result.QueuedAt).Nanoseconds()
				finishProcessingTiming(&job.result, observedAt, job.result.TerminalStatus)
				job.complete = true
				completed++
			}
		}
	}

	results := make([]benchmark.QueueJobResult, len(jobs))
	for index, job := range jobs {
		results[index] = job.result
	}
	return results, nil
}

func finishProcessingTiming(result *benchmark.QueueJobResult, completionAt time.Time, terminalStatus string) {
	if result.ProcessingStartedAt.IsZero() {
		result.ProcessingTimingError = fmt.Sprintf("terminal status %q was observed before an active state; reduce CLIENT_POLL_INTERVAL", terminalStatus)
		return
	}
	result.ProcessingTimingAvailable = true
	result.ProcessingWallClockNanoseconds = completionAt.Sub(result.ProcessingStartedAt).Nanoseconds()
}

func writeQueueResult(path string, result benchmark.QueueAdapterResult) error {
	contents, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode queue adapter result: %w", err)
	}
	contents = append(contents, '\n')
	if err := writeNewFile(path, contents); err != nil {
		return fmt.Errorf("write queue adapter result: %w", err)
	}
	return nil
}

// refusedSubmissionJob records a client that declined a submission as the
// did-not-finish it is. The timing fields describe exactly what happened: the
// submission started, the client answered, and the answer was terminal. There
// is no observation window to widen, so the terminal bound is the refusal
// itself and the uncertainty is zero — which is the truth here, unlike a
// timeout, where the client was still being waited on.
func refusedSubmissionJob(runID string, refused *SubmissionRefusedError, submissionStartedAt time.Time, metrics benchmark.ResourceMetrics) (benchmark.QueueJobResult, error) {
	refusedAt := time.Now()
	job := benchmark.QueueJobResult{
		TimingClock:                     "monotonic",
		RunID:                           runID,
		JobID:                           "refused-" + runID,
		SubmissionStartedAt:             submissionStartedAt,
		AcceptedAt:                      refusedAt,
		QueuedAt:                        refusedAt,
		CompletionAt:                    refusedAt,
		TerminalObservationLowerBound:   refusedAt,
		TerminalObservedAt:              refusedAt,
		TerminalObservationUncertainty:  0,
		SubmissionToTerminalNanoseconds: refusedAt.Sub(submissionStartedAt).Nanoseconds(),
		FixtureWallClockNanoseconds:     0,
		TerminalStatus:                  "failed",
		TerminalError:                   "client refused the submission: " + refused.Reason,
		ProcessingTimingError:           "the client refused the submission, so it never entered an active state",
	}
	if job.SubmissionToTerminalNanoseconds <= 0 {
		// A refusal answered inside the clock's resolution would produce a
		// zero duration, which the artifact contract reads as no measurement.
		job.SubmissionToTerminalNanoseconds = 1
	}
	if metrics.CPUTimeNanoseconds.Collector != "" {
		job.ResourceMetrics = &metrics
		if err := metrics.Validate(); err != nil {
			return benchmark.QueueJobResult{}, err
		}
	}
	return job, nil
}

// windowedCounter restates a counter's measurement window. The refusal path
// closes its counters at the same points the ordinary path does, so they
// carry the same window.
func windowedCounter(counter benchmark.CounterMeasurement, window string) benchmark.CounterMeasurement {
	counter.Window = window
	return counter
}

// refusedPeakRSSHint says why a refused fixture has no platform high-water
// mark, for the same reason an ordinary sequential fixture has none.
func refusedPeakRSSHint() benchmark.CounterMeasurement {
	return benchmark.UnavailableMeasurement(memoryHintScope, "cgroup-memory-peak", "unknown",
		"the container's cumulative memory high-water mark cannot be attributed to a single fixture in a sequential drain")
}
