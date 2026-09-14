//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/adversarial"
)

func TestClientCommandUsesFreshPathsAndResourceCaps(t *testing.T) {
	args := clientCommand("/scratch/one", "/results/one/bundle", clientEnvironment(8119, true, false))
	for _, required := range []string{"/usr/bin/env", "-i", "/usr/bin/choom", "1000", "--core=0", "--fsize=67108864", "--cpu=30", "WEAVER_DATA_DIR=/scratch/one/data", "/scratch/one/terminal.json"} {
		if !slices.Contains(args, required) {
			t.Fatal(required)
		}
	}
}

func TestClientEnvironmentRecordsPipeliningMode(t *testing.T) {
	if !slices.Contains(clientEnvironment(8119, true, false), "WEAVER_SERVER_1_PIPELINING=true") {
		t.Fatal("pipelining mode missing")
	}
	if !slices.Contains(clientEnvironment(8119, false, false), "WEAVER_SERVER_1_PIPELINING=false") {
		t.Fatal("sequential mode missing")
	}
	if !slices.Contains(clientEnvironment(8119, false, true), "RUST_LOG=info,weaver_server_core::pipeline::download=debug") {
		t.Fatal("download debug mode missing")
	}
}

func TestClientVersionMustBeExplicitAndExact(t *testing.T) {
	if err := validateClientVersion("weaver 0.11.3", ""); err == nil {
		t.Fatal("missing expected version accepted")
	}
	if err := validateClientVersion("weaver 0.11.3", "weaver 0.11.2"); err == nil {
		t.Fatal("version mismatch accepted")
	}
	if err := validateClientVersion("weaver 0.11.3", " weaver 0.11.3 "); err != nil {
		t.Fatal(err)
	}
}

func TestClientIdentityOverrideMustBeSHA256(t *testing.T) {
	valid := "64c4c2b6ed546237451cbfec33aa8bac1396865c1a266dd247c02b36ffe27c62"
	if got, err := validateIdentity(" " + valid + " "); err != nil || got != valid {
		t.Fatal(got, err)
	}
	for _, invalid := range []string{"", "xyz", valid[:62]} {
		if _, err := validateIdentity(invalid); err == nil {
			t.Fatalf("invalid identity accepted: %q", invalid)
		}
	}
}

func TestScratchCapacityRequiresExplicitLowDiskMode(t *testing.T) {
	if err := validateScratchCapacity(normalScratchFreeFloor, false); err != nil {
		t.Fatal(err)
	}
	if err := validateScratchCapacity(normalScratchFreeFloor-1, false); err == nil {
		t.Fatal("low scratch accepted without override")
	}
	if err := validateScratchCapacity(1, true); err != nil {
		t.Fatal(err)
	}
}

func TestNonzeroExitClassificationFollowsCaseExpectation(t *testing.T) {
	for _, test := range []struct {
		expectation adversarial.Expectation
		behavior    string
		review      bool
	}{
		{adversarial.Reject, "rejected", false},
		{adversarial.Contain, "contained_rejection", false},
		{adversarial.Accept, "nonzero_exit_requires_classification", true},
		{adversarial.Recover, "nonzero_exit_requires_classification", true},
	} {
		behavior, review := classifyNonzeroExit(test.expectation, false)
		if behavior != test.behavior || review != test.review {
			t.Fatalf("%s: got %s/%t", test.expectation, behavior, review)
		}
	}
	if behavior, review := classifyNonzeroExit(adversarial.Accept, true); behavior != "unsupported" || review {
		t.Fatal(behavior, review)
	}
}

func TestExpectedStallDoesNotHideOtherReviewReasons(t *testing.T) {
	safe := result{TimedOut: true, ExitCode: -1, Security: "inconclusive"}
	classifyNonterminal(&safe, true)
	if safe.Behavior != "contained_stall" || safe.ReviewRequired {
		t.Fatal(safe.Behavior, safe.ReviewRequired)
	}
	unsafe := result{TimedOut: true, ExitCode: -1, Security: "potential_finding", ReviewRequired: true}
	classifyNonterminal(&unsafe, true)
	if unsafe.Behavior != "did_not_finish" || !unsafe.ReviewRequired {
		t.Fatal(unsafe.Behavior, unsafe.ReviewRequired)
	}
	undeclared := result{TimedOut: true, ExitCode: -1, Security: "inconclusive"}
	classifyNonterminal(&undeclared, false)
	if undeclared.Behavior != "did_not_finish" || !undeclared.ReviewRequired {
		t.Fatal(undeclared.Behavior, undeclared.ReviewRequired)
	}
}

func TestUnsupportedDoesNotHideReviewReasons(t *testing.T) {
	safe := result{Behavior: "completed_policy_dependent"}
	classifyUnsupported(&safe, true)
	if safe.Behavior != "unsupported" {
		t.Fatal(safe.Behavior)
	}
	unsafe := result{Behavior: "unexpected_acceptance", ReviewRequired: true}
	classifyUnsupported(&unsafe, true)
	if unsafe.Behavior != "unexpected_acceptance" || !unsafe.ReviewRequired {
		t.Fatal(unsafe.Behavior, unsafe.ReviewRequired)
	}
}

func TestScratchCanariesDetectEscapeAndCommandExecution(t *testing.T) {
	root := t.TempDir()
	if err := initializeScratchCanaries(root); err != nil {
		t.Fatal(err)
	}
	if err := checkScratchCanaries(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "escape.canary"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkScratchCanaries(root); err == nil {
		t.Fatal("changed escape canary accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "escape.canary"), []byte(scratchEscapeCanary), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "execution.canary"), []byte("executed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkScratchCanaries(root); err == nil {
		t.Fatal("command execution canary accepted")
	}
}

func TestSelectOutputRootAllowsOnlyEmptyInternalStaging(t *testing.T) {
	complete := t.TempDir()
	if err := os.Mkdir(filepath.Join(complete, "input"), 0700); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(complete, ".weaver-staging")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	path, label, err := selectOutputRoot(complete)
	if err != nil || path != filepath.Join(complete, "input") || label != "input" {
		t.Fatal(path, label, err)
	}
	if err := os.WriteFile(filepath.Join(staging, "leftover"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectOutputRoot(complete); err == nil {
		t.Fatal("non-empty staging directory accepted")
	}
}

func TestSelectOutputRootAllowsOnlyEmptyNumericDirectUnpackJobs(t *testing.T) {
	complete := t.TempDir()
	if err := os.Mkdir(filepath.Join(complete, "input"), 0700); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(complete, ".weaver-direct-unpack")
	if err := os.MkdirAll(filepath.Join(scratch, "10000"), 0700); err != nil {
		t.Fatal(err)
	}
	if path, _, err := selectOutputRoot(complete); err != nil || path != filepath.Join(complete, "input") {
		t.Fatal(path, err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "10000", "leftover"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectOutputRoot(complete); err == nil {
		t.Fatal("non-empty direct-unpack job directory accepted")
	}
}

func TestIsolationRefusesWritableHostMounts(t *testing.T) {
	base := "1 0 0:1 / / rw - tmpfs tmpfs rw\n2 1 0:2 / /scratch rw - tmpfs tmpfs rw\n"
	if err := validateMounts(base); err != nil {
		t.Fatal(err)
	}
	readOnlyRoot := "1 0 0:1 / / ro - overlay overlay ro\n2 1 0:2 / /scratch rw - tmpfs tmpfs rw\n"
	if err := validateMounts(readOnlyRoot); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"3 1 8:1 /home/user /results rw - ext4 /dev/disk rw\n", "3 1 0:1 / /host rw - overlay overlay rw\n"} {
		if err := validateMounts(base + extra); err == nil {
			t.Fatal("host mount accepted")
		}
	}
	if err := validateMounts("1 0 8:1 / / rw - ext4 disk rw\n"); err == nil {
		t.Fatal("host root accepted")
	}
}
func TestBoundedLogReportsTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := boundedLog{file: f, limit: 4}
	if n, err := b.Write([]byte("abcdefgh")); n != 8 || err != nil {
		t.Fatal(n, err)
	}
	if !b.truncated || b.size != 4 {
		t.Fatal(b.size)
	}
	raw, err := os.ReadFile(p)
	if err != nil || string(raw) != "abcd" {
		t.Fatal(string(raw), err)
	}
}
