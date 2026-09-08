package fixture

import (
	"strings"
	"testing"
)

func breadthSet(id string, format ArchiveFormat, compressions []Compression) FixtureSet {
	set := FixtureSet{
		ID: id, WriterEra: "era", GeneratorToolchain: "rarlab-7.23",
		Class: BreadthFixtureClass, ArchiveFormat: format,
		Compressions: compressions, Solid: []bool{false},
		Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
		FileCount: 1,
	}
	if format != RAR4 && format != RAR5 {
		set.ArchiveWriter = "gnutools-bookworm"
	}
	if format.RequiresVolumes() {
		set.VolumeSize = "32m"
	}
	return set
}

// The point of validating combinations at matrix load is that a nonsense pair
// is an authoring mistake caught in a millisecond, not a generation-time
// surprise hours into a corpus build.
func TestMatrixRefusesCombinationsTheWriterCannotProduce(t *testing.T) {
	for name, testCase := range map[string]struct {
		set  FixtureSet
		want string
	}{
		"gzipped 7z": {
			set:  breadthSet("bad", SevenZip, []Compression{GzipCompression}),
			want: "no such method",
		},
		"PPMd zip": {
			set:  breadthSet("bad", Zip, []Compression{PPMdCompression}),
			want: "no such method",
		},
		"stored xz": {
			set:  breadthSet("bad", XZCompressed, []Compression{Store}),
			want: "no such method",
		},
		"compressed media": {
			set:  breadthSet("bad", Media, []Compression{DeflateCompression}),
			want: "no such method",
		},
		"encrypted tar": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.Encryptions = []Encryption{DataEncryption}
				return set
			}(),
			want: "no such mode",
		},
		"zip with encrypted headers": {
			set: func() FixtureSet {
				set := breadthSet("bad", Zip, []Compression{Store})
				set.Encryptions = []Encryption{HeaderEncryption}
				return set
			}(),
			want: "no such mode",
		},
		"solid zip": {
			set: func() FixtureSet {
				set := breadthSet("bad", Zip, []Compression{DeflateCompression})
				set.Solid = []bool{true}
				return set
			}(),
			want: "compresses each member independently",
		},
		"split tar": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.VolumeSize = "32m"
				return set
			}(),
			want: "cannot split its own output",
		},
		"unsplit 7z": {
			set: func() FixtureSet {
				set := breadthSet("bad", SevenZip, []Compression{Store})
				set.VolumeSize = ""
				return set
			}(),
			want: "empty volume_size",
		},
		"unnamed writer": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.ArchiveWriter = ""
				return set
			}(),
			want: "must name the pinned archive_writer",
		},
		"PPMd text model on RAR5": {
			set: func() FixtureSet {
				set := breadthSet("bad", RAR5, []Compression{Best})
				set.TextCompression = true
				return set
			}(),
			want: "RAR4-only",
		},
		"uuencoded archive": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.Encoding = UUEncodeEncoding
				return set
			}(),
			want: "container-less media lane",
		},
		"compressed wrapper": {
			set: func() FixtureSet {
				set := breadthSet("bad", RAR5, []Compression{Best})
				set.VolumeSize = "32m"
				set.InnerArchive = TarXZInnerArchive
				return set
			}(),
			want: "must store its inner_archive",
		},
		"wrapped tar": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.InnerArchive = TarXZInnerArchive
				return set
			}(),
			want: "only the RAR and 7z lanes wrap",
		},
		"invalid bytes_per_file": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.BytesPerFile = "224 megs"
				return set
			}(),
			want: "invalid bytes_per_file",
		},
	} {
		err := testCase.set.validate()
		if err == nil {
			t.Errorf("%s validated, want a refusal mentioning %q", name, testCase.want)
			continue
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s error = %v, want it to mention %q", name, err, testCase.want)
		}
	}
}

func TestMatrixAcceptsEveryShippedFormat(t *testing.T) {
	for _, testCase := range []struct {
		format      ArchiveFormat
		compression Compression
	}{
		{Tar, Store}, {Tar, GzipCompression}, {Tar, XZCompression},
		{XZCompressed, XZCompression},
		{Zip, Store}, {Zip, DeflateCompression},
		{SevenZip, LZMACompression}, {SevenZip, LZMA2Compression},
		{SevenZip, PPMdCompression}, {SevenZip, BZip2Compression},
		{RAR4, Normal}, {RAR4, Best}, {RAR5, Normal}, {RAR5, Best},
		{Media, Store},
	} {
		set := breadthSet("good", testCase.format, []Compression{testCase.compression})
		if err := set.validate(); err != nil {
			t.Errorf("%s/%s should validate: %v", testCase.format, testCase.compression, err)
		}
	}
}

func TestTarArgsNameTheFilterAndTheFileName(t *testing.T) {
	for _, testCase := range []struct {
		compression Compression
		name        string
		flag        string
	}{
		{Store, "fixture.tar", ""},
		{GzipCompression, "fixture.tar.gz", "--gzip"},
		{XZCompression, "fixture.tar.xz", "--xz"},
	} {
		archiveCase := ArchiveCase{ID: "tar", ArchiveFormat: Tar, Compression: testCase.compression}
		name, err := archiveCase.TarFileName()
		if err != nil || name != testCase.name {
			t.Fatalf("TarFileName() = %q, %v, want %q", name, err, testCase.name)
		}
		args, err := archiveCase.TarArgs("archive/"+name, "input", []string{"payload-01.mkv"})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		// The header dialect and member order are fixed, so the same payload
		// always produces the same tar.
		for _, want := range []string{"--create", "--format=gnu", "--sort=name", "--file archive/" + name} {
			if !strings.Contains(joined, want) {
				t.Errorf("tar args = %q, missing %q", joined, want)
			}
		}
		if testCase.flag != "" && !strings.Contains(joined, testCase.flag) {
			t.Errorf("tar args = %q, missing %q", joined, testCase.flag)
		}
	}
}

func TestZipArgsCoverTheInfoZipLanes(t *testing.T) {
	spanned := ArchiveCase{ID: "zip", ArchiveFormat: Zip, Compression: Store, Encryption: NoEncryption, VolumeSize: "32m"}
	args, err := spanned.ZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-0", "-X", "-s 32m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("spanned zip args = %q, missing %q", joined, want)
		}
	}
	encrypted := ArchiveCase{ID: "zip", ArchiveFormat: Zip, Compression: DeflateCompression, Encryption: DataEncryption}
	args, err = encrypted.ZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if joined = strings.Join(args, " "); !strings.Contains(joined, "-P "+FixturePassword) {
		t.Errorf("encrypted zip args = %q, want a non-interactive password", joined)
	}
	// Info-ZIP has no AES and no encrypted directory, so asking for one is an
	// error rather than a silently weaker lane.
	headers := ArchiveCase{ID: "zip", ArchiveFormat: Zip, Compression: Store, Encryption: HeaderEncryption}
	if _, err := headers.ZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"}); err == nil {
		t.Error("Info-ZIP cannot encrypt headers; the args builder should say so")
	}
}

func TestSevenZipZipLaneNamesItsCodecAndCipher(t *testing.T) {
	archiveCase := ArchiveCase{ID: "zip", ArchiveFormat: Zip, Compression: DeflateCompression, Encryption: DataEncryption}
	args, err := archiveCase.SevenZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-tzip", "-mm=Deflate", "-mem=AES256", "-p" + FixturePassword} {
		if !strings.Contains(joined, want) {
			t.Errorf("7-Zip zip args = %q, missing %q", joined, want)
		}
	}
	// -ms is a 7z concept; a zip has no solid block to switch on.
	if strings.Contains(joined, "-ms=") {
		t.Errorf("7-Zip zip args = %q, must not carry a solid switch", joined)
	}
}

func TestSevenZipNamesEveryCodecExplicitly(t *testing.T) {
	// A fixture that inherited the writer's default would silently change
	// codec the day a later 7-Zip release changed its mind, and the manifest
	// would still claim the old one.
	for compression, want := range map[Compression]string{
		LZMACompression:  "-m0=LZMA",
		LZMA2Compression: "-m0=LZMA2",
		PPMdCompression:  "-m0=PPMd",
		BZip2Compression: "-m0=BZip2",
		Store:            "-m0=Copy",
	} {
		archiveCase := ArchiveCase{ID: "7z", ArchiveFormat: SevenZip, Compression: compression, Encryption: NoEncryption, VolumeSize: "32m"}
		args, err := archiveCase.SevenZipArgs("../archive/fixture.7z", []string{"payload-01.mkv"})
		if err != nil {
			t.Fatal(err)
		}
		if joined := strings.Join(args, " "); !strings.Contains(joined, want) {
			t.Errorf("%s args = %q, missing %q", compression, joined, want)
		}
	}
}

func TestXZArgsAreSingleThreadedForReproducibility(t *testing.T) {
	// xz's multi-threaded encoder splits the input into independently
	// compressed blocks, so its output would otherwise depend on how many
	// cores the machine building the corpus happens to have.
	archiveCase := ArchiveCase{ID: "xz", ArchiveFormat: XZCompressed, Compression: XZCompression}
	args, err := archiveCase.XZArgs("payload-01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--threads=1", "--format=xz", "payload-01.mkv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("xz args = %q, missing %q", joined, want)
		}
	}
}

func TestByteSizeParsesTheMatrixSpellings(t *testing.T) {
	for input, want := range map[string]int64{
		"32m":  32 << 20,
		"224m": 224 << 20,
		"56m":  56 << 20,
		"128k": 128 << 10,
		"1g":   1 << 30,
		"1024": 1024,
	} {
		got, err := ByteSize(input)
		if err != nil || got != want {
			t.Errorf("ByteSize(%q) = %d, %v, want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "224 megs", "-1m", "m"} {
		if _, err := ByteSize(input); err == nil {
			t.Errorf("ByteSize(%q) should be refused", input)
		}
	}
}

func TestMatrixRefusesZipStructuresByName(t *testing.T) {
	zipStructureSet := func(structure ZipStructure, adjust func(*FixtureSet)) FixtureSet {
		set := breadthSet("bad", Zip, []Compression{Store})
		set.ZipStructure = structure
		if adjust != nil {
			adjust(&set)
		}
		return set
	}
	for name, testCase := range map[string]struct {
		set  FixtureSet
		want string
	}{
		"unknown structure": {
			set:  zipStructureSet("zip65", nil),
			want: "unsupported zip_structure",
		},
		"zip64 on a 7z": {
			set: func() FixtureSet {
				set := breadthSet("bad", SevenZip, []Compression{Store})
				set.ZipStructure = ForcedZip64Structure
				return set
			}(),
			want: "ZIP container structures",
		},
		"zip64 on a tar": {
			set: func() FixtureSet {
				set := breadthSet("bad", Tar, []Compression{Store})
				set.ZipStructure = StreamedZipStructure
				return set
			}(),
			want: "ZIP container structures",
		},
		"encrypted zip64": {
			set: zipStructureSet(ForcedZip64Structure, func(set *FixtureSet) {
				set.Encryptions = []Encryption{DataEncryption}
			}),
			want: "would make one failure two things at once",
		},
		"spanned stream": {
			set: zipStructureSet(StreamedZipStructure, func(set *FixtureSet) {
				set.VolumeSize = "32m"
			}),
			want: "written into a pipe and has no split to write",
		},
		"spanned large member": {
			set: zipStructureSet(LargeZip64Structure, func(set *FixtureSet) {
				set.PayloadLayout = Zip64LargePayloadLayout
				set.VolumeSize = "32m"
			}),
			want: "single volume holding one member past 4 GiB",
		},
		"large member without its layout": {
			set:  zipStructureSet(LargeZip64Structure, nil),
			want: "must declare payload_layout",
		},
		"forced zip64 over a disc layout": {
			set: zipStructureSet(ForcedZip64Structure, func(set *FixtureSet) {
				set.PayloadLayout = BluRayDiscPayloadLayout
			}),
			want: "over a payload that does not need them",
		},
		"large layout on an ordinary zip": {
			set: func() FixtureSet {
				set := breadthSet("bad", Zip, []Compression{Store})
				set.PayloadLayout = Zip64LargePayloadLayout
				return set
			}(),
			want: "outside zip_structure",
		},
		"large layout with several members": {
			set: zipStructureSet(LargeZip64Structure, func(set *FixtureSet) {
				set.PayloadLayout = Zip64LargePayloadLayout
				set.FileCount = 4
			}),
			want: "writes exactly one member",
		},
	} {
		err := testCase.set.validate()
		if err == nil {
			t.Errorf("%s validated, want a refusal mentioning %q", name, testCase.want)
			continue
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s error = %v, want it to mention %q", name, err, testCase.want)
		}
	}
}

func TestMatrixAcceptsTheThreeZipStructureLanes(t *testing.T) {
	forced := breadthSet("zip-zip64-forced", Zip, []Compression{Store})
	forced.ZipStructure = ForcedZip64Structure
	streamed := breadthSet("zip-streamed", Zip, []Compression{Store})
	streamed.ZipStructure = StreamedZipStructure
	streamed.FileCount = 2
	large := breadthSet("zip-zip64-large", Zip, []Compression{Store})
	large.ArchiveWriter = "sevenzip-26.02"
	large.ZipStructure = LargeZip64Structure
	large.PayloadLayout = Zip64LargePayloadLayout
	for _, set := range []FixtureSet{forced, streamed, large} {
		if err := set.validate(); err != nil {
			t.Errorf("%s should validate: %v", set.ID, err)
		}
		cases := Matrix{SchemaVersion: 2, Sets: []FixtureSet{set}}
		expanded, err := cases.Expand()
		if err != nil {
			t.Fatalf("%s: %v", set.ID, err)
		}
		if len(expanded) != 1 {
			t.Fatalf("%s expanded to %d cases, want 1", set.ID, len(expanded))
		}
		if expanded[0].ZipStructure != set.ZipStructure {
			t.Errorf("%s expanded with zip_structure %q, want %q", set.ID, expanded[0].ZipStructure, set.ZipStructure)
		}
	}
}

func TestZipStructureArgsSplitTheWritersByWhatTheyCanProduce(t *testing.T) {
	forced := ArchiveCase{ID: "zip-zip64-forced", ArchiveFormat: Zip, Compression: Store, Encryption: NoEncryption, ZipStructure: ForcedZip64Structure}
	args, err := forced.ZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-fz") {
		t.Errorf("forced zip64 args = %q, missing -fz", joined)
	}
	if !strings.Contains(joined, "../archive/fixture.zip") {
		t.Errorf("forced zip64 args = %q, want the archive named as a file", joined)
	}
	if forced.WritesToStandardOutput() {
		t.Error("the forced lane is written to a file, not a pipe")
	}

	// The streamed lane names "-" as the archive, which is what makes the
	// writer emit data descriptors instead of patching the local headers.
	streamed := ArchiveCase{ID: "zip-streamed", ArchiveFormat: Zip, Compression: Store, Encryption: NoEncryption, ZipStructure: StreamedZipStructure}
	if args, err = streamed.ZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"}); err != nil {
		t.Fatal(err)
	}
	if got := args[len(args)-2]; got != "-" {
		t.Errorf("streamed zip args = %q, want the archive named as a pipe", strings.Join(args, " "))
	}
	if strings.Contains(strings.Join(args, " "), "-fz") {
		t.Errorf("streamed zip args = %q, want no -fz: Info-ZIP writes a truncated zip64 record into a pipe", strings.Join(args, " "))
	}
	if !streamed.WritesToStandardOutput() {
		t.Error("the streamed lane is written to a pipe")
	}

	// The member past 4 GiB is 7-Zip's; Info-ZIP is never asked for it.
	large := ArchiveCase{ID: "zip-zip64-large", ArchiveFormat: Zip, Compression: Store, Encryption: NoEncryption, ZipStructure: LargeZip64Structure}
	if _, err := large.ZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"}); err == nil {
		t.Error("Info-ZIP should refuse the large-member lane by name")
	}
	if _, err := large.SevenZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"}); err != nil {
		t.Errorf("7-Zip writes the large-member lane: %v", err)
	}
	// 7-Zip has no force-zip64 switch and cannot stream a zip into a pipe.
	for _, structure := range []ZipStructure{ForcedZip64Structure, StreamedZipStructure} {
		unwritable := ArchiveCase{ID: "zip", ArchiveFormat: Zip, Compression: Store, Encryption: NoEncryption, ZipStructure: structure}
		if _, err := unwritable.SevenZipArgs("../archive/fixture.zip", []string{"payload-01.mkv"}); err == nil {
			t.Errorf("7-Zip should refuse the %q structure by name", structure)
		}
	}
}
