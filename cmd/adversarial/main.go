// adversarial generates and serves a client-neutral hostile Usenet corpus.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/scryer-media/usenet-bench/internal/adversarial"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var incomplete *incompleteGate
		if errors.As(err, &incomplete) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type incompleteGate struct{ Reason string }

func (e *incompleteGate) Error() string { return e.Reason }
func output(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func run(args []string, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: adversarial {list|generate|verify|serve|plan|record|compare-plan|observation-template|evaluate|compare} [options]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("case", "", "case ID")
	family := f.String("family", "", "select one family for list/generate")
	parent := f.String("output", "", "existing output directory outside a Git checkout")
	bundleDir := f.String("bundle", "", "generated bundle directory")
	listen := f.String("listen", "127.0.0.1:0", "loopback NNTP listener")
	duration := f.Duration("duration", 5*time.Minute, "responder lifetime including client startup (maximum five minutes)")
	observation := f.String("observation", "", "observation JSON file")
	observations := f.String("observations", "", "JSON array of observations")
	planFile := f.String("plan", "", "immutable adversarial plan JSON")
	clientsFile := f.String("clients", "", "JSON client identities for plan generation")
	resultsFile := f.String("results", "", "JSON array of portable recorded results")
	runID := f.String("run-id", "", "planned attempt ID")
	repetitions := f.Int("repetitions", 1, "independent repetitions")
	seed := f.Int64("seed", 1, "deterministic schedule seed")
	strict := f.Bool("strict", true, "fail incomplete or unsupported evidence (exit 2)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	switch args[0] {
	case "plan":
		var clients []adversarial.ClientIdentity
		if err := readJSON(*clientsFile, &clients); err != nil {
			return err
		}
		var ids []string
		for _, c := range adversarial.Catalog() {
			if (*id == "" || *id == c.ID) && (*family == "" || *family == c.Family) {
				ids = append(ids, c.ID)
			}
		}
		p, err := adversarial.NewRunPlan(ids, clients, *repetitions, *seed)
		if err != nil {
			return err
		}
		return output(w, p)
	case "record":
		var p adversarial.RunPlan
		var o adversarial.Observation
		if err := readJSON(*planFile, &p); err != nil {
			return err
		}
		if err := readJSON(*observation, &o); err != nil {
			return err
		}
		r, err := adversarial.RecordResult(p, *runID, o)
		if err != nil {
			return err
		}
		if err := output(w, r); err != nil {
			return err
		}
		if r.Verdict.Security == "fail" || r.Verdict.Behavior == "fail" {
			return fmt.Errorf("recorded case failure; preserve private evidence")
		}
		if *strict && (r.Verdict.Security != "pass" || (r.Verdict.Behavior != "pass" && r.Verdict.Behavior != "policy-dependent")) {
			return &incompleteGate{"recorded case is incomplete"}
		}
		return nil
	case "compare-plan":
		var p adversarial.RunPlan
		var records []adversarial.RecordedResult
		if err := readJSON(*planFile, &p); err != nil {
			return err
		}
		if err := readJSON(*resultsFile, &records); err != nil {
			return err
		}
		r, err := adversarial.ComparePlan(p, records)
		if err != nil {
			return err
		}
		if err := output(w, r); err != nil {
			return err
		}
		if r.Status == "failed" {
			return fmt.Errorf("planned comparison contains failures")
		}
		if *strict && r.Status != "passed" {
			return &incompleteGate{"planned comparison is incomplete"}
		}
		return nil
	case "list", "generate":
		var selected []adversarial.Case
		for _, c := range adversarial.Catalog() {
			if (*id == "" || c.ID == *id) && (*family == "" || c.Family == *family) {
				selected = append(selected, c)
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("no matching cases")
		}
		if args[0] == "list" {
			return output(w, selected)
		}
		if err := externalOutput(*parent); err != nil {
			return err
		}
		for _, c := range selected {
			b, err := adversarial.Generate(c.ID)
			if err != nil {
				return err
			}
			if _, err = adversarial.Export(*parent, b); err != nil {
				return err
			}
		}
		return output(w, map[string]any{"generated": len(selected), "output": *parent, "recipe_version": adversarial.RecipeVersion})
	case "verify":
		if *bundleDir == "" {
			return fmt.Errorf("--bundle is required")
		}
		b, err := adversarial.Verify(*bundleDir)
		if err != nil {
			return err
		}
		return output(w, map[string]any{"case": b.Manifest.Case.ID, "verified": true, "manifest_sha256": adversarial.ManifestDigest(b.Manifest)})
	case "serve":
		var b adversarial.Bundle
		var err error
		if *id != "" && *bundleDir != "" {
			return fmt.Errorf("select --case or --bundle")
		}
		if *bundleDir != "" {
			b, err = adversarial.Verify(*bundleDir)
		} else {
			b, err = adversarial.Generate(*id)
		}
		if err != nil {
			return err
		}
		if err := adversarial.LoopbackAddress(*listen); err != nil {
			return err
		}
		if *duration <= 0 || *duration > 5*time.Minute {
			return fmt.Errorf("duration must be positive and no greater than five minutes")
		}
		listener, err := net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, *duration)
		defer cancel()
		if err := output(w, map[string]any{"listen": listener.Addr().String(), "case": b.Manifest.Case.ID, "manifest_sha256": adversarial.ManifestDigest(b.Manifest)}); err != nil {
			_ = listener.Close()
			return err
		}
		s := adversarial.Server{Bundle: b, RunID: *runID}
		err = s.Serve(ctx, listener)
		if err != nil {
			return err
		}
		return output(w, s.Stats())
	case "observation-template":
		b, err := adversarial.Generate(*id)
		if err != nil {
			return err
		}
		checks := map[string]adversarial.Check{}
		for _, name := range adversarial.RequiredChecks {
			checks[name] = adversarial.Check{Status: "unknown"}
		}
		return output(w, adversarial.Observation{SchemaVersion: adversarial.SchemaVersion, CaseID: *id, ManifestSHA256: adversarial.ManifestDigest(b.Manifest), Checks: checks})
	case "evaluate":
		if *observation == "" || *bundleDir == "" {
			return fmt.Errorf("--observation and --bundle are required")
		}
		b, err := adversarial.Verify(*bundleDir)
		if err != nil {
			return err
		}
		var o adversarial.Observation
		if err := readJSON(*observation, &o); err != nil {
			return err
		}
		v, err := adversarial.Evaluate(b, o)
		if err != nil {
			return err
		}
		if err = output(w, v); err != nil {
			return err
		}
		if v.Security == "fail" || v.Behavior == "fail" {
			return fmt.Errorf("case failed; preserve the private observation and verdict")
		}
		if *strict && (v.Security != "pass" || (v.Behavior != "pass" && v.Behavior != "policy-dependent")) {
			return &incompleteGate{"case evidence is incomplete or unsupported"}
		}
		return nil
	case "compare":
		if *strict {
			return &incompleteGate{"strict comparison requires compare-plan --plan and --results; use --strict=false only for exploratory observations"}
		}
		if *observations == "" {
			return fmt.Errorf("--observations is required")
		}
		var input []adversarial.Observation
		if err := readJSON(*observations, &input); err != nil {
			return err
		}
		if len(input) == 0 {
			return fmt.Errorf("no observations")
		}
		cache := map[string]adversarial.Bundle{}
		var verdicts []adversarial.Verdict
		failed := false
		for _, o := range input {
			b, ok := cache[o.CaseID]
			if !ok {
				var err error
				b, err = adversarial.Generate(o.CaseID)
				if err != nil {
					return err
				}
				cache[o.CaseID] = b
			}
			v, err := adversarial.Evaluate(b, o)
			if err != nil {
				return err
			}
			verdicts = append(verdicts, v)
			failed = failed || v.Security == "fail" || v.Behavior == "fail"
		}
		matrix, err := adversarial.Matrix(verdicts)
		if err != nil {
			return err
		}
		if err = output(w, matrix); err != nil {
			return err
		}
		if failed {
			return fmt.Errorf("comparison contains failures; review private findings before publication")
		}
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func externalOutput(path string) error {
	if path == "" {
		return fmt.Errorf("--output is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("output must be an existing directory")
	}
	for p := resolved; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return fmt.Errorf("generated fixture output must be outside a Git checkout")
		} else if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return fmt.Errorf("JSON input exceeds 16 MiB")
	}
	return adversarial.DecodeJSON(raw, v)
}
