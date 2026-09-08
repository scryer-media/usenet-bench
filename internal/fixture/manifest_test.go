package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGeneratedManifestV4RequiresMatchingRepairMetadata(t *testing.T) {
	manifest := GeneratedManifest{
		SchemaVersion:      4,
		Case:               ArchiveCase{ID: "repair", RepairProfile: PAR2LightRepairProfile},
		ExpectedFiles:      []FileDigest{{Path: "payload.bin", Size: 1, BLAKE3: "a"}},
		SourceArchiveFiles: []FileDigest{{Path: "archive/fixture.part01.rar", Size: 1, BLAKE3: "b"}},
		ArchiveFiles:       []FileDigest{{Path: "archive/fixture.part01.rar", Size: 1, BLAKE3: "c"}},
		Repair: RepairDetails{
			Profile:               PAR2LightRepairProfile,
			PAR2RedundancyPercent: 10,
			Corruptions:           []CorruptionDetail{{Kind: "byte-flip", Path: "archive/fixture.part01.rar", Offset: 1, Length: 1}},
		},
	}
	path := writeTestManifest(t, manifest)
	loaded, err := LoadGeneratedManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repair.Profile != PAR2LightRepairProfile || len(loaded.SourceArchiveFiles) != 1 {
		t.Fatalf("loaded repair manifest = %#v", loaded)
	}

	manifest.Repair.Profile = RARRecoveryVolumeLightProfile
	path = writeTestManifest(t, manifest)
	if _, err := LoadGeneratedManifest(path); err == nil {
		t.Fatal("mismatched case and repair profile was accepted")
	}
}

func TestLoadGeneratedManifestUpgradesLegacyManifestAsClean(t *testing.T) {
	path := writeTestManifest(t, GeneratedManifest{
		SchemaVersion: 3,
		Case:          ArchiveCase{ID: "clean"},
		ExpectedFiles: []FileDigest{{Path: "payload.bin", Size: 1, BLAKE3: "a"}},
		ArchiveFiles:  []FileDigest{{Path: "archive/fixture.part01.rar", Size: 1, BLAKE3: "b"}},
	})
	loaded, err := LoadGeneratedManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repair.Profile != CleanRepairProfile || len(loaded.SourceArchiveFiles) != 1 {
		t.Fatalf("legacy manifest was not normalized as clean: %#v", loaded)
	}
}

func writeTestManifest(t *testing.T, manifest GeneratedManifest) string {
	t.Helper()
	contents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture-manifest.json")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every fixture written before the uuencode lane existed was yEnc, and its
// compression result can be recomputed from the digests it already carries,
// so an old corpus keeps working without being regenerated.
func TestLoadGeneratedManifestBackfillsEncodingAndCompression(t *testing.T) {
	manifest := GeneratedManifest{
		SchemaVersion: 6,
		Case: ArchiveCase{
			ID:            "old",
			RepairProfile: CleanRepairProfile,
			NZBOrder:      SequentialNZBOrder,
			Compression:   Store,
		},
		ExpectedFiles:      []FileDigest{{Path: "payload-01.mkv", Size: 200, BLAKE3: "a"}},
		SourceArchiveFiles: []FileDigest{{Path: "archive/fixture.part01.rar", Size: 100, BLAKE3: "b"}},
		ArchiveFiles:       []FileDigest{{Path: "archive/fixture.part01.rar", Size: 100, BLAKE3: "b"}},
		NZBFileOrder:       []string{"archive/fixture.part01.rar"},
		Repair:             RepairDetails{Profile: CleanRepairProfile},
	}
	loaded, err := LoadGeneratedManifest(writeTestManifest(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Encoding != YEncEncoding || loaded.Case.PostEncodingOrDefault() != YEncEncoding {
		t.Fatalf("encoding = %q, want yEnc", loaded.Encoding)
	}
	if loaded.Compression.PayloadBytes != 200 || loaded.Compression.ArchiveBytes != 100 || loaded.Compression.Ratio != 0.5 {
		t.Fatalf("compression backfill = %#v, want the measured 100/200", loaded.Compression)
	}
}

// The encoding field and the case's own are written together, so a manifest
// that disagrees with itself has been edited and is not trustworthy.
func TestLoadGeneratedManifestRefusesADisagreementAboutEncoding(t *testing.T) {
	manifest := GeneratedManifest{
		SchemaVersion: GeneratedManifestSchemaVersion,
		Case: ArchiveCase{
			ID:            "mixed",
			RepairProfile: CleanRepairProfile,
			NZBOrder:      SequentialNZBOrder,
			Encoding:      UUEncodeEncoding,
		},
		ExpectedFiles:      []FileDigest{{Path: "payload-01.mkv", Size: 1, BLAKE3: "a"}},
		SourceArchiveFiles: []FileDigest{{Path: "archive/payload-01.mkv", Size: 1, BLAKE3: "a"}},
		ArchiveFiles:       []FileDigest{{Path: "archive/payload-01.mkv", Size: 1, BLAKE3: "a"}},
		NZBFileOrder:       []string{"archive/payload-01.mkv"},
		Repair:             RepairDetails{Profile: CleanRepairProfile},
		Encoding:           YEncEncoding,
	}
	if _, err := LoadGeneratedManifest(writeTestManifest(t, manifest)); err == nil {
		t.Fatal("a manifest that disagrees with itself about encoding should not load")
	}
}
