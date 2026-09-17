package benchmark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

const testWeaverMarker = "weaver-output-v1:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + "\n"

// markerOutput writes one verified payload into a job directory, plus a marker
// file at markerDir (relative to the output root) with the given content.
func markerOutput(t *testing.T, markerDir, markerContent string) (fixtureDir, outputDir string) {
	t.Helper()
	return bookkeepingOutput(t, markerDir, weaverOutputMarker, markerContent)
}

// bookkeepingOutput is markerOutput for a named bookkeeping file, so each
// client's own file is exercised against the same verified payload.
func bookkeepingOutput(t *testing.T, markerDir, markerName, markerContent string) (fixtureDir, outputDir string) {
	t.Helper()
	fixtureDir = t.TempDir()
	outputDir = t.TempDir()
	contents := []byte("movie payload")
	payload := filepath.Join(outputDir, "job", "payload-01.mkv")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	writeVerificationManifest(t, fixtureDir, []fixture.FileDigest{{
		Path:   "payload-01.mkv",
		Size:   int64(len(contents)),
		BLAKE3: digest,
	}})
	marker := filepath.Join(outputDir, markerDir, markerName)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(markerContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixtureDir, outputDir
}

func TestVerifyClientOutputSetsAsideWeaverMarker(t *testing.T) {
	for _, dir := range []string{".", "job"} {
		t.Run(dir, func(t *testing.T) {
			fixtureDir, outputDir := markerOutput(t, dir, testWeaverMarker)
			verification, err := VerifyClientOutput(fixtureDir, outputDir, Weaver)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.ToSlash(filepath.Join(dir, weaverOutputMarker))
			if len(verification.ClientBookkeeping) != 1 || verification.ClientBookkeeping[0] != want {
				t.Fatalf("bookkeeping = %#v, want [%s]", verification.ClientBookkeeping, want)
			}
		})
	}
}

// testNZBFastManifest is the form the product writes: one JSON object holding
// the checksums of the recovery set it verified.
const testNZBFastManifest = `{"block_size":1048576,"created":1789668484,` +
	`"files":[{"crc":"609ac013","l":33554432,"m16":"15ad2d38fcf69bf3b3575a5af0cf5ad5",` +
	`"md5":"d846eed711881f092a2c398b928ca22b","n":"fixture.part1.rar","r":"source"}],` +
	`"job":"repair-rar4-par2-par2-light-normal-solid-none-incompressible",` +
	`"nzb_sha":"bbcedddab49ea7d50066","v":1}`

func TestVerifyClientOutputSetsAsideNZBFastManifest(t *testing.T) {
	for _, dir := range []string{".", "job"} {
		t.Run(dir, func(t *testing.T) {
			fixtureDir, outputDir := bookkeepingOutput(t, dir, nzbFastManifest, testNZBFastManifest)
			verification, err := VerifyClientOutput(fixtureDir, outputDir, NZBFast)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.ToSlash(filepath.Join(dir, nzbFastManifest))
			if len(verification.ClientBookkeeping) != 1 || verification.ClientBookkeeping[0] != want {
				t.Fatalf("bookkeeping = %#v, want [%s]", verification.ClientBookkeeping, want)
			}
		})
	}
}

func TestVerifyClientOutputRejectsNZBFastManifestOutsideItsExactForm(t *testing.T) {
	for name, tc := range map[string]struct {
		client  Client
		dir     string
		content string
	}{
		"no client":      {"", "job", testNZBFastManifest},
		"weaver":         {Weaver, "job", testNZBFastManifest},
		"sabnzbd":        {SABnzbd, "job", testNZBFastManifest},
		"nested deeper":  {NZBFast, "job/sub", testNZBFastManifest},
		"other version":  {NZBFast, "job", strings.Replace(testNZBFastManifest, `"v":1`, `"v":2`, 1)},
		"missing field":  {NZBFast, "job", strings.Replace(testNZBFastManifest, `"block_size":1048576,`, "", 1)},
		"unknown field":  {NZBFast, "job", strings.Replace(testNZBFastManifest, `{"block_size"`, `{"payload":"x","block_size"`, 1)},
		"trailing value": {NZBFast, "job", testNZBFastManifest + `{"v":1}`},
		"not json":       {NZBFast, "job", "fixture.part1.rar a1b2c3d4\n"},
	} {
		t.Run(name, func(t *testing.T) {
			fixtureDir, outputDir := bookkeepingOutput(t, tc.dir, nzbFastManifest, tc.content)
			_, err := VerifyClientOutput(fixtureDir, outputDir, tc.client)
			if err == nil || !strings.Contains(err.Error(), "unexpected or modified retained output") {
				t.Fatalf("err = %v, want the manifest rejected as unexpected output", err)
			}
		})
	}
}

func TestVerifyClientOutputRejectsMarkerOutsideItsExactForm(t *testing.T) {
	for name, tc := range map[string]struct {
		client  Client
		dir     string
		content string
	}{
		"no client":        {"", "job", testWeaverMarker},
		"nzbget":           {NZBGet, "job", testWeaverMarker},
		"sabnzbd":          {SABnzbd, "job", testWeaverMarker},
		"nested deeper":    {Weaver, "job/sub", testWeaverMarker},
		"uppercase digest": {Weaver, "job", strings.ToUpper(testWeaverMarker[:len(testWeaverMarker)-1]) + "\n"},
		"no newline":       {Weaver, "job", testWeaverMarker[:len(testWeaverMarker)-1] + "0"},
		"other version":    {Weaver, "job", strings.Replace(testWeaverMarker, "v1", "v2", 1)},
		"short digest":     {Weaver, "job", "weaver-output-v1:0123\n"},
	} {
		t.Run(name, func(t *testing.T) {
			fixtureDir, outputDir := markerOutput(t, tc.dir, tc.content)
			_, err := VerifyClientOutput(fixtureDir, outputDir, tc.client)
			if err == nil || !strings.Contains(err.Error(), "unexpected or modified retained output") {
				t.Fatalf("err = %v, want the marker rejected as unexpected output", err)
			}
		})
	}
}
