package main

import (
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// articleSummarySet is the standard 20-block pair, posted at one article size.
func articleSummarySet(profile benchmark.ArticleProfile) []benchmark.QueueArtifact {
	artifacts := make([]benchmark.QueueArtifact, 0, 40)
	for repetition := 1; repetition <= 20; repetition++ {
		for _, entry := range []struct {
			client      benchmark.Client
			measurement int64
		}{{benchmark.Weaver, int64(100 + repetition)}, {benchmark.SABnzbd, int64(80 + repetition)}} {
			artifact := summaryTestArtifact(entry.client, repetition, entry.measurement)
			for index := range artifact.Runs {
				artifact.Runs[index].ArticleProfile = profile
			}
			for index := range artifact.Jobs {
				artifact.Jobs[index].Run.ArticleProfile = profile
				setSummaryArticleEvidence(artifact.Jobs[index].Workload, profile.RawBytes)
				artifact.Jobs[index].WorkloadSHA256 = benchmark.EvidenceDigest(artifact.Jobs[index].Workload)
			}
			artifact.AdapterResult.ArticleProfile = profile
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts
}

func TestSummaryStratumCarriesTheArticleProfile(t *testing.T) {
	small := benchmark.ArticleProfile{ID: benchmark.Article384K, RawBytes: 384 << 10}
	report, err := buildSummaryReport(articleSummarySet(small), nil, benchmark.Weaver, benchmark.SABnzbd, 20, 17, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Comparisons) != 1 {
		t.Fatalf("expected one article stratum, got %d", len(report.Comparisons))
	}
	stratum := report.Comparisons[0].Stratum
	if stratum.ArticleProfileID != benchmark.Article384K || stratum.ArticleRawBytes != 384<<10 {
		t.Fatalf("the stratum must name the article size it was posted at: %#v", stratum)
	}
}

// Article size changes how many round trips a download takes, so pooling two
// sizes would average two different workloads into one number.
func TestSummaryNeverPoolsArticleSizes(t *testing.T) {
	mixed := append(
		articleSummarySet(benchmark.DefaultArticleProfile()),
		articleSummarySet(benchmark.ArticleProfile{ID: benchmark.Article384K, RawBytes: 384 << 10})...,
	)
	report, err := buildSummaryReport(mixed, nil, benchmark.Weaver, benchmark.SABnzbd, 20, 17, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Comparisons) != 2 {
		t.Fatalf("two article sizes must produce two strata, got %d", len(report.Comparisons))
	}
	seen := map[string]bool{}
	for _, comparison := range report.Comparisons {
		if seen[comparison.Stratum.ArticleProfileID] {
			t.Fatalf("article sizes were pooled: %#v", comparison.Stratum)
		}
		seen[comparison.Stratum.ArticleProfileID] = true
	}
}

// Encoding is a label rather than a key: a uuencoded fixture is already its
// own fixture, so it already has its own stratum. Carrying the label is what
// lets a reader see "this client did not finish, and the post was uuencoded"
// without going back to the corpus.
func TestSummaryLabelsTheEncodingWithoutSplittingTheStratum(t *testing.T) {
	artifacts := articleSummarySet(benchmark.DefaultArticleProfile())
	for index := range artifacts {
		for job := range artifacts[index].Jobs {
			artifacts[index].Jobs[job].Encoding = fixture.UUEncodeEncoding
			artifacts[index].Jobs[job].Workload.Manifest.Case.Encoding = fixture.UUEncodeEncoding
			setSummaryArticleEvidence(artifacts[index].Jobs[job].Workload, artifacts[index].Jobs[job].Run.ArticleProfile.RawBytes)
			artifacts[index].Jobs[job].WorkloadSHA256 = benchmark.EvidenceDigest(artifacts[index].Jobs[job].Workload)
		}
	}
	report, err := buildSummaryReport(artifacts, nil, benchmark.Weaver, benchmark.SABnzbd, 20, 17, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Comparisons) != 1 {
		t.Fatalf("the encoding label must not split the stratum, got %d comparisons", len(report.Comparisons))
	}
	if report.Comparisons[0].Encoding != fixture.UUEncodeEncoding {
		t.Fatalf("comparison encoding = %q, want uuencode", report.Comparisons[0].Encoding)
	}
}

func TestSummaryRefusesAFixtureRecordedAtTwoEncodings(t *testing.T) {
	artifacts := articleSummarySet(benchmark.DefaultArticleProfile())
	artifacts[0].Jobs[0].Encoding = fixture.UUEncodeEncoding
	if _, err := buildSummaryReport(artifacts, nil, benchmark.Weaver, benchmark.SABnzbd, 20, 17, 1_000); err == nil {
		t.Fatal("one fixture cannot have been posted both yEnc and uuencoded")
	}
}
