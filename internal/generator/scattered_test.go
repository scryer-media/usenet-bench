package generator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func scatteredSources() []fixture.FileDigest {
	// Three volumes of 40.5 articles each: every volume ends in a short tail.
	volume := int64(fixture.ScatteredArticleBytes)*40 + fixture.ScatteredArticleBytes/2
	return []fixture.FileDigest{
		{Path: "archive/fixture.part1.rar", Size: volume},
		{Path: "archive/fixture.part2.rar", Size: volume},
		{Path: "archive/fixture.part3.rar", Size: volume},
	}
}

func TestScatteredArticlesAreAlignedEvenAndNeverTheFirst(t *testing.T) {
	sources := scatteredSources()
	faults, err := scatteredArticles(sources, "scatter-set", 70)
	if err != nil {
		t.Fatal(err)
	}
	// 40 full-length articles per volume, less the leading one.
	candidates := 3*40 - 1
	if want := (candidates*70 + 999) / 1000; len(faults) != want {
		t.Fatalf("withheld %d articles, want %d", len(faults), want)
	}
	sizes := map[string]int64{}
	order := map[string]int{}
	for index, source := range sources {
		sizes[source.Path] = source.Size
		order[source.Path] = index
	}
	previous := -1
	for _, fault := range faults {
		if fault.Kind != fixture.WithheldArticleFault {
			t.Fatalf("fault kind = %q", fault.Kind)
		}
		if fault.Offset%fixture.ScatteredArticleBytes != 0 {
			t.Fatalf("fault at %d is not on an article boundary", fault.Offset)
		}
		if fault.Length != fixture.ScatteredArticleBytes || fault.Offset+int64(fault.Length) > sizes[fault.Path] {
			t.Fatalf("fault %#v is not a full-length article", fault)
		}
		global := order[fault.Path]*41 + int(fault.Offset/fixture.ScatteredArticleBytes)
		if global == 0 {
			t.Fatal("the leading article was withheld")
		}
		if global <= previous {
			t.Fatalf("faults are not strictly increasing: %d after %d", global, previous)
		}
		previous = global
	}
}

func TestScatteredArticlesAreSharedByBothFormats(t *testing.T) {
	first, err := scatteredArticles(scatteredSources(), "scatter-set", 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scatteredArticles(scatteredSources(), "scatter-set", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatal("the draw is not deterministic")
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatal("the draw is not deterministic")
		}
	}
	for _, pair := range [][2]fixture.RepairProfile{
		{fixture.PAR2ScatteredLightProfile, fixture.PAR3FFTScatteredLightProfile},
		{fixture.PAR2ScatteredHeavyProfile, fixture.PAR3FFTScatteredHeavyProfile},
	} {
		par2Mille, par2Redundancy, ok2 := pair[0].ScatteredDamage()
		par3Mille, par3Redundancy, ok3 := pair[1].ScatteredDamage()
		if !ok2 || !ok3 || par2Mille != par3Mille || par2Redundancy != par3Redundancy {
			t.Fatalf("%s and %s do not share one severity", pair[0], pair[1])
		}
		if par2Mille*100/1000 >= par2Redundancy {
			t.Fatalf("%s withholds %d per mille with only %d%% redundancy", pair[0], par2Mille, par2Redundancy)
		}
	}
}

func TestScatteredArticlesMatchAcrossTwinsOfSlightlyDifferentSize(t *testing.T) {
	// Each twin writes its own archive, so the volumes of one can come out a
	// few hundred KiB longer than the other's. The draw must still withhold
	// the same number of articles at the same relative positions.
	twin := func(lastVolume int64) []fixture.FileDigest {
		sources := make([]fixture.FileDigest, 0, 400)
		for volume := 1; volume < 400; volume++ {
			sources = append(sources, fixture.FileDigest{Path: fmt.Sprintf("archive/fixture.part%03d.rar", volume), Size: 256 << 20})
		}
		return append(sources, fixture.FileDigest{Path: "archive/fixture.part400.rar", Size: lastVolume})
	}
	article := int64(fixture.ScatteredArticleBytes)
	perVolume := (256 << 20) / article
	global := func(fault fixture.CorruptionDetail) int64 {
		var volume int64
		fmt.Sscanf(fault.Path, "archive/fixture.part%03d.rar", &volume)
		return (volume-1)*perVolume + fault.Offset/article
	}
	for _, perMille := range []int{10, 70} {
		longer, err := scatteredArticles(twin(200<<20), "scatter-set", perMille)
		if err != nil {
			t.Fatal(err)
		}
		shorter, err := scatteredArticles(twin(200<<20-600<<10), "scatter-set", perMille)
		if err != nil {
			t.Fatal(err)
		}
		if len(longer) != len(shorter) {
			t.Fatalf("%d per mille: the twins withhold %d and %d articles", perMille, len(longer), len(shorter))
		}
		for index := range longer {
			if delta := global(longer[index]) - global(shorter[index]); delta < -1 || delta > 1 {
				t.Fatalf("%d per mille: withheld article %d sits %d articles apart in the twins", perMille, index, delta)
			}
		}
	}
}

func TestScatteredArticlesRefuseAnImpossibleDraw(t *testing.T) {
	tiny := []fixture.FileDigest{{Path: "archive/fixture.rar", Size: fixture.ScatteredArticleBytes}}
	if _, err := scatteredArticles(tiny, "scatter-set", 10); err == nil {
		t.Fatal("a one-article post had an article withheld")
	}
	if _, err := scatteredArticles(scatteredSources(), "scatter-set", 0); err == nil {
		t.Fatal("zero per mille was accepted")
	}
}

func TestBlankWithheldArticlesZeroesOnlyTheirRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive", "fixture.part2.rar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := bytes.Repeat([]byte{0x5a}, 3*fixture.ScatteredArticleBytes)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	fault := fixture.CorruptionDetail{Kind: fixture.WithheldArticleFault, Path: "archive/fixture.part2.rar", Offset: fixture.ScatteredArticleBytes, Length: fixture.ScatteredArticleBytes}
	flip := fixture.CorruptionDetail{Kind: "byte-flip", Path: "archive/fixture.part2.rar", Offset: 0, Length: 16}
	if err := blankWithheldArticles(dir, []fixture.CorruptionDetail{flip, fault}); err != nil {
		t.Fatal(err)
	}
	changed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	article := fixture.ScatteredArticleBytes
	if len(changed) != len(original) {
		t.Fatal("blanking changed the file length")
	}
	if !bytes.Equal(changed[:article], original[:article]) || !bytes.Equal(changed[2*article:], original[2*article:]) {
		t.Fatal("blanking escaped the withheld article")
	}
	if !bytes.Equal(changed[article:2*article], make([]byte, article)) {
		t.Fatal("the withheld article was not zeroed")
	}
}
