package nntp

import (
	"os"
	"strings"
	"testing"
)

func TestArticleSizeRequiresMoreThanEqualSegmentCounts(t *testing.T) {
	m := articleSizeManifest(map[string]int64{"fixture.bin": 500}, "")
	p := writeArticleSizeNZB(t, map[string]int{"fixture.bin": 1})
	if err := AssertNZBSegmentCount(p, m, 768000); err != nil {
		t.Fatal(err)
	}
	if err := AssertNZBSegmentCount(p, m, 393216); err != nil {
		t.Fatal(err)
	}
	if err := AssertNZBArticleSize(p, m, 768000); err == nil {
		t.Fatal("accepted missing seed evidence")
	}
	if err := WriteArticleAttestation(p, m, 768000, "test-poster"); err != nil {
		t.Fatal(err)
	}
	if err := AssertNZBArticleSize(p, m, 768000); err != nil {
		t.Fatal(err)
	}
	if err := AssertNZBArticleSize(p, m, 393216); err == nil {
		t.Fatal("same count concealed a different article size")
	}
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(raw), `bytes="1"`, `bytes="2"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AssertNZBArticleSize(p, m, 768000); err == nil {
		t.Fatal("changed NZB was not detected")
	}
}

func TestSavedArticleEvidenceRequiresCompleteDistinctBoundaries(t *testing.T) {
	m := articleSizeManifest(map[string]int64{"fixture.bin": 1000}, "")
	p := writeArticleSizeNZB(t, map[string]int{"fixture.bin": 2})
	a, err := articleAttestation(p, m, 768, "test-poster")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ValidateFor(m, 768, a.NZBSHA256); err != nil {
		t.Fatal(err)
	}
	a.Segments[1] = a.Segments[0]
	if err = a.ValidateFor(m, 768, a.NZBSHA256); err == nil {
		t.Fatal("duplicate segment replaced missing coverage")
	}
}
