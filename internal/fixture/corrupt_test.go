package fixture

import (
	"bytes"
	"strings"
	"testing"
)

func TestCorruptArticleChangesOnlyItsMiddleBytes(t *testing.T) {
	original := bytes.Repeat([]byte{0x11, 0x22, 0x33}, 716800/3+1)[:716800]
	damaged := append([]byte(nil), original...)
	CorruptArticle(damaged)
	start, length := CorruptArticleBytes(len(original))
	if length != CorruptedArticleBytes || start != (716800-CorruptedArticleBytes)/2 {
		t.Fatalf("damaged range = %d+%d", start, length)
	}
	// The pattern is the generator's byte-flip pattern, 0xa5+i, which is zero at
	// one offset, so 127 of the 128 bytes change.
	changedBytes := 0
	for index := range original {
		inside := index >= start && index < start+length
		changed := original[index] != damaged[index]
		if changed && !inside {
			t.Fatalf("byte %d outside the damaged range changed", index)
		}
		if changed {
			changedBytes++
		}
	}
	if changedBytes != CorruptedArticleBytes-1 {
		t.Fatalf("%d bytes changed, want %d", changedBytes, CorruptedArticleBytes-1)
	}
	// The change is a fixed XOR, so it is the same every time and applying it
	// twice restores the article: the generator and the seeder agree on it.
	CorruptArticle(damaged)
	if !bytes.Equal(damaged, original) {
		t.Fatal("the damage is not a fixed, reversible change")
	}
	short := []byte{1, 2, 3}
	CorruptArticle(short)
	if bytes.Equal(short, []byte{1, 2, 3}) {
		t.Fatal("an article shorter than the damaged range was left intact")
	}
}

func TestDamagedArticleProfilesMirrorTheirMissingDataSiblings(t *testing.T) {
	siblings := map[RepairProfile]RepairProfile{
		PAR2SilentArticlesLightProfile:       PAR2ArticlesLightProfile,
		PAR2CorruptArticlesLightProfile:      PAR2ArticlesLightProfile,
		PAR2CorruptArticlesHeavyProfile:      PAR2ArticlesHeavyProfile,
		PAR3SilentArticlesLightProfile:       PAR3ArticlesLightProfile,
		PAR3CorruptArticlesLightProfile:      PAR3ArticlesLightProfile,
		PAR3FFTCorruptArticlesHeavyProfile:   PAR3FFTArticlesHeavyProfile,
		RARRecoveryRecordSilentLightProfile:  RARRecoveryRecordLightProfile,
		RARRecoveryRecordCorruptLightProfile: RARRecoveryRecordLightProfile,
		RARRecoveryVolumeSilentLightProfile:  RARRecoveryVolumeLightProfile,
		RARRecoveryVolumeCorruptLightProfile: RARRecoveryVolumeLightProfile,
		RARRecoveryVolumeCorruptHeavyProfile: RARRecoveryVolumeHeavyProfile,
	}
	for damaged, sibling := range siblings {
		if !damaged.Valid() || !damaged.DamagesArticles() || damaged.LosesArticles() || damaged.WithholdsArticles() {
			t.Fatalf("%s is not classified as a damaged-article profile", damaged)
		}
		if damaged.SilentlyCorruptsArticles() == damaged.CorruptsArticles() {
			t.Fatalf("%s must be exactly one of silent and corrupt", damaged)
		}
		if strings.Contains(string(damaged), "silent") != damaged.SilentlyCorruptsArticles() {
			t.Fatalf("%s is named against its kind", damaged)
		}
		if damaged.ArticleLossSeverity() != sibling.ArticleLossSeverity() {
			t.Fatalf("%s picks at severity %q, its sibling %s at %q", damaged, damaged.ArticleLossSeverity(), sibling, sibling.ArticleLossSeverity())
		}
		if damaged.UsesPAR2() != sibling.UsesPAR2() || damaged.UsesPAR3() != sibling.UsesPAR3() ||
			damaged.UsesRARRecoveryRecord() != sibling.UsesRARRecoveryRecord() || damaged.UsesRARRecoveryVolumes() != sibling.UsesRARRecoveryVolumes() ||
			damaged.DoublesRARRecoveryVolumes() != sibling.DoublesRARRecoveryVolumes() {
			t.Fatalf("%s does not use the recovery its sibling %s uses", damaged, sibling)
		}
		if !damaged.NeedsRecoveryShape() || !damaged.AcceptsRecoveryShape() {
			t.Fatalf("%s can be built without the article size a shape declares", damaged)
		}
	}
	for _, missing := range []RepairProfile{PAR2ArticlesLightProfile, PAR3FFTArticlesHeavyProfile, RARRecoveryRecordLightProfile, RARRecoveryVolumeHeavyProfile} {
		if missing.DamagesArticles() {
			t.Fatalf("missing-data profile %s reports damaged articles", missing)
		}
	}
}

func TestDamagedArticleSetsNeedAShapeAndCorruptOnesNeedYEnc(t *testing.T) {
	base := FixtureSet{ID: "damaged", Class: BreadthFixtureClass, WriterEra: "RAR 7.23", GeneratorToolchain: "rarlab-7.23", ArchiveFormat: RAR5,
		Compressions: []Compression{Store}, Solid: []bool{false}, Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
		RepairProfiles: []RepairProfile{PAR2CorruptArticlesLightProfile, RARRecoveryVolumeCorruptLightProfile}, FileCount: 1, BytesPerFile: "390700000", VolumeSize: "30m",
		RecoveryShape: RecoveryShape{SliceBytes: 195596, RecoveryBlocks: 167, ArticleBytes: 716800}}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	unshaped := base
	unshaped.RecoveryShape = RecoveryShape{}
	if err := unshaped.validate(); err == nil {
		t.Fatal("a damaged-article set was accepted without the article size of a shape")
	}
	// uuencode is posted only over the bare media lane, so the encoding rule is
	// checked there.
	media := FixtureSet{ID: "damaged-media", Class: BreadthFixtureClass, WriterEra: "not-applicable", GeneratorToolchain: "rarlab-7.23", ArchiveWriter: "rarlab-7.23", ArchiveFormat: Media,
		Compressions: []Compression{Store}, Solid: []bool{false}, Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
		RepairProfiles: []RepairProfile{PAR2CorruptArticlesLightProfile}, FileCount: 1, BytesPerFile: "1449401321",
		RecoveryShape: RecoveryShape{SliceBytes: 4194304, RecoveryBlocks: 35, ArticleBytes: 640000}}
	if err := media.validate(); err != nil {
		t.Fatal(err)
	}
	uuencoded := media
	uuencoded.Encoding = UUEncodeEncoding
	if err := uuencoded.validate(); err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("a corrupt-article set was accepted over uuencode: %v", err)
	}
	silentUU := uuencoded
	silentUU.RepairProfiles = []RepairProfile{PAR2SilentArticlesLightProfile}
	if err := silentUU.validate(); err != nil {
		t.Fatalf("a silent fault, which needs no yEnc CRC, was refused over uuencode: %v", err)
	}
	heavy := ArchiveCase{ID: "heavy", RepairProfile: RARRecoveryVolumeCorruptHeavyProfile}
	if count, err := heavy.RARRecoveryVolumeCount(5); err != nil || count != 2 {
		t.Fatalf("the corrupt heavy recovery-volume lane posts %d volumes, %v; want the heavy lane's 2", count, err)
	}
}
