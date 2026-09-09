//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scryer-media/usenet-bench/internal/adversarial"
)

const (
	assemblerFatalMarker   = "Fatal error in Assembler"
	assemblerRestartMarker = "Restarting because of crashed assembler"
)

type restartLoopCycle struct {
	Cycle       int       `json:"cycle"`
	NZOID       string    `json:"nzo_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	FatalAt     time.Time `json:"assembler_fatal_at"`
	RestartAt   time.Time `json:"watchdog_restart_at"`
	RecoveredAt time.Time `json:"api_recovered_at"`
}

type restartLoopReport struct {
	SchemaVersion int                     `json:"schema_version"`
	FixtureID     string                  `json:"fixture_id"`
	SABVersion    string                  `json:"sab_version"`
	StartedAt     time.Time               `json:"started_at"`
	FinishedAt    time.Time               `json:"finished_at"`
	Requested     int                     `json:"cycles_requested"`
	Completed     int                     `json:"cycles_completed"`
	FatalCount    int                     `json:"assembler_fatal_count"`
	RestartCount  int                     `json:"watchdog_restart_count"`
	Cycles        []restartLoopCycle      `json:"cycles"`
	Responder     adversarial.ServerStats `json:"responder"`
}

type observedLog struct {
	mu     sync.Mutex
	tail   string
	events map[string][]time.Time
}

func (l *observedLog) Write(data []byte) (int, error) {
	n, err := os.Stdout.Write(data)
	l.mu.Lock()
	defer l.mu.Unlock()
	text := l.tail + string(data)
	lines := strings.Split(text, "\n")
	l.tail = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		for _, marker := range []string{assemblerFatalMarker, assemblerRestartMarker} {
			if canonicalSABEvent(line, marker) {
				l.events[marker] = append(l.events[marker], time.Now().UTC())
			}
		}
	}
	return n, err
}

func canonicalSABEvent(line, marker string) bool {
	if !strings.Contains(line, marker) {
		return false
	}
	switch marker {
	case assemblerFatalMarker:
		return strings.Contains(line, "::ERROR::[assembler:")
	case assemblerRestartMarker:
		return strings.Contains(line, "::WARNING::[__init__:")
	default:
		return false
	}
}

func (l *observedLog) count(marker string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events[marker])
}

func (l *observedLog) wait(marker string, count int, timeout time.Duration, done <-chan error) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		if len(l.events[marker]) >= count {
			when := l.events[marker][count-1]
			l.mu.Unlock()
			return when, nil
		}
		l.mu.Unlock()
		select {
		case err := <-done:
			return time.Time{}, fmt.Errorf("SABnzbd exited while waiting for %q: %w", marker, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return time.Time{}, fmt.Errorf("timed out waiting for SABnzbd log marker %q occurrence %d", marker, count)
}

func restartLoop(args []string) error {
	flags := flag.NewFlagSet("restart-loop", flag.ContinueOnError)
	configDir := flags.String("config", "/scratch/config", "SABnzbd configuration directory")
	reportPath := flags.String("report", "/scratch/restart-loop.json", "JSON evidence report")
	cycles := flags.Int("cycles", 3, "number of crash/restart/resubmit cycles")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *cycles < 1 || *cycles > 10 {
		return fmt.Errorf("restart-loop requires no positional arguments and 1..10 cycles")
	}

	startedAt := time.Now().UTC()
	bundle, err := adversarial.Generate("rar5-packed-size-lie")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	server := &adversarial.Server{Bundle: bundle, RunID: "sabnzbd-rar5-restart-loop"}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serverCtx, listener) }()
	serverStopped := false
	stopResponder := func() {
		if serverStopped {
			return
		}
		stopServer()
		<-serverDone
		serverStopped = true
	}
	defer stopResponder()

	for _, dir := range []string{*configDir, filepath.Dir(*reportPath), "/scratch/incomplete", "/scratch/complete"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	if err := os.Setenv("WEAVER_INTERMEDIATE_DIR", "/scratch/incomplete"); err != nil {
		return err
	}
	if err := os.Setenv("WEAVER_COMPLETE_DIR", "/scratch/complete"); err != nil {
		return err
	}
	host, nntpPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	for name, value := range map[string]string{
		"WEAVER_SERVER_1_HOSTNAME":    host,
		"WEAVER_SERVER_1_PORT":        nntpPort,
		"WEAVER_SERVER_1_USERNAME":    "fixture",
		"WEAVER_SERVER_1_PASSWORD":    "fixture",
		"WEAVER_SERVER_1_CONNECTIONS": "2",
	} {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}

	port, err := availablePort()
	if err != nil {
		return err
	}
	ini, err := renderConfig(port)
	if err != nil {
		return err
	}
	configPath := filepath.Join(*configDir, "sabnzbd.ini")
	if err := os.WriteFile(configPath, []byte(ini), 0600); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, sabPython, sabMain,
		"--config-file", configPath,
		"--server", "127.0.0.1:"+strconv.Itoa(port),
		"--browser", "0", "--console", "--new",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logs := &observedLog{events: map[string][]time.Time{}}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/api"
	if err := waitReady(client, baseURL, done); err != nil {
		return err
	}
	var versionResponse struct {
		Version string `json:"version"`
	}
	if err := getJSON(client, apiURL(baseURL, "version", nil), &versionResponse); err != nil {
		return err
	}

	report := restartLoopReport{
		SchemaVersion: 1,
		FixtureID:     "rar5-packed-size-lie",
		SABVersion:    versionResponse.Version,
		StartedAt:     startedAt,
		Requested:     *cycles,
	}
	for cycle := 1; cycle <= *cycles; cycle++ {
		fatalTarget := logs.count(assemblerFatalMarker) + 1
		restartTarget := logs.count(assemblerRestartMarker) + 1
		submittedAt := time.Now().UTC()
		nzoID, err := submitData(client, baseURL, fmt.Sprintf("rar5-packed-size-lie-cycle-%02d.nzb", cycle), bundle.Data["input.nzb"])
		if err != nil {
			return fmt.Errorf("cycle %d submit: %w", cycle, err)
		}
		fatalAt, err := logs.wait(assemblerFatalMarker, fatalTarget, 15*time.Second, done)
		if err != nil {
			return fmt.Errorf("cycle %d: %w", cycle, err)
		}
		restartAt, err := logs.wait(assemblerRestartMarker, restartTarget, 45*time.Second, done)
		if err != nil {
			return fmt.Errorf("cycle %d: %w", cycle, err)
		}
		if err := waitUnavailable(client, baseURL, done); err != nil {
			return fmt.Errorf("cycle %d API shutdown: %w", cycle, err)
		}
		if err := waitReady(client, baseURL, done); err != nil {
			return fmt.Errorf("cycle %d API recovery: %w", cycle, err)
		}
		recoveredAt := time.Now().UTC()
		report.Cycles = append(report.Cycles, restartLoopCycle{
			Cycle: cycle, NZOID: nzoID, SubmittedAt: submittedAt,
			FatalAt: fatalAt, RestartAt: restartAt, RecoveredAt: recoveredAt,
		})
		report.Completed = cycle
		fmt.Printf("REPRO cycle=%d nzo_id=%s fatal=%s restart=%s recovered=%s\n", cycle, nzoID, fatalAt.Format(time.RFC3339Nano), restartAt.Format(time.RFC3339Nano), recoveredAt.Format(time.RFC3339Nano))
	}
	report.FinishedAt = time.Now().UTC()
	report.FatalCount = logs.count(assemblerFatalMarker)
	report.RestartCount = logs.count(assemblerRestartMarker)
	stopResponder()
	report.Responder = server.Stats()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*reportPath, append(encoded, '\n'), 0600); err != nil {
		return err
	}
	return nil
}

func waitUnavailable(client *http.Client, baseURL string, done <-chan error) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("SABnzbd exited before its API shutdown was observed: %w", err)
		default:
		}
		var response struct {
			Version string `json:"version"`
		}
		if err := getJSON(client, apiURL(baseURL, "version", nil), &response); err != nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("SABnzbd API did not become unavailable during restart")
}

func submitData(client *http.Client, baseURL, name string, data []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("name", name)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, bytes.NewReader(data)); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodPost, apiURL(baseURL, "addfile", nil), &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	var response struct {
		Status bool     `json:"status"`
		IDs    []string `json:"nzo_ids"`
	}
	if err := doJSON(client, request, &response); err != nil {
		return "", err
	}
	if !response.Status || len(response.IDs) != 1 || response.IDs[0] == "" {
		return "", fmt.Errorf("SABnzbd rejected NZB %q", name)
	}
	return response.IDs[0], nil
}
