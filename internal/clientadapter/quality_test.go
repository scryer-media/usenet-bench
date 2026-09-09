package clientadapter

import (
	"strings"
	"testing"
)

func TestExternalCgroupIdentityAndController(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, path := range []string{"docker/" + id, "system.slice/docker-" + id + ".scope"} {
		if err := validateContainerCgroupIdentity(path, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"../docker/" + id, "other/" + strings.Repeat("b", 64), "/docker/" + id, "prefix" + id} {
		if err := validateContainerCgroupIdentity(path, id); err == nil {
			t.Fatalf("accepted unrelated/local PID scope %s", path)
		}
	}
	contents := "3:perf_event:/perf/container\n2:cpu,cpuacct:/cpu/container\n"
	group, err := parseControllerCgroup(contents, "cpuacct")
	if err != nil || group != "cpu/container" {
		t.Fatalf("CPU selected another hierarchy: %s %v", group, err)
	}
}

func TestMultiplexedOrMissingPerfCoverageIsUnavailable(t *testing.T) {
	for _, line := range []string{"123;;instructions;1;50.00;", "123;;instructions;1;;", "123;;instructions;0;100.00;", "123;;instructions;NaN;100.00;", "123;;instructions;+Inf;100.00;", "123;;instructions;1;NaN;", "123;;instructions;1;100;\n456;;instructions;1;100;"} {
		if _, err := parsePerfInstructions(line); err == nil {
			t.Fatal(line)
		}
	}
}
