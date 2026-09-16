package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/scryer-media/usenet-bench/internal/nntp"
)

// writeAttestFixture lays out one fixture directory the way a seeded corpus
// from before the attestation existed looks: a manifest and an NZB, and
// nothing recording the article size.
func writeAttestFixture(t *testing.T, root, id string, size int64, segments int) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Schema 3 predates the repair and posting-order axes, so the loader fills
	// both in and the manifest stays small enough to read here.
	manifest := map[string]any{
		"schema_version": 3,
		"case":           map[string]any{"id": id},
		"expected_files": []map[string]any{{"path": "payload.bin", "size": size}},
		"archive_files":  []map[string]any{{"path": "archive/" + id + ".bin", "size": size}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture-manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	parts := make([]string, 0, segments)
	for number := 1; number <= segments; number++ {
		parts = append(parts, `<segment bytes="1" number="`+strconv.Itoa(number)+`">id`+strconv.Itoa(number)+`</segment>`)
	}
	nzb := `<?xml version="1.0" encoding="utf-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="bench" date="1" subject="&#34;` + id + `.bin&#34; (1/` + strconv.Itoa(segments) + `) 1">
    <groups><group>alt.binaries.test</group></groups>
    <segments>` + strings.Join(parts, "") + `</segments>
  </file>
</nzb>
`
	nzbPath := filepath.Join(dir, id+".nzb")
	if err := os.WriteFile(nzbPath, []byte(nzb), 0o600); err != nil {
		t.Fatal(err)
	}
	return nzbPath
}

func TestAttestWritesProvenanceARunAccepts(t *testing.T) {
	root := t.TempDir()
	// 32 MiB at 750k is 44 articles; the fixture agrees with the stratum.
	nzbPath := writeAttestFixture(t, root, "agrees", 32<<20, 44)
	var out bytes.Buffer
	if err := attestCorpus([]string{"--fixtures", root}, &out); err != nil {
		t.Fatalf("attest refused a corpus that matches the stratum: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ATTEST-DONE written=1 already-present=0 refused=0") {
		t.Fatalf("unexpected report: %s", out.String())
	}
	manifest, err := fixture.LoadGeneratedManifest(filepath.Join(filepath.Dir(nzbPath), "fixture-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The point of the backfill is that a run stops refusing the fixture.
	if err := nntp.AssertNZBArticleSize(nzbPath, manifest, 768000); err != nil {
		t.Fatalf("a run would still refuse the attested fixture: %v", err)
	}
	attestation, err := nntp.ReadArticleAttestation(nzbPath)
	if err != nil {
		t.Fatal(err)
	}
	if attestation.Producer != BackfillProducer {
		t.Fatalf("attestation does not say it was backfilled: %q", attestation.Producer)
	}

	// A second pass leaves the existing evidence alone.
	out.Reset()
	if err := attestCorpus([]string{"--fixtures", root}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "written=0 already-present=1") {
		t.Fatalf("second pass rewrote the corpus: %s", out.String())
	}
}

func TestAttestRefusesAFixtureSeededAtAnotherSize(t *testing.T) {
	root := t.TempDir()
	// 32 MiB in 86 articles is the 384k stratum, not the 750k one claimed.
	nzbPath := writeAttestFixture(t, root, "disagrees", 32<<20, 86)
	var out bytes.Buffer
	err := attestCorpus([]string{"--fixtures", root}, &out)
	if err == nil {
		t.Fatal("attested a corpus that was seeded at a different article size")
	}
	if _, statErr := os.Stat(nzbPath + ".articles.json"); statErr == nil {
		t.Fatal("wrote provenance for a fixture it could not prove")
	}
	if !strings.Contains(out.String(), "refused=1") {
		t.Fatalf("refusal not reported: %s", out.String())
	}
}

func TestAttestableFixturesSkipsDirectoriesThatAreNotFixtures(t *testing.T) {
	root := t.TempDir()
	writeAttestFixture(t, root, "real", 32<<20, 44)
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "readme.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := attestableFixtures(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || filepath.Base(found[0]) != "real.nzb" {
		t.Fatalf("unexpected fixtures: %v", found)
	}
}
