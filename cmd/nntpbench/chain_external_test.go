package main

import (
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

func externalChainConfig() map[string]any {
	return map[string]any{
		"schema_version": ChainSchemaVersion,
		"name":           "real",
		"stack":          ChainStackExternal,
		"adapters":       "adapters.json",
		"phases": []map[string]any{{
			"name":          "R",
			"mode":          "sequential",
			"plan":          "plan-R.json",
			"fixtures_root": "fixtures-external",
			"artifacts":     "artifacts-R",
			"article_size":  "700k",
			"plan_spec": map[string]any{
				"fixtures":    []string{"sab-test"},
				"transports":  []string{"tls"},
				"profile":     "equivalent-throughput",
				"repetitions": 3,
				"seed":        1,
			},
		}},
	}
}

func TestExternalChainDefaultsItsLinkAndProviderEnv(t *testing.T) {
	path := writeChainConfigFile(t, externalChainConfig())
	config, err := loadChainConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(config.ProviderEnv, ".env") || config.Phases[0].ServerLink != benchmark.LinkExternal {
		t.Fatalf("provider_env %q, link %q, want .env beside the config and the external link", config.ProviderEnv, config.Phases[0].ServerLink)
	}
	args := strings.Join(chainPhaseArgs(config, config.Phases[0]), " ")
	if !strings.Contains(args, "--provider-env "+config.ProviderEnv) || strings.Contains(args, "--nntp-host") || strings.Contains(args, "--password") {
		t.Fatalf("phase args %q, want only the provider env path", args)
	}
	plan, err := buildChainPlan(config.Phases[0], string(benchmark.MacOSNative))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Runs) != 9 {
		t.Fatalf("plan has %d runs, want three repetitions of three clients", len(plan.Runs))
	}
	for _, run := range plan.Runs {
		if run.Transport != benchmark.TLS || run.TLSValidation != benchmark.TLSPublicRoots {
			t.Fatalf("run %s is %s/%s, want tls validated against public roots", run.ID, run.Transport, run.TLSValidation)
		}
	}
}

func TestExternalChainRefusesASecondProviderOrAShapedPhase(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"nntp host":     {func(c map[string]any) { c["nntp_host"] = "news.example.net" }, "nntp_host"},
		"password file": {func(c map[string]any) { c["password_file"] = "pw" }, "password_file"},
		"connections":   {func(c map[string]any) { c["connections"] = 8 }, "connections"},
		"shaped link": {func(c map[string]any) {
			c["phases"].([]map[string]any)[0]["server_link"] = "1gbit"
		}, "external"},
		"plaintext": {func(c map[string]any) {
			c["phases"].([]map[string]any)[0]["plan_spec"].(map[string]any)["transports"] = []string{"plaintext", "tls"}
		}, "tls only"},
		"default transports": {func(c map[string]any) {
			delete(c["phases"].([]map[string]any)[0]["plan_spec"].(map[string]any), "transports")
		}, "must declare"},
		"queue transition": {func(c map[string]any) {
			c["phases"].([]map[string]any)[0]["mode"] = "queue-transition"
		}, "queue-transition"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := externalChainConfig()
			tc.mutate(raw)
			if _, err := loadChainConfig(writeChainConfigFile(t, raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestOnlyAnExternalChainMeasuresTheExternalLink(t *testing.T) {
	raw := minimalChainConfig()
	raw["phases"].([]map[string]any)[0]["server_link"] = benchmark.LinkExternal
	delete(raw["phases"].([]map[string]any)[0], "server_rtt")
	if _, err := loadChainConfig(writeChainConfigFile(t, raw)); err == nil {
		t.Fatal("a docker stack accepted a phase on the external link")
	}
	raw = minimalChainConfig()
	raw["provider_env"] = ".env"
	if _, err := loadChainConfig(writeChainConfigFile(t, raw)); err == nil || !strings.Contains(err.Error(), "provider_env") {
		t.Fatalf("error = %v, want a docker stack refusing provider_env", err)
	}
}

func TestCheckProviderPlanKeepsTheDeclaredTransport(t *testing.T) {
	plan, err := benchmark.BuildPlan(benchmark.PlanOptions{
		FixtureIDs:     []string{"sab-test"},
		Clients:        []benchmark.Client{benchmark.Weaver},
		Transports:     []benchmark.Transport{benchmark.TLS},
		Targets:        []benchmark.ExecutionTarget{benchmark.MacOSNative},
		Repetitions:    1,
		ServerLink:     benchmark.ServerLinkProfile{ID: benchmark.LinkExternal, Scope: "external_provider"},
		ArticleProfile: benchmark.ArticleProfile{ID: benchmark.Article700K, RawBytes: 700 << 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	tlsProvider := benchmark.ProviderEnv{Host: "news.example.net", Port: "563", TLS: true, Connections: 1}
	if err := checkProviderPlan(plan, tlsProvider); err != nil {
		t.Fatalf("a TLS plan against a TLS provider was refused: %v", err)
	}
	plaintextProvider := tlsProvider
	plaintextProvider.TLS = false
	if err := checkProviderPlan(plan, plaintextProvider); err == nil {
		t.Fatal("a TLS plan ran against a provider declared plaintext")
	}
}
