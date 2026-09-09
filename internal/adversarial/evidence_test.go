package adversarial

import "testing"

func TestEveryCaseHasDistinctReadableName(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Catalog() {
		if len(c.DisplayName) < 10 || c.DisplayName == c.ID || seen[c.DisplayName] {
			t.Fatalf("unreadable or duplicate name: %+v", c)
		}
		seen[c.DisplayName] = true
	}
}

func TestObserverEvidenceCannotBeReusedOrPartiallyBound(t *testing.T) {
	b, _ := Generate("nzb-empty")
	for name, mutate := range map[string]func(*Observation){
		"absent":           func(o *Observation) { o.Evidence = nil },
		"live process":     func(o *Observation) { o.Evidence.TreeStopped = false },
		"wrong root":       func(o *Observation) { o.Evidence.AssignedOutput += "-wrong" },
		"partial scope":    func(o *Observation) { o.Evidence.ResourceScope = "parent_only" },
		"dropped events":   func(o *Observation) { o.Evidence.DroppedEvents = 1 },
		"wrong run":        func(o *Observation) { o.Evidence.RunID = "another-run" },
		"missing artifact": func(o *Observation) { delete(o.Evidence.Artifacts, o.ResourceEvidence) },
		"extra artifact":   func(o *Observation) { o.Evidence.Artifacts["extra"] = EvidenceArtifact{} },
		"reused reference": func(o *Observation) {
			c := o.Checks[RequiredChecks[0]]
			c.Evidence = o.ResourceEvidence
			o.Checks[RequiredChecks[0]] = c
		},
		"tampered body": func(o *Observation) {
			a := o.Evidence.Artifacts[o.ResourceEvidence]
			a.Data = append(a.Data, ' ')
			o.Evidence.Artifacts[o.ResourceEvidence] = a
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := observed(t, b)
			mutate(&o)
			v, err := Evaluate(b, o)
			if err != nil || v.Security != "inconclusive" {
				t.Fatalf("incomplete evidence became pass: %+v %v", v, err)
			}
		})
	}
}
