package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/scryer-media/usenet-bench/internal/nntp"
)

// Wall timestamps are correlation annotations. Never reconstruct monotonic
// elapsed durations from JSON: serialization drops the monotonic readings.
func validElapsed(clock string, elapsed, wall int64) bool {
	return elapsed >= 0 && (clock == "monotonic" || (clock == "" && elapsed == wall))
}

func EvidenceDigest(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

// WorkloadEvidence snapshots the oracle and the exact submitted NZB before
// execution. Its digest includes writer identities, posted/withheld hashes,
// generation parameters and expected outputs, not only a human-readable ID.
type WorkloadEvidence struct {
	Articles  nntp.ArticleAttestation   `json:"articles"`
	Manifest  fixture.GeneratedManifest `json:"manifest"`
	NZBSHA256 string                    `json:"nzb_sha256"`
}

func SnapshotWorkload(m fixture.GeneratedManifest, nzbPath string) (*WorkloadEvidence, error) {
	b, err := os.ReadFile(nzbPath)
	if err != nil {
		return nil, err
	}
	s := sha256.Sum256(b)
	a, err := nntp.ReadArticleAttestation(nzbPath)
	if err != nil {
		return nil, err
	}
	w := &WorkloadEvidence{Manifest: m, NZBSHA256: hex.EncodeToString(s[:]), Articles: a}
	if err = a.ValidateFor(m, a.RawBytes, w.NZBSHA256); err != nil {
		return nil, err
	}
	return w, nil
}

func (a QueueArtifact) InfrastructureDigest() string {
	if a.ShaperBefore == nil {
		return EvidenceDigest("unshaped")
	}
	var link *ShaperLinkShaping
	if a.ShaperBefore.LinkShaping != nil {
		v := a.ShaperBefore.LinkShaping.declared()
		link = &v
	}
	return EvidenceDigest(struct {
		Build ShaperBuildIdentity
		Link  *ShaperLinkShaping
	}{a.ShaperBefore.Build, link})
}

func (a QueueArtifact) ValidateEvidence() error {
	if a.AdapterResult == nil {
		return fmt.Errorf("missing adapter result")
	}
	if err := a.AdapterResult.ValidateFor(queueSuite{ID: a.SuiteID, Runs: a.Runs}, a.SubmissionMode); err != nil {
		return err
	}
	if len(a.Jobs) != len(a.Runs) {
		return fmt.Errorf("incomplete job evidence")
	}
	for i, j := range a.Jobs {
		if j.Run != a.Runs[i] || !reflect.DeepEqual(j.AdapterResult, a.AdapterResult.Jobs[i]) {
			return fmt.Errorf("job identity differs from run")
		}
		if j.AdapterResult.TimingClock != "monotonic" {
			return fmt.Errorf("run %s lacks monotonic elapsed measurements; rerun legacy evidence", j.Run.ID)
		}
		if j.Workload == nil || !validSHA256(j.Workload.NZBSHA256) || j.WorkloadSHA256 != EvidenceDigest(j.Workload) {
			return fmt.Errorf("run %s lacks immutable workload evidence", j.Run.ID)
		}
		m := j.Workload.Manifest
		if err := j.Workload.Articles.ValidateFor(m, j.Run.ArticleProfile.RawBytes, j.Workload.NZBSHA256); err != nil {
			return fmt.Errorf("run %s: %w", j.Run.ID, err)
		}
		if m.Case.ID != j.Run.FixtureID || !j.FixtureClass.Valid() || m.Case.Class != j.FixtureClass || m.Case.PostEncodingOrDefault() != j.Encoding || !reflect.DeepEqual(m.Repair, j.Repair) || len(m.ExpectedFiles) == 0 || len(m.ArchiveFiles) == 0 {
			return fmt.Errorf("invalid fixture class or manifest evidence")
		}
		if j.Outcome == "dnf" {
			if j.Error == "" || a.Status != "completed_with_dnf" {
				return fmt.Errorf("invalid DNF evidence")
			}
			continue
		}
		if j.Outcome != "completed" || j.AdapterResult.TerminalStatus != "succeeded" || j.Error != "" || j.Verification == nil {
			return fmt.Errorf("invalid successful outcome")
		}
		if err := j.Verification.ValidateFor(m); err != nil {
			return err
		}
	}
	return nil
}

func (v OutputVerification) ValidateFor(m fixture.GeneratedManifest) error {
	if v.FixtureID != m.Case.ID || len(v.Files) != len(m.ExpectedFiles) || len(v.Files) == 0 {
		return fmt.Errorf("incomplete output verification")
	}
	seen := map[string]bool{}
	for i, f := range v.Files {
		want := m.ExpectedFiles[i]
		if f.ExpectedPath != want.Path || f.Size != want.Size || f.Size < 0 || !validSHA256(f.BLAKE3) || f.BLAKE3 != want.BLAKE3 || f.ActualPath == "" || seen[f.ActualPath] {
			return fmt.Errorf("invalid verified output %q", f.ExpectedPath)
		}
		seen[f.ActualPath] = true
		if path.IsAbs(f.ActualPath) || path.Clean(f.ActualPath) != f.ActualPath || f.ActualPath == ".." || strings.HasPrefix(f.ActualPath, "../") || strings.Contains(f.ActualPath, "\\") {
			return fmt.Errorf("invalid output-relative path")
		}
		if strings.Contains(want.Path, "/") && f.ActualPath != want.Path && !strings.HasSuffix(f.ActualPath, "/"+want.Path) {
			return fmt.Errorf("saved output lost required topology")
		}
	}
	allowed := allowedRetainedSidecars(m)
	seenSidecars := map[string]bool{}
	for _, f := range v.RetainedSidecars {
		matched := false
		for _, want := range allowed {
			if f.ExpectedPath == want.Path && f.Size == want.Size && validSHA256(f.BLAKE3) && f.BLAKE3 == want.BLAKE3 && path.Base(f.ActualPath) == path.Base(want.Path) {
				matched = true
			}
		}
		if !matched || seen[f.ActualPath] || seenSidecars[f.ExpectedPath] || path.IsAbs(f.ActualPath) || path.Clean(f.ActualPath) != f.ActualPath || strings.HasPrefix(f.ActualPath, "../") || strings.Contains(f.ActualPath, "\\") {
			return fmt.Errorf("invalid retained sidecar evidence")
		}
		seen[f.ActualPath] = true
		seenSidecars[f.ExpectedPath] = true
	}
	return nil
}
