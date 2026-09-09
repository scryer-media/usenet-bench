package nntp

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// UUBytesPerLine is the payload each uuencoded line carries. It is a property
// of the encoding, not a choice: a uuencode line encodes exactly 45 input
// bytes as 60 output characters.
const UUBytesPerLine = 45

// ArticlePayloadBytes is how many payload bytes one article of the declared
// raw size actually carries, for a given encoding. yEnc splits wherever it
// likes, so an article carries exactly the declared size. uuencode cannot:
// every line but the file's last must carry a full 45 bytes, or the
// concatenation of the parts is not a decodable stream, so an article carries
// the largest whole number of lines that fits.
func ArticlePayloadBytes(encoding fixture.PostEncoding, rawBytes int) (int, error) {
	if rawBytes <= 0 {
		return 0, fmt.Errorf("article size must be positive, got %d", rawBytes)
	}
	switch encoding {
	case fixture.UUEncodeEncoding:
		lines := rawBytes / UUBytesPerLine
		if lines == 0 {
			return 0, fmt.Errorf("article size %d bytes is smaller than one uuencode line", rawBytes)
		}
		return lines * UUBytesPerLine, nil
	case fixture.YEncEncoding, "":
		return rawBytes, nil
	default:
		return 0, fmt.Errorf("unsupported post encoding %q", encoding)
	}
}

// ExpectedSegmentCount is how many articles a file of this size occupies at
// the declared article size.
func ExpectedSegmentCount(size int64, payloadBytes int) int64 {
	if size <= 0 {
		return 0
	}
	if payloadBytes <= 0 {
		return 0
	}
	return 1 + (size-1)/int64(payloadBytes)
}

// AssertNZBSegmentCount is only a coarse consistency check: different article
// sizes can produce the same count. Use AssertNZBArticleSize for seed provenance.
func AssertNZBSegmentCount(nzbPath string, manifest fixture.GeneratedManifest, rawBytes int) error {
	payloadBytes, err := ArticlePayloadBytes(manifest.Case.PostEncodingOrDefault(), rawBytes)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(nzbPath)
	if err != nil {
		return fmt.Errorf("read seeded NZB %s: %w", nzbPath, err)
	}
	document, err := UnmarshalNZB(contents)
	if err != nil {
		return fmt.Errorf("decode seeded NZB %s: %w", nzbPath, err)
	}
	segments := map[string]int{}
	for _, file := range document.Files {
		name, err := nzbFileName(file.Subject)
		if err != nil {
			return fmt.Errorf("seeded NZB %s: %w", nzbPath, err)
		}
		segments[name] = len(file.Segments)
	}
	// Withheld files are listed but never posted, and their segment counts are
	// synthesised by the seeder rather than reported by the posting tool, so
	// they prove nothing about what the server holds.
	checked := 0
	mismatches := make([]string, 0)
	for _, file := range manifest.ArchiveFiles {
		name := path.Base(file.Path)
		found, ok := segments[name]
		if !ok {
			return fmt.Errorf("seeded NZB %s does not list posted file %q", nzbPath, name)
		}
		want := ExpectedSegmentCount(file.Size, payloadBytes)
		if int64(found) != want {
			mismatches = append(mismatches, fmt.Sprintf("%s: %d articles, expected %d", name, found, want))
		}
		checked++
	}
	if checked == 0 {
		return fmt.Errorf("fixture %q posts no files", manifest.Case.ID)
	}
	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		shown := mismatches
		if len(shown) > 3 {
			shown = shown[:3]
		}
		return fmt.Errorf(
			"fixture %q was seeded at a different article size than the %d-byte one this run declares: %s (reseed the corpus, or point this phase at the seed image built for its article size)",
			manifest.Case.ID, rawBytes, strings.Join(shown, "; "),
		)
	}
	return nil
}

func fileBase(name string) string { return path.Base(name) }
