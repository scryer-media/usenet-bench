package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

const (
	dockerChainTemplate = "../../configs/chains/latency-series.example.json"
	nativeChainTemplate = "../../configs/chains/raw-native.example.json"
)

// nativeChainTemplateOmits names the Docker phases a raw stack cannot run,
// with the reason. validateChainConfig refuses each of them on a raw stack, so
// the list is the harness's limit, not a choice of which phases to skip.
var nativeChainTemplateOmits = map[string]string{
	"C3-eqt-rtt10": "an NFS storage profile needs the throttled export container, which a raw stack does not run",
}

// readChainTemplate decodes a chain file as written, before any path is
// resolved, so two templates compare on what an operator copies.
func readChainTemplate(t *testing.T, path string) ChainConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config ChainConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return config
}

// nativeChainTemplateDrift lists every way the native template fails to twin
// the Docker one: a missing phase, or a twin measured under other conditions
// or against another corpus.
func nativeChainTemplateDrift(docker, native ChainConfig) []string {
	twins := make(map[string]ChainPhase, len(native.Phases))
	for _, phase := range native.Phases {
		twins[phase.Name] = phase
	}
	var drift []string
	for _, phase := range docker.Phases {
		if _, omitted := nativeChainTemplateOmits[phase.Name]; omitted {
			if _, present := twins[phase.Name]; present {
				drift = append(drift, fmt.Sprintf("phase %s is listed as one a raw stack cannot run, yet the native template has it", phase.Name))
			}
			continue
		}
		twin, ok := twins[phase.Name]
		if !ok {
			drift = append(drift, fmt.Sprintf("phase %s has no native twin", phase.Name))
			continue
		}
		fields := []struct {
			name         string
			docker, twin any
		}{
			{"plan_spec", phase.PlanSpec, twin.PlanSpec},
			{"server_link", phase.ServerLink, twin.ServerLink},
			{"server_rtt", phase.ServerRTT, twin.ServerRTT},
			{"article_size", phase.ArticleSize, twin.ArticleSize},
			{"mode", phase.Mode, twin.Mode},
		}
		for _, field := range fields {
			if !reflect.DeepEqual(field.docker, field.twin) {
				drift = append(drift, fmt.Sprintf("phase %s: %s differs from the Docker template", phase.Name, field.name))
			}
		}
		// A fixture set is named, so the same name must stand for the same
		// corpus in both files.
		if phase.PlanSpec != nil && phase.PlanSpec.FixtureSet != "" {
			set := phase.PlanSpec.FixtureSet
			if !reflect.DeepEqual(docker.FixtureSets[set], native.FixtureSets[set]) {
				drift = append(drift, fmt.Sprintf("phase %s: fixture set %q differs from the Docker template", phase.Name, set))
			}
		}
	}
	for name := range nativeChainTemplateOmits {
		found := false
		for _, phase := range docker.Phases {
			found = found || phase.Name == name
		}
		if !found {
			drift = append(drift, fmt.Sprintf("omitted phase %s is no longer in the Docker template; drop it from the list", name))
		}
	}
	return drift
}

// Every rig runs the same phases. Phases have been added to the Docker
// template alone before, leaving the native lanes measuring a fraction of the
// series, so each Docker phase must have a native twin measured under the same
// conditions against the same corpus, unless the raw stack cannot run it.
func TestNativeChainTemplateTwinsEveryDockerPhase(t *testing.T) {
	docker := readChainTemplate(t, dockerChainTemplate)
	native := readChainTemplate(t, nativeChainTemplate)
	if len(docker.Phases) == 0 {
		t.Fatal("the Docker template declares no phases; this test is checking nothing")
	}
	for _, problem := range nativeChainTemplateDrift(docker, native) {
		t.Error(problem)
	}
}
