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
	marker := filepath.Join(outputDir, markerDir, weaverOutputMarker)
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
