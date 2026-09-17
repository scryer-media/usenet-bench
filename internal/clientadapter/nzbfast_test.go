package clientadapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// nzbfastServers is the shape of the file the product reads at startup.
type nzbfastServers struct {
	Servers []struct {
		Host        string `json:"host"`
		Port        int    `json:"port"`
		TLS         bool   `json:"tls"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		Connections int    `json:"connections"`
	} `json:"servers"`
}

func TestNZBFastConfigIsTheProviderAndNothingElse(t *testing.T) {
	cfg := testConfig(t, benchmark.NZBFast, benchmark.Plaintext, benchmark.TLSNotApplicable)
	spec, err := cfg.RenderProductConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Every other setting stays at what the release ships, so the file the
	// suite writes must hold the server list and no tuning of its own.
	var parsed nzbfastServers
	decoder := json.NewDecoder(strings.NewReader(string(spec.ConfigContent)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatalf("rendered config is not a bare server list: %v\n%s", err, spec.ConfigContent)
	}
	if len(parsed.Servers) != 1 {
		t.Fatalf("servers = %d, want exactly the benchmark provider", len(parsed.Servers))
	}
	server := parsed.Servers[0]
	if server.Host != cfg.NNTPHost || server.TLS || server.Connections != cfg.Connections {
		t.Fatalf("server = %#v, want the plaintext benchmark provider at %d connections", server, cfg.Connections)
	}
}

func TestNZBFastRunsTheReleaseWithoutPerformanceTuning(t *testing.T) {
	cfg := testConfig(t, benchmark.NZBFast, benchmark.Plaintext, benchmark.TLSNotApplicable)
	spec, err := cfg.RenderProductConfig()
	if err != nil {
		t.Fatal(err)
	}
	// The lab settles where the product writes, what it authenticates with,
	// and the two behaviours that would otherwise leave the bench: a lookup
	// against public metadata services, and a cleanup that parks its archives
	// instead of deleting them. Nothing here may touch how fast it runs.
	allowed := map[string]bool{
		"NZBFAST_APIKEY": true, "NZBFAST_OUT": true,
		"NZBFAST_NO_ENRICH": true, "NZBFAST_NO_TRASH": true,
		"PUID": true, "PGID": true, "TZ": true,
	}
	for _, entry := range spec.Environment {
		name, _, _ := strings.Cut(entry, "=")
		if !allowed[name] {
			t.Fatalf("%s is not one of the settings the lab may make; the product is measured as it ships", name)
		}
	}
	if !strings.Contains(string(spec.Rendered), "NZBFAST_NO_TRASH=1") {
		t.Fatal("the audit record must state that cleanup deletes rather than parks")
	}
}

func TestNZBFastRendersTheSameProductInBothProfiles(t *testing.T) {
	// The product downloads, verifies and extracts in one pass however it is
	// started, so there is no second configuration for the direct-unpack
	// profile to select -- and a difference here would be a tuning knob.
	stock := testConfig(t, benchmark.NZBFast, benchmark.Plaintext, benchmark.TLSNotApplicable)
	stock.Profile = benchmark.ProfileStock
	direct := testConfig(t, benchmark.NZBFast, benchmark.Plaintext, benchmark.TLSNotApplicable)
	direct.Profile = benchmark.ProfileEquivalentThroughput
	direct.FixtureDir, direct.NZBPath, direct.NNTPCAFile = stock.FixtureDir, stock.NZBPath, stock.NNTPCAFile
	stockSpec, err := stock.RenderProductConfig()
	if err != nil {
		t.Fatal(err)
	}
	directSpec, err := direct.RenderProductConfig()
	if err != nil {
		t.Fatal(err)
	}
	if string(stockSpec.ConfigContent) != string(directSpec.ConfigContent) {
		t.Fatal("the two profiles must render the same product")
	}
}

func TestNZBFastVerifiedTLSTakesTheBenchmarkCA(t *testing.T) {
	cfg := testConfig(t, benchmark.NZBFast, benchmark.TLS, benchmark.TLSCAVerified)
	spec, err := cfg.RenderProductConfig()
	if err != nil {
		t.Fatal(err)
	}
	var parsed nzbfastServers
	if err := json.Unmarshal(spec.ConfigContent, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Servers) != 1 || !parsed.Servers[0].TLS {
		t.Fatalf("servers = %#v, want one TLS provider", parsed.Servers)
	}
	var extraCA string
	for _, entry := range spec.Environment {
		if name, value, _ := strings.Cut(entry, "="); name == "NZBFAST_EXTRA_CA" {
			extraCA = value
		}
	}
	if extraCA == "" {
		t.Fatal("CA-verified TLS must hand the product the benchmark CA rather than turn validation off")
	}
}

func TestNZBFastRefusesANonNumericPort(t *testing.T) {
	cfg := testConfig(t, benchmark.NZBFast, benchmark.Plaintext, benchmark.TLSNotApplicable)
	cfg.NNTPPort = "nntp"
	if _, err := cfg.RenderProductConfig(); err == nil {
		t.Fatal("a port the product cannot parse must be refused while the config is rendered")
	}
}
