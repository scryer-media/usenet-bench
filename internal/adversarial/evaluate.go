package adversarial

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
)

var RequiredChecks = []string{"filesystem_containment", "process_execution", "network_egress", "secret_redaction", "privilege_preservation", "queue_liveness", "cleanup", "memory_safety"}

type Check struct {
	Status   string `json:"status"`   // pass, fail, unknown
	Evidence string `json:"evidence"` // observer artifact reference, not product log alone
}

// Observation comes from an independent observer after the client and all of
// its helpers stop. Empty fields mean unknown, never zero resource usage.
// This is an evidence interchange format, not an OS monitoring implementation.
type Observation struct {
	Evidence            *ObserverEvidence `json:"observer_evidence,omitempty"`
	SchemaVersion       int               `json:"schema_version"`
	CaseID              string            `json:"case_id"`
	ManifestSHA256      string            `json:"manifest_sha256"`
	Client              string            `json:"client"`
	Version             string            `json:"version"`
	Platform            string            `json:"platform"`
	ConfigSHA256        string            `json:"config_sha256"`
	Outcome             string            `json:"outcome"` // accepted, rejected, repaired, crashed, timeout, unsupported
	ExternalTermination bool              `json:"external_termination"`
	Checks              map[string]Check  `json:"checks"`
	Usage               *Limits           `json:"usage,omitempty"`
	ResourceEvidence    string            `json:"resource_evidence"`
	OutputDirectory     string            `json:"output_directory,omitempty"`
}

func (o *Observation) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if termination, ok := fields["external_termination"]; !ok || string(termination) == "null" {
		return fmt.Errorf("external_termination must be explicitly observed")
	}
	type plain Observation
	var decoded plain
	if err := DecodeJSON(raw, &decoded); err != nil {
		return err
	}
	*o = Observation(decoded)
	return nil
}

type Verdict struct {
	DisplayName    string   `json:"display_name"`
	ReachedStage   string   `json:"reached_stage"`
	CaseID         string   `json:"case_id"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	Client         string   `json:"client"`
	Version        string   `json:"version"`
	Platform       string   `json:"platform"`
	ConfigSHA256   string   `json:"config_sha256"`
	Security       string   `json:"security"`
	Behavior       string   `json:"behavior"`
	Reasons        []string `json:"reasons"`
}

func ManifestDigest(m Manifest) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return Digest(b)
}

func Evaluate(b Bundle, o Observation) (Verdict, error) {
	v := Verdict{CaseID: b.Manifest.Case.ID, ManifestSHA256: ManifestDigest(b.Manifest), Client: o.Client, Version: o.Version, Platform: o.Platform, ConfigSHA256: o.ConfigSHA256, Security: "inconclusive", Behavior: "inconclusive", Reasons: []string{}}
	v.DisplayName = b.Manifest.Case.DisplayName
	if o.SchemaVersion != SchemaVersion || o.CaseID != v.CaseID || o.ManifestSHA256 != v.ManifestSHA256 {
		return v, fmt.Errorf("observation does not identify this exact generated manifest")
	}
	if strings.TrimSpace(o.Client) == "" || strings.TrimSpace(o.Version) == "" || strings.TrimSpace(o.Platform) == "" || !validDigest(o.ConfigSHA256) {
		return v, fmt.Errorf("client, version, platform and configuration digest are required")
	}
	if !slices.Contains([]string{"accepted", "rejected", "repaired", "crashed", "timeout", "unsupported"}, o.Outcome) {
		return v, fmt.Errorf("invalid outcome %q", o.Outcome)
	}
	failed, unknown := false, false
	if err := o.validateObserverEvidence(); err != nil {
		unknown = true
		v.Reasons = append(v.Reasons, err.Error())
	} else {
		v.ReachedStage = o.Evidence.ReachedStage
	}
	if b.Manifest.Case.Family == "nntp" {
		var transcript *ServerStats
		if o.Evidence != nil {
			transcript = o.Evidence.Responder
		}
		if transcript == nil || transcript.RunID != o.Evidence.RunID || transcript.ManifestSHA256 != o.ManifestSHA256 || transcript.FaultDeliveries < 1 || transcript.DroppedEvents != 0 || transcript.StoppedAt.IsZero() {
			unknown = true
			v.Reasons = append(v.Reasons, "intended NNTP fault delivery was not established for this attempt")
		}
	}
	for key := range o.Checks {
		if !slices.Contains(RequiredChecks, key) {
			return v, fmt.Errorf("unknown security check %q", key)
		}
	}
	for _, key := range RequiredChecks {
		check, ok := o.Checks[key]
		if !ok || check.Status == "" || check.Status == "unknown" {
			unknown = true
			v.Reasons = append(v.Reasons, "unobserved: "+key)
			continue
		}
		if check.Status != "pass" && check.Status != "fail" {
			return v, fmt.Errorf("invalid status for %s", key)
		}
		if check.Status == "fail" {
			failed = true
			v.Reasons = append(v.Reasons, "security finding: "+key)
		}
		if strings.TrimSpace(check.Evidence) == "" {
			unknown = true
			v.Reasons = append(v.Reasons, "missing evidence: "+key)
		}
	}
	if o.Outcome == "crashed" || o.Outcome == "timeout" || o.ExternalTermination {
		failed = true
		v.Reasons = append(v.Reasons, "client did not contain the case within its own execution budget")
	}
	if o.Usage == nil || o.ResourceEvidence == "" {
		unknown = true
		v.Reasons = append(v.Reasons, "resource envelope was not independently observed")
	}
	if o.Usage != nil {
		got, want := *o.Usage, b.Manifest.Limits
		values := []struct {
			name      string
			got, want int64
		}{
			{"wall_milliseconds", got.WallMilliseconds, want.WallMilliseconds}, {"cpu_milliseconds", got.CPUMilliseconds, want.CPUMilliseconds}, {"memory_bytes", got.MemoryBytes, want.MemoryBytes}, {"written_bytes", got.WrittenBytes, want.WrittenBytes}, {"entries", got.Entries, want.Entries}, {"file_descriptors", got.FileDescriptors, want.FileDescriptors}, {"connections", got.Connections, want.Connections},
		}
		for _, x := range values {
			if x.got < 0 {
				return v, fmt.Errorf("negative measurement: %s", x.name)
			}
			if x.got > x.want {
				failed = true
				v.Reasons = append(v.Reasons, "resource budget exceeded: "+x.name)
			}
		}
	}
	if failed {
		v.Security = "fail"
	} else if !unknown {
		v.Security = "pass"
	}
	if o.Outcome == "unsupported" {
		if v.Security == "pass" {
			v.Security = "inconclusive"
		}
		v.Reasons = append(v.Reasons, "unsupported target was not exercised")
		v.Behavior = "unsupported"
		return v, nil
	}
	if o.Outcome == "crashed" || o.Outcome == "timeout" || o.ExternalTermination {
		v.Behavior = "fail"
		return v, nil
	}
	// Inspect every final-output root, including containment-policy cases and
	// rejected submissions. An observer assertion cannot override a detected
	// unsafe filesystem object or a committed payload after rejection.
	if o.OutputDirectory == "" {
		if v.Security == "pass" {
			v.Security = "inconclusive"
		}
		v.Reasons = append(v.Reasons, "final output postconditions were not inspected")
	} else if err := InspectOutput(o.OutputDirectory, b.Manifest.Limits, o.Outcome == "rejected"); err != nil {
		var safety *OutputSafetyError
		if errors.As(err, &safety) {
			v.Security = "fail"
		} else if v.Security == "pass" {
			v.Security = "inconclusive"
		}
		v.Behavior = "fail"
		v.Reasons = append(v.Reasons, err.Error())
		return v, nil
	}
	switch b.Manifest.Case.Expectation {
	case Contain:
		v.Behavior = "policy-dependent"
	case Reject:
		if o.Outcome == "rejected" && o.OutputDirectory != "" {
			v.Behavior = "pass"
		} else if o.Outcome == "rejected" {
			v.Behavior = "inconclusive"
		} else {
			v.Behavior = "fail"
			v.Reasons = append(v.Reasons, "expected rejection")
		}
	case Accept, Recover:
		if o.Outcome != "accepted" && o.Outcome != "repaired" {
			v.Behavior = "fail"
			v.Reasons = append(v.Reasons, "expected byte-correct output")
			break
		}
		if o.OutputDirectory == "" {
			v.Reasons = append(v.Reasons, "no output directory for independent verification")
			break
		}
		if err := VerifyOutput(o.OutputDirectory, b.Manifest.ExpectedOutputs, b.Manifest.Limits); err != nil {
			var safety *OutputSafetyError
			if errors.As(err, &safety) {
				v.Security = "fail"
			}
			v.Behavior = "fail"
			v.Reasons = append(v.Reasons, err.Error())
		} else {
			v.Behavior = "pass"
		}
	}
	return v, nil
}

func validDigest(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }

// VerifyOutput reads through a capability root and caps both enumeration and
// reads. The observer supplies the delivered payload directory; retained
// download/repair sidecars belong in a separate work directory. Every output
// must match the independent generator's declared path and hash.
func VerifyOutput(path string, expected map[string]string, limits Limits) error {
	if len(expected) == 0 {
		return fmt.Errorf("recipe has no output oracle")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := map[string]bool{}
	entries := int64(0)
	total := int64(0)
	var readBytes int64
	err = boundedWalk(root, limits, func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		entries++
		if entries > limits.Entries {
			return unsafeOutput("output entry budget exceeded")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return unsafeOutput("output contains link: %q", name)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return unsafeOutput("output contains special entry: %q", name)
		}
		total += info.Size()
		if total > limits.WrittenBytes {
			return unsafeOutput("output byte budget exceeded")
		}
		want, ok := expected[name]
		if !ok {
			return fmt.Errorf("unexpected delivered output: %q", name)
		}
		f, err := openEvidence(root, name)
		if err != nil {
			return err
		}
		opened, err := f.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			_ = f.Close()
			return unsafeOutput("output changed during verification: %q", name)
		}
		linked, err := hasMultipleLinks(f)
		if err != nil || linked {
			_ = f.Close()
			return unsafeOutput("output hardlink check failed for %q: linked=%t error=%v", name, linked, err)
		}
		h := sha256.New()
		n, readErr := io.Copy(h, io.LimitReader(f, limits.WrittenBytes-readBytes+1))
		readBytes += n
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if readBytes > limits.WrittenBytes {
			return unsafeOutput("output actual-read budget exceeded")
		}
		if n != opened.Size() || hex.EncodeToString(h.Sum(nil)) != want {
			return fmt.Errorf("output digest mismatch: %q", name)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	for name := range expected {
		if !seen[name] {
			return fmt.Errorf("missing output: %q", name)
		}
	}
	return nil
}

// Matrix preserves every case/client tuple and refuses duplicates. It does
// not pool safety, compatibility, unknown results or unlike configurations.
func Matrix(verdicts []Verdict) ([]Verdict, error) {
	seen := map[string]bool{}
	for _, v := range verdicts {
		key := v.CaseID + "\x00" + v.Client + "\x00" + v.Version + "\x00" + v.Platform + "\x00" + v.ConfigSHA256
		if seen[key] {
			return nil, fmt.Errorf("duplicate comparison cell for %s/%s", v.CaseID, v.Client)
		}
		seen[key] = true
	}
	sort.Slice(verdicts, func(i, j int) bool {
		a, b := verdicts[i], verdicts[j]
		return a.CaseID+"\x00"+a.Client+"\x00"+a.Version+"\x00"+a.Platform+"\x00"+a.ConfigSHA256 < b.CaseID+"\x00"+b.Client+"\x00"+b.Version+"\x00"+b.Platform+"\x00"+b.ConfigSHA256
	})
	return verdicts, nil
}
