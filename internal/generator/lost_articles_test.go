package generator

import (
	"fmt"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// Every article-loss fixture in the checked-in matrix must be buildable at
// its declared size: the lost articles are whole articles of the post's own
// article size, never the first, and they damage no more slices than the
// post has recovery blocks, counted the way PAR2 counts them, per file.
func TestLostArticlesFitEveryDeclaredShape(t *testing.T) {
	matrix, err := fixture.LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, c := range cases {
		if !c.RepairProfile.LosesArticles() {
			continue
		}
		checked++
		shape := c.RecoveryShape
		size, err := fixture.ByteSize(c.BytesPerFile)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		var sources []fixture.FileDigest
		if c.VolumeSize != "" {
			volume, err := fixture.ByteSize(c.VolumeSize)
			if err != nil {
				t.Fatalf("%s: %v", c.ID, err)
			}
			for index, left := 1, size; left > 0; index, left = index+1, left-volume {
				sources = append(sources, fixture.FileDigest{Path: fmt.Sprintf("archive/fixture.part%02d.rar", index), Size: min(left, volume)})
			}
		} else {
			for index := 1; index <= c.FileCount; index++ {
				sources = append(sources, fixture.FileDigest{Path: fmt.Sprintf("archive/payload-%02d.mkv", index), Size: size})
			}
		}
		faults, damaged, err := lostArticles(sources, c.RepairProfile, shape)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		target := c.RepairProfile.ArticleLossSlices(shape.RecoveryBlocks)
		if len(faults) == 0 || damaged == 0 || damaged > target || damaged > shape.RecoveryBlocks {
			t.Fatalf("%s: %d lost articles damage %d slices, target %d, recovery %d", c.ID, len(faults), damaged, target, shape.RecoveryBlocks)
		}
		t.Logf("%s: %d lost articles damage %d of %d recovery blocks", c.ID, len(faults), damaged, shape.RecoveryBlocks)
		// Heavy must be a real workout: at least three quarters of the budget.
		if c.RepairProfile.ArticleLossSeverity() == "heavy" && damaged*4 < target*3 {
			t.Fatalf("%s: heavy loss damages only %d of a %d-slice target", c.ID, damaged, target)
		}
		sizes := map[string]int64{}
		for _, source := range sources {
			sizes[source.Path] = source.Size
		}
		seen := map[string]bool{}
		for _, fault := range faults {
			key := fmt.Sprintf("%s@%d", fault.Path, fault.Offset)
			if seen[key] {
				t.Fatalf("%s: article %s lost twice", c.ID, key)
			}
			seen[key] = true
			if fault.Kind != fixture.WithheldArticleFault || fault.Length != shape.ArticleBytes || fault.Offset%int64(shape.ArticleBytes) != 0 || fault.Offset+int64(fault.Length) > sizes[fault.Path] {
				t.Fatalf("%s: lost article is not one whole article: %+v", c.ID, fault)
			}
			if fault.Path == sources[0].Path && fault.Offset == 0 {
				t.Fatalf("%s: the first article of the post was lost", c.ID)
			}
		}
		again, _, err := lostArticles(sources, c.RepairProfile, shape)
		if err != nil || fmt.Sprint(again) != fmt.Sprint(faults) {
			t.Fatalf("%s: the lost articles are not deterministic", c.ID)
		}
	}
	if checked == 0 {
		t.Fatal("the matrix declares no article-loss fixtures; this test is checking nothing")
	}
}

func TestLostArticlesRefuseAPostTooSmallForItsShape(t *testing.T) {
	shape := fixture.RecoveryShape{SliceBytes: 4 << 20, RecoveryBlocks: 1, ArticleBytes: 716800}
	sources := []fixture.FileDigest{{Path: "archive/payload-01.mkv", Size: 64 << 20}}
	// 716800-byte articles over 4-byte slices: any one article damages far
	// more slices than the single recovery block covers.
	shape.SliceBytes = 4
	if _, _, err := lostArticles(sources, fixture.PAR2ArticlesLightProfile, shape); err == nil {
		t.Fatal("a post with one recovery block lost an article that damages thousands of slices")
	}
}

// A PAR3 article-loss lane exists to be read beside the PAR2 lane of the same
// shape, so the two must lose the very same articles: the draw depends on the
// shape and the severity, never on the parity format.
func TestPAR3ArticleLossMatchesItsPAR2Twin(t *testing.T) {
	shape := fixture.RecoveryShape{SliceBytes: 4194304, RecoveryBlocks: 35, ArticleBytes: 640000}
	sources := []fixture.FileDigest{{Path: "archive/payload-01.mkv", Size: 1449401321}}
	for par2, par3 := range map[fixture.RepairProfile]fixture.RepairProfile{
		fixture.PAR2ArticlesLightProfile: fixture.PAR3ArticlesLightProfile,
		fixture.PAR2ArticlesHeavyProfile: fixture.PAR3FFTArticlesHeavyProfile,
	} {
		want, wantDamaged, err := lostArticles(sources, par2, shape)
		if err != nil {
			t.Fatal(err)
		}
		got, gotDamaged, err := lostArticles(sources, par3, shape)
		if err != nil {
			t.Fatal(err)
		}
		if gotDamaged != wantDamaged || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s loses different articles from %s", par3, par2)
		}
	}
}
