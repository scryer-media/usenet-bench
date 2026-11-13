package generator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// damageArticles turns the articles a damaged-article profile picked into its
// fault. A silent profile changes those bytes in the case files now, after the
// recovery material was written and before anything is posted, so the poster
// encodes the wrong bytes with matching CRCs; each fault becomes the byte-flip
// it made. A corrupt profile leaves the files intact and marks each article
// for the seeder, which posts a damaged copy of it. Any other profile keeps
// its articles as they were picked.
func damageArticles(caseDir string, profile fixture.RepairProfile, picked []fixture.CorruptionDetail) ([]fixture.CorruptionDetail, error) {
	faults := make([]fixture.CorruptionDetail, 0, len(picked))
	for _, article := range picked {
		switch {
		case profile.SilentlyCorruptsArticles():
			fault, err := corruptArticleOnDisk(caseDir, article)
			if err != nil {
				return nil, err
			}
			faults = append(faults, fault)
		case profile.CorruptsArticles():
			article.Kind = fixture.CorruptArticleFault
			faults = append(faults, article)
		default:
			faults = append(faults, article)
		}
	}
	return faults, nil
}

// corruptArticleOnDisk applies fixture.CorruptArticle to one article of a file
// under root and returns the bytes it changed.
func corruptArticleOnDisk(root string, article fixture.CorruptionDetail) (fixture.CorruptionDetail, error) {
	path := filepath.Join(root, filepath.FromSlash(article.Path))
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fixture.CorruptionDetail{}, fmt.Errorf("open %s to damage an article: %w", article.Path, err)
	}
	defer file.Close()
	contents := make([]byte, article.Length)
	if _, err := io.ReadFull(io.NewSectionReader(file, article.Offset, int64(article.Length)), contents); err != nil {
		return fixture.CorruptionDetail{}, fmt.Errorf("read the article at %d in %s: %w", article.Offset, article.Path, err)
	}
	fixture.CorruptArticle(contents)
	start, length := fixture.CorruptArticleBytes(article.Length)
	if _, err := file.WriteAt(contents[start:start+length], article.Offset+int64(start)); err != nil {
		return fixture.CorruptionDetail{}, fmt.Errorf("damage the article at %d in %s: %w", article.Offset, article.Path, err)
	}
	return fixture.CorruptionDetail{Kind: fixture.ByteFlipFault, Path: article.Path, Offset: article.Offset + int64(start), Length: length}, nil
}

// middleArticles picks one full-length article from the middle of each target
// volume, for the recovery-volume lanes, whose sibling removes those volumes
// whole. A volume too short to hold a full article is refused.
func middleArticles(targets []fixture.FileDigest, articleBytes int) ([]fixture.CorruptionDetail, error) {
	if articleBytes < 1 {
		return nil, fmt.Errorf("damaged-article volumes need an article size")
	}
	picked := make([]fixture.CorruptionDetail, 0, len(targets))
	for _, target := range targets {
		full := target.Size / int64(articleBytes)
		if full < 1 {
			return nil, fmt.Errorf("volume %s of %d bytes holds no full %d-byte article", target.Path, target.Size, articleBytes)
		}
		picked = append(picked, fixture.CorruptionDetail{
			Kind:   fixture.WithheldArticleFault,
			Path:   target.Path,
			Offset: full / 2 * int64(articleBytes),
			Length: articleBytes,
		})
	}
	return picked, nil
}

// damagedSlices counts the distinct slices the faults touch, each file cut on
// its own from its first byte, as PAR2 and the PAR3 lanes cut them.
func damagedSlices(faults []fixture.CorruptionDetail, sliceBytes int64) int {
	type slice struct {
		path  string
		index int64
	}
	damaged := make(map[slice]struct{})
	for _, fault := range faults {
		if fault.Length <= 0 {
			continue
		}
		for index := fault.Offset / sliceBytes; index <= (fault.Offset+int64(fault.Length)-1)/sliceBytes; index++ {
			damaged[slice{fault.Path, index}] = struct{}{}
		}
	}
	return len(damaged)
}

// articleStagings lists the ways a client can end up holding the faulted
// articles, as the blankCorrupt argument of stageArticleFaults. A corrupt
// article is either kept, with its wrong bytes, by a client that does not
// check the CRC or keeps what it decoded anyway, or discarded and left as a
// hole by one that does. Both must repair, so both are verified.
func articleStagings(faults []fixture.CorruptionDetail) []bool {
	for _, fault := range faults {
		if fault.Kind == fixture.CorruptArticleFault {
			return []bool{false, true}
		}
	}
	return []bool{false}
}

// stageArticleFaults puts a repair-verification copy into the state a client
// assembles: withheld articles zeroed, and corrupt articles either changed the
// way the seeder changes them or, with blankCorrupt, zeroed as if discarded.
// Byte-flip faults are already in the files the copy was taken from.
func stageArticleFaults(verifyDir string, faults []fixture.CorruptionDetail, blankCorrupt bool) error {
	if err := blankWithheldArticles(verifyDir, faults); err != nil {
		return err
	}
	for _, fault := range faults {
		if fault.Kind != fixture.CorruptArticleFault {
			continue
		}
		if blankCorrupt {
			blank := fault
			blank.Kind = fixture.WithheldArticleFault
			if err := blankWithheldArticles(verifyDir, []fixture.CorruptionDetail{blank}); err != nil {
				return err
			}
			continue
		}
		if _, err := corruptArticleOnDisk(verifyDir, fault); err != nil {
			return err
		}
	}
	return nil
}

// stagingName names a staging in an error, so a failed verification says
// which reading of a corrupt article did not repair.
func stagingName(faults []fixture.CorruptionDetail, blankCorrupt bool) string {
	if len(articleStagings(faults)) == 1 {
		return ""
	}
	if blankCorrupt {
		return " (corrupt articles discarded)"
	}
	return " (corrupt articles kept)"
}
