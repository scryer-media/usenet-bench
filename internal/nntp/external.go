package nntp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// ExternalImport describes a post that already exists on a real provider.
type ExternalImport struct {
	NZBPath      string
	FixturesRoot string
	FixtureID    string
	Class        fixture.FixtureClass
	Description  string
	// ArticleRawBytes is the article size the chain phase will declare. The
	// import proves the NZB was split at exactly that size, so a result can
	// never be filed under a stratum the post does not belong to.
	ArticleRawBytes int
}

// yencSubjectExpression reads the part count and file size a yEnc poster
// writes at the end of every subject: `"name" yEnc (1/37) 26214400`.
var yencSubjectExpression = regexp.MustCompile(`yEnc \((\d+)/(\d+)\) (\d+)\s*$`)

// fixtureIDExpression keeps an imported id usable as a directory and file name
// on every host the chain runs on.
var fixtureIDExpression = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ImportExternalNZB makes a fixture directory for a post the benchmark did not
// make: the NZB, copied under the fixture's id, and a manifest describing what
// it posts. Every file's size comes from its subject line, which is what lets
// the chain check the article size exactly as it does for a seeded corpus.
// The payload is unknown, so the manifest expects no files; the first run to
// finish pins its output. An existing fixture directory is never touched.
func ImportExternalNZB(request ExternalImport) (fixture.GeneratedManifest, error) {
	if !fixtureIDExpression.MatchString(request.FixtureID) {
		return fixture.GeneratedManifest{}, fmt.Errorf("fixture id %q must be lower-case letters, digits and hyphens", request.FixtureID)
	}
	if !request.Class.Valid() {
		return fixture.GeneratedManifest{}, fmt.Errorf("fixture class %q is not headline or breadth", request.Class)
	}
	contents, err := os.ReadFile(request.NZBPath)
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("read NZB: %w", err)
	}
	document, err := UnmarshalNZB(contents)
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("decode NZB %s: %w", request.NZBPath, err)
	}
	files, order, groups, err := externalPostedFiles(document, request.ArticleRawBytes)
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("NZB %s: %w", request.NZBPath, err)
	}
	digest := sha256.Sum256(contents)
	manifest := fixture.GeneratedManifest{
		SchemaVersion: fixture.GeneratedManifestSchemaVersion,
		Case: fixture.ArchiveCase{
			ID:            request.FixtureID,
			SetID:         request.FixtureID,
			Class:         request.Class,
			Encryption:    fixture.NoEncryption,
			RepairProfile: fixture.CleanRepairProfile,
			NZBOrder:      fixture.SequentialNZBOrder,
			Encoding:      fixture.YEncEncoding,
			FileCount:     len(files),
		},
		ArchiveFiles:       files,
		SourceArchiveFiles: append([]fixture.FileDigest(nil), files...),
		NZBFileOrder:       order,
		Repair:             fixture.RepairDetails{Profile: fixture.CleanRepairProfile},
		Encoding:           fixture.YEncEncoding,
		External: &fixture.ExternalPostDetails{
			Description:     request.Description,
			NZBSHA256:       hex.EncodeToString(digest[:]),
			ArticleRawBytes: request.ArticleRawBytes,
			Groups:          groups,
		},
	}
	if err := manifest.ValidatePostedSize(); err != nil {
		return fixture.GeneratedManifest{}, err
	}

	dir := filepath.Join(request.FixturesRoot, request.FixtureID)
	if err := os.MkdirAll(request.FixturesRoot, 0o755); err != nil {
		return fixture.GeneratedManifest{}, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fixture.GeneratedManifest{}, fmt.Errorf("%s already exists; an imported fixture, and any output pinned beside it, is never overwritten", dir)
		}
		return fixture.GeneratedManifest{}, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fixture.GeneratedManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, request.FixtureID+".nzb"), contents, 0o644); err != nil {
		return fixture.GeneratedManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture-manifest.json"), append(encoded, '\n'), 0o644); err != nil {
		return fixture.GeneratedManifest{}, err
	}
	// The written fixture is read back through the same checks a chain
	// applies, so an import that succeeds is one a phase will accept.
	loaded, err := fixture.LoadGeneratedManifest(filepath.Join(dir, "fixture-manifest.json"))
	if err != nil {
		return fixture.GeneratedManifest{}, err
	}
	if err := AssertNZBArticleSize(filepath.Join(dir, request.FixtureID+".nzb"), loaded, request.ArticleRawBytes); err != nil {
		return fixture.GeneratedManifest{}, err
	}
	return loaded, nil
}

// externalPostedFiles reads the posted files out of an NZB in its own order.
// It refuses a post it could not describe exactly: a subject without a size,
// a file listed twice, or a file whose segments are not numbered 1..n, which
// is what a truncated or hand-edited NZB looks like.
func externalPostedFiles(document NZBDocument, rawBytes int) ([]fixture.FileDigest, []string, []string, error) {
	if len(document.Files) == 0 {
		return nil, nil, nil, fmt.Errorf("lists no files")
	}
	payload, err := ArticlePayloadBytes(fixture.YEncEncoding, rawBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	seen := map[string]bool{}
	groupSet := map[string]bool{}
	files := make([]fixture.FileDigest, 0, len(document.Files))
	order := make([]string, 0, len(document.Files))
	for index, file := range document.Files {
		name, err := nzbFileName(file.Subject)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("file %d: %w", index+1, err)
		}
		if name != filepath.Base(name) || name == "." || name == ".." {
			return nil, nil, nil, fmt.Errorf("file %d names %q, which is not a plain file name", index+1, name)
		}
		if seen[name] {
			return nil, nil, nil, fmt.Errorf("lists %q twice", name)
		}
		seen[name] = true
		match := yencSubjectExpression.FindStringSubmatch(file.Subject)
		if match == nil {
			return nil, nil, nil, fmt.Errorf("subject for %q carries no yEnc part count and size", name)
		}
		parts, _ := strconv.Atoi(match[2])
		size, err := strconv.ParseInt(match[3], 10, 64)
		if err != nil || size <= 0 {
			return nil, nil, nil, fmt.Errorf("subject for %q has an invalid size %q", name, match[3])
		}
		if parts != len(file.Segments) {
			return nil, nil, nil, fmt.Errorf("%q declares %d parts but lists %d segments", name, parts, len(file.Segments))
		}
		numbers := make([]int, 0, len(file.Segments))
		for _, segment := range file.Segments {
			numbers = append(numbers, segment.Number)
		}
		sort.Ints(numbers)
		for position, number := range numbers {
			if number != position+1 {
				return nil, nil, nil, fmt.Errorf("%q does not number its segments 1..%d", name, len(numbers))
			}
		}
		if want := ExpectedSegmentCount(size, payload); int64(len(file.Segments)) != want {
			return nil, nil, nil, fmt.Errorf("%q is %d bytes in %d articles, which is not %d-byte articles (%d expected); import it at the article size it was posted at", name, size, len(file.Segments), rawBytes, want)
		}
		for _, group := range file.Groups {
			groupSet[string(group)] = true
		}
		files = append(files, fixture.FileDigest{Path: name, Size: size})
		order = append(order, name)
	}
	groups := make([]string, 0, len(groupSet))
	for group := range groupSet {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return files, order, groups, nil
}
