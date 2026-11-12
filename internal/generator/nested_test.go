package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestNestedLayersPullInTheirWriters(t *testing.T) {
	deep := fixture.ArchiveCase{ID: "deep", ArchiveFormat: fixture.RAR5, Nested: "tar:xz > zip:store > rar5:store:15m+par2"}
	flat := fixture.ArchiveCase{ID: "flat", ArchiveFormat: fixture.RAR5, Nested: "rar5:store:15m"}
	if !caseUsesGNUTools(deep) || !caseUsesSevenZip(deep) || !selectedCasesRequirePAR2([]fixture.ArchiveCase{deep}, nil) {
		t.Fatal("a chain with tar, zip and PAR2 layers did not require their toolchains")
	}
	if caseUsesGNUTools(flat) || caseUsesSevenZip(flat) || selectedCasesRequirePAR2([]fixture.ArchiveCase{flat}, nil) {
		t.Fatal("a RAR-only chain required a toolchain it never runs")
	}
}

func TestNestedPrimaryIsTheArchiveOrItsFirstVolume(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"level1-01.part02.rar", "level1-01.part01.rar", "level1-02.7z", "level1.par2", "level1-010.rar"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if first, err := nestedPrimary(dir, fixture.NestedArchiveStem(1, 1)); err != nil || first != "level1-01.part01.rar" {
		t.Fatalf("primary %q, %v", first, err)
	}
	if first, err := nestedPrimary(dir, fixture.NestedArchiveStem(1, 2)); err != nil || first != "level1-02.7z" {
		t.Fatalf("primary %q, %v", first, err)
	}
	if _, err := nestedPrimary(dir, fixture.NestedArchiveStem(1, 3)); err == nil {
		t.Fatal("a missing nested archive was not reported")
	}
}
