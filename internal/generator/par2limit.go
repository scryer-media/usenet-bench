package generator

import (
	"fmt"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// par2LimitComparison counts what the withheld articles would cost the best
// PAR2 set a poster could make over the same files, at the same redundancy.
// It refuses a post PAR2 could still cover with one block per article, and a
// fault PAR2 could still repair: either would make the lane's claim false.
func par2LimitComparison(sources []fixture.FileDigest, faults []fixture.CorruptionDetail, redundancyPercent int) (fixture.PAR2LimitComparison, error) {
	var total int64
	for _, source := range sources {
		total += source.Size
	}
	articles := par2SourceBlocks(sources, fixture.ScatteredArticleBytes)
	if articles <= fixture.PAR2BlockLimit {
		return fixture.PAR2LimitComparison{}, fmt.Errorf("the post has %d articles, which a PAR2 set holds at one block per article; the lane needs more than %d", articles, fixture.PAR2BlockLimit)
	}
	blockSize := (total + fixture.PAR2BlockLimit - 1) / fixture.PAR2BlockLimit
	blockSize = (blockSize + 3) &^ 3
	for par2SourceBlocks(sources, blockSize) > fixture.PAR2BlockLimit {
		blockSize += 4
	}
	type block struct {
		path  string
		index int64
	}
	damaged := map[block]struct{}{}
	for _, fault := range faults {
		if fault.Kind != fixture.WithheldArticleFault || fault.Length <= 0 {
			return fixture.PAR2LimitComparison{}, fmt.Errorf("fault %#v is not a withheld article", fault)
		}
		last := (fault.Offset + int64(fault.Length) - 1) / blockSize
		for index := fault.Offset / blockSize; index <= last; index++ {
			damaged[block{fault.Path, index}] = struct{}{}
		}
	}
	comparison := fixture.PAR2LimitComparison{
		BlockSize:        blockSize,
		SourceBlocks:     par2SourceBlocks(sources, blockSize),
		DamagedBlocks:    int64(len(damaged)),
		PAR3LostBlocks:   int64(len(faults)),
		PAR3SourceBlocks: articles,
	}
	comparison.RecoveryBlocks = par2RecoveryBlocks(comparison.SourceBlocks, redundancyPercent)
	for comparison.RequiredRedundancyPercent = redundancyPercent; par2RecoveryBlocks(comparison.SourceBlocks, comparison.RequiredRedundancyPercent) < comparison.DamagedBlocks; {
		comparison.RequiredRedundancyPercent++
	}
	if comparison.RecoveryBlocks >= comparison.DamagedBlocks {
		return comparison, fmt.Errorf("a PAR2 set at %d%% redundancy holds %d recovery blocks and could repair the %d blocks the withheld articles damage", redundancyPercent, comparison.RecoveryBlocks, comparison.DamagedBlocks)
	}
	return comparison, nil
}

// par2SourceBlocks is PAR2's source block count: each file is cut on its own.
func par2SourceBlocks(sources []fixture.FileDigest, blockSize int64) int64 {
	var blocks int64
	for _, source := range sources {
		blocks += (source.Size + blockSize - 1) / blockSize
	}
	return blocks
}

// par2RecoveryBlocks is the recovery block count par2cmdline writes for a
// redundancy percentage, rounded to nearest the way it rounds.
func par2RecoveryBlocks(sourceBlocks int64, redundancyPercent int) int64 {
	return (sourceBlocks*int64(redundancyPercent) + 50) / 100
}
