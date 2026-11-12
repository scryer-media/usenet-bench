package fixture

import (
	"slices"
	"strings"
	"testing"
)

func TestExpandCoversEveryAxis(t *testing.T) {
	matrix := Matrix{
		SchemaVersion: 2,
		Sets: []FixtureSet{{
			ID:                 "modern-rar5",
			Class:              BreadthFixtureClass,
			WriterEra:          "RAR 5.x-7.x compatibility",
			GeneratorToolchain: "rarlab-7.23",
			ArchiveFormat:      RAR5,
			Compressions:       []Compression{Store, Normal},
			Solid:              []bool{false, true},
			Encryptions:        []Encryption{NoEncryption, DataEncryption, HeaderEncryption},
			Payloads:           []PayloadKind{IncompressiblePayload, CompressiblePayload},
			FileCount:          4,
			VolumeSize:         "32m",
		}},
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatalf("Expand() error = %v", err)
	}
	if got, want := len(cases), 24; got != want {
		t.Fatalf("len(Expand()) = %d, want %d", got, want)
	}
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.ID] = true
	}
	if !seen["modern-rar5-normal-solid-headers-compressible"] {
		t.Fatalf("expanded matrix omitted solid encrypted compressed fixture")
	}
}

func TestRARArgsAreExplicit(t *testing.T) {
	c := ArchiveCase{
		ID:            "case",
		ArchiveFormat: RAR5,
		Compression:   Best,
		Solid:         true,
		Encryption:    HeaderEncryption,
		VolumeSize:    "32m",
	}
	args, err := c.RARArgs("archive/fixture.rar", []string{"input/one.bin", "input/two.bin"})
	if err != nil {
		t.Fatalf("RARArgs() error = %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-ma5", "-qo-", "-m5", "-md256m", "-s", "-hp" + FixturePassword, "-v32m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("RARArgs() = %q, missing %q", joined, want)
		}
	}
}

func TestQuickOpenLaneKeepsTheRAR5QuickOpenRecords(t *testing.T) {
	c := ArchiveCase{
		ID:            "quick-open-case",
		ArchiveFormat: RAR5,
		Compression:   Store,
		Solid:         false,
		Encryption:    NoEncryption,
		VolumeSize:    "32m",
		QuickOpen:     true,
	}
	args, err := c.RARArgs("archive/fixture.rar", []string{"input/one.bin"})
	if err != nil {
		t.Fatalf("RARArgs() error = %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-qo-") {
		t.Fatalf("quick-open lane must not suppress the records: %q", joined)
	}
	if !strings.Contains(joined, "-ma5 -qo+") {
		t.Fatalf("quick-open lane must request a record for every header: %q", joined)
	}
}

func TestQuickOpenIsRejectedOutsideRAR5(t *testing.T) {
	for _, format := range []ArchiveFormat{RAR4, SevenZip} {
		set := FixtureSet{
			ID: "quick-open-" + string(format), WriterEra: "era", GeneratorToolchain: "toolchain",
			Class:         BreadthFixtureClass,
			ArchiveFormat: format, Compressions: []Compression{Store}, Solid: []bool{false},
			Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
			FileCount: 1, VolumeSize: "32m", QuickOpen: true,
		}
		if err := set.validate(); err == nil || !strings.Contains(err.Error(), "quick_open") {
			t.Errorf("%s set with quick_open validated, want a quick_open error, got %v", format, err)
		}
	}
}

func TestExpandCarriesQuickOpenOntoEveryCase(t *testing.T) {
	matrix := Matrix{SchemaVersion: 2, Sets: []FixtureSet{{
		ID: "rar5-7-quickopen", WriterEra: "RAR 7.23", GeneratorToolchain: "rarlab-7.23",
		Class:         BreadthFixtureClass,
		ArchiveFormat: RAR5, Compressions: []Compression{Store}, Solid: []bool{false},
		Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
		FileCount: 4, VolumeSize: "32m", QuickOpen: true,
	}}}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || !cases[0].QuickOpen || cases[0].ID != "rar5-7-quickopen-store-nonsolid-none-incompressible" {
		t.Fatalf("Expand() = %+v, want one quick-open case", cases)
	}
}

func TestRAR4ReleaseCompressionUsesItsMaximumDictionary(t *testing.T) {
	c := ArchiveCase{
		ID:            "rar4-release",
		ArchiveFormat: RAR4,
		Compression:   Best,
		Solid:         true,
		Encryption:    NoEncryption,
		VolumeSize:    "32m",
	}
	args, err := c.RARArgs("archive/fixture.rar", []string{"input/one.bin"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-m5", "-md4096", "-s"} {
		if !strings.Contains(joined, want) {
			t.Errorf("RAR4 args = %q, missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "-qo-") {
		t.Fatalf("RAR4 args must not use RAR5-only quick-open control: %q", joined)
	}
}

// Normal is the writer's own default level, which is what a release
// compressed without thinking about it carries. It must not silently become
// the maximum: the corpus has a separate lane for that, and pooling the two
// would leave no fixture measuring ordinary compression at all.
func TestNormalCompressionIsTheWritersDefaultLevel(t *testing.T) {
	for _, format := range []ArchiveFormat{RAR4, RAR5} {
		c := ArchiveCase{
			ID:            "normal-" + string(format),
			ArchiveFormat: format,
			Compression:   Normal,
			Encryption:    NoEncryption,
			VolumeSize:    "32m",
		}
		args, err := c.RARArgs("archive/fixture.rar", []string{"input/one.bin"})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-m3") {
			t.Errorf("%s normal args = %q, want -m3", format, joined)
		}
		if strings.Contains(joined, "-md") {
			t.Errorf("%s normal args = %q, must leave the dictionary at the writer default", format, joined)
		}
	}
}

func TestLegacyRAR4ArgsUseTheLockedWriterDefault(t *testing.T) {
	c := ArchiveCase{
		ID:            "legacy-case",
		ArchiveFormat: RAR4,
		Compression:   Store,
		Solid:         false,
		Encryption:    DataEncryption,
		VolumeSize:    "32m",
	}
	args, err := c.RARArgs("archive/fixture.rar", []string{"input/one.bin"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-ma") {
		t.Fatalf("legacy RAR4 must not claim a newer -ma format selector: %q", joined)
	}
	for _, want := range []string{"-m0", "-s-", "-p" + FixturePassword, "-v32m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("legacy RAR4 args = %q, missing %q", joined, want)
		}
	}
}

func TestBluRayLayoutDoesNotRequireUniformFileCount(t *testing.T) {
	matrix := Matrix{SchemaVersion: 2, Sets: []FixtureSet{{
		ID:                 "modern-rar5-bluray",
		Class:              BreadthFixtureClass,
		WriterEra:          "RAR 5.x-7.x compatibility",
		GeneratorToolchain: "rarlab-7.23",
		ArchiveFormat:      RAR5,
		Compressions:       []Compression{Normal},
		Solid:              []bool{true},
		Encryptions:        []Encryption{NoEncryption},
		Payloads:           []PayloadKind{IncompressiblePayload},
		PayloadLayout:      BluRayDiscPayloadLayout,
		VolumeSize:         "32m",
	}}}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(cases), 1; got != want {
		t.Fatalf("cases = %d, want %d", got, want)
	}
	if cases[0].PayloadLayout != BluRayDiscPayloadLayout {
		t.Fatalf("layout = %q", cases[0].PayloadLayout)
	}
}

func TestRepairProfilesAreExplicitFixtureCases(t *testing.T) {
	matrix := Matrix{SchemaVersion: 2, Sets: []FixtureSet{{
		ID:                 "repair-rar5",
		Class:              BreadthFixtureClass,
		WriterEra:          "RAR 7.23",
		GeneratorToolchain: "rarlab-7.23",
		ArchiveFormat:      RAR5,
		Compressions:       []Compression{Normal},
		Solid:              []bool{true},
		Encryptions:        []Encryption{NoEncryption},
		Payloads:           []PayloadKind{IncompressiblePayload},
		RepairProfiles:     []RepairProfile{PAR2LightRepairProfile, RARRecoveryVolumeHeavyProfile},
		FileCount:          4,
		VolumeSize:         "32m",
	}}}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(cases), 2; got != want {
		t.Fatalf("cases = %d, want %d", got, want)
	}
	if got, want := cases[0].ID, "repair-rar5-par2-light-normal-solid-none-incompressible"; got != want {
		t.Fatalf("case id = %q, want %q", got, want)
	}
	if cases[1].RepairProfile != RARRecoveryVolumeHeavyProfile {
		t.Fatalf("second repair profile = %q", cases[1].RepairProfile)
	}
}

func TestFixtureSetRequiresAClass(t *testing.T) {
	matrix := Matrix{
		SchemaVersion: 2,
		Sets: []FixtureSet{{
			ID:                 "unclassed",
			WriterEra:          "RAR 7.23",
			GeneratorToolchain: "rarlab-7.23",
			ArchiveFormat:      RAR5,
			Compressions:       []Compression{Store},
			Solid:              []bool{false},
			Encryptions:        []Encryption{HeaderEncryption},
			Payloads:           []PayloadKind{IncompressiblePayload},
			FileCount:          1,
			VolumeSize:         "32m",
		}},
	}
	if _, err := matrix.Expand(); err == nil || !strings.Contains(err.Error(), "class") {
		t.Fatalf("matrix without a class expanded: %v", err)
	}
	matrix.Sets[0].Class = FixtureClass("popular")
	if _, err := matrix.Expand(); err == nil {
		t.Fatal("matrix with an unknown class expanded")
	}
	matrix.Sets[0].Class = HeadlineFixtureClass
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].Class != HeadlineFixtureClass {
		t.Fatalf("expanded case did not carry the set's class: %+v", cases)
	}
}

func TestCheckedInMatrixClassesTheHeadlineAsRealPostShapes(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	headline := 0
	for _, c := range cases {
		if c.Class != HeadlineFixtureClass {
			continue
		}
		headline++
		if c.Compression != Store || (c.ArchiveFormat != RAR4 && c.ArchiveFormat != RAR5 && c.ArchiveFormat != Media) || c.Payload != IncompressiblePayload || c.FileCount != 1 {
			t.Fatalf("headline fixture %s is not a single incompressible movie, stored in RAR or posted bare: %+v", c.ID, c)
		}
		switch c.RepairProfile {
		case CleanRepairProfile:
		case PAR2ArticlesLightProfile, PAR2ArticlesHeavyProfile, PAR2HeavyWithheldProfile:
			// A headline repair copies an observed post, so its PAR2
			// geometry is declared rather than left at the fixed default.
			if !c.RecoveryShape.Declared() {
				t.Fatalf("headline fixture %s repairs with PAR2 but declares no real-post recovery_shape", c.ID)
			}
		default:
			t.Fatalf("headline fixture %s carries repair profile %q; only clean and real-shape PAR2 profiles are the common case", c.ID, c.RepairProfile)
		}
		// Bare media is what most real posts are, and it is only in the
		// headline class as a PAR2 repair.
		if c.ArchiveFormat == Media && !c.RepairProfile.LosesArticles() {
			t.Fatalf("headline media fixture %s is not an article-loss PAR2 repair", c.ID)
		}
	}
	if headline == 0 {
		t.Fatal("checked-in matrix declares no headline fixtures")
	}
	// Every RARLAB writer era must have a clean headline lane in each posting
	// form: most real posts are not encrypted, and the encrypted forms must
	// still be measured beside the clear one rather than instead of it.
	for _, era := range []string{"rarlab-3.93", "rarlab-4.20", "rarlab-5.00", "rarlab-6.24", "rarlab-7.23"} {
		for _, encryption := range []Encryption{NoEncryption, HeaderEncryption, DataEncryption} {
			found := false
			for _, c := range cases {
				if c.Class == HeadlineFixtureClass && c.GeneratorToolchain == era && c.RepairProfile == CleanRepairProfile && c.Encryption == encryption {
					found = true
				}
			}
			if !found {
				t.Fatalf("no clean headline fixture for writer %s with %s encryption", era, encryption)
			}
		}
	}
	// PAR2 repair is part of the common case, not a compatibility extra: the
	// stored RAR from the 4.20, 5.00 and 7.23 writers must carry a headline
	// lane for a lost article and for a withheld volume, in each of the three
	// postings.
	for _, era := range []string{"rarlab-4.20", "rarlab-5.00", "rarlab-7.23"} {
		for _, profile := range []RepairProfile{PAR2ArticlesLightProfile, PAR2HeavyWithheldProfile} {
			for _, encryption := range []Encryption{NoEncryption, HeaderEncryption, DataEncryption} {
				found := false
				for _, c := range cases {
					if c.Class == HeadlineFixtureClass && c.GeneratorToolchain == era && c.RepairProfile == profile && c.Encryption == encryption {
						found = true
					}
				}
				if !found {
					t.Fatalf("no headline %s fixture for writer %s with %s encryption", profile, era, encryption)
				}
			}
		}
	}
}

func TestArticleLossProfilesNeedADeclaredRecoveryShape(t *testing.T) {
	set := FixtureSet{
		ID: "media", Class: BreadthFixtureClass, WriterEra: "not-applicable", GeneratorToolchain: "rarlab-7.23", ArchiveWriter: "rarlab-7.23",
		ArchiveFormat: Media, Compressions: []Compression{Store}, Solid: []bool{false}, Encryptions: []Encryption{NoEncryption},
		Payloads: []PayloadKind{IncompressiblePayload}, RepairProfiles: []RepairProfile{PAR2ArticlesHeavyProfile}, FileCount: 1,
	}
	if err := set.validate(); err == nil {
		t.Fatal("an article-loss profile was accepted without a recovery_shape")
	}
	set.RecoveryShape = RecoveryShape{SliceBytes: 2643160, RecoveryBlocks: 40, ArticleBytes: 716800}
	if err := set.validate(); err != nil {
		t.Fatal(err)
	}
	set.RecoveryShape.SliceBytes = 2643161
	if err := set.validate(); err == nil {
		t.Fatal("a slice size that is not a multiple of four was accepted")
	}
	set.RecoveryShape.SliceBytes = 2643160
	set.RepairProfiles = []RepairProfile{PAR2ScatteredHeavyProfile}
	if err := set.validate(); err == nil {
		t.Fatal("a recovery_shape was accepted on a profile that cuts its own slices")
	}
}

// The RAR recovery kinds have no census figure, so they take the median RAR
// post's PAR2 redundancy: 167 recovery blocks over 1998 slices is 8.4%, which
// is an 8% recovery record, one recovery volume over thirteen, and two for the
// heavy lane.
func TestRARRecoveryKindsBorrowThePAR2ShapesRedundancy(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	var par2 RecoveryShape
	for _, c := range cases {
		if c.SetID == "repair-rar5-7-store-par2" {
			par2 = c.RecoveryShape
		}
	}
	records, volumes := 0, 0
	for _, c := range cases {
		switch {
		case c.RepairProfile.UsesRARRecoveryRecord():
			records++
			if c.RecoveryShape != par2 {
				t.Fatalf("%s does not carry the RAR PAR2 post's shape", c.ID)
			}
			percent, err := c.RARRecoveryRecordPercent()
			if err != nil || percent != 8 {
				t.Fatalf("%s: recovery record %d%%, %v", c.ID, percent, err)
			}
			args, err := c.RARArgs("archive/fixture.rar", []string{"payload.bin"})
			if err != nil || !slices.Contains(args, "-rr8p") {
				t.Fatalf("%s: RAR arguments %v, %v", c.ID, args, err)
			}
		case c.RepairProfile.UsesRARRecoveryVolumes() && c.RecoveryShape.Declared():
			volumes++
			if c.RecoveryShape != par2 {
				t.Fatalf("%s does not carry the RAR PAR2 post's shape", c.ID)
			}
			want := 1
			if c.RepairProfile == RARRecoveryVolumeHeavyProfile {
				want = 2
			}
			if count, err := c.RARRecoveryVolumeCount(13); err != nil || count != want {
				t.Fatalf("%s: %d recovery volumes, %v; want %d", c.ID, count, err, want)
			}
			args, err := c.RARArgs("archive/fixture.rar", []string{"payload.bin"})
			if err != nil || slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "-rr") }) {
				t.Fatalf("%s: a recovery-volume lane was given a recovery record: %v, %v", c.ID, args, err)
			}
		}
	}
	if records != 2 || volumes != 2 {
		t.Fatalf("matrix has %d recovery-record and %d shaped recovery-volume fixtures, want 2 and 2", records, volumes)
	}
	// Without a shape the counts stay the fixed one and two.
	bare := ArchiveCase{ID: "bare", RepairProfile: RARRecoveryVolumeHeavyProfile}
	if count, err := bare.RARRecoveryVolumeCount(5); err != nil || count != 2 {
		t.Fatalf("unshaped heavy lane posts %d recovery volumes, %v", count, err)
	}
}

func TestRecoveryRecordProfileIsRAROnlyAndOwnsItsSet(t *testing.T) {
	base := FixtureSet{ID: "rr", Class: BreadthFixtureClass, WriterEra: "RAR 7.23", GeneratorToolchain: "rarlab-7.23", ArchiveFormat: RAR5,
		Compressions: []Compression{Store}, Solid: []bool{false}, Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
		RepairProfiles: []RepairProfile{RARRecoveryRecordLightProfile}, FileCount: 1, BytesPerFile: "390700000", VolumeSize: "30m",
		RecoveryShape: RecoveryShape{SliceBytes: 195596, RecoveryBlocks: 167, ArticleBytes: 716800}}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	mixed := base
	mixed.RepairProfiles = []RepairProfile{RARRecoveryRecordLightProfile, PAR2ArticlesLightProfile}
	if err := mixed.validate(); err == nil {
		t.Fatal("a recovery-record set shared its archive with another profile")
	}
	unshaped := base
	unshaped.RecoveryShape = RecoveryShape{}
	if err := unshaped.validate(); err == nil {
		t.Fatal("a recovery-record set was accepted without a borrowed shape")
	}
}
