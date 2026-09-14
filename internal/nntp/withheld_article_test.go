package nntp

import (
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func postedOnlyPlan(t *testing.T) postingPlan {
	t.Helper()
	manifest := testManifest()
	manifest.WithheldFiles = nil
	manifest.NZBFileOrder = []string{
		"archive/fixture.part03.rar",
		"archive/fixture.par2",
		"archive/fixture.part01.rar",
	}
	plan, err := newPostingPlan(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func withheldArticle(path string, article int64) fixture.CorruptionDetail {
	return fixture.CorruptionDetail{
		Kind:   fixture.WithheldArticleFault,
		Path:   path,
		Offset: article * fixture.ScatteredArticleBytes,
		Length: fixture.ScatteredArticleBytes,
	}
}

func TestWithholdArticlesRenamesExactlyTheChosenSegments(t *testing.T) {
	faults := []fixture.CorruptionDetail{
		{Kind: "byte-flip", Path: "archive/fixture.part01.rar", Offset: 7, Length: 1},
		withheldArticle("archive/fixture.part03.rar", 1),
		withheldArticle("archive/fixture.part01.rar", 0),
	}
	document, count, err := withholdArticles(postedDocument(), postedOnlyPlan(t), faults, "run-1", "bench-fixture", fixture.ScatteredArticleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("withheld %d articles, want 2", count)
	}
	ids := map[string]bool{}
	for _, file := range document.Files {
		for _, segment := range file.Segments {
			ids[segment.MessageID] = true
		}
	}
	for _, kept := range []string{"a1", "b1", "c2"} {
		if !ids[kept] {
			t.Fatalf("posted article %s lost its identifier: %v", kept, ids)
		}
	}
	for _, withheld := range []string{"a2", "c1"} {
		if ids[withheld] {
			t.Fatalf("withheld article %s is still named in the NZB", withheld)
		}
	}
	if got := document.Files[0].Segments[1].MessageID; got != withheldArticleMessageID("run-1", "bench-fixture", 1, 2) || !strings.Contains(got, "-missing") {
		t.Fatalf("withheld segment identifier = %q", got)
	}
	if strings.Contains(MessageIDTemplate("run-1", "bench-fixture"), "missing") {
		t.Fatal("the posted template can collide with the withheld-article namespace")
	}
}

func TestWithholdArticlesRefusesAnotherArticleSize(t *testing.T) {
	faults := []fixture.CorruptionDetail{withheldArticle("archive/fixture.part03.rar", 1)}
	if _, _, err := withholdArticles(postedDocument(), postedOnlyPlan(t), faults, "run-1", "bench-fixture", 384000); err == nil {
		t.Fatal("articles chosen at 750k were withheld from a 384k seed")
	}
}

func TestWithholdArticlesRefusesMisplacedFaults(t *testing.T) {
	cases := map[string]fixture.CorruptionDetail{
		"unaligned":    {Kind: fixture.WithheldArticleFault, Path: "archive/fixture.part03.rar", Offset: 5, Length: 10},
		"unknown file": withheldArticle("archive/fixture.part09.rar", 0),
		"past the end": withheldArticle("archive/fixture.part03.rar", 5),
	}
	for name, fault := range cases {
		if _, _, err := withholdArticles(postedDocument(), postedOnlyPlan(t), []fixture.CorruptionDetail{fault}, "run-1", "bench-fixture", fixture.ScatteredArticleBytes); err == nil {
			t.Fatalf("%s fault was accepted", name)
		}
	}
	twice := []fixture.CorruptionDetail{withheldArticle("archive/fixture.part03.rar", 0), withheldArticle("archive/fixture.part03.rar", 0)}
	if _, _, err := withholdArticles(postedDocument(), postedOnlyPlan(t), twice, "run-1", "bench-fixture", fixture.ScatteredArticleBytes); err == nil {
		t.Fatal("the same article was withheld twice")
	}
}

func TestWithholdArticlesIsANoOpWithoutArticleFaults(t *testing.T) {
	document, count, err := withholdArticles(postedDocument(), postedOnlyPlan(t), nil, "run-1", "bench-fixture", 50)
	if err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if document.Files[0].Segments[0].MessageID != "a1" {
		t.Fatal("a document with nothing withheld was rewritten")
	}
}
