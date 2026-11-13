package generator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// A damaged-article lane is read beside its missing-data sibling, so the two
// must fault the very same articles.
func TestDamagedArticleLanesPickTheirSiblingsArticles(t *testing.T) {
	shape := fixture.RecoveryShape{SliceBytes: 195596, RecoveryBlocks: 167, ArticleBytes: 716800}
	var sources []fixture.FileDigest
	for index := 1; index <= 13; index++ {
		sources = append(sources, fixture.FileDigest{Path: fmt.Sprintf("archive/fixture.part%02d.rar", index), Size: 30 << 20})
	}
	for damaged, sibling := range map[fixture.RepairProfile]fixture.RepairProfile{
		fixture.PAR2SilentArticlesLightProfile:       fixture.PAR2ArticlesLightProfile,
		fixture.PAR2CorruptArticlesLightProfile:      fixture.PAR2ArticlesLightProfile,
		fixture.PAR2CorruptArticlesHeavyProfile:      fixture.PAR2ArticlesHeavyProfile,
		fixture.RARRecoveryRecordSilentLightProfile:  fixture.RARRecoveryRecordLightProfile,
		fixture.RARRecoveryRecordCorruptLightProfile: fixture.RARRecoveryRecordLightProfile,
	} {
		want, wantDamaged, err := lostArticles(sources, sibling, shape)
		if err != nil {
			t.Fatal(err)
		}
		got, gotDamaged, err := lostArticles(sources, damaged, shape)
		if err != nil {
			t.Fatal(err)
		}
		if gotDamaged != wantDamaged || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s picks different articles from %s", damaged, sibling)
		}
	}
}

func damagedFixtureDir(t *testing.T, size int) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	contents := make([]byte, size)
	for index := range contents {
		contents[index] = byte(index * 7)
	}
	if err := os.MkdirAll(filepath.Join(dir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "archive", "fixture.part02.rar"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, contents
}

func readDamagedFixture(t *testing.T, dir string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(dir, "archive", "fixture.part02.rar"))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func pickedArticle(offset int64) fixture.CorruptionDetail {
	return fixture.CorruptionDetail{Kind: fixture.WithheldArticleFault, Path: "archive/fixture.part02.rar", Offset: offset, Length: 1024}
}

func TestSilentDamageChangesTheFileWhereTheArticleIs(t *testing.T) {
	dir, original := damagedFixtureDir(t, 4096)
	faults, err := damageArticles(dir, fixture.PAR2SilentArticlesLightProfile, []fixture.CorruptionDetail{pickedArticle(1024)})
	if err != nil {
		t.Fatal(err)
	}
	want := fixture.CorruptionDetail{Kind: fixture.ByteFlipFault, Path: "archive/fixture.part02.rar", Offset: 1024 + 448, Length: 128}
	if len(faults) != 1 || faults[0] != want {
		t.Fatalf("faults = %+v, want %+v", faults, want)
	}
	expected := append([]byte(nil), original...)
	fixture.CorruptArticle(expected[1024:2048])
	if !bytes.Equal(readDamagedFixture(t, dir), expected) {
		t.Fatal("the silent fault is not the shared article damage at the picked article")
	}
	if got := damagedSlices(faults, 100); got != 2 {
		t.Fatalf("128 bytes at 1472 damage %d 100-byte slices, want 2", got)
	}
}

// A corrupt fault leaves the posted file intact, and its verification stages
// both readings a client can hold: the damaged bytes, and a hole.
func TestCorruptDamageLeavesTheFileAndStagesBothReadings(t *testing.T) {
	dir, original := damagedFixtureDir(t, 4096)
	faults, err := damageArticles(dir, fixture.PAR2CorruptArticlesLightProfile, []fixture.CorruptionDetail{pickedArticle(2048)})
	if err != nil {
		t.Fatal(err)
	}
	if len(faults) != 1 || faults[0].Kind != fixture.CorruptArticleFault || faults[0].Offset != 2048 || faults[0].Length != 1024 {
		t.Fatalf("faults = %+v", faults)
	}
	if !bytes.Equal(readDamagedFixture(t, dir), original) {
		t.Fatal("a corrupt fault changed the file that is posted")
	}
	if stagings := articleStagings(faults); len(stagings) != 2 {
		t.Fatalf("a corrupt fault is verified in %d readings, want 2", len(stagings))
	}
	if stagings := articleStagings([]fixture.CorruptionDetail{pickedArticle(0)}); len(stagings) != 1 {
		t.Fatalf("a withheld fault is verified in %d readings, want 1", len(stagings))
	}

	kept, _ := damagedFixtureDir(t, 4096)
	if err := stageArticleFaults(kept, faults, false); err != nil {
		t.Fatal(err)
	}
	expected := append([]byte(nil), original...)
	fixture.CorruptArticle(expected[2048:3072])
	if !bytes.Equal(readDamagedFixture(t, kept), expected) {
		t.Fatal("the kept reading is not the bytes the seeder posts")
	}

	discarded, _ := damagedFixtureDir(t, 4096)
	if err := stageArticleFaults(discarded, faults, true); err != nil {
		t.Fatal(err)
	}
	expected = append([]byte(nil), original...)
	copy(expected[2048:3072], make([]byte, 1024))
	if !bytes.Equal(readDamagedFixture(t, discarded), expected) {
		t.Fatal("the discarded reading is not a hole where the article was")
	}
}

func TestMiddleArticlesPickOneWholeArticlePerVolume(t *testing.T) {
	targets := []fixture.FileDigest{{Path: "archive/fixture.part06.rar", Size: 10*1000 + 500}, {Path: "archive/fixture.part07.rar", Size: 3 * 1000}}
	picked, err := middleArticles(targets, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(picked) != 2 || picked[0].Offset != 5000 || picked[1].Offset != 1000 || picked[0].Length != 1000 || picked[0].Path != targets[0].Path {
		t.Fatalf("picked = %+v", picked)
	}
	if _, err := middleArticles([]fixture.FileDigest{{Path: "archive/short.rar", Size: 999}}, 1000); err == nil {
		t.Fatal("a volume shorter than one article was given a damaged article")
	}
}
