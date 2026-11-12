package fixture

import (
	"slices"
	"strings"
	"testing"
)

func TestNestedChainParsesEveryLayerOption(t *testing.T) {
	layers, err := NestedChain("zip:deflate*6 > rar5:store:15m+par2 > 7z:normal").Layers()
	if err != nil {
		t.Fatal(err)
	}
	want := []NestedLayer{
		{Format: Zip, Compression: DeflateCompression, Archives: 6},
		{Format: RAR5, Compression: Store, VolumeSize: "15m", Archives: 1, PAR2: true},
		{Format: SevenZip, Compression: Normal, Archives: 1},
	}
	if !slices.Equal(layers, want) {
		t.Fatalf("parsed %+v", layers)
	}
	if name := layers[1].ArchiveName(2, 1); name != "level2-01.rar" {
		t.Fatalf("layer archive name %q", name)
	}
	for _, bad := range []string{"rar4:store", "zip:store:10m", "tar:deflate", "7z", "rar5:store*1", "rar5:store:big"} {
		if _, err := NestedChain(bad).Layers(); err == nil {
			t.Fatalf("nested chain %q was accepted", bad)
		}
	}
}

func TestNestedLayersAreWrittenWithThePostedArchivesArguments(t *testing.T) {
	parent := ArchiveCase{ID: "parent"}
	layers, err := NestedChain("rar5:normal:15m > 7z:store > zip:deflate > tar:xz").Layers()
	if err != nil {
		t.Fatal(err)
	}
	rar, err := layers[0].Case(parent, 1).RARArgs("nested-stage/level1-01.rar", []string{"input/payload-01.mkv"})
	if err != nil || !slices.Contains(rar, "-ma5") || !slices.Contains(rar, "-m3") || !slices.Contains(rar, "-v15m") {
		t.Fatalf("RAR layer arguments %v, %v", rar, err)
	}
	sevenZip, err := layers[1].SevenZipArgs(parent, 2, "../nested-stage/level2-01.7z", []string{"level1-01.part1.rar"})
	if err != nil || !slices.Contains(sevenZip, "-t7z") || !slices.Contains(sevenZip, "-m0=Copy") || slices.ContainsFunc(sevenZip, func(arg string) bool { return strings.HasPrefix(arg, "-v") }) {
		t.Fatalf("7z layer arguments %v, %v", sevenZip, err)
	}
	zip, err := layers[2].SevenZipArgs(parent, 3, "../nested-stage/level3-01.zip", []string{"level2-01.7z"})
	if err != nil || !slices.Contains(zip, "-tzip") || !slices.Contains(zip, "-mm=Deflate") {
		t.Fatalf("zip layer arguments %v, %v", zip, err)
	}
	tar, err := layers[3].Case(parent, 4).TarArgs("nested-stage/level4-01.tar.xz", "input", []string{"level3-01.zip"})
	if err != nil || !slices.Contains(tar, "--xz") {
		t.Fatalf("tar layer arguments %v, %v", tar, err)
	}
}

// The checked-in matrix carries the nested shapes the bench is meant to load
// an unpack graph with: two, three and five containers deep, two broad ones,
// one that alternates stored and compressed layers, and one that needs a
// repair at two levels.
func TestCheckedInMatrixCarriesTheNestedStressShapes(t *testing.T) {
	matrix, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := matrix.Expand()
	if err != nil {
		t.Fatal(err)
	}
	depths := map[int]int{}
	broad, alternating, nestedRepair := 0, 0, 0
	for _, c := range cases {
		layers, err := c.Nested.Layers()
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		if len(layers) == 0 {
			continue
		}
		depths[c.Depth()]++
		if c.ArchiveFormat != RAR5 || c.VolumeSize != "30m" {
			t.Fatalf("%s is not a RAR5 set in 30 MiB volumes", c.ID)
		}
		siblings, inner := 1, false
		// Outside in, starting from the posted archive.
		flips, previous := 0, c.Compression != Store
		for index := len(layers) - 1; index >= 0; index-- {
			siblings = max(siblings, layers[index].Archives)
			inner = inner || layers[index].PAR2
			if layers[index].Compressed() != previous {
				flips++
			}
			previous = layers[index].Compressed()
		}
		if siblings > 1 {
			broad++
		}
		if len(layers) >= 3 && flips == len(layers) {
			alternating++
		}
		if inner && c.RepairProfile.UsesPAR2() {
			nestedRepair++
		}
	}
	// Ten nested lanes in all, and the depth kinds are as deep as named.
	nested := 0
	for _, c := range cases {
		if c.Nested == "" {
			continue
		}
		nested++
		kind, _, _ := strings.Cut(strings.TrimPrefix(c.ID, "nested-"), "-")
		if want, ok := map[string]int{"2deep": 2, "3deep": 3, "5deep": 5}[kind]; ok && c.Depth() != want {
			t.Fatalf("%s is %d containers deep", c.ID, c.Depth())
		}
	}
	if nested != 10 {
		t.Fatalf("the matrix has %d nested lanes, want 10", nested)
	}
	if depths[2] == 0 || depths[3] == 0 || depths[5] == 0 {
		t.Fatalf("nested depths in the matrix: %v, want 2, 3 and 5", depths)
	}
	if broad < 2 || alternating < 1 || nestedRepair < 1 {
		t.Fatalf("matrix has %d broad, %d alternating and %d nested-repair lanes", broad, alternating, nestedRepair)
	}
}

func TestNestedChainIsRejectedWhereItCannotBeWritten(t *testing.T) {
	base := FixtureSet{ID: "nested", Class: BreadthFixtureClass, WriterEra: "RAR 7.23", GeneratorToolchain: "rarlab-7.23", ArchiveFormat: RAR5,
		Compressions: []Compression{Store}, Solid: []bool{false}, Encryptions: []Encryption{NoEncryption}, Payloads: []PayloadKind{IncompressiblePayload},
		Nested: "rar5:store*2", FileCount: 2, VolumeSize: "30m"}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	tooBroad := base
	tooBroad.Nested = "rar5:store*3"
	if err := tooBroad.validate(); err == nil {
		t.Fatal("two files were spread over three archives")
	}
	encrypted := base
	encrypted.Encryptions = []Encryption{HeaderEncryption}
	if err := encrypted.validate(); err == nil {
		t.Fatal("a nested chain was accepted inside an encrypted archive")
	}
	unrepairable := base
	unrepairable.Nested = "rar5:store+par2"
	unrepairable.FileCount = 1
	if err := unrepairable.validate(); err == nil {
		t.Fatal("a nested PAR2 set over a single file was accepted")
	}
}
