package benchmark

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestDeleteOutputFilesRetainsOutputRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "job", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DeleteOutputFiles(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("output root still contains %d entries", len(entries))
	}
}

func TestHashFileUsesBLAKE3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85"
	if got != want {
		t.Fatalf("BLAKE3(abc) = %s, want %s", got, want)
	}
}

func TestVerifyOutputAllowsContentPreservingRename(t *testing.T) {
	fixtureDir := t.TempDir()
	outputDir := t.TempDir()
	contents := []byte("movie payload")
	actualPath := filepath.Join(outputDir, "release-name.mkv")
	if err := os.WriteFile(actualPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(actualPath)
	if err != nil {
		t.Fatal(err)
	}
	writeVerificationManifest(t, fixtureDir, []fixture.FileDigest{{
		Path:   "payload-01.mkv",
		Size:   int64(len(contents)),
		BLAKE3: digest,
	}})

	verification, err := VerifyOutput(fixtureDir, outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(verification.Files) != 1 || verification.Files[0].ActualPath != "release-name.mkv" {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestVerifyOutputRejectsOneFlattenedFileForTwoExpectedMembers(t *testing.T) {
	fixtureDir := t.TempDir()
	outputDir := t.TempDir()
	contents := []byte("certificate")
	actualPath := filepath.Join(outputDir, "id.bdmv")
	if err := os.WriteFile(actualPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(actualPath)
	if err != nil {
		t.Fatal(err)
	}
	writeVerificationManifest(t, fixtureDir, []fixture.FileDigest{
		{Path: "CERTIFICATE/id.bdmv", Size: int64(len(contents)), BLAKE3: digest},
		{Path: "CERTIFICATE/BACKUP/id.bdmv", Size: int64(len(contents)), BLAKE3: digest},
	})

	if _, err := VerifyOutput(fixtureDir, outputDir); err == nil {
		t.Fatal("one flattened output file satisfied two expected members")
	}
}

func writeVerificationManifest(t *testing.T, fixtureDir string, expected []fixture.FileDigest) {
	t.Helper()
	manifest := fixture.GeneratedManifest{
		SchemaVersion: 3,
		Case:          fixture.ArchiveCase{ID: "verification"},
		ExpectedFiles: expected,
		ArchiveFiles:  []fixture.FileDigest{{Path: "archive/fixture.part01.rar", Size: 1, BLAKE3: "fixture"}},
	}
	contents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "fixture-manifest.json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repair lane posts recovery material on top of the archive, and the clients
// differ on whether they delete it once the repair is done. Weaver and NZBGet
// both keep the RAR recovery volume; SABnzbd has no reader for one at all. A
// retained copy is accepted where it still carries the bytes it was posted
// under, and nothing else is.
func TestVerifyOutputAcceptsRetainedPostedRecoveryVolume(t *testing.T) {
	fixtureDir := t.TempDir()
	outputDir := t.TempDir()
	payload := []byte("repaired payload")
	payloadPath := filepath.Join(outputDir, "payload-01.mkv")
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	payloadDigest, err := hashFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	recovery := []byte("recovery volume bytes")
	recoveryPath := filepath.Join(outputDir, "fixture.part1.rev")
	if err := os.WriteFile(recoveryPath, recovery, 0o644); err != nil {
		t.Fatal(err)
	}
	recoveryDigest, err := hashFile(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}

	writeRepairManifest(t, fixtureDir,
		[]fixture.FileDigest{{Path: "payload-01.mkv", Size: int64(len(payload)), BLAKE3: payloadDigest}},
		[]fixture.FileDigest{{Path: "archive/fixture.part1.rar", Size: 1, BLAKE3: "volume"}},
		[]fixture.FileDigest{
			{Path: "archive/fixture.part1.rar", Size: 1, BLAKE3: "volume"},
			{Path: "archive/fixture.part1.rev", Size: int64(len(recovery)), BLAKE3: recoveryDigest},
		})

	verification, err := VerifyOutput(fixtureDir, outputDir)
	if err != nil {
		t.Fatalf("a retained recovery volume failed verification: %v", err)
	}
	if len(verification.RetainedRepairMaterial) != 1 ||
		verification.RetainedRepairMaterial[0].ExpectedPath != "archive/fixture.part1.rev" {
		t.Fatalf("recovery volume not recorded: %#v", verification.RetainedRepairMaterial)
	}

	// A client that deletes it is equally correct: the material is accepted,
	// never required.
	if err := os.Remove(recoveryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyOutput(fixtureDir, outputDir); err != nil {
		t.Fatalf("deleting the recovery volume failed verification: %v", err)
	}

	// Bytes that are not what was posted stay a failure, under any name.
	if err := os.WriteFile(recoveryPath, []byte("rewritten by the client"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyOutput(fixtureDir, outputDir); err == nil {
		t.Fatal("a rewritten recovery volume passed verification")
	}
}

// The damaged volume of a repair lane keeps its source path and only changes
// its bytes, so it is not recovery material and a client may not leave it.
func TestVerifyOutputStillRejectsARetainedDamagedVolume(t *testing.T) {
	fixtureDir := t.TempDir()
	outputDir := t.TempDir()
	payload := []byte("repaired payload")
	payloadPath := filepath.Join(outputDir, "payload-01.mkv")
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	payloadDigest, err := hashFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	damaged := []byte("damaged volume")
	damagedPath := filepath.Join(outputDir, "fixture.part3.rar")
	if err := os.WriteFile(damagedPath, damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	damagedDigest, err := hashFile(damagedPath)
	if err != nil {
		t.Fatal(err)
	}

	writeRepairManifest(t, fixtureDir,
		[]fixture.FileDigest{{Path: "payload-01.mkv", Size: int64(len(payload)), BLAKE3: payloadDigest}},
		[]fixture.FileDigest{{Path: "archive/fixture.part3.rar", Size: 1, BLAKE3: "intact"}},
		[]fixture.FileDigest{{Path: "archive/fixture.part3.rar", Size: int64(len(damaged)), BLAKE3: damagedDigest}})

	if _, err := VerifyOutput(fixtureDir, outputDir); err == nil {
		t.Fatal("a retained damaged archive volume passed verification")
	}
}

func writeRepairManifest(t *testing.T, fixtureDir string, expected, source, posted []fixture.FileDigest) {
	t.Helper()
	// Schema 4 is where the repair axis arrives: below it the loader treats
	// every posted file as an intact source volume, and there is no recovery
	// material to speak of.
	manifest := fixture.GeneratedManifest{
		SchemaVersion: 4,
		Case: fixture.ArchiveCase{
			ID:            "repair-verification",
			RepairProfile: fixture.RARRecoveryVolumeLightProfile,
		},
		Repair:             fixture.RepairDetails{Profile: fixture.RARRecoveryVolumeLightProfile},
		ExpectedFiles:      expected,
		SourceArchiveFiles: source,
		ArchiveFiles:       posted,
	}
	contents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "fixture-manifest.json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}
