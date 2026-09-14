package adversarial

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func observed(t *testing.T, b Bundle) Observation {
	checks := map[string]Check{}
	for _, key := range RequiredChecks {
		checks[key] = Check{"pass", key + ".json"}
	}
	return withTestEvidence(Observation{SchemaVersion: SchemaVersion, CaseID: b.Manifest.Case.ID, ManifestSHA256: ManifestDigest(b.Manifest), Client: "test-client", Version: "1.0", Platform: "test", ConfigSHA256: Digest([]byte("config")), Outcome: "rejected", Checks: checks, Usage: &Limits{100, 50, 1 << 20, 100, 1, 10, 1}, ResourceEvidence: "resources.json", OutputDirectory: t.TempDir()})
}

// Synthetic evidence is for evaluator unit tests, not a platform detector.
func withTestEvidence(o Observation) Observation {
	e := &ObserverEvidence{RunID: "test-run", PlanSHA256: Digest([]byte("plan")), ObserverSHA256: Digest([]byte("observer")), ClientBinarySHA256: Digest([]byte("binary")), AssignedOutput: o.OutputDirectory, TreeStopped: true, ReachedStage: "entry", ResourceScope: "client_process_tree", Artifacts: map[string]EvidenceArtifact{}}
	add := func(ref, check, status string, usage *Limits) {
		raw, _ := json.Marshal(EvidenceStatement{RunID: e.RunID, ManifestSHA256: o.ManifestSHA256, Check: check, Status: status, Collector: "unit-test", Instrumentation: "synthetic", SelfTestSHA256: Digest([]byte("test")), Usage: usage})
		e.Artifacts[ref] = EvidenceArtifact{SHA256: Digest(raw), Data: raw}
	}
	for key, check := range o.Checks {
		add(check.Evidence, key, check.Status, nil)
	}
	add(o.ResourceEvidence, "resources", "measured", o.Usage)
	o.Evidence = e
	return o
}

func TestNoObservationCannotPass(t *testing.T) {
	b, _ := Generate("nzb-empty")
	o := observed(t, b)
	o.Checks = nil
	o.Usage = nil
	v, err := Evaluate(b, o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Security != "inconclusive" || v.Behavior != "pass" {
		t.Fatalf("unobserved rejection: %+v", v)
	}
}

func TestScoringNeverConflatesSafetyAndCompatibility(t *testing.T) {
	b, _ := Generate("control-yenc")
	o := observed(t, b)
	v, err := Evaluate(b, o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Security != "pass" || v.Behavior != "fail" {
		t.Fatalf("reject-everything scored as resilient: %+v", v)
	}
	o.Outcome = "unsupported"
	v, err = Evaluate(b, o)
	if err != nil || v.Behavior != "unsupported" {
		t.Fatalf("unsupported format: %+v %v", v, err)
	}
}

func TestEverySecurityFindingFails(t *testing.T) {
	b, _ := Generate("nzb-empty")
	for _, key := range RequiredChecks {
		t.Run(key, func(t *testing.T) {
			o := observed(t, b)
			o.Checks[key] = Check{"fail", "observer/finding.json"}
			v, err := Evaluate(b, o)
			if err != nil || v.Security != "fail" {
				t.Fatalf("finding lost: %+v %v", v, err)
			}
		})
	}
}

func TestEveryResourceCounterHasBoundaryChecks(t *testing.T) {
	b, _ := Generate("nzb-empty")
	for i := 0; i < 7; i++ {
		for _, delta := range []int64{-1, 0, 1} {
			o := observed(t, b)
			usage := b.Manifest.Limits
			fields := []*int64{&usage.WallMilliseconds, &usage.CPUMilliseconds, &usage.MemoryBytes, &usage.WrittenBytes, &usage.Entries, &usage.FileDescriptors, &usage.Connections}
			*fields[i] += delta
			o.Usage = &usage
			o = withTestEvidence(o)
			v, err := Evaluate(b, o)
			if err != nil {
				t.Fatal(err)
			}
			want := "pass"
			if delta == 1 {
				want = "fail"
			}
			if v.Security != want {
				t.Fatalf("counter %d delta %d: %+v", i, delta, v)
			}
		}
	}
}

func TestMissingResourceFieldsAreNotZero(t *testing.T) {
	for _, raw := range []string{"{}", "null", `{"wall_milliseconds":0}`, `{"wall_milliseconds":0,"cpu_milliseconds":0,"memory_bytes":0,"written_bytes":0,"entries":0,"file_descriptors":0,"connections":null}`} {
		var l Limits
		if json.Unmarshal([]byte(raw), &l) == nil {
			t.Fatalf("accepted missing fields: %s", raw)
		}
	}
	l := DefaultLimits()
	raw, _ := json.Marshal(l)
	var got Limits
	if err := json.Unmarshal(raw, &got); err != nil || got != l {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
}

func TestExternalKillAndCrashAreNotContainment(t *testing.T) {
	b, _ := Generate("nzb-empty")
	for _, outcome := range []string{"crashed", "timeout", "rejected"} {
		o := observed(t, b)
		o.Outcome = outcome
		o.ExternalTermination = outcome == "rejected"
		v, err := Evaluate(b, o)
		if err != nil || v.Security != "fail" || v.Behavior != "fail" {
			t.Fatalf("kill scored as success: %+v %v", v, err)
		}
	}
}

func TestOutputVerificationIsIndependentAndBounded(t *testing.T) {
	b, _ := Generate("control-yenc")
	o := observed(t, b)
	o.Outcome = "accepted"
	o.OutputDirectory = t.TempDir()
	file := filepath.Join(o.OutputDirectory, "payload.bin")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Evaluate(b, o)
	if err != nil || v.Behavior != "pass" {
		t.Fatalf("valid output: %+v %v", v, err)
	}
	if err := os.WriteFile(file, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err = Evaluate(b, o)
	if err != nil || v.Behavior != "fail" {
		t.Fatal("corrupt output accepted")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", file); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutput(o.OutputDirectory, b.Manifest.ExpectedOutputs, b.Manifest.Limits); err == nil {
		t.Fatal("followed output symlink")
	}
}

func TestManifestBindingAndDuplicateCells(t *testing.T) {
	b, _ := Generate("nzb-empty")
	o := observed(t, b)
	o.ManifestSHA256 = Digest(nil)
	if _, err := Evaluate(b, o); err == nil {
		t.Fatal("wrong fixture observations accepted")
	}
	o = observed(t, b)
	v, _ := Evaluate(b, o)
	if _, err := Matrix([]Verdict{v, v}); err == nil {
		t.Fatal("duplicate cell accepted")
	}
}
