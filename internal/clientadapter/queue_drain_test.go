package clientadapter

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// drainAPI is a product that holds every job queued until the whole suite has
// been submitted and registered with the monitor, then finishes one job per
// poll in reverse submission order.
// It refuses the submission named refuse.
type drainAPI struct {
	productAPI
	refuse string

	mu        sync.Mutex
	planned   int
	submitted int
	order     []string
	finished  map[string]bool
}

func (api *drainAPI) queue(_ context.Context, _ string, _ string, options queueOptions) (string, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.submitted++
	if options.submissionName == api.refuse {
		return "", &SubmissionRefusedError{Client: "fake", Reason: "duplicate"}
	}
	if !options.forceAccept {
		return "", fmt.Errorf("drain submission %s was not forced", options.submissionName)
	}
	id := fmt.Sprintf("job-%d", api.submitted)
	api.order = append(api.order, id)
	return id, nil
}

func (api *drainAPI) observe(_ context.Context, ids []string) (map[string]jobObservation, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	observations := make(map[string]jobObservation, len(ids))
	for _, id := range ids {
		observations[id] = jobObservation{state: jobQueued, status: "QUEUED"}
	}
	if api.submitted < api.planned {
		return observations, nil
	}
	// Nothing finishes until the monitor is asking about every accepted job,
	// which it does only once the submitter has registered the last of them.
	for _, id := range api.order {
		if _, asked := observations[id]; !asked && !api.finished[id] {
			return observations, nil
		}
	}
	for index := len(api.order) - 1; index >= 0; index-- {
		id := api.order[index]
		if api.finished[id] {
			continue
		}
		api.finished[id] = true
		observations[id] = jobObservation{state: jobComplete, status: "Completed"}
		break
	}
	return observations, nil
}

// The native drain runs through SubmitAndDrain: every job is submitted before
// any terminal state is awaited, every accepted job is monitored to terminal
// whatever order the product finishes them in, a refused copy is a recorded
// did-not-finish, and the suite bounds cover all of them.
func TestSubmitAndDrainSubmitsEveryJobThenMonitorsAllToTerminal(t *testing.T) {
	const planned = 10
	fake := &drainAPI{planned: planned, refuse: "queue-transition-04.nzb", finished: map[string]bool{}}
	inputs := make([]benchmark.QueueInputJob, 0, planned)
	for index := range planned {
		inputs = append(inputs, benchmark.QueueInputJob{
			RunID:          fmt.Sprintf("run-%04d", index+1),
			FixtureID:      "direct-mkv-200mb",
			NZBPath:        "fixture.nzb",
			SubmissionName: fmt.Sprintf("queue-transition-%02d.nzb", index+1),
			ForceAccept:    true,
		})
	}
	api := &API{product: fake}
	jobs, err := api.SubmitAndDrain(context.Background(), time.Millisecond, time.Hour, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != planned {
		t.Fatalf("drain returned %d jobs, want %d", len(jobs), planned)
	}
	var lastAccepted time.Time
	byRun := map[string]benchmark.QueueJobResult{}
	for _, job := range jobs {
		byRun[job.RunID] = job
		if job.TerminalStatus == "succeeded" && job.AcceptedAt.After(lastAccepted) {
			lastAccepted = job.AcceptedAt
		}
	}
	refused := byRun["run-0004"]
	if refused.TerminalStatus != "failed" || refused.ResourceMetrics != nil {
		t.Fatalf("refused copy was not recorded as a did-not-finish without its own metrics: %#v", refused)
	}
	for runID, job := range byRun {
		if runID == "run-0004" {
			continue
		}
		if job.TerminalStatus != "succeeded" {
			t.Fatalf("job %s ended %q", runID, job.TerminalStatus)
		}
		if job.CompletionAt.Before(lastAccepted) {
			t.Fatalf("job %s finished before the last submission was accepted", runID)
		}
	}
	// The product finished the last submission first: run-0010 must complete
	// no later than run-0001.
	if byRun["run-0010"].CompletionAt.After(byRun["run-0001"].CompletionAt) {
		t.Fatalf("completion order was not the product's: run-0010 at %s, run-0001 at %s", byRun["run-0010"].CompletionAt, byRun["run-0001"].CompletionAt)
	}
	started, completed := QueueSuiteBounds(jobs)
	for _, job := range jobs {
		if job.SubmissionStartedAt.Before(started) || job.CompletionAt.After(completed) {
			t.Fatalf("job %s lies outside the suite bounds %s to %s", job.RunID, started, completed)
		}
	}
	if !started.Equal(byRun["run-0001"].SubmissionStartedAt) {
		t.Fatalf("suite started at %s, want the first submission %s", started, byRun["run-0001"].SubmissionStartedAt)
	}
	if !completed.Equal(byRun["run-0001"].CompletionAt) {
		t.Fatalf("suite completed at %s, want run-0001's completion, the last %s", completed, byRun["run-0001"].CompletionAt)
	}
}
