package generator

import (
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func shippedGNUTools(t *testing.T) GNUToolsToolchain {
	t.Helper()
	toolchain, err := LoadGNUToolsToolchain("../../docker/gnutools/toolchain.json")
	if err != nil {
		t.Fatal(err)
	}
	return toolchain
}

// These writers have no upstream release tarball to pin by hash, so the pin
// is the base image digest plus the exact package versions. If either half
// can go missing, the lane has an unpinned writer.
func TestGNUToolsToolchainRequiresADigestAndEveryVersion(t *testing.T) {
	valid := shippedGNUTools(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("the shipped toolchain should validate: %v", err)
	}
	if !strings.Contains(valid.BaseImage, "@sha256:") {
		t.Fatalf("shipped base image %q is not digest-pinned", valid.BaseImage)
	}
	floating := valid
	floating.BaseImage = "debian:bookworm-slim"
	if err := floating.Validate(); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("a floating base image should be refused, got %v", err)
	}
	for _, name := range gnuToolsRequiredPackages {
		missing := valid
		missing.Packages = map[string]string{}
		for key, value := range valid.Packages {
			if key != name {
				missing.Packages[key] = value
			}
		}
		if err := missing.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("a toolchain with no %s version should be refused, got %v", name, err)
		}
	}
}

// The manifest names the writer that produced the bytes as precisely as the
// RAR and 7-Zip lanes do: image, digest-pinned base, and every version.
func TestGNUToolsManifestIDCarriesThePackageVersions(t *testing.T) {
	toolchain := shippedGNUTools(t)
	id := toolchain.ManifestID()
	if id.ID != toolchain.ID || id.Image != toolchain.Image || id.Version != toolchain.BaseImage {
		t.Fatalf("manifest id = %#v, want the image and its digest-pinned base", id)
	}
	for _, name := range gnuToolsRequiredPackages {
		if id.Packages[name] != toolchain.Packages[name] {
			t.Fatalf("manifest id omits the %s version", name)
		}
	}
	// The copy must not alias the toolchain's own map, or a later mutation of
	// one silently rewrites the other.
	id.Packages["tar"] = "rewritten"
	if toolchain.Packages["tar"] == "rewritten" {
		t.Fatal("ManifestID() shares its package map with the toolchain")
	}
}

func TestCaseUsesGNUToolsCoversEveryLaneThatInvokesThem(t *testing.T) {
	for name, testCase := range map[string]struct {
		archiveCase fixture.ArchiveCase
		want        bool
	}{
		"tar":            {fixture.ArchiveCase{ArchiveFormat: fixture.Tar}, true},
		"xz":             {fixture.ArchiveCase{ArchiveFormat: fixture.XZCompressed}, true},
		"zip":            {fixture.ArchiveCase{ArchiveFormat: fixture.Zip}, true},
		"wrapped rar":    {fixture.ArchiveCase{ArchiveFormat: fixture.RAR5, InnerArchive: fixture.TarXZInnerArchive}, true},
		"rar with sfv":   {fixture.ArchiveCase{ArchiveFormat: fixture.RAR5, Sidecar: fixture.SFVSidecar}, true},
		"media with sfv": {fixture.ArchiveCase{ArchiveFormat: fixture.Media, Sidecar: fixture.SFVSidecar}, true},
		"plain rar":      {fixture.ArchiveCase{ArchiveFormat: fixture.RAR5}, false},
		"plain 7z":       {fixture.ArchiveCase{ArchiveFormat: fixture.SevenZip}, false},
		"plain media":    {fixture.ArchiveCase{ArchiveFormat: fixture.Media}, false},
	} {
		if got := caseUsesGNUTools(testCase.archiveCase); got != testCase.want {
			t.Errorf("caseUsesGNUTools(%s) = %v, want %v", name, got, testCase.want)
		}
	}
}

func TestSelectedCasesRequireTheOracleOnlyForTheUUEncodedLane(t *testing.T) {
	cases := []fixture.ArchiveCase{
		{ID: "yenc", ArchiveFormat: fixture.Media},
		{ID: "uu", ArchiveFormat: fixture.Media, Encoding: fixture.UUEncodeEncoding},
	}
	if !selectedCasesRequireUUCodec(cases, nil) {
		t.Fatal("a corpus containing the uuencoded lane needs the oracle")
	}
	if selectedCasesRequireUUCodec(cases, map[string]bool{"yenc": true}) {
		t.Fatal("a run over the yEnc lane alone must not need the uuencode oracle")
	}
	if !selectedCasesRequireUUCodec(cases, map[string]bool{"uu": true}) {
		t.Fatal("a run over the uuencoded lane alone needs the oracle")
	}
}

func TestPrimaryArchiveFileOpensEachFormatCorrectly(t *testing.T) {
	// A spanned zip's central directory is in the trailing fixture.zip, and
	// Info-ZIP refuses to be pointed at a .z01.
	spanned := []fixture.FileDigest{
		{Path: "archive/fixture.z01"},
		{Path: "archive/fixture.z02"},
		{Path: "archive/fixture.zip"},
	}
	got, err := primaryArchiveFile(spanned, fixture.Zip)
	if err != nil || got != "archive/fixture.zip" {
		t.Fatalf("spanned zip primary = %q, %v, want archive/fixture.zip", got, err)
	}
	if _, err := primaryArchiveFile(spanned[:2], fixture.Zip); err == nil {
		t.Fatal("a zip set with no fixture.zip should be refused")
	}
	// Every other format is opened at its first file in read order.
	volumes := []fixture.FileDigest{{Path: "archive/fixture.part01.rar"}, {Path: "archive/fixture.part02.rar"}}
	if got, err := primaryArchiveFile(volumes, fixture.RAR5); err != nil || got != "archive/fixture.part01.rar" {
		t.Fatalf("rar primary = %q, %v", got, err)
	}
	// The SFV sidecar sorts ahead of a media payload, and it is not what a
	// reader opens.
	withSidecar := []fixture.FileDigest{{Path: "archive/fixture.sfv"}, {Path: "archive/payload-01.mkv"}}
	if got, err := primaryArchiveFile(withSidecar, fixture.Media); err != nil || got != "archive/payload-01.mkv" {
		t.Fatalf("media primary = %q, %v, want the payload rather than the sidecar", got, err)
	}
}

func TestIsArchiveVolumeIsPerFormat(t *testing.T) {
	for name, testCase := range map[string]struct {
		file   string
		format fixture.ArchiveFormat
		want   bool
	}{
		"rar volume":     {"fixture.part01.rar", fixture.RAR5, true},
		"7z volume":      {"fixture.7z.001", fixture.SevenZip, true},
		"plain tar":      {"fixture.tar", fixture.Tar, true},
		"filtered tar":   {"fixture.tar.xz", fixture.Tar, true},
		"xz stream":      {"payload-01.mkv.xz", fixture.XZCompressed, true},
		"zip part":       {"fixture.z01", fixture.Zip, true},
		"zip directory":  {"fixture.zip", fixture.Zip, true},
		"media payload":  {"payload-01.mkv", fixture.Media, true},
		"sidecar on rar": {"fixture.sfv", fixture.RAR5, true},
		"stray tar":      {"notes.txt", fixture.Tar, false},
		"stray rar":      {"payload-01.mkv", fixture.RAR5, false},
	} {
		if got := isArchiveVolume(testCase.file, testCase.format); got != testCase.want {
			t.Errorf("isArchiveVolume(%s) = %v, want %v", name, got, testCase.want)
		}
	}
}
