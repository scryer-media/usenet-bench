package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestCheckedInPAR3ToolchainPinsTheReferenceRevision(t *testing.T) {
	toolchain, err := LoadPAR3Toolchain(filepath.Join("..", "..", "docker", "par3", "toolchain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if toolchain.Revision != "2971702e501f1350b1c7b9d11369af9157d6ed56" {
		t.Fatalf("PAR3 reference revision = %s", toolchain.Revision)
	}
	if !strings.HasPrefix(toolchain.URL, "https://github.com/Parchive/par3cmdline/") {
		t.Fatalf("PAR3 reference is not fetched from the official repository: %s", toolchain.URL)
	}
	id := toolchain.ManifestID()
	if id.Version != toolchain.Revision || id.SHA256 != toolchain.SHA256 || id.Binary != "par3" {
		t.Fatalf("manifest id does not carry the pin: %+v", id)
	}
}

func TestPAR3ToolchainRefusesALooseRevision(t *testing.T) {
	toolchain := PAR3Toolchain{
		SchemaVersion: 1, ID: "par3", Image: "par3:x", Platform: "linux/amd64",
		URL:      "https://github.com/Parchive/par3cmdline/archive/main.tar.gz",
		SHA256:   strings.Repeat("a", 64),
		Revision: "main",
	}
	if err := toolchain.Validate(); err == nil {
		t.Fatal("a branch name was accepted as a PAR3 revision")
	}
	toolchain.Revision = strings.Repeat("b", 40)
	if err := toolchain.Validate(); err == nil || !strings.Contains(err.Error(), "does not name revision") {
		t.Fatalf("a URL that does not fetch the pinned revision was accepted: %v", err)
	}
}

func TestPAR3RecipesMirrorPAR2AndDeclareTheirCode(t *testing.T) {
	light, err := par3Parameters(fixture.PAR3LightRepairProfile)
	if err != nil {
		t.Fatal(err)
	}
	par2Redundancy, par2Missing := par2RepairParameters(fixture.PAR2LightRepairProfile)
	if light.redundancy != par2Redundancy || light.withheld != par2Missing || light.flips != 1 || light.codeArg != "-e1" {
		t.Fatalf("par3-light does not mirror par2-light: %+v", light)
	}
	heavy, err := par3Parameters(fixture.PAR3HeavyWithheldProfile)
	if err != nil {
		t.Fatal(err)
	}
	par2Redundancy, par2Missing = par2RepairParameters(fixture.PAR2HeavyWithheldProfile)
	if heavy.redundancy != par2Redundancy || heavy.withheld != par2Missing || heavy.flips != 0 || heavy.codeArg != "-e1" {
		t.Fatalf("par3-heavy-withheld does not mirror par2-heavy-withheld: %+v", heavy)
	}
	fft, err := par3Parameters(fixture.PAR3FFTHeavyWithheldProfile)
	if err != nil {
		t.Fatal(err)
	}
	if fft.codeArg != "-e8" || fft.withheld != 1 || fft.flips != 1 {
		t.Fatalf("par3-fft-heavy-withheld is not a compound FFT fault: %+v", fft)
	}
	inside, err := par3Parameters(fixture.PAR3InsideLightProfile)
	if err != nil {
		t.Fatal(err)
	}
	if inside.codeArg != "" || inside.withheld != 0 || inside.flips != par3InsideFaults {
		t.Fatalf("par3-inside-light recipe = %+v", inside)
	}
	if _, err := par3Parameters(fixture.PAR2LightRepairProfile); err == nil {
		t.Fatal("a PAR2 profile produced a PAR3 recipe")
	}
}

func TestSelectedCasesRequirePAR3OnlyForPAR3Profiles(t *testing.T) {
	cases := []fixture.ArchiveCase{
		{ID: "clean", RepairProfile: fixture.CleanRepairProfile},
		{ID: "par2", RepairProfile: fixture.PAR2LightRepairProfile},
		{ID: "par3", RepairProfile: fixture.PAR3InsideLightProfile},
	}
	if !selectedCasesRequirePAR3(cases, nil) {
		t.Fatal("an unfiltered run containing a PAR3 lane did not load the PAR3 toolchain")
	}
	if selectedCasesRequirePAR3(cases, map[string]bool{"clean": true, "par2": true}) {
		t.Fatal("a run without PAR3 lanes asked for the PAR3 toolchain")
	}
}

func TestArchiveRelativeNameKeepsThePostedNameFlat(t *testing.T) {
	name, err := archiveRelativeName("archive/fixture.part2.rar")
	if err != nil || name != "fixture.part2.rar" {
		t.Fatalf("archiveRelativeName = %q, %v", name, err)
	}
	for _, bad := range []string{"fixture.part2.rar", "archive/", "archive/../escape"} {
		if _, err := archiveRelativeName(bad); err == nil {
			t.Fatalf("archiveRelativeName accepted %q", bad)
		}
	}
}

func TestFlipArchiveBytesAtStaysInsideTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := bytes.Repeat([]byte{0x11}, 4096)
	if err := os.WriteFile(filepath.Join(dir, "archive", "fixture.zip"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	target := fixture.FileDigest{Path: "archive/fixture.zip", Size: int64(len(original))}
	fault, err := flipArchiveBytesAt(dir, target, 4090)
	if err != nil {
		t.Fatal(err)
	}
	if fault.Offset != int64(len(original)-lightCorruptBytes) || fault.Length != lightCorruptBytes {
		t.Fatalf("fault = %+v, want the range pulled back inside the file", fault)
	}
	damaged, err := os.ReadFile(filepath.Join(dir, "archive", "fixture.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(damaged, original) || len(damaged) != len(original) {
		t.Fatal("the flip did not change the file in place")
	}
}
