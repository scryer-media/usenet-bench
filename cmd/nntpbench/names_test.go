package main

import (
	"strings"
	"testing"
)

func TestEveryExamplePhaseHasReadableName(t *testing.T) {
	for _, path := range []string{"../../configs/chains/latency-series.example.json", "../../configs/chains/raw-native.example.json"} {
		config, err := loadChainConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, phase := range config.Phases {
			name := phase.displayName()
			if strings.TrimSpace(phase.DisplayName) == "" || name == phase.Name || seen[name] || !strings.Contains(name, "round-trip latency") || !strings.Contains(name, "KiB articles") {
				t.Fatalf("unreadable or duplicate phase label: %q: %q", phase.Name, name)
			}
			seen[name] = true
			if phase.Name == "B3-rtt10" && !strings.Contains(name, "Mixed archive downloads") {
				t.Fatal(name)
			}
		}
	}
}

func TestLegacyPhaseNameIsNotDecoded(t *testing.T) {
	p := ChainPhase{Name: "arbitrary-code", Mode: "sequential", ServerLink: "1gbit", ServerRTT: "10ms"}
	if strings.Contains(p.displayName(), p.Name) || !strings.Contains(p.displayName(), "Individual download performance") {
		t.Fatal(p.displayName())
	}
}
