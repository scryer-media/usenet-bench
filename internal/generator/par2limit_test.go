package generator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func postOf(totalBytes, volumeBytes int64) []fixture.FileDigest {
	var sources []fixture.FileDigest
	for offset, index := int64(0), 1; offset < totalBytes; offset, index = offset+volumeBytes, index+1 {
		sources = append(sources, fixture.FileDigest{Path: fmt.Sprintf("archive/fixture.part%03d.rar", index), Size: min(volumeBytes, totalBytes-offset)})
	}
	return sources
}

func TestPAR2LimitComparisonCountsEveryBlockAnArticleTouches(t *testing.T) {
	sources := postOf(26<<30, 256<<20)
	faults, err := scatteredArticles(sources, "past-cap", 70)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := par2LimitComparison(sources, faults, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", comparison)
	if comparison.BlockSize%4 != 0 || comparison.SourceBlocks > fixture.PAR2BlockLimit || par2SourceBlocks(sources, comparison.BlockSize-4) <= fixture.PAR2BlockLimit {
		t.Fatalf("block size %d is not PAR2's smallest legal one (%d blocks)", comparison.BlockSize, comparison.SourceBlocks)
	}
	if comparison.BlockSize <= fixture.ScatteredArticleBytes || comparison.PAR3SourceBlocks <= fixture.PAR2BlockLimit {
		t.Fatalf("the post fits PAR2 at one block per article: %#v", comparison)
	}
	if comparison.PAR3LostBlocks != int64(len(faults)) {
		t.Fatalf("PAR3 loses %d blocks for %d articles", comparison.PAR3LostBlocks, len(faults))
	}
	// An article of 768000 bytes inside blocks of about 852 KiB straddles a
	// boundary unless it happens to sit wholly inside one block.
	if comparison.DamagedBlocks < comparison.PAR3LostBlocks*18/10 || comparison.DamagedBlocks > 2*comparison.PAR3LostBlocks {
		t.Fatalf("%d withheld articles damage %d PAR2 blocks", comparison.PAR3LostBlocks, comparison.DamagedBlocks)
	}
	if comparison.RecoveryBlocks >= comparison.DamagedBlocks || comparison.RequiredRedundancyPercent <= 10 {
		t.Fatalf("PAR2 could repair: %#v", comparison)
	}
	if par2RecoveryBlocks(comparison.SourceBlocks, comparison.RequiredRedundancyPercent) < comparison.DamagedBlocks ||
		par2RecoveryBlocks(comparison.SourceBlocks, comparison.RequiredRedundancyPercent-1) >= comparison.DamagedBlocks {
		t.Fatalf("required redundancy %d%% is not the least that repairs", comparison.RequiredRedundancyPercent)
	}
}

func TestPAR2LimitComparisonRefusesWhatPAR2CouldServe(t *testing.T) {
	small := postOf(8<<30, 256<<20)
	faults, err := scatteredArticles(small, "past-cap", 70)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := par2LimitComparison(small, faults, 10); err == nil || !strings.Contains(err.Error(), "one block per article") {
		t.Fatalf("an 8 GiB post was accepted past the limit: %v", err)
	}
	large := postOf(26<<30, 256<<20)
	light, err := scatteredArticles(large, "past-cap", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := par2LimitComparison(large, light, 5); err == nil || !strings.Contains(err.Error(), "could repair") {
		t.Fatalf("light damage PAR2 can repair was accepted: %v", err)
	}
}

func TestPAR2RecoveryBlocksRoundLikePAR2(t *testing.T) {
	// Observed from par2cmdline-turbo 1.4.0 over 32768 source blocks.
	for redundancy, want := range map[int]int64{15: 4915, 20: 6554, 25: 8192} {
		if got := par2RecoveryBlocks(32768, redundancy); got != want {
			t.Fatalf("%d%% of 32768 blocks = %d recovery blocks, par2 writes %d", redundancy, got, want)
		}
	}
}
