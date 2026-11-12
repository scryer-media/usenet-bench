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
	if light.redundancy != par2Redundancy || light.withheld != par2Missing || light.flips != 1 || light.code != "fft" {
		t.Fatalf("par3-light does not mirror par2-light: %+v", light)
	}
	heavy, err := par3Parameters(fixture.PAR3HeavyWithheldProfile)
	if err != nil {
		t.Fatal(err)
	}
	par2Redundancy, par2Missing = par2RepairParameters(fixture.PAR2HeavyWithheldProfile)
	if heavy.redundancy != par2Redundancy || heavy.withheld != par2Missing || heavy.flips != 0 || heavy.code != "fft" {
		t.Fatalf("par3-heavy-withheld does not mirror par2-heavy-withheld: %+v", heavy)
	}
	fft, err := par3Parameters(fixture.PAR3FFTHeavyWithheldProfile)
	if err != nil {
		t.Fatal(err)
	}
	if fft.code != "fft" || fft.withheld != 1 || fft.flips != 1 {
		t.Fatalf("par3-fft-heavy-withheld is not a compound FFT fault: %+v", fft)
	}
	inside, err := par3Parameters(fixture.PAR3InsideLightProfile)
	if err != nil {
		t.Fatal(err)
	}
	if inside.code != "reference-default" || inside.withheld != 0 || inside.flips != par3InsideFaults {
		t.Fatalf("par3-inside-light recipe = %+v", inside)
	}
	if _, err := par3Parameters(fixture.PAR2LightRepairProfile); err == nil {
		t.Fatal("a PAR2 profile produced a PAR3 recipe")
	}
}

// The reference is needed only for the embedded lane; rarpar writes every
// standalone PAR3 set.
func TestSelectedCasesRequireEachPAR3WriterOnlyForItsLanes(t *testing.T) {
	cases := []fixture.ArchiveCase{
		{ID: "clean", RepairProfile: fixture.CleanRepairProfile},
		{ID: "par2", RepairProfile: fixture.PAR2LightRepairProfile},
		{ID: "inside", RepairProfile: fixture.PAR3InsideLightProfile},
		{ID: "standalone", RepairProfile: fixture.PAR3HeavyWithheldProfile},
	}
	if !selectedCasesRequirePAR3(cases, nil) || !selectedCasesRequireRarpar(cases, nil) {
		t.Fatal("an unfiltered run containing both PAR3 lanes did not load both writers")
	}
	if selectedCasesRequirePAR3(cases, map[string]bool{"clean": true, "par2": true, "standalone": true}) {
		t.Fatal("a run without the embedded lane asked for the PAR3 reference")
	}
	if selectedCasesRequireRarpar(cases, map[string]bool{"clean": true, "par2": true, "inside": true}) {
		t.Fatal("a run without a standalone PAR3 lane asked for rarpar")
	}
}

func TestCheckedInRarparToolchainPinsAPublishedRelease(t *testing.T) {
	toolchain, err := LoadRarparToolchain(filepath.Join("..", "..", "docker", "rarpar", "toolchain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(toolchain.URL, "https://github.com/scryer-media/rarpar/releases/download/rarpar-v"+toolchain.Version+"/") {
		t.Fatalf("rarpar is not fetched from its own release: %s", toolchain.URL)
	}
	id := toolchain.ManifestID()
	if id.Version != toolchain.Version || id.SHA256 != toolchain.SHA256 || id.Binary != "rarpar" {
		t.Fatalf("manifest id does not carry the pin: %+v", id)
	}
	toolchain.Version = "latest"
	if err := toolchain.Validate(); err == nil {
		t.Fatal("a floating tag was accepted as a rarpar version")
	}
	toolchain.Version = "9.9.9"
	if err := toolchain.Validate(); err == nil || !strings.Contains(err.Error(), "not an asset of release") {
		t.Fatalf("a URL from another release was accepted: %v", err)
	}
}

// Every standalone PAR3 set is FFT, and its declared capacity must hold the
// recovery blocks the set is created with: 167 blocks need 256, two cohorts
// sharing 3635 need 2048 each.
func TestEveryStandalonePAR3RecipeIsFFTWithAFittingCapacity(t *testing.T) {
	for _, profile := range []fixture.RepairProfile{
		fixture.PAR3LightRepairProfile, fixture.PAR3HeavyWithheldProfile, fixture.PAR3FFTHeavyWithheldProfile,
		fixture.PAR3ArticlesLightProfile, fixture.PAR3FFTArticlesHeavyProfile,
		fixture.PAR3FFTScatteredLightProfile, fixture.PAR3FFTScatteredHeavyProfile, fixture.PAR3FFTPastPAR2CapProfile,
	} {
		recipe, err := par3Parameters(profile)
		if err != nil || recipe.code != "fft" {
			t.Fatalf("%s is not an FFT recipe: %+v, %v", profile, recipe, err)
		}
	}
	rar := []fixture.FileDigest{{Path: "archive/fixture.part01.rar", Size: 390700000}}
	if got, err := fftCapacityLog2(rar, 195596, 167, 0, 1); err != nil || got != 8 {
		t.Fatalf("167 recovery blocks: capacity 2^%d, %v", got, err)
	}
	if got, err := fftCapacityLog2(rar, 195596, 256, 0, 1); err != nil || got != 8 {
		t.Fatalf("256 recovery blocks: capacity 2^%d, %v", got, err)
	}
	if got, err := fftCapacityLog2(rar, 195596, 1, 0, 1); err != nil || got != 0 {
		t.Fatalf("one recovery block: capacity 2^%d, %v", got, err)
	}
	pastCap := []fixture.FileDigest{{Path: "archive/fixture.part01.rar", Size: 26 << 30}}
	if got, err := fftCapacityLog2(pastCap, fixture.ScatteredArticleBytes, 0, 10, 2); err != nil || got != 11 {
		t.Fatalf("past-cap lane in two cohorts: capacity 2^%d, %v", got, err)
	}
	if _, err := fftCapacityLog2(pastCap, fixture.ScatteredArticleBytes, 0, 10, 1); err != nil {
		t.Fatalf("36351 blocks and 4096 recovery fit one cohort: %v", err)
	}
	huge := []fixture.FileDigest{{Path: "archive/fixture.part01.rar", Size: 60 << 30}}
	if _, err := fftCapacityLog2(huge, fixture.ScatteredArticleBytes, 0, 10, 1); err == nil {
		t.Fatal("a cohort past the 65536-point transform was accepted")
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
