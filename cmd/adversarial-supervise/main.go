//go:build !windows

// adversarial-supervise is an exploratory, supervised Weaver CLI runner.
// It deliberately does not manufacture complete security-observer evidence.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scryer-media/usenet-bench/internal/adversarial"
	"github.com/zeebo/blake3"
)

type boundedLog struct {
	mu          sync.Mutex
	file        *os.File
	size, limit int
	truncated   bool
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	keep := min(n, b.limit-b.size)
	if keep < n {
		b.truncated = true
	}
	if keep > 0 {
		if _, err := b.file.Write(p[:keep]); err != nil {
			return 0, err
		}
		b.size += keep
	}
	return n, nil
}

type entry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}
type result struct {
	SchemaVersion     int                     `json:"schema_version"`
	RecipeVersion     string                  `json:"recipe_version"`
	Case              adversarial.Case        `json:"case"`
	ManifestSHA256    string                  `json:"manifest_sha256"`
	ClientKind        string                  `json:"client_kind"`
	ClientVersion     string                  `json:"client_version"`
	ClientSHA256      string                  `json:"client_sha256"`
	StartedAt         time.Time               `json:"started_at"`
	FinishedAt        time.Time               `json:"finished_at"`
	WallMilliseconds  int64                   `json:"wall_milliseconds"`
	ExitCode          int                     `json:"exit_code"`
	TerminationSignal string                  `json:"termination_signal,omitempty"`
	TimedOut          bool                    `json:"timed_out"`
	LogTruncated      bool                    `json:"log_truncated"`
	TraceTruncated    bool                    `json:"trace_truncated"`
	LogSHA256         string                  `json:"log_sha256"`
	TraceSHA256       string                  `json:"trace_sha256"`
	ClientLog         []byte                  `json:"client_log_base64"`
	TraceLog          []byte                  `json:"trace_log_base64"`
	ConfigSHA256      string                  `json:"config_sha256"`
	ServerPipelining  bool                    `json:"server_pipelining"`
	DownloadDebug     bool                    `json:"download_debug"`
	ScratchFreeBytes  uint64                  `json:"scratch_free_bytes"`
	LowScratch        bool                    `json:"low_scratch_override"`
	Unsupported       bool                    `json:"declared_unsupported"`
	ExpectedStall     bool                    `json:"declared_expected_stall"`
	Responder         adversarial.ServerStats `json:"responder"`
	Files             []entry                 `json:"files"`
	OutputRoot        string                  `json:"output_root"`
	OutputError       string                  `json:"output_error,omitempty"`
	DeclaredMetadata  map[string]string       `json:"declared_metadata_sha256,omitempty"`
	Behavior          string                  `json:"behavior"`
	Security          string                  `json:"security"`
	ReviewRequired    bool                    `json:"review_required"`
	Notes             []string                `json:"notes"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	f := flag.NewFlagSet("adversarial-supervise", flag.ContinueOnError)
	cases := f.String("cases", "", "explicit comma-separated case IDs, at most 64; runs stop on review-required findings")
	family := f.String("family", "", "run one exact catalog family, mutually exclusive with --cases")
	destination := f.String("output", "/results", "new private evidence directory inside the outer sandbox")
	wall := f.Duration("wall-limit", 60*time.Second, "per-case elapsed limit, at most 60 seconds")
	pipelining := f.Bool("server-pipelining", true, "declare the synthetic NNTP server as pipelining-capable")
	debugDownload := f.Bool("debug-download", false, "enable Weaver download-pipeline debug logs")
	allowLowScratch := f.Bool("allow-low-scratch", false, "permit deliberate low-disk regression runs")
	expectedVersion := f.String("expected-client-version", "", "required exact supervised client --version output")
	clientKind := f.String("client-kind", "weaver", "supervised client contract: weaver or sabnzbd")
	identityOverride := f.String("client-identity-sha256", "", "optional image or distribution SHA-256 in place of the launcher file hash")
	unsupportedCSV := f.String("unsupported-cases", "", "explicit comma-separated cases excluded by a separately proven client capability gap")
	expectedStallCSV := f.String("expected-stall-cases", "", "explicit comma-separated contain cases expected to remain queued at the wall limit")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *wall <= 0 || *wall > 60*time.Second {
		return fmt.Errorf("invalid supervised run options")
	}
	if *clientKind != "weaver" && *clientKind != "sabnzbd" {
		return fmt.Errorf("client kind must be weaver or sabnzbd")
	}
	if err := requireIsolation(); err != nil {
		return err
	}
	scratchFree, err := availableBytes("/scratch")
	if err != nil {
		return fmt.Errorf("inspect scratch capacity: %w", err)
	}
	if err := validateScratchCapacity(scratchFree, *allowLowScratch); err != nil {
		return err
	}
	if err := initializeScratchCanaries("/scratch"); err != nil {
		return fmt.Errorf("initialize fixed exploit canaries: %w", err)
	}
	if !strings.HasPrefix(filepath.Clean(*destination), "/scratch/") {
		return fmt.Errorf("evidence must remain in /scratch tmpfs and stream to the external supervisor")
	}
	var ids []string
	switch {
	case *family != "" && *cases != "":
		return fmt.Errorf("--family and --cases are mutually exclusive")
	case *family != "":
		for _, candidate := range adversarial.Catalog() {
			if candidate.Family == *family {
				ids = append(ids, candidate.ID)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("unknown or empty case family %q", *family)
		}
	case *cases != "":
		ids = strings.Split(*cases, ",")
	default:
		ids = []string{"control-yenc"}
	}
	if len(ids) > 64 {
		return fmt.Errorf("at most 64 cases per supervised batch")
	}
	seen := map[string]bool{}
	unsupported := map[string]bool{}
	expectedStall := map[string]bool{}
	if *unsupportedCSV != "" {
		for _, id := range strings.Split(*unsupportedCSV, ",") {
			if id == "" || unsupported[id] {
				return fmt.Errorf("invalid duplicate or empty unsupported case")
			}
			unsupported[id] = true
		}
	}
	if *expectedStallCSV != "" {
		for _, id := range strings.Split(*expectedStallCSV, ",") {
			if id == "" || expectedStall[id] {
				return fmt.Errorf("invalid duplicate or empty expected-stall case")
			}
			expectedStall[id] = true
		}
	}
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate case %s", id)
		}
		seen[id] = true
		bundle, err := adversarial.Generate(id)
		if err != nil {
			return err
		}
		if expectedStall[id] && bundle.Manifest.Case.Expectation != adversarial.Contain {
			return fmt.Errorf("expected-stall declaration requires a contain case: %s", id)
		}
	}
	for id := range unsupported {
		if !seen[id] {
			return fmt.Errorf("unsupported case is not in this batch: %s", id)
		}
	}
	for id := range expectedStall {
		if !seen[id] {
			return fmt.Errorf("expected-stall case is not in this batch: %s", id)
		}
	}
	if err := os.Mkdir(*destination, 0700); err != nil {
		return fmt.Errorf("evidence destination must be new: %w", err)
	}
	versionCmd := exec.Command("/opt/weaver", "--version")
	versionRaw, err := versionCmd.Output()
	if err != nil {
		return err
	}
	version := strings.TrimSpace(string(versionRaw))
	if err := validateClientVersion(version, *expectedVersion); err != nil {
		return err
	}
	identity, err := hashFile("/opt/weaver")
	if err != nil {
		return err
	}
	if *identityOverride != "" {
		identity, err = validateIdentity(*identityOverride)
		if err != nil {
			return err
		}
	}
	for _, id := range ids {
		r, err := runCase(id, *destination, *wall, *clientKind, version, identity, *pipelining, *debugDownload, scratchFree, *allowLowScratch, unsupported[id], expectedStall[id])
		if err != nil {
			return fmt.Errorf("case %s: %w", id, err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
			return err
		}
		if r.ReviewRequired {
			return fmt.Errorf("case %s requires human review; stopped before the next hostile case", id)
		}
	}
	return nil
}

func validateIdentity(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("client identity must be a 64-character SHA-256")
	}
	return value, nil
}

func validateClientVersion(actual, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return fmt.Errorf("--expected-client-version is required")
	}
	if actual != expected {
		return fmt.Errorf("expected exact client version %q, got %q", expected, actual)
	}
	return nil
}

const normalScratchFreeFloor = 640 << 20

func availableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func validateScratchCapacity(available uint64, allowLow bool) error {
	if available < normalScratchFreeFloor && !allowLow {
		return fmt.Errorf("scratch has %d free bytes; normal runs require at least %d so Weaver's 512 MiB UU-spool floor cannot throttle yEnc controls (use --allow-low-scratch only for the deliberate regression)", available, normalScratchFreeFloor)
	}
	return nil
}

func requireIsolation() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("supervised runner requires Linux bubblewrap isolation")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, n := range interfaces {
		if n.Flags&net.FlagLoopback == 0 {
			return fmt.Errorf("refusing a network namespace with non-loopback interface %s", n.Name)
		}
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	return validateMounts(string(mounts))
}

func validateMounts(mounts string) error {
	rootContained, scratchTmpfs := false, false
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if strings.Contains(","+fields[5]+",", ",rw,") && !strings.Contains(line, " - tmpfs ") && fields[4] != "/proc" && !strings.HasPrefix(fields[4], "/proc/") && fields[4] != "/dev" && !strings.HasPrefix(fields[4], "/dev/") {
			return fmt.Errorf("refusing writable non-tmpfs mount %s", fields[4])
		}
		if fields[4] == "/" {
			rootContained = strings.Contains(line, " - tmpfs ") || strings.Contains(","+fields[5]+",", ",ro,")
		}
		if strings.Contains(line, " - tmpfs ") {
			scratchTmpfs = scratchTmpfs || fields[4] == "/scratch"
		}
	}
	if !rootContained || !scratchTmpfs {
		return fmt.Errorf("refusing to run outside a read-only or tmpfs-root sandbox with /scratch tmpfs")
	}
	return nil
}

func clientEnvironment(port int, pipelining, debugDownload bool) []string {
	env := []string{
		"PATH=/opt:/usr/local/bin:/lsiopy/bin:/usr/bin:/bin", "HOME=/job/home", "TMPDIR=/job/tmp", "LANG=C", "RUST_BACKTRACE=1", "PYTHONDONTWRITEBYTECODE=1",
		"WEAVER_DATA_DIR=/job/data", "WEAVER_INTERMEDIATE_DIR=/job/incomplete", "WEAVER_COMPLETE_DIR=/job/complete",
		"WEAVER_PROPAGATION_DELAY_SECS=0", "WEAVER_STARTUP_IOPS=50000", "WEAVER_CLEANUP_AFTER_EXTRACT=true", "WEAVER_DIRECT_UNPACK=on",
		"WEAVER_SERVER_1_HOSTNAME=127.0.0.1", "WEAVER_SERVER_1_PORT=" + strconv.Itoa(port), "WEAVER_SERVER_1_TLS=false",
		"WEAVER_SERVER_1_USERNAME=benchmark", "WEAVER_SERVER_1_PASSWORD=synthetic-only", "WEAVER_SERVER_1_CONNECTIONS=2", "WEAVER_SERVER_1_ACTIVE=true", "WEAVER_SERVER_1_PIPELINING=" + strconv.FormatBool(pipelining),
	}
	if debugDownload {
		env = append(env, "RUST_LOG=info,weaver_server_core::pipeline::download=debug")
	}
	return env
}

func clientCommand(root, input string, env []string) []string {
	args := []string{"/usr/bin/env", "-i"}
	for _, e := range env {
		args = append(args, strings.ReplaceAll(e, "/job/", root+"/"))
	}
	// Prefer the untrusted client as the OOM victim so the much smaller observer
	// can still inventory the sandbox and emit a result after a memory attack.
	return append(args, "/usr/bin/choom", "-n", "1000", "--", "/usr/bin/prlimit", "--core=0", "--nofile=256", "--fsize=67108864", "--cpu=30", "--", "/opt/weaver", "--config", filepath.Join(root, "data"), "download", filepath.Join(input, "input.nzb"), "--report", filepath.Join(root, "terminal.json"))
}

func runCase(id, destination string, wall time.Duration, clientKind, version, identity string, pipelining, debugDownload bool, scratchFree uint64, lowScratch, unsupported, expectedStall bool) (result, error) {
	r := result{SchemaVersion: 1, ClientKind: clientKind, ClientVersion: version, ClientSHA256: identity, ServerPipelining: pipelining, DownloadDebug: debugDownload, ScratchFreeBytes: scratchFree, LowScratch: lowScratch, Unsupported: unsupported, ExpectedStall: expectedStall, Behavior: "inconclusive", Security: "inconclusive", Notes: []string{"exploratory CLI run, not a complete security observation", "outer cgroup caps include supervisor; per-client memory and cumulative writes are not measured", "no sanitizer, secret-access detector or same-process queue-liveness evidence", "network isolation prevents external egress; rejected connection attempts require trace review"}}
	r.Notes = append(r.Notes, "client and diagnostic observer share the disposable namespace: evidence is NOT tamper-resistant; no host directories are mounted writable")
	if lowScratch {
		r.Notes = append(r.Notes, "low-scratch override enabled: this is a deliberate resource-regression run, not a normal fixture result")
	}
	if unsupported {
		r.Notes = append(r.Notes, "client capability gap declared explicitly; this exclusion requires a separately proven valid control")
	}
	if expectedStall {
		r.Notes = append(r.Notes, "bounded-rate nonterminal provider behavior declared explicitly; timeout remains security-inconclusive")
	}
	b, err := adversarial.Generate(id)
	if err != nil {
		return r, err
	}
	r.Case = b.Manifest.Case
	r.RecipeVersion = b.Manifest.RecipeVersion
	r.ManifestSHA256 = adversarial.ManifestDigest(b.Manifest)
	evidence := filepath.Join(destination, id)
	if err := os.Mkdir(evidence, 0700); err != nil {
		return r, err
	}
	input, err := adversarial.Export(evidence, b)
	if err != nil {
		return r, err
	}
	root, err := os.MkdirTemp("/scratch", "case-")
	if err != nil {
		return r, err
	}
	// Retain the generated root for this namespace's lifetime. Only the outer
	// sandbox teardown removes it; no recursive host-path cleanup is needed.
	for _, dir := range []string{"data", "incomplete", "complete", "home", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			return r, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "escape.canary"), []byte("unchanged supervision canary\n"), 0600); err != nil {
		return r, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return r, err
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	s := &adversarial.Server{Bundle: b, RunID: id}
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Serve(serverCtx, listener) }()
	env := clientEnvironment(listener.Addr().(*net.TCPAddr).Port, pipelining, debugDownload)
	commandArgs := clientCommand(root, input, env)
	canonicalEnv := clientEnvironment(0, pipelining, debugDownload)
	configRaw, _ := json.Marshal(canonicalEnv)
	r.ConfigSHA256 = adversarial.Digest(configRaw)
	if err := saveJSON(filepath.Join(evidence, "launch.json"), map[string]any{"command": commandArgs, "environment": env}); err != nil {
		return r, err
	}
	logFile, err := os.OpenFile(filepath.Join(evidence, "client.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return r, err
	}
	defer logFile.Close()
	traceFile, err := os.OpenFile(filepath.Join(evidence, "syscalls.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return r, err
	}
	defer traceFile.Close()
	logs := &boundedLog{file: logFile, limit: 2 << 20}
	trace := &boundedLog{file: traceFile, limit: 16 << 20}
	ctx, cancel := context.WithTimeout(context.Background(), wall)
	defer cancel()
	traceArgs := []string{"--kill-on-exit", "-f", "-qq", "-s", "256", "-e", "trace=%file,%process,%network", "-o", "/dev/fd/3"}
	traceArgs = append(traceArgs, commandArgs...)
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return r, err
	}
	defer readPipe.Close()
	cmd := exec.CommandContext(ctx, "/usr/bin/strace", traceArgs...)
	cmd.Dir = root
	cmd.ExtraFiles = []*os.File{writePipe}
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.WaitDelay = 3 * time.Second
	traceDone := make(chan error, 1)
	go func() { _, err := io.Copy(trace, readPipe); traceDone <- err }()
	r.StartedAt = time.Now()
	err = cmd.Start()
	_ = writePipe.Close()
	if err != nil {
		_ = readPipe.Close()
		<-traceDone
		stopServer()
		<-serverDone
		return r, err
	}
	waitErr := cmd.Wait()
	r.WallMilliseconds = time.Since(r.StartedAt).Milliseconds()
	r.FinishedAt = time.Now()
	r.TimedOut = ctx.Err() != nil
	r.ExitCode = cmd.ProcessState.ExitCode()
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		r.TerminationSignal = status.Signal().String()
	}
	_ = readPipe.SetReadDeadline(time.Now().Add(time.Second))
	traceErr := <-traceDone
	if traceErr != nil {
		r.Notes = append(r.Notes, "trace pipe: "+traceErr.Error())
	}
	stopServer()
	if err := <-serverDone; err != nil {
		r.Notes = append(r.Notes, "responder: "+err.Error())
	}
	r.Responder = s.Stats()
	r.LogTruncated = logs.truncated
	r.TraceTruncated = trace.truncated
	_ = logFile.Close()
	_ = traceFile.Close()
	r.LogSHA256, _ = hashFile(filepath.Join(evidence, "client.log"))
	r.TraceSHA256, _ = hashFile(filepath.Join(evidence, "syscalls.log"))
	r.ClientLog, _ = os.ReadFile(filepath.Join(evidence, "client.log"))
	r.TraceLog, _ = os.ReadFile(filepath.Join(evidence, "syscalls.log"))
	r.Files, err = inventory(root)
	if err != nil {
		r.Notes = append(r.Notes, "inventory: "+err.Error())
		r.ReviewRequired = true
	}
	canary, err := os.ReadFile(filepath.Join(root, "escape.canary"))
	if err != nil || string(canary) != "unchanged supervision canary\n" {
		r.ReviewRequired = true
		r.Security = "potential_finding"
		r.Notes = append(r.Notes, "outside-output canary changed")
	}
	if err := checkScratchCanaries("/scratch"); err != nil {
		r.ReviewRequired = true
		r.Security = "potential_finding"
		r.Notes = append(r.Notes, "fixed exploit canary: "+err.Error())
	}
	complete := filepath.Join(root, "complete")
	r.OutputRoot = "complete"
	selected, label, selectErr := selectOutputRoot(complete)
	if selectErr != nil {
		r.OutputError = selectErr.Error()
		r.ReviewRequired = true
	} else {
		complete = selected
		r.OutputRoot = filepath.Join("complete", label)
	}
	if err := adversarial.InspectOutput(complete, b.Manifest.Limits, b.Manifest.Case.Expectation == adversarial.Reject); err != nil {
		r.OutputError = err.Error()
		r.ReviewRequired = true
	}
	var terminal struct {
		SchemaVersion int    `json:"schema_version"`
		Status        string `json:"status"`
	}
	reportRaw, reportErr := os.ReadFile(filepath.Join(root, "terminal.json"))
	if reportErr == nil {
		reportErr = json.Unmarshal(reportRaw, &terminal)
	}
	if r.TimedOut || r.ExitCode < 0 {
		classifyNonterminal(&r, expectedStall)
	} else if waitErr == nil && reportErr == nil && terminal.Status == "complete" {
		r.Behavior = "completed_policy_dependent"
		if len(b.Manifest.ExpectedOutputs) > 0 {
			if clientKind == "weaver" {
				canonical, err := filepath.EvalSymlinks(complete)
				if err != nil {
					return r, err
				}
				markerDigest := blake3.Sum256([]byte(canonical))
				r.DeclaredMetadata = map[string]string{".weaver-output-dir": adversarial.Digest([]byte(fmt.Sprintf("weaver-output-v1:%x\n", markerDigest)))}
			}
			if err := adversarial.VerifyOutputWithMetadata(complete, b.Manifest.ExpectedOutputs, r.DeclaredMetadata, b.Manifest.Limits); err != nil {
				r.OutputError = err.Error()
				if unsupported {
					r.Behavior = "unsupported"
				} else {
					r.Behavior = "output_mismatch"
					r.ReviewRequired = true
				}
			} else {
				r.Behavior = "verified_output"
			}
		}
		if b.Manifest.Case.Expectation == adversarial.Reject {
			r.Behavior = "unexpected_acceptance"
			r.ReviewRequired = true
		}
	} else if waitErr != nil && r.ExitCode > 0 {
		r.Behavior, r.ReviewRequired = classifyNonzeroExit(b.Manifest.Case.Expectation, unsupported)
	} else {
		r.Behavior = "missing_terminal_outcome"
		r.ReviewRequired = true
	}
	classifyUnsupported(&r, unsupported)
	if err := saveJSON(filepath.Join(evidence, "result.json"), r); err != nil {
		return r, err
	}
	return r, nil
}

const scratchEscapeCanary = "unchanged fixed escape canary\n"

func initializeScratchCanaries(root string) error {
	escape := filepath.Join(root, "escape.canary")
	f, err := os.OpenFile(escape, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(scratchEscapeCanary); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, "execution.canary")); err == nil {
		return fmt.Errorf("execution canary already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func checkScratchCanaries(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "escape.canary"))
	if err != nil || string(raw) != scratchEscapeCanary {
		return fmt.Errorf("escape canary changed")
	}
	if _, err := os.Lstat(filepath.Join(root, "execution.canary")); err == nil {
		return fmt.Errorf("command-execution canary was created")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func classifyUnsupported(r *result, unsupported bool) {
	if unsupported && !r.ReviewRequired {
		r.Behavior = "unsupported"
	}
}

func classifyNonterminal(r *result, expectedStall bool) {
	if expectedStall && r.TimedOut && r.Security == "inconclusive" && r.OutputError == "" && !r.ReviewRequired {
		r.Behavior = "contained_stall"
		return
	}
	r.Behavior = "did_not_finish"
	r.ReviewRequired = true
}

func selectOutputRoot(complete string) (string, string, error) {
	entries, err := os.ReadDir(complete)
	if err != nil {
		return complete, "", err
	}
	var candidate string
	for _, entry := range entries {
		if entry.Name() == ".weaver-staging" {
			if !entry.IsDir() {
				return complete, "", fmt.Errorf("internal staging path is not a directory")
			}
			staging, err := os.ReadDir(filepath.Join(complete, entry.Name()))
			if err != nil {
				return complete, "", err
			}
			if len(staging) != 0 {
				return complete, "", fmt.Errorf("internal staging directory is not empty")
			}
			continue
		}
		if entry.Name() == ".weaver-direct-unpack" {
			if err := validateDirectUnpackScratch(filepath.Join(complete, entry.Name()), entry); err != nil {
				return complete, "", err
			}
			continue
		}
		if !entry.IsDir() || candidate != "" {
			return complete, "", fmt.Errorf("final root must contain exactly one job directory")
		}
		candidate = entry.Name()
	}
	if candidate == "" {
		return complete, "", nil
	}
	return filepath.Join(complete, candidate), candidate, nil
}

func validateDirectUnpackScratch(path string, root os.DirEntry) error {
	if !root.IsDir() {
		return fmt.Errorf("internal direct-unpack path is not a directory")
	}
	jobs, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(jobs) > 64 {
		return fmt.Errorf("internal direct-unpack directory has too many job entries")
	}
	for _, job := range jobs {
		id, err := strconv.ParseUint(job.Name(), 10, 64)
		if err != nil || strconv.FormatUint(id, 10) != job.Name() || !job.IsDir() {
			return fmt.Errorf("unexpected internal direct-unpack entry %q", job.Name())
		}
		children, err := os.ReadDir(filepath.Join(path, job.Name()))
		if err != nil {
			return err
		}
		if len(children) != 0 {
			return fmt.Errorf("internal direct-unpack job directory %q is not empty", job.Name())
		}
	}
	return nil
}

func classifyNonzeroExit(expectation adversarial.Expectation, unsupported bool) (string, bool) {
	if unsupported {
		return "unsupported", false
	}
	switch expectation {
	case adversarial.Reject:
		return "rejected", false
	case adversarial.Contain:
		return "contained_rejection", false
	default:
		return "nonzero_exit_requires_classification", true
	}
}

func inventory(root string) ([]entry, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []entry
	var readBytes int64
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > 64 {
			return fmt.Errorf("depth limit")
		}
		f, err := r.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			entries, err := f.ReadDir(64)
			for _, e := range entries {
				if len(out) >= 8192 {
					return fmt.Errorf("entry limit")
				}
				name := filepath.Join(dir, e.Name())
				info, err := r.Lstat(name)
				if err != nil {
					return err
				}
				item := entry{Path: name, Size: info.Size(), Mode: info.Mode().String()}
				if info.Mode()&os.ModeSymlink != 0 {
					item.Link, _ = r.Readlink(name)
				} else if info.Mode().IsRegular() {
					if info.Size() > 64<<20-readBytes {
						return fmt.Errorf("inventory read budget exceeded at %s", name)
					}
					h := sha256.New()
					f, err := r.Open(name)
					if err != nil {
						return err
					}
					n, readErr := io.Copy(h, io.LimitReader(f, 64<<20-readBytes+1))
					f.Close()
					readBytes += n
					if readErr != nil {
						return readErr
					}
					if readBytes > 64<<20 {
						return fmt.Errorf("inventory read limit")
					}
					item.SHA256 = hex.EncodeToString(h.Sum(nil))
				}
				out = append(out, item)
				if e.IsDir() {
					if err := walk(name, depth+1); err != nil {
						return err
					}
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	err = walk(".", 0)
	return out, err
}
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func saveJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
