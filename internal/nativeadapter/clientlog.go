package nativeadapter

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/clientadapter"
)

// Terminal observation sources recorded on a native job result.
const (
	// terminalSourcePublicAPI is the polled public API: the terminal lies
	// somewhere between the last answer that showed the job pending and the
	// answer that showed it terminal.
	terminalSourcePublicAPI = "public_api"
	// terminalSourceClientLog is the client's own log, whose completion line
	// carries the client's own timestamp for the terminal instant.
	terminalSourceClientLog = "client_log"
)

// clientReportedTerminal narrows a polled terminal observation to the instant
// the client itself logged for the job's completion.
//
// The public APIs report completion only to whole seconds, so the poll is
// the only API-side bound, and its window is the client's own commit latency
// plus two round trips: a bare-file post that SABnzbd finishes in two seconds
// spends most of that window with the job already complete. The client's log
// is written by the same process, on the same host clock, at millisecond
// resolution or better, so once the poll has confirmed the terminal state the
// log gives the instant it happened.
//
// The polled window is kept as the sanity bound: the logged instant must lie
// inside it, since the poll saw the job pending at the window's start and
// terminal at its end. A log that disagrees, is missing, or names the moment
// more than once is reported as an error and the polled observation stands.
// Clients whose logs carry no sub-second timestamps get no narrowing.
func clientReportedTerminal(client benchmark.Client, configDir string, polled clientadapter.TerminalObservation) (clientadapter.TerminalObservation, error) {
	var (
		path       string
		resolution time.Duration
		parse      func(line string) (time.Time, bool, error)
	)
	switch client {
	case benchmark.SABnzbd:
		path = filepath.Join(configDir, "logs", "sabnzbd.log")
		resolution = time.Millisecond
		parse = parseSABnzbdCompletion
	case benchmark.Weaver:
		path = filepath.Join(configDir, "native-client.log")
		resolution = time.Microsecond
		parse = parseWeaverCompletion
	default:
		return polled, fmt.Errorf("%s does not log its completion with a sub-second timestamp", client)
	}
	reported, err := singleLoggedInstant(path, parse)
	if err != nil {
		return polled, err
	}
	// The log stamps the instant at its resolution, truncated, so the true
	// instant lies within one resolution step after the stamp.
	observation := clientadapter.TerminalObservation{LowerBound: reported, ObservedAt: reported.Add(resolution)}
	if reported.Before(polled.LowerBound) || observation.ObservedAt.After(polled.ObservedAt) {
		return polled, fmt.Errorf("%s logged its completion at %s, outside the polled terminal window %s to %s", client, reported.Format(time.RFC3339Nano), polled.LowerBound.Format(time.RFC3339Nano), polled.ObservedAt.Format(time.RFC3339Nano))
	}
	return observation, nil
}

// singleLoggedInstant scans a log for the one line the parser accepts and
// returns its instant. One native process serves one job, so a completion
// logged twice means the log is not the one job's, and none means the client
// did not say when it finished.
func singleLoggedInstant(path string, parse func(line string) (time.Time, bool, error)) (time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("read client log: %w", err)
	}
	defer file.Close()
	var (
		found   time.Time
		matches int
	)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		instant, ok, err := parse(scanner.Text())
		if err != nil {
			return time.Time{}, fmt.Errorf("client log %s: %w", filepath.Base(path), err)
		}
		if !ok {
			continue
		}
		matches++
		found = instant
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, fmt.Errorf("read client log %s: %w", filepath.Base(path), err)
	}
	switch matches {
	case 0:
		return time.Time{}, fmt.Errorf("client log %s has no completion line", filepath.Base(path))
	case 1:
		return found, nil
	default:
		return time.Time{}, fmt.Errorf("client log %s has %d completion lines, want one", filepath.Base(path), matches)
	}
}

// sabnzbdCompletionMarker is the notification SABnzbd sends when a job's
// post-processing has finished. The notifier logs every notification with
// its title and type at INFO, which the harness's SABnzbd config keeps
// enabled, so the line is there whether or not any notifier is configured.
const sabnzbdCompletionMarker = "Sending notification: Download Completed - "

// parseSABnzbdCompletion recognizes SABnzbd's completion line, whose prefix
// is Python logging's default timestamp in the process's local zone:
// "2026-09-26 10:18:25,747::INFO::[notifier:169] Sending notification: ...".
func parseSABnzbdCompletion(line string) (time.Time, bool, error) {
	if !strings.Contains(line, sabnzbdCompletionMarker) || !strings.Contains(line, "(type=complete,") {
		return time.Time{}, false, nil
	}
	stamp, _, ok := strings.Cut(line, "::")
	if !ok {
		return time.Time{}, false, fmt.Errorf("completion line has no timestamp: %q", line)
	}
	instant, err := time.ParseInLocation("2006-01-02 15:04:05,000", stamp, time.Local)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("completion line timestamp: %w", err)
	}
	return instant, true, nil
}

// weaverCompletionMarker is the line weaver logs when a job has finished its
// terminal post-processing and left the pipeline.
const weaverCompletionMarker = "job completed after terminal post-processing"

// parseWeaverCompletion recognizes weaver's completion line, which starts
// with an RFC 3339 timestamp carrying microseconds and the zone offset.
func parseWeaverCompletion(line string) (time.Time, bool, error) {
	if !strings.Contains(line, weaverCompletionMarker) {
		return time.Time{}, false, nil
	}
	stamp, _, ok := strings.Cut(strings.TrimSpace(line), " ")
	if !ok {
		return time.Time{}, false, fmt.Errorf("completion line has no timestamp: %q", line)
	}
	instant, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("completion line timestamp: %w", err)
	}
	return instant, true, nil
}
