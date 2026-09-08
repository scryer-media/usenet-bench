package nntp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestArticlePayloadBytesFollowsTheEncoding(t *testing.T) {
	// yEnc splits wherever the poster likes, so an article carries exactly
	// the declared size.
	got, err := ArticlePayloadBytes(fixture.YEncEncoding, 768000)
	if err != nil || got != 768000 {
		t.Fatalf("yEnc payload bytes = %d, %v, want 768000", got, err)
	}
	// A manifest written before the encoding field existed is yEnc.
	if got, err := ArticlePayloadBytes("", 768000); err != nil || got != 768000 {
		t.Fatalf("empty encoding payload bytes = %d, %v, want 768000", got, err)
	}
	// uuencode can only split on a line boundary, so the article carries the
	// largest whole number of 45-byte lines that fits.
	for _, testCase := range []struct{ raw, want int }{
		{768000, 767970},
		{393216, 393210},
	} {
		got, err := ArticlePayloadBytes(fixture.UUEncodeEncoding, testCase.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != testCase.want || got%UUBytesPerLine != 0 {
			t.Fatalf("uuencode payload bytes for %d = %d, want %d", testCase.raw, got, testCase.want)
		}
	}
	if _, err := ArticlePayloadBytes(fixture.UUEncodeEncoding, 40); err == nil {
		t.Fatal("an article smaller than one uuencode line should be refused")
	}
	if _, err := ArticlePayloadBytes("base64", 768000); err == nil {
		t.Fatal("an unknown encoding should be refused")
	}
}

func TestExpectedSegmentCountRoundsUp(t *testing.T) {
	if got := ExpectedSegmentCount(768000, 768000); got != 1 {
		t.Fatalf("exact fit = %d segments, want 1", got)
	}
	if got := ExpectedSegmentCount(768001, 768000); got != 2 {
		t.Fatalf("one byte over = %d segments, want 2", got)
	}
	if got := ExpectedSegmentCount(0, 768000); got != 0 {
		t.Fatalf("empty file = %d segments, want 0", got)
	}
}

// writeArticleSizeNZB writes an NZB whose files carry the given segment
// counts, in the shape the seeder emits.
func writeArticleSizeNZB(t *testing.T, counts map[string]int) string {
	t.Helper()
	files := make([]NZBFile, 0, len(counts))
	for name, count := range counts {
		segments := make([]NZBSegment, 0, count)
		for number := 1; number <= count; number++ {
			segments = append(segments, NZBSegment{Bytes: 1, Number: number, MessageID: "id"})
		}
		files = append(files, NZBFile{
			Poster:   "bench",
			Subject:  `"` + name + `" (1/` + strconv.Itoa(count) + `) 1`,
			Groups:   []NZBGroup{"alt.binaries.test"},
			Segments: segments,
		})
	}
	contents, err := MarshalNZB(files)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.nzb")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func articleSizeManifest(sizes map[string]int64, withheld string) fixture.GeneratedManifest {
	manifest := fixture.GeneratedManifest{Case: fixture.ArchiveCase{ID: "case"}}
	for name, size := range sizes {
		manifest.ArchiveFiles = append(manifest.ArchiveFiles, fixture.FileDigest{Path: "archive/" + name, Size: size})
	}
	if withheld != "" {
		manifest.WithheldFiles = []fixture.FileDigest{{Path: "archive/" + withheld, Size: 1}}
	}
	return manifest
}

func TestAssertNZBArticleSizeAcceptsTheDeclaredStratum(t *testing.T) {
	// A 32 MiB volume is 44 articles at 750 KiB and 86 at 384 KiB, which is
	// what makes the NZB's own segment counts sufficient proof.
	const volume = 32 << 20
	manifest := articleSizeManifest(map[string]int64{"fixture.part01.rar": volume}, "")
	large := writeArticleSizeNZB(t, map[string]int{"fixture.part01.rar": 44})
	if err := AssertNZBArticleSize(large, manifest, 768000); err != nil {
		t.Fatalf("a corpus seeded at 750 KiB was rejected: %v", err)
	}
	small := writeArticleSizeNZB(t, map[string]int{"fixture.part01.rar": 86})
	if err := AssertNZBArticleSize(small, manifest, 393216); err != nil {
		t.Fatalf("a corpus seeded at 384 KiB was rejected: %v", err)
	}
	err := AssertNZBArticleSize(large, manifest, 393216)
	if err == nil || !strings.Contains(err.Error(), "seeded at a different article size") {
		t.Fatalf("a 750 KiB corpus under a 384 KiB plan = %v, want a loud refusal", err)
	}
	if !strings.Contains(err.Error(), "reseed the corpus") {
		t.Fatalf("the refusal must tell the operator what to do, got %v", err)
	}
}

func TestAssertNZBArticleSizeIgnoresWithheldFiles(t *testing.T) {
	// Withheld segment counts are synthesised by the seeder rather than
	// reported by the poster, so they say nothing about what the server was
	// actually given and must not be checked.
	manifest := articleSizeManifest(map[string]int64{"fixture.part01.rar": 32 << 20}, "fixture.part02.rar")
	path := writeArticleSizeNZB(t, map[string]int{"fixture.part01.rar": 44, "fixture.part02.rar": 7})
	if err := AssertNZBArticleSize(path, manifest, 768000); err != nil {
		t.Fatalf("a withheld file's segment count must not be checked: %v", err)
	}
}

func TestAssertNZBArticleSizeRefusesAMissingPostedFile(t *testing.T) {
	manifest := articleSizeManifest(map[string]int64{"fixture.part01.rar": 32 << 20}, "")
	path := writeArticleSizeNZB(t, map[string]int{"fixture.part09.rar": 44})
	if err := AssertNZBArticleSize(path, manifest, 768000); err == nil || !strings.Contains(err.Error(), "does not list posted file") {
		t.Fatalf("error = %v, want a refusal naming the missing file", err)
	}
}
