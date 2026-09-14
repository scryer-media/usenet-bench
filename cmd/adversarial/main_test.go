package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/adversarial"
)

func TestListFiltersAndInvalidSelection(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"list", "--family", "repair"}, &out); err != nil {
		t.Fatal(err)
	}
	var cases []adversarial.Case
	if err := json.Unmarshal(out.Bytes(), &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 7 {
		t.Fatal(len(cases))
	}
	for _, args := range [][]string{{}, {"unknown"}, {"list", "--case", "absent"}, {"list", "extra"}, {"generate"}, {"serve", "--case", "control-yenc", "--listen", "0.0.0.0:119"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatalf("accepted bad arguments %v", args)
		}
	}
}

func TestGenerateVerifyTemplate(t *testing.T) {
	parent := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"generate", "--case", "control-yenc", "--output", parent}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--bundle", filepath.Join(parent, "control-yenc")}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"generate", "--case", "control-yenc", "--output", parent}, &out); err == nil {
		t.Fatal("overwrote generated bundle")
	}
	out.Reset()
	if err := run([]string{"observation-template", "--case", "control-yenc"}, &out); err != nil {
		t.Fatal(err)
	}
	var o adversarial.Observation
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if o.Usage != nil || o.Outcome != "" {
		t.Fatal("template invented observations")
	}
	for _, c := range o.Checks {
		if c.Status != "unknown" {
			t.Fatal("template invented security pass")
		}
	}
}

func TestGenerateRefusesGitCheckoutAndSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: unused"), 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "generated")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if externalOutput(sub) == nil {
		t.Fatal("generated archives permitted inside Git checkout")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(sub, alias); err != nil {
		t.Fatal(err)
	}
	if externalOutput(alias) == nil {
		t.Fatal("symlink bypassed checkout check")
	}
}

func TestJSONRejectsUnknownAndTrailingFields(t *testing.T) {
	for _, raw := range []string{`{"unexpected":true}`, `{} {}`, `{} false`} {
		path := filepath.Join(t.TempDir(), "observation.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		var o adversarial.Observation
		if readJSON(path, &o) == nil {
			t.Fatal("accepted invalid observation", raw)
		}
	}
}
