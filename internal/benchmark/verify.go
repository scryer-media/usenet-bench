package benchmark

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/zeebo/blake3"
)

type OutputVerification struct {
	FixtureID        string               `json:"fixture_id"`
	Files            []VerifiedOutputFile `json:"files"`
	RetainedSidecars []VerifiedOutputFile `json:"retained_sidecars,omitempty"`
	// RetainedRepairMaterial lists the posted recovery files a client left in
	// its completion directory, each matching the digest it was posted under.
	RetainedRepairMaterial []VerifiedOutputFile `json:"retained_repair_material,omitempty"`
	// SmallMembers lists the under-floor files a pinned fixture's oracle
	// recorded that this client also produced, matched by content.
	SmallMembers []VerifiedOutputFile `json:"small_members,omitempty"`
	// ClientBookkeeping lists the files a client is known to leave in its own
	// completion directory for itself, which were checked and set aside.
	ClientBookkeeping []string `json:"client_bookkeeping,omitempty"`
	// Reference says what an external fixture's output was checked against:
	// "pinned" when it matched output an earlier run pinned, "pinned-here"
	// when this run was the first to finish and pinned its own. It is empty
	// for a generated fixture, whose manifest is the oracle.
	Reference string `json:"reference,omitempty"`
}

// Pinned-output references.
const (
	ReferencePinned     = "pinned"
	ReferencePinnedHere = "pinned-here"
)

// minimumPinnedFileBytes is the smallest output file a pin takes. Clients
// leave small bookkeeping files of their own beside what they extract -- logs,
// reports, markers -- and each client leaves different ones, so a pin that
// took them would fail every client but the one that pinned.
const minimumPinnedFileBytes = 1 << 20

type VerifiedOutputFile struct {
	ExpectedPath string `json:"expected_path"`
	ActualPath   string `json:"actual_path"`
	Size         int64  `json:"size"`
	BLAKE3       string `json:"blake3"`
}

// VerifyOutput accepts client-specific completion nesting and filename
// deobfuscation. It prefers an exact expected basename, then falls back to a
// unique byte-count and BLAKE3 match. An output file can satisfy only one
// expected member, so a flattened-name collision remains a verification
// failure rather than being hidden by content matching.
func VerifyOutput(fixtureDir, outputDir string) (OutputVerification, error) {
	return VerifyClientOutput(fixtureDir, outputDir, "")
}

// VerifyClientOutput is VerifyOutput for output a named client produced. The
// only difference is that the client's own bookkeeping files, which it writes
// into every completion directory by design, are checked for their exact form
// and set aside instead of failing the run as unexpected output. Anything else
// left behind still fails it.
func VerifyClientOutput(fixtureDir, outputDir string, client Client) (OutputVerification, error) {
	manifest, err := fixture.LoadGeneratedManifest(filepath.Join(fixtureDir, "fixture-manifest.json"))
	if err != nil {
		return OutputVerification{}, err
	}
	actual, err := discoverFiles(outputDir)
	if err != nil {
		return OutputVerification{}, err
	}
	reference := ""
	var smallMembers []fixture.FileDigest
	if len(manifest.ExpectedFiles) == 0 && manifest.External != nil {
		pinned, found, err := loadPinnedOutput(fixtureDir)
		if err != nil {
			return OutputVerification{}, err
		}
		if !found {
			return pinOutput(fixtureDir, outputDir, manifest, actual)
		}
		manifest.ExpectedFiles = pinned.Files
		smallMembers = pinned.SmallFiles
		reference = ReferencePinned
	}
	allCandidates := make([]discoveredFile, 0)
	for _, byName := range actual {
		allCandidates = append(allCandidates, byName...)
	}
	sort.Slice(allCandidates, func(i, j int) bool { return allCandidates[i].path < allCandidates[j].path })
	result := OutputVerification{FixtureID: manifest.Case.ID, Files: make([]VerifiedOutputFile, 0, len(manifest.ExpectedFiles)), Reference: reference}
	used := make(map[string]bool, len(manifest.ExpectedFiles))
	digests := make(map[string]string)
	for _, expected := range manifest.ExpectedFiles {
		verified, err := verifyExpectedFile(expected, actual[filepath.Base(expected.Path)], used, digests, outputDir)
		if err != nil {
			return OutputVerification{}, err
		}
		if verified == nil {
			verified, err = verifyExpectedFile(expected, allCandidates, used, digests, outputDir)
			if err != nil {
				return OutputVerification{}, err
			}
		}
		if verified == nil {
			return OutputVerification{}, fmt.Errorf("no unused output file matching %s passed size and BLAKE3 verification", expected.Path)
		}
		// Nested payloads (for example disc structures) preserve their relative
		// topology. Only a client-specific enclosing completion directory is allowed.
		// A pinned path starts with the pinning client's own job directory, so
		// it is matched by content alone.
		if reference == "" && strings.Contains(expected.Path, "/") && verified.ActualPath != expected.Path && !strings.HasSuffix(verified.ActualPath, "/"+expected.Path) {
			return OutputVerification{}, fmt.Errorf("output %s lost required topology %s", verified.ActualPath, expected.Path)
		}
		used[filepath.Clean(filepath.Join(outputDir, filepath.FromSlash(verified.ActualPath)))] = true
		result.Files = append(result.Files, *verified)
	}
	// A declared SFV sidecar may be removed or retained by the client. Retained
	// copies must match the posted digest; archives and arbitrary extras never
	// receive a filename-extension exemption.
	allowed := allowedRetainedSidecars(manifest)
	// The recovery material a repair lane posts is in the same position: no
	// client is obliged to delete a .rev or a parity set once it has served
	// its purpose, and the ones that keep it keep it byte for byte.
	repair := allowedRetainedRepairMaterial(manifest)
	for _, candidate := range allCandidates {
		if used[candidate.path] {
			continue
		}
		bookkeeping, err := isClientBookkeeping(client, outputDir, candidate)
		if err != nil {
			return OutputVerification{}, err
		}
		if bookkeeping {
			used[candidate.path] = true
			relative, err := filepath.Rel(outputDir, candidate.path)
			if err != nil {
				return OutputVerification{}, err
			}
			result.ClientBookkeeping = append(result.ClientBookkeeping, filepath.ToSlash(relative))
			continue
		}
		var matched *VerifiedOutputFile
		for _, sidecar := range allowed {
			if filepath.Base(sidecar.Path) != filepath.Base(candidate.path) {
				continue
			}
			matched, err = verifyExpectedFile(sidecar, []discoveredFile{candidate}, used, digests, outputDir)
			if err != nil {
				return OutputVerification{}, err
			}
			if matched != nil {
				break
			}
		}
		if matched == nil {
			// An under-floor member the oracle recorded is matched by content
			// alone: a client may name a small extracted file after its own
			// job rather than after the archive member.
			member, err := matchSmallMember(smallMembers, candidate, used, digests, outputDir, &result)
			if err != nil {
				return OutputVerification{}, err
			}
			if member {
				continue
			}
			kept, err := matchRepairMaterial(repair, candidate, used, digests, outputDir, &result)
			if err != nil {
				return OutputVerification{}, err
			}
			if kept {
				continue
			}
			return OutputVerification{}, fmt.Errorf("unexpected or modified retained output: %s", candidate.path)
		}
		used[candidate.path] = true
		for _, previous := range result.RetainedSidecars {
			if previous.ExpectedPath == matched.ExpectedPath {
				return OutputVerification{}, fmt.Errorf("duplicate retained sidecar %s", matched.ExpectedPath)
			}
		}
		result.RetainedSidecars = append(result.RetainedSidecars, *matched)
	}
	return result, nil
}

// matchSmallMember accepts one retained file as an under-floor member the
// oracle recorded, and reports whether it did. Each recorded member satisfies
// at most one file, so two copies of the same bytes still fail the run.
func matchSmallMember(members []fixture.FileDigest, candidate discoveredFile, used map[string]bool, digests map[string]string, outputDir string, result *OutputVerification) (bool, error) {
	for _, member := range members {
		taken := false
		for _, previous := range result.SmallMembers {
			if previous.ExpectedPath == member.Path {
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		matched, err := verifyExpectedFile(member, []discoveredFile{candidate}, used, digests, outputDir)
		if err != nil {
			return false, err
		}
		if matched == nil {
			continue
		}
		used[candidate.path] = true
		result.SmallMembers = append(result.SmallMembers, *matched)
		return true, nil
	}
	return false, nil
}

// loadPinnedOutput reads an external fixture's pinned output, if a run has
// pinned one yet.
func loadPinnedOutput(fixtureDir string) (fixture.PinnedOutput, bool, error) {
	path := filepath.Join(fixtureDir, fixture.PinnedOutputName)
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fixture.PinnedOutput{}, false, nil
	}
	if err != nil {
		return fixture.PinnedOutput{}, false, fmt.Errorf("read pinned output %s: %w", path, err)
	}
	var pinned fixture.PinnedOutput
	if err := json.Unmarshal(contents, &pinned); err != nil {
		return fixture.PinnedOutput{}, false, fmt.Errorf("decode pinned output %s: %w", path, err)
	}
	if pinned.SchemaVersion != 1 || len(pinned.Files) == 0 {
		return fixture.PinnedOutput{}, false, fmt.Errorf("pinned output %s is incomplete", path)
	}
	for _, file := range append(append([]fixture.FileDigest{}, pinned.Files...), pinned.SmallFiles...) {
		if file.Size <= 0 || len(file.BLAKE3) != 64 {
			return fixture.PinnedOutput{}, false, fmt.Errorf("pinned output %s has an invalid entry for %s", path, file.Path)
		}
	}
	return pinned, true, nil
}

// pinOutput makes the first finished run's output the oracle for an external
// fixture. It takes only what the client extracted: never a file the post
// itself carried under the same name, which is an archive volume or recovery
// file left behind rather than a result, and never a small or hidden file,
// which is the client's own bookkeeping. A run that extracted nothing pins
// nothing and fails.
func pinOutput(fixtureDir, outputDir string, manifest fixture.GeneratedManifest, actual map[string][]discoveredFile) (OutputVerification, error) {
	posted := make(map[string]bool, len(manifest.ArchiveFiles))
	for _, file := range manifest.PostedFiles() {
		posted[filepath.Base(file.Path)] = true
	}
	candidates := make([]discoveredFile, 0)
	small := make([]discoveredFile, 0)
	for name, files := range actual {
		if posted[name] || strings.HasPrefix(name, ".") {
			continue
		}
		for _, file := range files {
			if file.size >= minimumPinnedFileBytes {
				candidates = append(candidates, file)
				continue
			}
			small = append(small, file)
		}
	}
	if len(candidates) == 0 {
		return OutputVerification{}, fmt.Errorf("output %s holds nothing extracted from %s to pin: every file is either one the post carried or under %d bytes", outputDir, manifest.Case.ID, minimumPinnedFileBytes)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	sort.Slice(small, func(i, j int) bool { return small[i].path < small[j].path })
	pinned := fixture.PinnedOutput{
		SchemaVersion: 1,
		FixtureID:     manifest.Case.ID,
		PinnedAt:      time.Now().UTC().Format(time.RFC3339),
		PinnedFrom:    outputDir,
	}
	result := OutputVerification{FixtureID: manifest.Case.ID, Reference: ReferencePinnedHere}
	for _, candidate := range candidates {
		digest, err := hashFile(candidate.path)
		if err != nil {
			return OutputVerification{}, err
		}
		relative, err := filepath.Rel(outputDir, candidate.path)
		if err != nil {
			return OutputVerification{}, err
		}
		relative = filepath.ToSlash(relative)
		pinned.Files = append(pinned.Files, fixture.FileDigest{Path: relative, Size: candidate.size, BLAKE3: digest})
		result.Files = append(result.Files, VerifiedOutputFile{ExpectedPath: relative, ActualPath: relative, Size: candidate.size, BLAKE3: digest})
	}
	// An archive can carry a file under the floor -- a readme beside the
	// payload -- and the floor cannot tell one from a client's own bookkeeping.
	// Recording both here costs nothing: a later client either reproduces the
	// bytes or does not, and its own bookkeeping never matches them.
	for _, candidate := range small {
		digest, err := hashFile(candidate.path)
		if err != nil {
			return OutputVerification{}, err
		}
		relative, err := filepath.Rel(outputDir, candidate.path)
		if err != nil {
			return OutputVerification{}, err
		}
		pinned.SmallFiles = append(pinned.SmallFiles, fixture.FileDigest{Path: filepath.ToSlash(relative), Size: candidate.size, BLAKE3: digest})
	}
	contents, err := json.MarshalIndent(pinned, "", "  ")
	if err != nil {
		return OutputVerification{}, err
	}
	path := filepath.Join(fixtureDir, fixture.PinnedOutputName)
	// Exclusive creation: a pin is written once and never replaced, so an
	// operator who wants a different oracle has to remove it deliberately.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return OutputVerification{}, fmt.Errorf("pin the output of %s: %w", manifest.Case.ID, err)
	}
	if _, err := file.Write(append(contents, '\n')); err != nil {
		file.Close()
		return OutputVerification{}, fmt.Errorf("pin the output of %s: %w", manifest.Case.ID, err)
	}
	if err := file.Close(); err != nil {
		return OutputVerification{}, fmt.Errorf("pin the output of %s: %w", manifest.Case.ID, err)
	}
	return result, nil
}

// weaverOutputMarker is the file Weaver writes into each job's completion
// directory to recognise the directory as its own on a later restart. Its
// content is "weaver-output-v1:" and the hex BLAKE3 of the directory's
// canonical path as Weaver saw it, then a newline. That path is the client's
// view (inside a container, for instance), so the harness checks the form and
// not the digest.
const (
	weaverOutputMarker       = ".weaver-output-dir"
	weaverOutputMarkerPrefix = "weaver-output-v1:"
)

// nzbFastManifest is the file nzbfast writes into a finished job's directory:
// the block checksums of the recovery set it verified, kept so the payload can
// still be checked once the recovery files themselves are cleaned up. It is
// written only for a job that had a recovery set, and the product ships with it
// on, so the harness takes it rather than configuring it away.
const nzbFastManifest = ".nzbfast.manifest"

// isClientBookkeeping reports whether an otherwise unexpected output file is a
// known client bookkeeping file in its exact form, written by the client that
// owns it, in the output root or a job directory directly beneath it.
func isClientBookkeeping(client Client, outputDir string, candidate discoveredFile) (bool, error) {
	var check func(discoveredFile) (bool, error)
	switch {
	case client == Weaver && filepath.Base(candidate.path) == weaverOutputMarker:
		check = isWeaverOutputMarker
	case client == NZBFast && filepath.Base(candidate.path) == nzbFastManifest:
		check = isNZBFastManifest
	default:
		return false, nil
	}
	parent, err := filepath.Rel(outputDir, filepath.Dir(candidate.path))
	if err != nil {
		return false, err
	}
	if parent != "." && strings.ContainsRune(filepath.ToSlash(parent), '/') {
		return false, nil
	}
	return check(candidate)
}

// isNZBFastManifest checks the manifest's exact form: a single JSON object at
// version 1 carrying every field the product writes and nothing else. What the
// checksums inside it say about the payload is the client's own claim, which
// the harness verifies against the fixture itself and never reads from here.
func isNZBFastManifest(candidate discoveredFile) (bool, error) {
	contents, err := os.ReadFile(candidate.path)
	if err != nil {
		return false, err
	}
	var document struct {
		Version   *int              `json:"v"`
		Created   *int64            `json:"created"`
		NZBSHA    *string           `json:"nzb_sha"`
		Job       *string           `json:"job"`
		BlockSize *int64            `json:"block_size"`
		Files     []json.RawMessage `json:"files"`
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return false, nil
	}
	if decoder.More() {
		return false, nil
	}
	if document.Version == nil || *document.Version != 1 {
		return false, nil
	}
	return document.Created != nil && document.NZBSHA != nil && document.Job != nil &&
		document.BlockSize != nil && document.Files != nil, nil
}

func isWeaverOutputMarker(candidate discoveredFile) (bool, error) {
	wantSize := int64(len(weaverOutputMarkerPrefix) + 64 + 1)
	if candidate.size != wantSize {
		return false, nil
	}
	contents, err := os.ReadFile(candidate.path)
	if err != nil {
		return false, err
	}
	text := string(contents)
	if !strings.HasPrefix(text, weaverOutputMarkerPrefix) || !strings.HasSuffix(text, "\n") {
		return false, nil
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(text, weaverOutputMarkerPrefix), "\n")
	if len(digest) != 64 {
		return false, nil
	}
	for _, r := range digest {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false, nil
		}
	}
	return true, nil
}

// allowedRetainedRepairMaterial names the posted files a repair lane added on
// top of the archive itself: everything posted that is not one of the intact
// source volumes. That is the parity set or the recovery volume, taken from
// the manifest's own structure rather than from a filename extension, so a
// damaged volume — which keeps its source path and only changes its bytes —
// is never mistaken for recovery material. A corpus seeded before the source
// volumes were recorded names nothing here, and stays as strict as it was.
func allowedRetainedRepairMaterial(m fixture.GeneratedManifest) []fixture.FileDigest {
	if len(m.SourceArchiveFiles) == 0 {
		return nil
	}
	source := make(map[string]bool, len(m.SourceArchiveFiles))
	for _, f := range m.SourceArchiveFiles {
		source[f.Path] = true
	}
	var allowed []fixture.FileDigest
	for _, f := range m.ArchiveFiles {
		if source[f.Path] {
			continue
		}
		allowed = append(allowed, f)
	}
	return allowed
}

// matchRepairMaterial accepts one retained file as posted recovery material,
// and reports whether it did. A posted file satisfies at most one candidate,
// so a second copy of the same recovery volume still fails the run.
func matchRepairMaterial(material []fixture.FileDigest, candidate discoveredFile, used map[string]bool, digests map[string]string, outputDir string, result *OutputVerification) (bool, error) {
	for _, posted := range material {
		if filepath.Base(posted.Path) != filepath.Base(candidate.path) {
			continue
		}
		taken := false
		for _, previous := range result.RetainedRepairMaterial {
			if previous.ExpectedPath == posted.Path {
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		matched, err := verifyExpectedFile(posted, []discoveredFile{candidate}, used, digests, outputDir)
		if err != nil {
			return false, err
		}
		if matched == nil {
			continue
		}
		used[candidate.path] = true
		result.RetainedRepairMaterial = append(result.RetainedRepairMaterial, *matched)
		return true, nil
	}
	return false, nil
}

func allowedRetainedSidecars(m fixture.GeneratedManifest) []fixture.FileDigest {
	var allowed []fixture.FileDigest
	for _, s := range m.Sidecars {
		if s.Kind != fixture.SFVSidecar {
			continue
		}
		for _, f := range m.ArchiveFiles {
			if f.Path == s.Path {
				allowed = append(allowed, f)
			}
		}
	}
	return allowed
}

func verifyExpectedFile(expected fixture.FileDigest, candidates []discoveredFile, used map[string]bool, digests map[string]string, outputDir string) (*VerifiedOutputFile, error) {
	for _, candidate := range candidates {
		if used[candidate.path] || candidate.size != expected.Size {
			continue
		}
		digest, ok := digests[candidate.path]
		if !ok {
			var err error
			digest, err = hashFile(candidate.path)
			if err != nil {
				return nil, err
			}
			digests[candidate.path] = digest
		}
		if digest != expected.BLAKE3 {
			continue
		}
		candidatePath, err := filepath.Rel(outputDir, candidate.path)
		if err != nil {
			return nil, err
		}
		return &VerifiedOutputFile{
			ExpectedPath: expected.Path,
			ActualPath:   filepath.ToSlash(candidatePath),
			Size:         expected.Size,
			BLAKE3:       digest,
		}, nil
	}
	return nil, nil
}

// DeleteOutputFiles removes completed download contents while retaining the
// output root itself. A live Docker bind mount continues to reference that
// root, so removing the root directory would make subsequent fixture cleanup
// depend on container-specific mount behaviour.
func DeleteOutputFiles(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read client output %s: %w", outputDir, err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(outputDir, entry.Name())); err != nil {
			return fmt.Errorf("delete client output %s: %w", entry.Name(), err)
		}
	}
	return nil
}

type discoveredFile struct {
	path string
	size int64
}

func discoverFiles(root string) (map[string][]discoveredFile, error) {
	files := map[string][]discoveredFile{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("nonregular output: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[entry.Name()] = append(files[entry.Name()], discoveredFile{path: path, size: info.Size()})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("scan client output %s: %w", root, err)
	}
	for _, candidates := range files {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	}
	return files, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := blake3.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
