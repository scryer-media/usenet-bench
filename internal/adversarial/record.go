package adversarial

import (
	"encoding/json"
	"fmt"
)

func ManifestLikeDigest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return Digest(raw)
}

// RecordedResult freezes the evaluated outcome after independent output
// inspection. Comparison never reopens mutable or already-cleaned outputs.
// Hash binding detects accidental modification; it does not authenticate an
// untrusted observer. Observer binaries and evidence are part of the run plan.
type RecordedResult struct {
	Observation       *Observation `json:"observation"`
	SchemaVersion     int          `json:"schema_version"`
	RunID             string       `json:"run_id"`
	PlanSHA256        string       `json:"plan_sha256"`
	IdentitySHA256    string       `json:"identity_sha256"`
	ObservationSHA256 string       `json:"observation_sha256"`
	Verdict           Verdict      `json:"verdict"`
	SHA256            string       `json:"sha256"`
}

func RecordResult(p RunPlan, runID string, o Observation) (RecordedResult, error) {
	if err := p.Validate(); err != nil {
		return RecordedResult{}, err
	}
	for _, run := range p.Runs {
		if run.ID == runID {
			m := p.Manifests[run.CaseID]
			c := p.Clients[run.ClientIndex]
			if o.Client != c.Client || o.Version != c.Version || o.Platform != c.Platform || o.ConfigSHA256 != c.ConfigSHA256 {
				return RecordedResult{}, fmt.Errorf("observation differs from planned client")
			}
			v, err := Evaluate(Bundle{Manifest: m}, o)
			if err != nil {
				return RecordedResult{}, err
			}
			r := RecordedResult{SchemaVersion: 1, RunID: runID, PlanSHA256: p.SHA256, IdentitySHA256: ManifestLikeDigest(c), ObservationSHA256: ManifestLikeDigest(o), Verdict: v}
			r.Observation = &o
			if o.Evidence == nil || o.Evidence.RunID != runID || o.Evidence.PlanSHA256 != p.SHA256 || o.Evidence.ObserverSHA256 != c.ObserverSHA256 || o.Evidence.ClientBinarySHA256 != c.BinarySHA256 {
				if r.Verdict.Security == "pass" {
					r.Verdict.Security = "inconclusive"
				}
				r.Verdict.Reasons = append(r.Verdict.Reasons, "observer was not bound to this planned execution")
			}
			r.SHA256 = r.digest()
			return r, nil
		}
	}
	return RecordedResult{}, fmt.Errorf("unplanned attempt %s", runID)
}
func (r RecordedResult) digest() string { r.SHA256 = ""; return ManifestLikeDigest(r) }
func (r RecordedResult) Validate() error {
	if r.Observation == nil || r.ObservationSHA256 != ManifestLikeDigest(r.Observation) {
		return fmt.Errorf("recorded observation digest mismatch")
	}
	if r.SchemaVersion != 1 || r.RunID == "" || !validDigest(r.PlanSHA256) || !validDigest(r.IdentitySHA256) || !validDigest(r.ObservationSHA256) || r.SHA256 != r.digest() {
		return fmt.Errorf("invalid or modified recorded result")
	}
	o, v := r.Observation, r.Verdict
	if v.CaseID != o.CaseID || v.ManifestSHA256 != o.ManifestSHA256 || v.Client != o.Client || v.Version != o.Version || v.Platform != o.Platform || v.ConfigSHA256 != o.ConfigSHA256 {
		return fmt.Errorf("verdict differs from observed identity")
	}
	if v.Security != "pass" && v.Security != "fail" && v.Security != "inconclusive" {
		return fmt.Errorf("unknown security verdict")
	}
	if v.Behavior != "pass" && v.Behavior != "fail" && v.Behavior != "inconclusive" && v.Behavior != "unsupported" && v.Behavior != "policy-dependent" {
		return fmt.Errorf("unknown behavior verdict")
	}
	if v.Security == "pass" {
		if err := o.validateObserverEvidence(); err != nil {
			return err
		}
		if o.Evidence.RunID != r.RunID || o.Evidence.PlanSHA256 != r.PlanSHA256 {
			return fmt.Errorf("passing verdict is not bound to recorded attempt")
		}
	}
	return nil
}
