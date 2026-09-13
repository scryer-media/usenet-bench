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
func TestCheckedInMatrixCarriesTheFivePAR3BreadthLanes(t *testing.T) {
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
