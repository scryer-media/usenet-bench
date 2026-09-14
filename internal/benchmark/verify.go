package benchmark

import (
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
	manifest, err := fixture.LoadGeneratedManifest(filepath.Join(fixtureDir, "fixture-manifest.json"))
	if err != nil {
		return OutputVerification{}, err
	}
	actual, err := discoverFiles(outputDir)
	if err != nil {
		return OutputVerification{}, err
	}
	reference := ""
	if len(manifest.ExpectedFiles) == 0 && manifest.External != nil {
		pinned, found, err := loadPinnedOutput(fixtureDir)
		if err != nil {
			return OutputVerification{}, err
		}
		if !found {
			return pinOutput(fixtureDir, outputDir, manifest, actual)
		}
		manifest.ExpectedFiles = pinned.Files
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
	for _, candidate := range allCandidates {
		if used[candidate.path] {
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
	for _, file := range pinned.Files {
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
	for name, files := range actual {
		if posted[name] || strings.HasPrefix(name, ".") {
			continue
		}
		for _, file := range files {
			if file.size >= minimumPinnedFileBytes {
				candidates = append(candidates, file)
			}
		}
	}
	if len(candidates) == 0 {
		return OutputVerification{}, fmt.Errorf("output %s holds nothing extracted from %s to pin: every file is either one the post carried or under %d bytes", outputDir, manifest.Case.ID, minimumPinnedFileBytes)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
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
