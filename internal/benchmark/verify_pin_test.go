package benchmark

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func writeExternalVerificationManifest(t *testing.T, fixtureDir string) {
	t.Helper()
	manifest := fixture.GeneratedManifest{
		SchemaVersion: 3,
		Case:          fixture.ArchiveCase{ID: "external"},
		ArchiveFiles:  []fixture.FileDigest{{Path: "post.part01.rar", Size: 1}, {Path: "post.par2", Size: 1}},
		External:      &fixture.ExternalPostDetails{ArticleRawBytes: 700 << 10},
	}
	contents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "fixture-manifest.json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeOutputFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyOutputPinsTheFirstExternalOutputAndHoldsLaterRunsToIt(t *testing.T) {
	fixtureDir := t.TempDir()
	writeExternalVerificationManifest(t, fixtureDir)
	payload := bytes.Repeat([]byte("payload-"), (minimumPinnedFileBytes/8)+1)

	first := t.TempDir()
	writeOutputFile(t, filepath.Join(first, "job", "test.bin"), payload)
	// Left-behind post files, client bookkeeping and hidden files are not
	// what the post extracts to, and must not become part of the oracle.
	writeOutputFile(t, filepath.Join(first, "job", "post.part01.rar"), payload)
	writeOutputFile(t, filepath.Join(first, "job", "client.log"), []byte("small"))
	writeOutputFile(t, filepath.Join(first, "job", ".marker"), payload)

	pinned, err := VerifyOutput(fixtureDir, first)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Reference != ReferencePinnedHere || len(pinned.Files) != 1 || pinned.Files[0].ActualPath != "job/test.bin" {
		t.Fatalf("first verification = %#v, want only the extracted file pinned here", pinned)
	}
	if _, err := os.Stat(filepath.Join(fixtureDir, fixture.PinnedOutputName)); err != nil {
		t.Fatalf("no pin was written: %v", err)
	}

	second := t.TempDir()
	writeOutputFile(t, filepath.Join(second, "renamed-job", "test.bin"), payload)
	matched, err := VerifyOutput(fixtureDir, second)
	if err != nil {
		t.Fatal(err)
	}
	if matched.Reference != ReferencePinned {
		t.Fatalf("second verification reference = %q, want %q", matched.Reference, ReferencePinned)
	}

	corrupt := t.TempDir()
	damaged := append([]byte(nil), payload...)
	damaged[0] ^= 0xff
	writeOutputFile(t, filepath.Join(corrupt, "test.bin"), damaged)
	if _, err := VerifyOutput(fixtureDir, corrupt); err == nil {
		t.Fatal("output that differs from the pin verified")
	}
}

func TestVerifyOutputRefusesToPinAnOutputWithNothingExtracted(t *testing.T) {
	fixtureDir := t.TempDir()
	writeExternalVerificationManifest(t, fixtureDir)
	output := t.TempDir()
	writeOutputFile(t, filepath.Join(output, "post.part01.rar"), bytes.Repeat([]byte("x"), minimumPinnedFileBytes))
	writeOutputFile(t, filepath.Join(output, "notes.txt"), []byte("small"))
	if _, err := VerifyOutput(fixtureDir, output); err == nil || !strings.Contains(err.Error(), "nothing extracted") {
		t.Fatalf("error = %v, want a refusal to pin", err)
	}
	if _, err := os.Stat(filepath.Join(fixtureDir, fixture.PinnedOutputName)); !os.IsNotExist(err) {
		t.Fatalf("a refused pin still wrote %s: %v", fixture.PinnedOutputName, err)
	}
}

func TestVerifyOutputAcceptsASmallPinnedMemberUnderAClientChosenName(t *testing.T) {
	fixtureDir := t.TempDir()
	writeExternalVerificationManifest(t, fixtureDir)
	payload := bytes.Repeat([]byte("payload-"), (minimumPinnedFileBytes/8)+1)
	// Archives carry small files too: a readme beside the payload is below the
	// floor that keeps client bookkeeping out of the oracle.
	readme := []byte("what this post is")

	first := t.TempDir()
	writeOutputFile(t, filepath.Join(first, "job", "test.bin"), payload)
	writeOutputFile(t, filepath.Join(first, "job", "test-explanation.txt"), readme)
	pinned, err := VerifyOutput(fixtureDir, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Files) != 1 {
		t.Fatalf("pinned %d required files, want only the payload", len(pinned.Files))
	}

	// A client that names the small file after its own job still produced the
	// bytes the post carried, so the run stands.
	renamed := t.TempDir()
	writeOutputFile(t, filepath.Join(renamed, "sab-job", "test.bin"), payload)
	writeOutputFile(t, filepath.Join(renamed, "sab-job", "sab-job-explanation.txt"), readme)
	matched, err := VerifyOutput(fixtureDir, renamed)
	if err != nil {
		t.Fatalf("a renamed small member failed the run: %v", err)
	}
	if len(matched.SmallMembers) != 1 || matched.SmallMembers[0].ActualPath != "sab-job/sab-job-explanation.txt" {
		t.Fatalf("small members = %#v, want the renamed copy", matched.SmallMembers)
	}

	// Leaving it out is allowed; leaving something else behind is not.
	absent := t.TempDir()
	writeOutputFile(t, filepath.Join(absent, "job", "test.bin"), payload)
	if _, err := VerifyOutput(fixtureDir, absent); err != nil {
		t.Fatalf("a client that deleted the small member failed the run: %v", err)
	}
	foreign := t.TempDir()
	writeOutputFile(t, filepath.Join(foreign, "job", "test.bin"), payload)
	writeOutputFile(t, filepath.Join(foreign, "job", "test-explanation.txt"), []byte("something else"))
	if _, err := VerifyOutput(fixtureDir, foreign); err == nil {
		t.Fatal("a small file the oracle never saw passed verification")
	}
}
