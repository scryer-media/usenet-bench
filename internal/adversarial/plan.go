package adversarial

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

type ClientIdentity struct {
	Client         string            `json:"client"`
	Version        string            `json:"version"`
	Platform       string            `json:"platform"`
	ConfigSHA256   string            `json:"config_sha256"`
	BinarySHA256   string            `json:"binary_sha256"`
	ObserverSHA256 string            `json:"observer_sha256"`
	Tools          map[string]string `json:"tools_sha256"`
}

type PlannedCase struct {
	ID          string `json:"id"`
	CaseID      string `json:"case_id"`
	ClientIndex int    `json:"client_index"`
	Repetition  int    `json:"repetition"`
}

// RunPlan is the denominator, including controls and repeated attempts. Saved
// manifests allow historical comparisons without regenerating current recipes.
type RunPlan struct {
	SchemaVersion int                 `json:"schema_version"`
	Seed          int64               `json:"seed"`
	Repetitions   int                 `json:"repetitions"`
	Clients       []ClientIdentity    `json:"clients"`
	Manifests     map[string]Manifest `json:"manifests"`
	Runs          []PlannedCase       `json:"runs"`
	SHA256        string              `json:"sha256"`
}

func BaselineFor(c Case) string {
	switch c.Recipe {
	case "control":
		return c.ID
	case "assembly":
		return "control-multipart"
	case "repair", "repair-multi", "par2":
		return "control-par2"
	case "uu":
		return "control-uuencode"
	case "zip", "tar", "rar4", "rar5", "7z", "gzip", "deflate":
		return "control-" + c.Recipe
	case "path":
		for _, family := range []string{"zip", "tar", "rar4", "rar5", "7z", "par2"} {
			if c.Family == family+"-path" {
				return "control-" + family
			}
		}
	}
	return "control-yenc"
}

func NewRunPlan(ids []string, clients []ClientIdentity, repetitions int, seed int64) (RunPlan, error) {
	p := RunPlan{SchemaVersion: 1, Seed: seed, Repetitions: repetitions, Clients: clients, Manifests: map[string]Manifest{}}
	if len(ids) == 0 || len(ids) > 4096 || repetitions < 1 || repetitions > 100 || len(clients) == 0 || len(clients) > 32 || len(ids)*2*len(clients)*repetitions > 100000 {
		return p, fmt.Errorf("select a bounded plan: 1..100 repetitions, 1..32 clients and at most 100000 attempts including controls")
	}
	for _, id := range ids {
		b, err := Generate(id)
		if err != nil {
			return p, err
		}
		p.Manifests[id] = b.Manifest
		baseline, err := Generate(BaselineFor(b.Manifest.Case))
		if err != nil {
			return p, err
		}
		p.Manifests[baseline.Manifest.Case.ID] = baseline.Manifest
	}
	p.Runs = plannedCases(p)
	p.SHA256 = p.digest()
	return p, p.Validate()
}

func (p RunPlan) digest() string { p.SHA256 = ""; return ManifestLikeDigest(p) }

func plannedCases(p RunPlan) []PlannedCase {
	if p.Repetitions < 1 || p.Repetitions > 100 || len(p.Clients) > 32 || len(p.Manifests) > 4096 || len(p.Manifests)*len(p.Clients)*p.Repetitions > 100000 {
		return nil
	}
	ids := make([]string, 0, len(p.Manifests))
	for id := range p.Manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rng := rand.New(rand.NewSource(p.Seed))
	var runs []PlannedCase
	for r := 1; r <= p.Repetitions; r++ {
		var block []PlannedCase
		for _, id := range ids {
			for client := range p.Clients {
				block = append(block, PlannedCase{CaseID: id, ClientIndex: client, Repetition: r})
			}
		}
		rng.Shuffle(len(block), func(i, j int) { block[i], block[j] = block[j], block[i] })
		for _, run := range block {
			run.ID = fmt.Sprintf("attempt-%06d", len(runs)+1)
			runs = append(runs, run)
		}
	}
	return runs
}

func (p RunPlan) Validate() error {
	if p.SchemaVersion != 1 || len(p.Clients) == 0 || len(p.Manifests) == 0 || len(p.Runs) == 0 || p.SHA256 != p.digest() {
		return fmt.Errorf("invalid or modified adversarial plan")
	}
	clients := map[string]bool{}
	for _, c := range p.Clients {
		key := ManifestLikeDigest(c)
		if clients[key] || strings.TrimSpace(c.Client) == "" || strings.TrimSpace(c.Version) == "" || strings.TrimSpace(c.Platform) == "" || !validDigest(c.ConfigSHA256) || !validDigest(c.BinarySHA256) || !validDigest(c.ObserverSHA256) {
			return fmt.Errorf("incomplete or duplicate client identity")
		}
		clients[key] = true
		for name, digest := range c.Tools {
			if strings.TrimSpace(name) == "" || !validDigest(digest) {
				return fmt.Errorf("invalid tool identity")
			}
		}
	}
	for id, m := range p.Manifests {
		if id != m.Case.ID || m.RecipeVersion == "" || m.SchemaVersion != SchemaVersion {
			return fmt.Errorf("invalid planned manifest")
		}
		if _, ok := p.Manifests[BaselineFor(m.Case)]; !ok {
			return fmt.Errorf("missing baseline for %s", id)
		}
	}
	if ManifestLikeDigest(p.Runs) != ManifestLikeDigest(plannedCases(p)) {
		return fmt.Errorf("plan does not contain its exact randomized repetition set")
	}
	return nil
}

type PlannedComparison struct {
	PlanSHA256 string    `json:"plan_sha256"`
	Planned    int       `json:"planned"`
	Recorded   int       `json:"recorded"`
	Missing    []string  `json:"missing"`
	Verdicts   []Verdict `json:"verdicts"`
	Status     string    `json:"status"` // passed, failed, incomplete
}

// ComparePlan never infers its denominator from supplied observations.
func ComparePlan(p RunPlan, records []RecordedResult) (PlannedComparison, error) {
	r := PlannedComparison{PlanSHA256: p.SHA256, Planned: len(p.Runs), Status: "passed", Missing: []string{}}
	if err := p.Validate(); err != nil {
		return r, err
	}
	byID := map[string]RecordedResult{}
	for _, record := range records {
		if _, ok := byID[record.RunID]; ok {
			return r, fmt.Errorf("duplicate attempt %s", record.RunID)
		}
		if err := record.Validate(); err != nil {
			return r, err
		}
		byID[record.RunID] = record
	}
	for _, run := range p.Runs {
		record, ok := byID[run.ID]
		if !ok {
			r.Missing = append(r.Missing, run.ID)
			continue
		}
		delete(byID, run.ID)
		c := p.Clients[run.ClientIndex]
		v := record.Verdict
		if record.PlanSHA256 != p.SHA256 || v.CaseID != run.CaseID || v.ManifestSHA256 != ManifestDigest(p.Manifests[run.CaseID]) || v.Client != c.Client || v.Version != c.Version || v.Platform != c.Platform || v.ConfigSHA256 != c.ConfigSHA256 || record.IdentitySHA256 != ManifestLikeDigest(c) {
			return r, fmt.Errorf("attempt %s differs from planned identity", run.ID)
		}
		r.Recorded++
		r.Verdicts = append(r.Verdicts, v)
		if v.Security == "fail" || v.Behavior == "fail" {
			r.Status = "failed"
		} else if v.Security != "pass" || (v.Behavior != "pass" && v.Behavior != "policy-dependent") {
			if r.Status != "failed" {
				r.Status = "incomplete"
			}
		}
	}
	if len(byID) > 0 {
		return r, fmt.Errorf("unplanned attempts supplied")
	}
	if len(r.Missing) > 0 && r.Status != "failed" {
		r.Status = "incomplete"
	}
	return r, nil
}
