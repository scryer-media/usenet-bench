package fixture

import (
	"strings"
	"testing"
)

func par3Set(format ArchiveFormat, profile RepairProfile) FixtureSet {
	set := FixtureSet{
		ID:                 "par3-lane",
		Class:              BreadthFixtureClass,
		WriterEra:          "par3cmdline",
		GeneratorToolchain: "rarlab-7.23",
		ArchiveFormat:      format,
		Compressions:       []Compression{Store},
		Solid:              []bool{false},
		Encryptions:        []Encryption{NoEncryption},
		Payloads:           []PayloadKind{IncompressiblePayload},
		RepairProfiles:     []RepairProfile{profile},
		FileCount:          1,
		VolumeSize:         "32m",
	}
	switch format {
	case Zip:
		set.ArchiveWriter = "gnutools-bookworm"
		set.VolumeSize = ""
	case Media:
		set.ArchiveWriter = "rarlab-7.23"
		set.VolumeSize = ""
		set.FileCount = 4
	}
	return set
}

func expandOne(set FixtureSet) error {
	_, err := Matrix{SchemaVersion: 2, Sets: []FixtureSet{set}}.Expand()
	return err
}

func TestPAR3ProfilesAreValidAndDistinguishEmbedding(t *testing.T) {
	for _, profile := range []RepairProfile{PAR3LightRepairProfile, PAR3HeavyWithheldProfile, PAR3FFTHeavyWithheldProfile, PAR3InsideLightProfile} {
		if !profile.Valid() || !profile.UsesPAR3() || profile.UsesPAR2() || profile.UsesRARRecoveryVolumes() {
			t.Fatalf("profile %q is not classified as PAR3 only", profile)
		}
		if got, want := profile.EmbedsPAR3(), profile == PAR3InsideLightProfile; got != want {
			t.Fatalf("%q EmbedsPAR3() = %v, want %v", profile, got, want)
		}
	}
	if PAR2LightRepairProfile.UsesPAR3() || CleanRepairProfile.UsesPAR3() {
		t.Fatal("a non-PAR3 profile reports PAR3 material")
	}
}

func TestPAR3ProfilesAcceptTheShapesTheGeneratorBuilds(t *testing.T) {
	for _, set := range []FixtureSet{
		par3Set(RAR5, PAR3LightRepairProfile),
		par3Set(RAR5, PAR3HeavyWithheldProfile),
		par3Set(SevenZip, PAR3HeavyWithheldProfile),
		par3Set(Media, PAR3FFTHeavyWithheldProfile),
		par3Set(Zip, PAR3InsideLightProfile),
	} {
		if set.ArchiveFormat == SevenZip {
			set.ArchiveWriter = "sevenzip-26.02"
		}
		if err := expandOne(set); err != nil {
			t.Fatalf("%s with %v refused: %v", set.ArchiveFormat, set.RepairProfiles, err)
		}
	}
}

func TestPAR3ProfilesRefuseShapesTheyCannotFault(t *testing.T) {
	tests := []struct {
		name   string
		set    func() FixtureSet
		reason string
	}{
		{"embedded in RAR", func() FixtureSet { return par3Set(RAR5, PAR3InsideLightProfile) }, "embedded"},
		{"embedded in a spanned zip", func() FixtureSet {
			set := par3Set(Zip, PAR3InsideLightProfile)
			set.VolumeSize = "32m"
			return set
		}, "one container file"},
		{"embedded in an encrypted zip", func() FixtureSet {
			set := par3Set(Zip, PAR3InsideLightProfile)
			set.Encryptions = []Encryption{DataEncryption}
			return set
		}, "encryption"},
		{"embedded in zip64", func() FixtureSet {
			set := par3Set(Zip, PAR3InsideLightProfile)
			set.ZipStructure = ForcedZip64Structure
			return set
		}, "zip_structure"},
		{"separate files over an unsplit 7z", func() FixtureSet {
			set := par3Set(SevenZip, PAR3HeavyWithheldProfile)
			set.ArchiveWriter = "sevenzip-26.02"
			set.VolumeSize = ""
			return set
		}, "split"},
		{"compound fault over too few media files", func() FixtureSet {
			set := par3Set(Media, PAR3FFTHeavyWithheldProfile)
			set.FileCount = 3
			return set
		}, "file_count"},
	}
	for _, test := range tests {
		err := expandOne(test.set())
		if err == nil || !strings.Contains(err.Error(), test.reason) {
			t.Fatalf("%s: err = %v, want one mentioning %q", test.name, err, test.reason)
		}
	}
}

// The PAR3 lanes are release-over-release timings, so their identity must not
// drift: a renamed or dropped lane silently breaks the comparison.
func TestCheckedInMatrixCarriesThePAR3BreadthLanes(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := LoadCorpus("../../fixtures/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	inCorpus := map[string]bool{}
	for _, id := range corpus.FixtureIDs {
		inCorpus[id] = true
	}
	want := map[string]RepairProfile{
		"repair-rar5-7-store-par3-par3-light-store-nonsolid-none-incompressible":                     PAR3LightRepairProfile,
		"repair-rar5-7-store-par3-par3-heavy-withheld-store-nonsolid-none-incompressible":            PAR3HeavyWithheldProfile,
		"repair-rar5-7-store-par3-headers-par3-heavy-withheld-store-nonsolid-headers-incompressible": PAR3HeavyWithheldProfile,
		"repair-media-par3-fft-par3-fft-heavy-withheld-store-nonsolid-none-incompressible":           PAR3FFTHeavyWithheldProfile,
		"repair-zip-store-par3-inside-par3-inside-light-store-nonsolid-none-incompressible":          PAR3InsideLightProfile,
		"repair-rar5-7-store-scatter-par3-fft-scattered-light-store-nonsolid-none-incompressible":    PAR3FFTScatteredLightProfile,
		"repair-rar5-7-store-scatter-par3-fft-scattered-heavy-store-nonsolid-none-incompressible":    PAR3FFTScatteredHeavyProfile,
		"repair-rar5-7-store-pastcap-par3-fft-past-par2-cap-store-nonsolid-none-incompressible":      PAR3FFTPastPAR2CapProfile,
	}
	found := 0
	for _, c := range cases {
		if !c.RepairProfile.UsesPAR3() {
			continue
		}
		found++
		profile, ok := want[c.ID]
		if !ok || profile != c.RepairProfile {
			t.Fatalf("unexpected PAR3 lane %s (%s)", c.ID, c.RepairProfile)
		}
		if c.Class != BreadthFixtureClass {
			t.Fatalf("PAR3 lane %s is %s; neither oracle client reads PAR3, so it cannot be headline", c.ID, c.Class)
		}
		if !inCorpus[c.ID] {
			t.Fatalf("PAR3 lane %s is missing from the declared corpus", c.ID)
		}
	}
	if found != len(want) {
		t.Fatalf("matrix expands to %d PAR3 lanes, want %d", found, len(want))
	}
}

func TestScatteredOrderInterleavesPAR3Material(t *testing.T) {
	if !isRepairMaterial("archive/fixture.vol0+1.par3") || !isRepairMaterial("archive/FIXTURE.PAR3") {
		t.Fatal("PAR3 files are not treated as repair material")
	}
}

func TestScatteredLanesPairEveryPAR3LaneWithItsPAR2Twin(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	bySet := map[string]map[RepairProfile]ArchiveCase{}
	for _, c := range cases {
		if !c.RepairProfile.WithholdsArticles() || c.RepairProfile.ExceedsPAR2BlockLimit() {
			continue
		}
		if bySet[c.SetID] == nil {
			bySet[c.SetID] = map[RepairProfile]ArchiveCase{}
		}
		bySet[c.SetID][c.RepairProfile] = c
	}
	lanes := bySet["repair-rar5-7-store-scatter"]
	if len(bySet) != 1 || len(lanes) != 4 {
		t.Fatalf("scattered lanes = %v", bySet)
	}
	for _, pair := range [][2]RepairProfile{
		{PAR2ScatteredLightProfile, PAR3FFTScatteredLightProfile},
		{PAR2ScatteredHeavyProfile, PAR3FFTScatteredHeavyProfile},
	} {
		par2, par3 := lanes[pair[0]], lanes[pair[1]]
		if !par2.RepairProfile.UsesPAR2() || par2.RepairProfile.UsesPAR3() || !par3.RepairProfile.UsesPAR3() || par3.RepairProfile.UsesPAR2() {
			t.Fatalf("%s/%s do not split by parity format", pair[0], pair[1])
		}
		if par2.BytesPerFile != par3.BytesPerFile || par2.VolumeSize != par3.VolumeSize || par2.ArchiveFormat != par3.ArchiveFormat {
			t.Fatalf("%s and %s are not over the same archive", par2.ID, par3.ID)
		}
	}
	// PAR2 must stay at its best geometry, one block per article, which
	// its 32768-block limit allows only below about 25 GiB.
	size, err := ByteSize(lanes[PAR2ScatteredHeavyProfile].BytesPerFile)
	if err != nil {
		t.Fatal(err)
	}
	if blocks := size / ScatteredArticleBytes; blocks >= 32768*9/10 {
		t.Fatalf("the scattered set needs %d article-sized PAR2 blocks, too close to PAR2's limit", blocks)
	}
}

func TestScatteredProfilesRefuseUUEncodedPosts(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range matrix.Sets {
		if set.ID != "repair-rar5-7-store-scatter" {
			continue
		}
		if err := set.validate(); err != nil {
			t.Fatalf("the checked-in scattered set is invalid: %v", err)
		}
		set.Encoding = UUEncodeEncoding
		if err := set.validate(); err == nil {
			t.Fatal("a uuencoded post accepted withheld articles")
		}
		return
	}
	t.Fatal("the scattered set is missing from the matrix")
}

func TestPastPAR2CapLaneMustDeclareAPostPAR2CannotHold(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range matrix.Sets {
		if set.ID != "repair-rar5-7-store-pastcap" {
			continue
		}
		if err := set.validate(); err != nil {
			t.Fatalf("the checked-in past-cap set is invalid: %v", err)
		}
		if len(set.RepairProfiles) != 1 || !set.RepairProfiles[0].ExceedsPAR2BlockLimit() || !set.RepairProfiles[0].UsesPAR3() || set.RepairProfiles[0].UsesPAR2() {
			t.Fatalf("past-cap set profiles = %v", set.RepairProfiles)
		}
		set.BytesPerFile = "8g"
		if err := set.validate(); err == nil {
			t.Fatal("an 8 GiB post was accepted as past PAR2's block limit")
		}
		return
	}
	t.Fatal("the past-cap set is missing from the matrix")
}
