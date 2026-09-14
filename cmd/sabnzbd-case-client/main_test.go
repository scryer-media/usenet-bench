//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseOneShotArgs(t *testing.T) {
	config, input, report, err := parseOneShotArgs([]string{"--config", "/scratch/config", "download", "/scratch/input.nzb", "--report", "/scratch/report.json"})
	if err != nil || config != "/scratch/config" || input != "/scratch/input.nzb" || report != "/scratch/report.json" {
		t.Fatal(config, input, report, err)
	}
	if _, _, _, err := parseOneShotArgs([]string{"download", "/scratch/input.nzb"}); err == nil {
		t.Fatal("incomplete one-shot arguments accepted")
	}
}

func TestRenderConfigUsesSyntheticServerAndPrivatePaths(t *testing.T) {
	values := map[string]string{
		"WEAVER_INTERMEDIATE_DIR":     "/scratch/incomplete",
		"WEAVER_COMPLETE_DIR":         "/scratch/complete",
		"WEAVER_SERVER_1_HOSTNAME":    "127.0.0.1",
		"WEAVER_SERVER_1_PORT":        "8119",
		"WEAVER_SERVER_1_USERNAME":    "benchmark",
		"WEAVER_SERVER_1_PASSWORD":    "synthetic-only",
		"WEAVER_SERVER_1_CONNECTIONS": "2",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	config, err := renderConfig(18080)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"host = 127.0.0.1", "port = 8119", "download_dir = /scratch/incomplete", "complete_dir = /scratch/complete", "direct_unpack = 1", "direct_unpack_tested = 1", "deobfuscate_final_filenames = 0"} {
		if !strings.Contains(config, expected) {
			t.Fatalf("missing %q", expected)
		}
	}
	for key := range values {
		_ = os.Unsetenv(key)
	}
}

func TestAPIURLIsAuthenticatedAndStable(t *testing.T) {
	got := apiURL("http://127.0.0.1:8080/api", "version", nil)
	if !strings.Contains(got, "apikey="+apiKey) || !strings.Contains(got, "mode=version") || !strings.Contains(got, "output=json") {
		t.Fatal(got)
	}
}

func TestCanonicalSABEventIgnoresDuplicateNotifierMessages(t *testing.T) {
	tests := []struct {
		line   string
		marker string
		want   bool
	}{
		{"::INFO::[notifier:169] Sending notification: Error - Fatal error in Assembler", assemblerFatalMarker, false},
		{"::ERROR::[assembler:320] Fatal error in Assembler", assemblerFatalMarker, true},
		{"::INFO::[notifier:169] Sending notification: Warning - Restarting because of crashed assembler", assemblerRestartMarker, false},
		{"::WARNING::[__init__:595] Restarting because of crashed assembler", assemblerRestartMarker, true},
	}
	for _, test := range tests {
		if got := canonicalSABEvent(test.line, test.marker); got != test.want {
			t.Errorf("canonicalSABEvent(%q, %q) = %v, want %v", test.line, test.marker, got, test.want)
		}
	}
}
