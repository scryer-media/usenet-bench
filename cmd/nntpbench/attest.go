package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/scryer-media/usenet-bench/internal/nntp"
)

// BackfillProducer marks an attestation that was written for a corpus seeded
// before the poster recorded one, so a reader can tell the two apart. The
// boundaries are still the poster's: they are recomputed from the seeded NZB
// and the manifest it was generated from, and only written once the NZB's
// segment counts have been checked against the requested article size.
const BackfillProducer = "backfill-from-seeded-nzb"

// attestCorpus writes the article-size provenance for a corpus that predates
// it. It posts nothing and rewrites no fixture: the article size is proven the
// same way a run proves it, by checking every posted file's segment count
// against the size the corpus is claimed to carry, and a fixture whose NZB
// disagrees is refused rather than attested.
func attestCorpus(args []string, out io.Writer) error {
	f := flag.NewFlagSet("attest", flag.ContinueOnError)
	root := f.String("fixtures", "", "corpus directory holding the generated fixtures")
	article := f.String("article", benchmark.Article750K, "article-size stratum the corpus was seeded at")
	producer := f.String("producer", BackfillProducer, "poster identity to record")
	force := f.Bool("force", false, "rewrite attestations that are already present")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("attest accepts flags only")
	}
	if *root == "" {
		return fmt.Errorf("attest needs --fixtures")
	}
	profile, err := benchmark.ResolveArticleProfile(*article)
	if err != nil {
		return err
	}
	fixtures, err := attestableFixtures(*root)
	if err != nil {
		return err
	}
	if len(fixtures) == 0 {
		return fmt.Errorf("no generated fixtures under %s", *root)
	}
	var written, kept int
	var refused []string
	for _, nzbPath := range fixtures {
		manifest, err := fixture.LoadGeneratedManifest(filepath.Join(filepath.Dir(nzbPath), "fixture-manifest.json"))
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", nzbPath, err))
			continue
		}
		if !*force {
			if _, err := nntp.ReadArticleAttestation(nzbPath); err == nil {
				kept++
				continue
			}
		}
		// Provenance is only worth writing where the corpus itself proves the
		// stratum: same check a run makes before it starts a client.
		if err := nntp.AssertNZBSegmentCount(nzbPath, manifest, profile.RawBytes); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", nzbPath, err))
			continue
		}
		if err := nntp.WriteArticleAttestation(nzbPath, manifest, profile.RawBytes, *producer); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", nzbPath, err))
			continue
		}
		// A written attestation has to satisfy the run-time check, or the
		// corpus is no better off than before.
		if err := nntp.AssertNZBArticleSize(nzbPath, manifest, profile.RawBytes); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", nzbPath, err))
			continue
		}
		written++
		if _, err := fmt.Fprintf(out, "attested %s at %s\n", nzbPath, profile); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "ATTEST-DONE written=%d already-present=%d refused=%d\n", written, kept, len(refused)); err != nil {
		return err
	}
	if len(refused) > 0 {
		for _, r := range refused {
			fmt.Fprintln(out, "refused", r)
		}
		return fmt.Errorf("%d fixture(s) could not be attested at %s", len(refused), profile)
	}
	return nil
}

// attestableFixtures finds every generated fixture's NZB under root. A fixture
// directory is one holding a manifest beside exactly one NZB.
func attestableFixtures(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || path == root {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, "fixture-manifest.json")); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		matches, err := filepath.Glob(filepath.Join(path, "*.nzb"))
		if err != nil {
			return err
		}
		switch len(matches) {
		case 0:
			return fmt.Errorf("fixture %s has a manifest but no NZB", path)
		case 1:
			found = append(found, matches[0])
			return nil
		default:
			return fmt.Errorf("fixture %s holds %d NZBs", path, len(matches))
		}
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}
