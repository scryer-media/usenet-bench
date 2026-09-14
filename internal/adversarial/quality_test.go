package adversarial

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUnknownFindingsAndUnevidencedResourceViolations(t *testing.T) {
	b, _ := Generate("nzb-empty")
	o := observed(t, b)
	o.Checks["integrity"] = Check{Status: "fail"}
	if _, err := Evaluate(b, o); err == nil {
		t.Fatal("unknown finding silently ignored")
	}
	delete(o.Checks, "integrity")
	o.ResourceEvidence = ""
	o.Usage.MemoryBytes = b.Manifest.Limits.MemoryBytes + 1
	v, err := Evaluate(b, o)
	if err != nil || v.Security != "fail" {
		t.Fatalf("reported overrun lost: %+v %v", v, err)
	}
	o.Usage.MemoryBytes = -1
	if _, err := Evaluate(b, o); err == nil {
		t.Fatal("negative measurement accepted")
	}
}

func TestOutputSafetyAppliesToControlsContainmentAndRejection(t *testing.T) {
	for _, id := range []string{"control-yenc", "yenc-bad-crc", "nzb-empty"} {
		b, _ := Generate(id)
		o := observed(t, b)
		o.Outcome = "accepted"
		if id == "nzb-empty" {
			o.Outcome = "rejected"
		}
		if err := os.Symlink("missing-canary", filepath.Join(o.OutputDirectory, "unsafe")); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		v, err := Evaluate(b, o)
		if err != nil || v.Security != "fail" {
			t.Fatalf("unsafe %s passed: %+v %v", id, v, err)
		}
	}
}

func TestStrictJSONRejectsAmbiguousOrUnobservedEvidence(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`, `[] {}`, `{"external_termination":null}`, `{}`} {
		var o Observation
		if err := DecodeJSON([]byte(raw), &o); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestBundleInventoryIsExact(t *testing.T) {
	b, _ := Generate("control-yenc")
	dir, err := Export(t.TempDir(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "undeclared.txt"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(dir); err == nil {
		t.Fatal("undeclared artifact accepted")
	}
}

func TestPlannedComparisonCannotOmitCasesOrReplayAttempts(t *testing.T) {
	c := ClientIdentity{Client: "test-client", Version: "1.0", Platform: "test", ConfigSHA256: Digest([]byte("config")), BinarySHA256: Digest([]byte("binary")), ObserverSHA256: Digest([]byte("observer"))}
	p, err := NewRunPlan([]string{"nzb-empty"}, []ClientIdentity{c}, 2, 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Runs) != 4 {
		t.Fatalf("required controls/repetitions missing: %+v", p.Runs)
	}
	r, err := ComparePlan(p, nil)
	if err != nil || r.Status != "incomplete" || len(r.Missing) != 4 {
		t.Fatalf("missing cases passed: %+v %v", r, err)
	}
	run := p.Runs[0]
	o := observed(t, Bundle{Manifest: p.Manifests[run.CaseID]})
	record, err := RecordResult(p, run.ID, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ComparePlan(p, []RecordedResult{record, record}); err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	// Recorded results survive output cleanup and retain digest validation.
	if err := os.RemoveAll(o.OutputDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := ComparePlan(p, []RecordedResult{record}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(record)
	var decoded RecordedResult
	if err := DecodeJSON(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Verdict.Security = "fabricated"
	if err := decoded.Validate(); err == nil {
		t.Fatal("modified receipt accepted")
	}
}
