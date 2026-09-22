package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// A provenance block that cannot be reconstructed from the artifacts is worse
// than none: it would state conditions nobody observed. This checks it is
// built from the run's own evidence -- the client's self-reported version, the
// image digest the daemon resolved, the connection count each client was
// given -- and not from anything the report supplies itself.
func TestReportProvenanceStatesWhatTheArtifactsRecorded(t *testing.T) {
	artifacts := []benchmark.QueueArtifact{
		provenanceTestArtifact(benchmark.Weaver, 1, 4, 2_000_000_000, 8<<30),
		provenanceTestArtifact(benchmark.SABnzbd, 1, 4, 2_000_000_000, 8<<30),
	}
	provenance := buildReportProvenance(provenanceInputs{Artifacts: artifacts, Manifest: provenanceTestManifest()})
	if provenance.HarnessCommit != "abc123" {
		t.Fatalf("harness commit: %q", provenance.HarnessCommit)
	}
	if len(provenance.Clients) != 2 {
		t.Fatalf("clients: %#v", provenance.Clients)
	}
	if provenance.Clients[0].Version != "test" || provenance.Clients[0].ImageDigest == "" {
		t.Fatalf("client provenance lost the build it measured: %#v", provenance.Clients[0])
	}
	if provenance.Lane.Connections != 4 || !provenance.Lane.ConnectionsEqual {
		t.Fatalf("connection parity: %#v", provenance.Lane)
	}
	if len(provenance.Corpus.FixtureIDs) != 1 || provenance.Corpus.FixturesDigest == "" {
		t.Fatalf("corpus identity: %#v", provenance.Corpus)
	}
	if provenance.Host.CPUModel != "Test CPU" || provenance.Host.DockerVersion == "" {
		t.Fatalf("host facts: %#v", provenance.Host)
	}
	if provenance.DockerParity == nil || !provenance.DockerParity.Equal {
		t.Fatalf("equal containers reported unequal: %#v", provenance.DockerParity)
	}
	if len(provenance.Warnings) != 0 {
		t.Fatalf("a clean phase warned: %#v", provenance.Warnings)
	}
}

// Two clients given different machines are not a comparison of the clients.
// The report still publishes the measurements -- they exist and the reader is
// entitled to them -- but it has to say so where the numbers are, both in the
// JSON and in the rendered header.
func TestReportProvenanceWarnsWhenContainersAreNotEqual(t *testing.T) {
	artifacts := []benchmark.QueueArtifact{
		provenanceTestArtifact(benchmark.Weaver, 1, 4, 2_000_000_000, 8<<30),
		provenanceTestArtifact(benchmark.SABnzbd, 1, 4, 1_000_000_000, 4<<30),
	}
	provenance := buildReportProvenance(provenanceInputs{Artifacts: artifacts, Manifest: provenanceTestManifest()})
	if provenance.DockerParity == nil || provenance.DockerParity.Equal {
		t.Fatal("unequal container ceilings were reported as parity")
	}
	if len(provenance.DockerParity.Findings) != 2 {
		t.Fatalf("expected a CPU finding and a memory finding: %#v", provenance.DockerParity.Findings)
	}
	var rendered strings.Builder
	printProvenanceHeader(&rendered, &provenance)
	if !strings.Contains(rendered.String(), "WARN") || !strings.Contains(rendered.String(), "not given equal machines") {
		t.Fatalf("the rendered header did not warn:\n%s", rendered.String())
	}
}

// A client given a different connection count than the others is measured
// under different conditions, which the lane's own premise forbids.
func TestReportProvenanceWarnsOnUnequalConnections(t *testing.T) {
	artifacts := []benchmark.QueueArtifact{
		provenanceTestArtifact(benchmark.Weaver, 1, 8, 2_000_000_000, 8<<30),
		provenanceTestArtifact(benchmark.SABnzbd, 1, 4, 2_000_000_000, 8<<30),
	}
	provenance := buildReportProvenance(provenanceInputs{Artifacts: artifacts, Manifest: provenanceTestManifest()})
	if provenance.Lane.ConnectionsEqual {
		t.Fatal("unequal connection counts were reported as equal")
	}
	if !containsSubstring(provenance.Warnings, "different connection counts") {
		t.Fatalf("warnings: %#v", provenance.Warnings)
	}
}

// A harness that cannot say which revision it is is still usable, but the
// report has to admit it rather than print an empty field.
func TestReportProvenanceWarnsOnUnknownHarnessCommit(t *testing.T) {
	manifest := provenanceTestManifest()
	manifest.HarnessCommit = ""
	provenance := buildReportProvenance(provenanceInputs{
		Artifacts: []benchmark.QueueArtifact{provenanceTestArtifact(benchmark.Weaver, 1, 4, 2_000_000_000, 8<<30)},
		Manifest:  manifest,
	})
	if provenance.HarnessCommit != "unknown" || !containsSubstring(provenance.Warnings, "harness commit is unknown") {
		t.Fatalf("unknown commit was not stated: %q %#v", provenance.HarnessCommit, provenance.Warnings)
	}
}

// The renderer prints the conditions before the figures, and prints the cost
// columns beside the wall clock. This is the contract the whole command
// exists for, so it is checked on rendered text rather than on the structs.
func TestRenderReportPutsConditionsFirstAndPrintsCostColumns(t *testing.T) {
	artifacts := make([]benchmark.QueueArtifact, 0, 40)
	for repetition := 1; repetition <= 20; repetition++ {
		artifacts = append(artifacts,
			summaryTestArtifact(benchmark.Weaver, repetition, int64(100+repetition)),
			summaryTestArtifact(benchmark.SABnzbd, repetition, int64(80+repetition)),
		)
	}
	summary, err := buildSummaryReport(artifacts, nil, benchmark.Weaver, benchmark.SABnzbd, 20, 17, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	provenance := buildReportProvenance(provenanceInputs{Artifacts: artifacts, Manifest: provenanceTestManifest()})
	summary.Provenance = &provenance
	contents, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	if err := renderReport(contents, &rendered); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()
	conditions := strings.Index(text, "CONDITIONS")
	comparison := strings.Index(text, "PAIRED COMPARISON")
	if conditions != 0 || comparison < conditions {
		t.Fatalf("conditions did not come first:\n%s", text)
	}
	for _, want := range []string{"CPU", "PEAK RSS", "DEVICE WRITES", "METHOD", "nzbfast is measured on the Linux container lane only", "KNOWN GAPS", "shipped defaults"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered report is missing %q:\n%s", want, text)
		}
	}
}

// The interleaved leg's report has to make the drift readable: a median per
// client with its spread, and every arm in run order with the host's own
// received bytes beside the payload it was supposed to move.
func TestInterleavedReportMediansArmsAndNICWitness(t *testing.T) {
	var artifacts []benchmark.QueueArtifact
	durations := map[benchmark.Client][]int64{
		benchmark.Weaver:  {100, 140, 120},
		benchmark.SABnzbd: {200, 260, 240},
	}
	for pass := 1; pass <= 3; pass++ {
		for _, client := range []benchmark.Client{benchmark.Weaver, benchmark.SABnzbd} {
			artifact := summaryTestArtifact(client, pass, durations[client][pass-1])
			// One arm sees far more traffic on the wire than its payload:
			// somebody else was using the link.
			delta := uint64(100 << 20)
			if client == benchmark.SABnzbd && pass == 2 {
				delta = uint64(400 << 20)
			}
			artifact.HostNICReceive = &benchmark.NICReceiveDelta{
				Available: true, Collector: "proc-net-dev",
				BeforeBytes: 0, AfterBytes: delta, DeltaBytes: delta,
			}
			artifacts = append(artifacts, artifact)
		}
	}
	report, err := buildInterleavedReport(artifacts, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Arms) != 6 || len(report.Clients) != 2 {
		t.Fatalf("unexpected shape: %d arms, %d clients", len(report.Arms), len(report.Clients))
	}
	for index, arm := range report.Arms {
		if arm.Order != index+1 {
			t.Fatalf("arms are not in run order: %#v", report.Arms)
		}
	}
	for _, client := range report.Clients {
		want := durations[client.Client]
		if client.FinishedArms != 3 || client.WallClockMedianNanoseconds != want[2] {
			t.Fatalf("%s median is not the middle arm: %#v", client.Client, client)
		}
		if client.WallClockMinNanoseconds != want[0] || client.WallClockMaxNanoseconds != want[1] {
			t.Fatalf("%s min/max: %#v", client.Client, client)
		}
	}
	flagged := 0
	for _, arm := range report.Arms {
		if arm.Contamination != "" {
			flagged++
		}
	}
	if flagged != 1 {
		t.Fatalf("expected exactly the contaminated arm to be flagged, got %d", flagged)
	}
	if !containsSubstring(report.Warnings, "contaminated link") {
		t.Fatalf("warnings: %#v", report.Warnings)
	}

	var rendered strings.Builder
	if err := renderInterleavedReport(report, &rendered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), "FLAGGED") || !strings.Contains(rendered.String(), "STARTED (LOCAL)") {
		t.Fatalf("rendered interleaved report:\n%s", rendered.String())
	}
}

// A randomized root read as an interleaved one would report a median over
// passes that were never passes.
func TestInterleavedReportRefusesARandomizedRoot(t *testing.T) {
	artifacts := []benchmark.QueueArtifact{summaryTestArtifact(benchmark.Weaver, 1, 100)}
	if _, err := buildInterleavedReport(artifacts, 0); err == nil {
		t.Fatal("a randomized root was summarized as interleaved")
	}
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func provenanceTestManifest() executionManifest {
	return executionManifest{
		SchemaVersion: 1,
		HarnessCommit: "abc123",
		Host:          hostProvenanceFacts(),
	}
}

func hostProvenanceFacts() hostFingerprint {
	return hostFingerprint{
		Hostname: "bench-host",
		GOOS:     "darwin",
		GOARCH:   "arm64",
		NumCPU:   10,
		Facts: map[string]hostFact{
			"cpu_model":      {Value: "Test CPU"},
			"memory_bytes":   {Value: "17179869184"},
			"kernel":         {Value: "Darwin 25.6.0"},
			"docker_version": {Value: "27.0.0 (client 27.0.0)"},
		},
	}
}

// provenanceTestArtifact is a finished sequential suite carrying the container
// readback, the connection count and the image digest a real Docker lane
// records.
func provenanceTestArtifact(client benchmark.Client, repetition, connections int, nanoCPUs, memory int64) benchmark.QueueArtifact {
	artifact := summaryTestFixtureArtifact("fixture-a", fixture.HeadlineFixtureClass, client, repetition, 100)
	artifact.AdapterResult.Connections = connections
	artifact.AdapterResult.ContainerRuntime = &benchmark.ContainerRuntime{
		Inspected:                  true,
		ContainerID:                "container-" + string(client),
		Image:                      "example/" + string(client) + ":pinned",
		ImageDigest:                "sha256:" + strings.Repeat("c", 64),
		NanoCPUs:                   nanoCPUs,
		MemoryLimitBytes:           memory,
		PidsLimit:                  512,
		NetworkMode:                "bench-net",
		WorkingDirMountType:        "volume",
		WorkingDirMountDestination: "/downloads/complete",
	}
	return artifact
}
