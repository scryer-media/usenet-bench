package generator

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// scatteredArticles chooses the articles a scattered profile withholds. The
// post's articles are numbered in posting-file order and split into as many
// equal runs as there are articles to withhold, and one article is drawn from
// each run, so the damage covers the whole post evenly without clustering
// into one repair region. The very first article, which carries the leading
// archive header, is never chosen: the lane measures repair, not a client's
// handling of a post whose first bytes are gone. Only full-length articles
// are candidates, so every withheld article removes exactly
// ScatteredArticleBytes of decoded data.
//
// The draw is seeded by the set and the severity, never by the profile, and
// it picks positions as fractions of the post rather than article numbers.
// A PAR2 lane and a PAR3 lane of the same severity each write their own copy
// of the archive, and a re-rendered payload can differ by a few hundred KiB,
// so the twins cannot rely on byte-identical posts; they do withhold the same
// number of articles at the same relative positions, which is the same
// decoded loss spread the same way.
func scatteredArticles(sources []fixture.FileDigest, setID string, perMille int) ([]fixture.CorruptionDetail, error) {
	if perMille <= 0 || perMille >= 1000 {
		return nil, fmt.Errorf("withheld articles per mille %d is out of range", perMille)
	}
	type article struct {
		path   string
		offset int64
	}
	var candidates []article
	first := true
	for _, source := range sources {
		for offset := int64(0); offset+fixture.ScatteredArticleBytes <= source.Size; offset += fixture.ScatteredArticleBytes {
			if first {
				first = false
				continue
			}
			candidates = append(candidates, article{path: source.Path, offset: offset})
		}
	}
	count := (len(candidates)*perMille + 999) / 1000
	if count < 1 || count > len(candidates) {
		return nil, fmt.Errorf("a post of %d full-length articles cannot withhold %d per mille of them", len(candidates), perMille)
	}
	seed := fnv.New64a()
	fmt.Fprintf(seed, "%s/withheld-articles/%d", setID, perMille)
	random := rand.New(rand.NewPCG(seed.Sum64(), uint64(perMille)))
	faults := make([]fixture.CorruptionDetail, 0, count)
	for run := 0; run < count; run++ {
		position := (float64(run) + random.Float64()) / float64(count)
		low := run * len(candidates) / count
		high := (run + 1) * len(candidates) / count
		index := min(max(int(position*float64(len(candidates))), low), high-1)
		chosen := candidates[index]
		faults = append(faults, fixture.CorruptionDetail{
			Kind:   fixture.WithheldArticleFault,
			Path:   chosen.path,
			Offset: chosen.offset,
			Length: fixture.ScatteredArticleBytes,
		})
	}
	return faults, nil
}

// blankWithheldArticles zeroes every withheld article in a repair
// verification copy. That is the file a client assembles when those articles
// never arrive: the right length, with nothing where the refused articles
// were.
func blankWithheldArticles(verifyDir string, faults []fixture.CorruptionDetail) error {
	for _, fault := range faults {
		if fault.Kind != fixture.WithheldArticleFault {
			continue
		}
		path := filepath.Join(verifyDir, filepath.FromSlash(fault.Path))
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("blank withheld article in %s: %w", fault.Path, err)
		}
		_, writeErr := file.WriteAt(make([]byte, fault.Length), fault.Offset)
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("blank withheld article at %d in %s: %w", fault.Offset, fault.Path, writeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
