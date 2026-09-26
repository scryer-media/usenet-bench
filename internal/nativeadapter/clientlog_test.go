package nativeadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/clientadapter"
)

// A SABnzbd log captured from a bare-file post on the Windows lane: the job
// left the queue at "Decoding finished", the completion notification is the
// client's own terminal instant, and the history row followed 112 ms later.
const sabnzbdCompletionLog = `2026-09-26 10:18:23,787::INFO::[notifier:169] Sending notification: NZB added to queue - direct-mkv-200mb.nzb (type=download, job_cat=*)
2026-09-26 10:18:25,736::INFO::[assembler:307] Decoding finished \\?\C:\bench\incomplete\direct-mkv-200mb\direct-200mb.mkv
2026-09-26 10:18:25,738::INFO::[nzbqueue:391] [N/A] Removing job direct-mkv-200mb
2026-09-26 10:18:25,740::INFO::[notifier:169] Sending notification: Post-processing - direct-mkv-200mb (type=pp, job_cat=*)
2026-09-26 10:18:25,747::INFO::[notifier:169] Sending notification: Download Completed - direct-mkv-200mb (type=complete, job_cat=*)
2026-09-26 10:18:25,859::INFO::[database:386] Added job direct-mkv-200mb to history
2026-09-26 10:18:25,862::INFO::[notifier:169] Sending notification: SABnzbd - Queue finished (type=queue_done, job_cat=None)
`

const weaverCompletionLog = `2026-09-26T10:18:22.331485-06:00  INFO weaver_server_core::pipeline::completion::finalize::output: starting final move job_id=10000 dest=C:\bench\complete\direct-mkv-200mb
2026-09-26T10:18:22.333904-06:00  INFO weaver_server_core::pipeline::completion::finalize::output: built-in pipeline completed final move job_id=10000 moved=1
2026-09-26T10:18:22.334167-06:00  INFO weaver_server_core::pipeline::completion::finalize::output: job completed after terminal post-processing job_id=10000
`

func writeClientLog(t *testing.T, configDir, relative, contents string) {
	t.Helper()
	path := filepath.Join(configDir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func localStamp(t *testing.T, layout, value string) time.Time {
	t.Helper()
	instant, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return instant
}

// SABnzbd's completion notification is logged to the millisecond by the
// client itself, so the polled window narrows to that millisecond.
func TestSABnzbdLogNarrowsThePolledTerminalToItsCompletionNotification(t *testing.T) {
	configDir := t.TempDir()
	writeClientLog(t, configDir, "logs/sabnzbd.log", sabnzbdCompletionLog)
	completed := localStamp(t, "2006-01-02 15:04:05,000", "2026-09-26 10:18:25,747")
	polled := clientadapter.TerminalObservation{LowerBound: completed.Add(-79 * time.Millisecond), ObservedAt: completed.Add(89 * time.Millisecond)}

	narrowed, err := clientReportedTerminal(benchmark.SABnzbd, configDir, polled)
	if err != nil {
		t.Fatalf("narrow from the client log: %v", err)
	}
	if !narrowed.LowerBound.Equal(completed) || narrowed.ObservedAt.Sub(narrowed.LowerBound) != time.Millisecond {
		t.Fatalf("narrowed observation %+v is not the logged millisecond starting %v", narrowed, completed)
	}
}

// Weaver stamps its completion line to the microsecond.
func TestWeaverLogNarrowsThePolledTerminalToItsCompletionLine(t *testing.T) {
	configDir := t.TempDir()
	writeClientLog(t, configDir, "native-client.log", weaverCompletionLog)
	completed, err := time.Parse(time.RFC3339Nano, "2026-09-26T10:18:22.334167-06:00")
	if err != nil {
		t.Fatal(err)
	}
	polled := clientadapter.TerminalObservation{LowerBound: completed.Add(-37 * time.Millisecond), ObservedAt: completed.Add(64 * time.Millisecond)}

	narrowed, err := clientReportedTerminal(benchmark.Weaver, configDir, polled)
	if err != nil {
		t.Fatalf("narrow from the client log: %v", err)
	}
	if !narrowed.LowerBound.Equal(completed) || narrowed.ObservedAt.Sub(narrowed.LowerBound) != time.Microsecond {
		t.Fatalf("narrowed observation %+v is not the logged microsecond starting %v", narrowed, completed)
	}
}

// The poll saw the job pending at the window's start and terminal at its
// end, so a logged completion outside that window contradicts the poll and
// cannot replace it.
func TestALoggedCompletionOutsideThePolledWindowIsRefused(t *testing.T) {
	configDir := t.TempDir()
	writeClientLog(t, configDir, "logs/sabnzbd.log", sabnzbdCompletionLog)
	completed := localStamp(t, "2006-01-02 15:04:05,000", "2026-09-26 10:18:25,747")
	for name, polled := range map[string]clientadapter.TerminalObservation{
		"logged before the window": {LowerBound: completed.Add(20 * time.Millisecond), ObservedAt: completed.Add(120 * time.Millisecond)},
		"logged after the window":  {LowerBound: completed.Add(-120 * time.Millisecond), ObservedAt: completed.Add(-20 * time.Millisecond)},
	} {
		kept, err := clientReportedTerminal(benchmark.SABnzbd, configDir, polled)
		if err == nil || !strings.Contains(err.Error(), "outside the polled terminal window") {
			t.Fatalf("%s: err = %v, want a window refusal", name, err)
		}
		if kept != polled {
			t.Fatalf("%s: refusal returned %+v, want the polled observation %+v", name, kept, polled)
		}
	}
}

// One process serves one job. A log with no completion line, or with more
// than one, cannot say when this job finished.
func TestTheClientLogMustNameTheCompletionExactlyOnce(t *testing.T) {
	completed := localStamp(t, "2006-01-02 15:04:05,000", "2026-09-26 10:18:25,747")
	polled := clientadapter.TerminalObservation{LowerBound: completed.Add(-79 * time.Millisecond), ObservedAt: completed.Add(89 * time.Millisecond)}
	completionLine := "2026-09-26 10:18:25,747::INFO::[notifier:169] Sending notification: Download Completed - direct-mkv-200mb (type=complete, job_cat=*)\n"
	for name, contents := range map[string]string{
		"no completion line":   strings.Replace(sabnzbdCompletionLog, completionLine, "", 1),
		"two completion lines": sabnzbdCompletionLog + completionLine,
	} {
		configDir := t.TempDir()
		writeClientLog(t, configDir, "logs/sabnzbd.log", contents)
		kept, err := clientReportedTerminal(benchmark.SABnzbd, configDir, polled)
		if err == nil {
			t.Fatalf("%s: narrowed to %+v, want a refusal", name, kept)
		}
		if kept != polled {
			t.Fatalf("%s: refusal returned %+v, want the polled observation %+v", name, kept, polled)
		}
	}
	configDir := t.TempDir()
	if _, err := clientReportedTerminal(benchmark.SABnzbd, configDir, polled); err == nil {
		t.Fatal("a missing log narrowed the observation")
	}
}

// NZBGet's log carries whole-second timestamps, so its terminal stays polled.
func TestAClientWithoutSubSecondLogTimestampsStaysPolled(t *testing.T) {
	polled := clientadapter.TerminalObservation{LowerBound: time.Now(), ObservedAt: time.Now().Add(time.Millisecond)}
	kept, err := clientReportedTerminal(benchmark.NZBGet, t.TempDir(), polled)
	if err == nil || kept != polled {
		t.Fatalf("NZBGet narrowed to %+v with err %v; want the polled observation and a refusal", kept, err)
	}
}
