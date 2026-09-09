package adversarial

import "strings"

// RecipeContract describes intended coverage, never inferred execution coverage.
// A runner must separately report the stage it actually reached.
type RecipeContract struct {
	Baseline          string   `json:"baseline"`
	Component         string   `json:"component"`
	Mutation          string   `json:"mutation"`
	Invariant         string   `json:"invariant"`
	Prerequisites     []string `json:"prerequisites"`
	PreservedEnvelope string   `json:"preserved_envelope"`
	CoverageLimit     string   `json:"coverage_limit"`
}

func contractFor(c Case) RecipeContract {
	component := c.Recipe
	if c.Recipe == "path" {
		component = strings.TrimSuffix(c.Family, "-path")
	}
	r := RecipeContract{Baseline: BaselineFor(c), Component: component, Mutation: c.Variant, Invariant: "bounded processing; no unsafe filesystem, process, network, privilege or secret side effects", Prerequisites: []string{"isolated assigned output root", "stopped client process tree before verification", "run-bound independent observer evidence"}, PreservedEnvelope: "generated yEnc and NZB framing except the explicitly targeted layer", CoverageLimit: "recipe intent alone does not prove the target component was exercised"}
	if c.Expectation == Accept || c.Expectation == Recover {
		r.Invariant += "; exact expected output bytes and required output paths"
	}
	if c.Expectation == Reject {
		r.Invariant += "; explicit rejection with no committed payload"
	}
	if component == "rar4" || component == "rar5" || component == "7z" {
		r.CoverageLimit = "built-in seed uses stored/Copy members; malformed coder/encryption metadata is header-parser coverage, not valid compressed/encrypted execution"
	}
	if c.Recipe == "repair" {
		r.CoverageLimit = "single-slice baseline; nontrivial algebra is covered separately by repair-multi"
	}
	if c.Recipe == "par2" && strings.HasPrefix(c.Variant, "consistent-") {
		r.PreservedEnvelope += "; File ID, Main references, IFSC reference, recovery set ID and every packet MD5 are recomputed consistently"
		r.CoverageLimit = "targets metadata consistency/size checks beyond identity validation; supplied file bytes intentionally disagree with the declared length or first-16K hash"
	}
	if c.Recipe == "repair-multi" {
		r.CoverageLimit = "two files, nine input slices, partial final slices, damage beyond first 16 KiB; independent par2 oracle test required"
	}
	if c.Recipe == "nzb" {
		r.PreservedEnvelope = "article bytes remain a valid single-part yEnc control; only NZB metadata is mutated"
	}
	if c.Family == "historical-regression" {
		switch c.Variant {
		case "sab-cve-2021-29488-par2-parent-escape":
			r.Prerequisites = append(r.Prerequisites, "client executes PAR2 filename placement for a directly downloaded index")
			r.CoverageLimit = "the PAR2 placement sink is reached only when the client consumes the supplied index and attempts to place the matching downloaded file"
		case "sab-ghsa-75g3-unpacked-par2-parent-escape":
			r.Prerequisites = append(r.Prerequisites, "client reclassifies an archive-extracted PAR2 index as repair metadata")
			r.CoverageLimit = "if extracted PAR2 files are inert outputs, this exercises archive extraction policy but does not reach the historical post-extraction PAR2 placement sink"
		case "sab-ghsa-mjwj-symlink-dotdot-par2-escape":
			r.Prerequisites = append(r.Prerequisites, "extractor materializes the ZIP symlink", "client reclassifies the extracted PAR2 index as repair metadata")
			r.CoverageLimit = "the combined sink is reached only if the symlink is materialized and the extracted PAR2 index is subsequently processed; regular-file link handling or inert PAR2 output prevents the chain"
		}
	}
	return r
}
