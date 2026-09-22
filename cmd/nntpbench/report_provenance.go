package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/buildinfo"
)

// Every summary this harness writes carries a provenance block, and every
// rendered report prints it first. A benchmark that survives being read by
// somebody who would rather it were wrong has to say, without being asked,
// which harness produced it, which build of each client ran, over which
// corpus, on which machine, under which link. None of that is new evidence:
// the artifacts already carry all of it. What was missing was one place that
// states it, so a reader is not reconstructing the conditions from a
// directory of JSON before they can start disagreeing with the numbers.

// reportProvenanceSchema is the version of the block below. It is stated
// rather than inferred, because the block outlives the run that wrote it.
const reportProvenanceSchema = 1

type reportProvenance struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	// HarnessCommit is the revision of the harness that took the
	// measurements, read from the run's execution manifest -- not from the
	// binary running the summary, which may be a different build entirely.
	HarnessCommit string             `json:"harness_commit"`
	Corpus        corpusProvenance   `json:"corpus"`
	Lane          laneProvenance     `json:"lane"`
	Clients       []clientProvenance `json:"clients"`
	Host          hostProvenance     `json:"host"`
	// DockerParity is present for the Docker lane: what the daemon actually
	// gave each container, and whether the containers were given equal
	// machines.
	DockerParity *dockerParity `json:"docker_parity,omitempty"`
	// Warnings are the provenance's own complaints: a fact it could not
	// establish, or one it established and does not like. They are part of
	// the record, not diagnostics printed to a terminal and lost.
	Warnings []string `json:"warnings,omitempty"`
}

// corpusProvenance identifies the workload. FixturesDigest is over each
// fixture's immutable workload identity, so two runs of the same corpus agree
// on one short string and a regenerated corpus does not.
type corpusProvenance struct {
	FixtureIDs       []string `json:"fixture_ids"`
	FixturesDigest   string   `json:"fixtures_digest"`
	ArticleProfileID string   `json:"article_profile_id"`
	ArticleRawBytes  int      `json:"article_raw_bytes"`
}

type laneProvenance struct {
	// Target is docker-linux, macos-native or windows-native.
	Target  benchmark.ExecutionTarget `json:"target"`
	Profile string                    `json:"profile"`
	// ServerLinkID names the shaped link, or "external" for a real provider.
	ServerLinkID     string   `json:"server_link_id"`
	RTTMicros        uint64   `json:"server_rtt_micros"`
	EgressBPS        uint64   `json:"server_egress_bits_per_second"`
	BurstBytes       uint64   `json:"server_burst_bytes"`
	StorageProfileID string   `json:"storage_profile_id"`
	Transports       []string `json:"transports"`
	// Connections is the per-client server connection count, or 0 when the
	// artifacts predate its recording. ConnectionsEqual is false when the
	// clients were not all given the same one, which is not a comparison.
	Connections      int  `json:"connections"`
	ConnectionsEqual bool `json:"connections_equal"`
}

// clientProvenance is one measured product: which build ran, what it says its
// own version is, and how it reached the server.
type clientProvenance struct {
	Client benchmark.Client `json:"client"`
	// Adapter is the catalog lane: the client and archive toolchain pair the
	// adapter was selected by.
	Adapter string `json:"adapter"`
	// Identity is the image reference for a Docker lane (pinned by digest)
	// and the launched binary's path for a native one.
	Identity string `json:"identity"`
	// Version is what the client itself answered over its public API, with
	// the image label as the fallback the adapter records when the API gave
	// nothing.
	Version string `json:"version"`
	// ImageDigest is the daemon's own resolution of the image, Docker lane
	// only; the identity above is the reference the run asked for.
	ImageDigest              string                  `json:"image_digest,omitempty"`
	ArchiveToolchainIdentity string                  `json:"archive_toolchain_identity"`
	RenderedConfigSHA256     string                  `json:"rendered_config_sha256,omitempty"`
	Connections              int                     `json:"connections,omitempty"`
	TLSValidation            benchmark.TLSValidation `json:"tls_validation"`
	TransportLabel           string                  `json:"transport_label"`
}

type hostProvenance struct {
	Hostname    string `json:"hostname"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUModel    string `json:"cpu_model"`
	LogicalCPUs int    `json:"logical_cpus"`
	MemoryBytes uint64 `json:"memory_bytes"`
	Kernel      string `json:"kernel"`
	// DockerVersion is the daemon and client version for a Docker lane, empty
	// elsewhere.
	DockerVersion string `json:"docker_version,omitempty"`
}

// dockerParity is the Docker lane's equal-machine claim, stated per container
// from the daemon's own readback and checked rather than asserted.
type dockerParity struct {
	Containers []dockerParityContainer `json:"containers"`
	// Equal is true when every client container reports the same CPU and
	// memory ceiling. When it is false, Findings says which pair differs: a
	// phase whose clients were not given the same machine is not a comparison
	// of the clients.
	Equal    bool     `json:"cpu_and_memory_equal"`
	Findings []string `json:"findings,omitempty"`
}

type dockerParityContainer struct {
	Client           benchmark.Client `json:"client"`
	Inspected        bool             `json:"inspected"`
	Unavailable      string           `json:"unavailable,omitempty"`
	CPUs             string           `json:"cpus"`
	MemoryLimitBytes string           `json:"memory_limit_bytes"`
	PidsLimit        string           `json:"pids_limit"`
	WorkingDirMount  string           `json:"working_dir_mount"`
	NetworkMode      string           `json:"network_mode"`
}

// provenanceInputs is what a summarizer hands the builder: the artifacts it
// summarized and the execution manifest they were bound to.
type provenanceInputs struct {
	Artifacts []benchmark.QueueArtifact
	Manifest  executionManifest
}

func buildReportProvenance(inputs provenanceInputs) reportProvenance {
	provenance := reportProvenance{
		SchemaVersion: reportProvenanceSchema,
		GeneratedAt:   time.Now().UTC(),
		HarnessCommit: strings.TrimSpace(inputs.Manifest.HarnessCommit),
	}
	if provenance.HarnessCommit == "" || provenance.HarnessCommit == buildinfo.UnknownCommit {
		provenance.HarnessCommit = buildinfo.UnknownCommit
		provenance.Warnings = append(provenance.Warnings, buildinfo.UnknownCommitWarning)
	}
	provenance.Host = hostProvenanceFrom(inputs.Manifest)
	provenance.Corpus, provenance.Lane, provenance.Clients = laneAndClients(inputs.Artifacts, &provenance)
	provenance.DockerParity = dockerParityFrom(inputs.Artifacts, provenance.Lane.Target, &provenance)
	sort.Strings(provenance.Warnings)
	return provenance
}

// laneAndClients walks the artifacts once. Every axis it reports is a value
// the artifacts agree on; a disagreement is a warning rather than a silently
// chosen first value, because a lane that mixed two of anything is not the
// lane the report says it is.
func laneAndClients(artifacts []benchmark.QueueArtifact, provenance *reportProvenance) (corpusProvenance, laneProvenance, []clientProvenance) {
	corpus := corpusProvenance{}
	lane := laneProvenance{}
	fixtures := make(map[string]string)
	transports := make(map[string]bool)
	clients := make(map[benchmark.Client]clientProvenance)
	connections := make(map[benchmark.Client]int)
	for _, artifact := range artifacts {
		for _, job := range artifact.Jobs {
			run := job.Run
			if previous, ok := fixtures[run.FixtureID]; ok && previous != job.WorkloadSHA256 {
				provenance.Warnings = append(provenance.Warnings,
					fmt.Sprintf("fixture %s appears under two workload identities; the corpus digest describes neither", run.FixtureID))
			}
			fixtures[run.FixtureID] = job.WorkloadSHA256
			transports[string(run.Transport)] = true
			noteSingleValue(provenance, "execution target", string(lane.Target), string(run.ExecutionTarget), func() { lane.Target = run.ExecutionTarget })
			noteSingleValue(provenance, "profile", lane.Profile, run.Profile, func() { lane.Profile = run.Profile })
			noteSingleValue(provenance, "server link", lane.ServerLinkID, run.ServerLink.ID, func() {
				lane.ServerLinkID = run.ServerLink.ID
				lane.RTTMicros = run.ServerLink.RTTMicros
				lane.EgressBPS = run.ServerLink.EgressBitsPerSecond
				lane.BurstBytes = run.ServerLink.BurstBytes
			})
			noteSingleValue(provenance, "storage profile", lane.StorageProfileID, run.StorageProfile.ID, func() {
				lane.StorageProfileID = run.StorageProfile.ID
			})
			noteSingleValue(provenance, "article profile", corpus.ArticleProfileID, run.ArticleProfile.ID, func() {
				corpus.ArticleProfileID = run.ArticleProfile.ID
				corpus.ArticleRawBytes = run.ArticleProfile.RawBytes
			})
		}
		result := artifact.AdapterResult
		if result == nil {
			continue
		}
		entry := clientProvenance{
			Client:                   result.Client,
			Adapter:                  string(result.Client) + "/" + string(result.ArchiveToolchain),
			Identity:                 result.ClientIdentity,
			Version:                  result.ClientVersion,
			ArchiveToolchainIdentity: result.ArchiveToolchainIdentity,
			RenderedConfigSHA256:     result.RenderedConfigSHA256,
			Connections:              result.Connections,
			TLSValidation:            result.TLSValidation,
			TransportLabel:           result.TransportLabel,
		}
		if result.ContainerRuntime != nil {
			entry.ImageDigest = result.ContainerRuntime.ImageDigest
		}
		if previous, ok := clients[entry.Client]; ok {
			if previous.Identity != entry.Identity || previous.Version != entry.Version {
				provenance.Warnings = append(provenance.Warnings,
					fmt.Sprintf("%s ran as more than one build in this phase (%s %s and %s %s)",
						entry.Client, previous.Identity, previous.Version, entry.Identity, entry.Version))
			}
			// The rendered configuration legitimately differs per fixture
			// (paths, archive passwords), so only the build is compared.
			entry.RenderedConfigSHA256 = ""
		}
		clients[entry.Client] = entry
		if entry.Connections > 0 {
			connections[entry.Client] = entry.Connections
		}
	}
	for fixtureID := range fixtures {
		corpus.FixtureIDs = append(corpus.FixtureIDs, fixtureID)
	}
	sort.Strings(corpus.FixtureIDs)
	corpus.FixturesDigest = corpusDigest(corpus.FixtureIDs, fixtures)
	for transport := range transports {
		lane.Transports = append(lane.Transports, transport)
	}
	sort.Strings(lane.Transports)

	lane.ConnectionsEqual = true
	for _, client := range sortedClients(connections) {
		count := connections[client]
		if lane.Connections == 0 {
			lane.Connections = count
			continue
		}
		if lane.Connections != count {
			lane.ConnectionsEqual = false
			provenance.Warnings = append(provenance.Warnings,
				fmt.Sprintf("clients were given different connection counts (%d and %d); the lane's premise is one count for every client", lane.Connections, count))
		}
	}
	if lane.Connections == 0 {
		lane.ConnectionsEqual = false
		provenance.Warnings = append(provenance.Warnings,
			"connection count is not recorded in these artifacts; they predate the harness recording it per client")
	}

	entries := make([]clientProvenance, 0, len(clients))
	for _, client := range sortedClientEntries(clients) {
		entries = append(entries, clients[client])
	}
	return corpus, lane, entries
}

// noteSingleValue records the first value of an axis and warns when a later
// artifact carries a different one.
func noteSingleValue(provenance *reportProvenance, axis, current, observed string, set func()) {
	if strings.TrimSpace(observed) == "" {
		return
	}
	if current == "" {
		set()
		return
	}
	if current != observed {
		provenance.Warnings = append(provenance.Warnings,
			fmt.Sprintf("summary mixes %s %q and %q; the header states only the first", axis, current, observed))
	}
}

func corpusDigest(ids []string, workloads map[string]string) string {
	if len(ids) == 0 {
		return ""
	}
	pairs := make([][2]string, 0, len(ids))
	for _, id := range ids {
		pairs = append(pairs, [2]string{id, workloads[id]})
	}
	return benchmark.EvidenceDigest(pairs)
}

func sortedClients(counts map[benchmark.Client]int) []benchmark.Client {
	clients := make([]benchmark.Client, 0, len(counts))
	for client := range counts {
		clients = append(clients, client)
	}
	sort.Slice(clients, func(left, right int) bool { return clients[left] < clients[right] })
	return clients
}

func sortedClientEntries(entries map[benchmark.Client]clientProvenance) []benchmark.Client {
	clients := make([]benchmark.Client, 0, len(entries))
	for client := range entries {
		clients = append(clients, client)
	}
	sort.Slice(clients, func(left, right int) bool { return clients[left] < clients[right] })
	return clients
}

// dockerParityFrom states what each client container actually ran with and
// whether the containers were given the same machine. It is a WARN rather
// than a refusal: the measurements exist and the reader is entitled to them,
// but they are not a comparison of the clients if the clients were not given
// equal machines, and the report has to say so where the numbers are.
func dockerParityFrom(artifacts []benchmark.QueueArtifact, target benchmark.ExecutionTarget, provenance *reportProvenance) *dockerParity {
	if target != benchmark.DockerLinux {
		return nil
	}
	type allowance struct {
		cpus    float64
		cpusOK  bool
		memory  int64
		runtime benchmark.ContainerRuntime
	}
	seen := make(map[benchmark.Client]allowance)
	parity := &dockerParity{Equal: true}
	for _, artifact := range artifacts {
		result := artifact.AdapterResult
		if result == nil {
			continue
		}
		runtime := benchmark.ContainerRuntime{Unavailable: "the adapter recorded no container runtime readback"}
		if result.ContainerRuntime != nil {
			runtime = *result.ContainerRuntime
		}
		cpus, cpusOK := runtime.CPUAllowance()
		if previous, ok := seen[result.Client]; ok {
			if previous.cpusOK == cpusOK && previous.cpus == cpus && previous.memory == runtime.MemoryLimitBytes {
				continue
			}
			parity.Equal = false
			parity.Findings = append(parity.Findings,
				fmt.Sprintf("%s ran with more than one container ceiling inside this phase", result.Client))
		}
		seen[result.Client] = allowance{cpus: cpus, cpusOK: cpusOK, memory: runtime.MemoryLimitBytes, runtime: runtime}
	}
	clients := make([]benchmark.Client, 0, len(seen))
	for client := range seen {
		clients = append(clients, client)
	}
	sort.Slice(clients, func(left, right int) bool { return clients[left] < clients[right] })
	for index, client := range clients {
		entry := seen[client]
		parity.Containers = append(parity.Containers, dockerParityContainer{
			Client:           client,
			Inspected:        entry.runtime.Inspected,
			Unavailable:      entry.runtime.Unavailable,
			CPUs:             entry.runtime.CPULabel(),
			MemoryLimitBytes: entry.runtime.MemoryLabel(),
			PidsLimit:        entry.runtime.PidsLabel(),
			WorkingDirMount:  entry.runtime.MountLabel(),
			NetworkMode:      entry.runtime.NetworkLabel(),
		})
		if !entry.runtime.Inspected {
			parity.Equal = false
			parity.Findings = append(parity.Findings,
				fmt.Sprintf("%s has no container readback (%s); equal machines cannot be shown", client, entry.runtime.Unavailable))
			continue
		}
		if index == 0 {
			continue
		}
		first := seen[clients[0]]
		if !first.runtime.Inspected {
			continue
		}
		if first.cpus != entry.cpus || first.cpusOK != entry.cpusOK {
			parity.Equal = false
			parity.Findings = append(parity.Findings,
				fmt.Sprintf("%s ran with %s CPUs and %s with %s", clients[0], first.runtime.CPULabel(), client, entry.runtime.CPULabel()))
		}
		if first.memory != entry.memory {
			parity.Equal = false
			parity.Findings = append(parity.Findings,
				fmt.Sprintf("%s ran with a %s byte memory ceiling and %s with %s", clients[0], first.runtime.MemoryLabel(), client, entry.runtime.MemoryLabel()))
		}
	}
	sort.Strings(parity.Findings)
	for _, finding := range parity.Findings {
		provenance.Warnings = append(provenance.Warnings, "docker parity: "+finding)
	}
	return parity
}

// hostProvenanceFrom lifts the machine's identity out of the raw host facts
// the execution manifest captured. The facts stay in the manifest verbatim;
// these are the few a reader needs before anything else.
func hostProvenanceFrom(manifest executionManifest) hostProvenance {
	host := hostProvenance{
		Hostname:    manifest.Host.Hostname,
		OS:          manifest.Host.GOOS,
		Arch:        manifest.Host.GOARCH,
		LogicalCPUs: manifest.Host.NumCPU,
		CPUModel:    "unknown",
		Kernel:      "unknown",
	}
	facts := manifest.Host.Facts
	if value := factValue(facts, "kernel"); value != "" {
		host.Kernel = value
	}
	host.DockerVersion = factValue(facts, "docker_version")
	switch manifest.Host.GOOS {
	case "darwin":
		if value := factValue(facts, "cpu_model"); value != "" {
			host.CPUModel = value
		}
		if value := factValue(facts, "memory_bytes"); value != "" {
			if parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil {
				host.MemoryBytes = parsed
			}
		}
	case "linux":
		if value := firstFieldAfter(factValue(facts, "cpu"), "model name"); value != "" {
			host.CPUModel = value
		}
		if value := firstFieldAfter(factValue(facts, "memory"), "MemTotal"); value != "" {
			// /proc/meminfo reports kibibytes.
			if parsed, err := strconv.ParseUint(strings.Fields(value)[0], 10, 64); err == nil {
				host.MemoryBytes = parsed * 1024
			}
		}
	case "windows":
		if value := windowsProcessorName(factValue(facts, "hardware")); value != "" {
			host.CPUModel = value
		}
	}
	return host
}

func factValue(facts map[string]hostFact, key string) string {
	fact, ok := facts[key]
	if !ok || strings.TrimSpace(fact.Value) == "" {
		return ""
	}
	return strings.TrimSpace(fact.Value)
}

// firstFieldAfter reads the first "<label> : <value>" line of a /proc file.
func firstFieldAfter(contents, label string) string {
	for _, line := range strings.Split(contents, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != label {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// windowsProcessorName reads the Name out of the Win32_Processor JSON the
// Windows collector captures, whether PowerShell emitted one object or an
// array of them.
func windowsProcessorName(contents string) string {
	if strings.TrimSpace(contents) == "" {
		return ""
	}
	var single struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal([]byte(contents), &single); err == nil && strings.TrimSpace(single.Name) != "" {
		return strings.TrimSpace(single.Name)
	}
	var many []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal([]byte(contents), &many); err == nil && len(many) > 0 {
		return strings.TrimSpace(many[0].Name)
	}
	return ""
}
