package adversarial

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type EvidenceArtifact struct {
	SHA256 string          `json:"sha256"`
	Data   json.RawMessage `json:"data"`
}

// ObserverEvidence is emitted by an independent collector, never the client
// under test. Capabilities not supplied by a platform backend remain unknown.
// Artifacts are embedded for portable audit; the hashes are not signatures.
type ObserverEvidence struct {
	Responder          *ServerStats                `json:"responder,omitempty"`
	RunID              string                      `json:"run_id"`
	PlanSHA256         string                      `json:"plan_sha256"`
	ObserverSHA256     string                      `json:"observer_sha256"`
	ClientBinarySHA256 string                      `json:"client_binary_sha256"`
	AssignedOutput     string                      `json:"assigned_output"`
	TreeStopped        bool                        `json:"tree_stopped"`
	ReachedStage       string                      `json:"reached_stage"`
	ResourceScope      string                      `json:"resource_scope"`
	DroppedEvents      uint64                      `json:"dropped_events"`
	Artifacts          map[string]EvidenceArtifact `json:"artifacts"`
}

// EvidenceStatement is the typed interchange with platform collectors. A
// nonempty path or hash alone cannot establish any of these assertions.
type EvidenceStatement struct {
	RunID           string  `json:"run_id"`
	ManifestSHA256  string  `json:"manifest_sha256"`
	Check           string  `json:"check"`
	Status          string  `json:"status"`
	Collector       string  `json:"collector"`
	Instrumentation string  `json:"instrumentation"`
	SelfTestSHA256  string  `json:"self_test_sha256"`
	Usage           *Limits `json:"usage,omitempty"`
}

func (o Observation) validateObserverEvidence() error {
	e := o.Evidence
	if e == nil {
		return fmt.Errorf("independent observer evidence is absent")
	}
	if e.RunID == "" || !validDigest(e.PlanSHA256) || !validDigest(e.ObserverSHA256) || !validDigest(e.ClientBinarySHA256) || !e.TreeStopped || e.AssignedOutput != o.OutputDirectory || e.ResourceScope != "client_process_tree" || e.DroppedEvents != 0 {
		return fmt.Errorf("observer identity, stopped-tree/root binding, resource scope or event completeness is invalid")
	}
	if e.ReachedStage != "entry" && e.ReachedStage != "component" && e.ReachedStage != "application" {
		return fmt.Errorf("target stage was not independently established")
	}
	refs := map[string]string{}
	for check, observation := range o.Checks {
		if observation.Status == "pass" || observation.Status == "fail" {
			if strings.TrimSpace(observation.Evidence) == "" || refs[observation.Evidence] != "" {
				return fmt.Errorf("each observer check requires a distinct artifact")
			}
			refs[observation.Evidence] = check
		}
	}
	if strings.TrimSpace(o.ResourceEvidence) == "" || refs[o.ResourceEvidence] != "" {
		return fmt.Errorf("resources require a distinct observer artifact")
	}
	refs[o.ResourceEvidence] = "resources"
	if len(e.Artifacts) != len(refs) {
		return fmt.Errorf("observer artifact inventory differs from referenced evidence")
	}
	var total int
	for reference, check := range refs {
		a, ok := e.Artifacts[reference]
		total += len(a.Data)
		if !ok || len(a.Data) == 0 || len(a.Data) > 1<<20 || total > 8<<20 || a.SHA256 != Digest(a.Data) {
			return fmt.Errorf("missing, oversized or modified observer artifact %q", reference)
		}
		var statement EvidenceStatement
		if err := DecodeJSON(a.Data, &statement); err != nil {
			return err
		}
		if statement.RunID != e.RunID || statement.ManifestSHA256 != o.ManifestSHA256 || statement.Check != check || strings.TrimSpace(statement.Collector) == "" || strings.TrimSpace(statement.Instrumentation) == "" || !validDigest(statement.SelfTestSHA256) {
			return fmt.Errorf("observer artifact does not establish %s for this attempt", check)
		}
		if check == "resources" {
			if statement.Usage == nil || o.Usage == nil || *statement.Usage != *o.Usage {
				return fmt.Errorf("resource evidence differs from reported measurements")
			}
		} else if statement.Status != o.Checks[check].Status {
			return fmt.Errorf("observer status differs from reported %s", check)
		}
	}
	return nil
}

// CollectEvidenceFiles snapshots collector-produced JSON through a capability
// root. It does not manufacture observations for unavailable instrumentation.
func CollectEvidenceFiles(directory string, references []string) (map[string]EvidenceArtifact, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := map[string]EvidenceArtifact{}
	total := 0
	for _, name := range references {
		if _, ok := result[name]; ok {
			continue
		}
		raw, err := readRegular(root, name, 1<<20)
		if err != nil {
			return nil, err
		}
		total += len(raw)
		if total > 8<<20 {
			return nil, fmt.Errorf("observer evidence byte limit exceeded")
		}
		var statement EvidenceStatement
		if err := DecodeJSON(raw, &statement); err != nil {
			return nil, err
		}
		result[name] = EvidenceArtifact{SHA256: Digest(raw), Data: json.RawMessage(raw)}
	}
	return result, nil
}
