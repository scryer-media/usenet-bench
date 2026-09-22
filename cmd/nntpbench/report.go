package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// `nntpbench report` renders a summary this harness wrote as text a person can
// read. Everything it prints is already in the JSON: it adds no number, drops
// no result and reorders nothing except to put the conditions before the
// figures. A reader who wants to check it against the artifacts can, and a
// reader who wants to argue with it knows exactly what they are arguing with.
//
// The header is not decoration. A benchmark of other people's products is read
// by people with a reason to doubt it, and the first question every one of
// them asks is what actually ran. Answering that before the numbers, every
// time, without being asked, is the difference between a result and a claim.

// reportMethod is fixed text. It is the ethos the whole harness is built to,
// and it is stated the same way in every report so that it can be checked
// against the artifacts rather than re-argued each time.
const reportMethod = `METHOD

Every client is the vendor's shipped product, run with its shipped defaults.
Nothing here is tuned, and no client carries a profile written by this
harness. Each client gets one run per lane, the same connection count, the
same server and the same corpus, and is driven only through the public API
its own users drive it through. Every setting that was in force is rendered
into the run's artifact, so what the numbers were taken under is on the
record rather than in a description of it.

Wall clock is measured from submission to the client's own terminal state,
and a result counts only when the output it produced has been independently
verified against the fixture's hashes. A client that does not finish is
reported as not finishing, on the fixture where it happened, rather than
dropped.

The real-provider leg is the one exception to a single run per lane. Nothing
shapes that link and nothing holds it still, so the leg runs the client list
forward, then reversed, then forward again, and reports each client's median
across its passes with the minimum and maximum beside it and every arm in the
order it ran.`

// reportNZBFastPlacement explains the one client that does not appear in every
// lane, in one sentence, because an absence a reader has to infer looks like
// an absence somebody arranged.
const reportNZBFastPlacement = `nzbfast is measured on the Linux container lane only, where container overhead
is nil; the container image is the packaging the vendor leads with, and this
harness measures it in that form rather than on a native lane.`

// reportKnownGaps is the standing list. A report that prints only the gaps a
// particular run happened to hit would let a quiet run look stronger than a
// noisy one; these hold for every run this harness produces.
var reportKnownGaps = []string{
	"Bytes written to device are collected on the Docker lane only, from the container's own cgroup. Both native lanes report the counter unavailable with that reason rather than as zero, so a cost comparison that includes them is a comparison of two of the three columns.",
	"The host NIC counter that witnesses the real-provider leg is read on Linux and macOS only. A Windows host records it unavailable, and that leg's arms then rest on the client's own accounting alone.",
	"The container cgroup charges every helper a client spawns -- unpackers, repair tools -- to that client, which is the intent. A native lane charges the client's process tree, which is the same intent by a different mechanism, and the two are never pooled: a comparison whose clients were measured at different scopes is withheld rather than made.",
	"A shaped lane's link is attested from the shaper's own counters. The real-provider leg has no shaper and therefore no attestation: its numbers describe one provider on one evening and are never pooled with a shaped lane's.",
	"A breadth fixture that no client finishes is reported as a did-not-finish for each of them on that fixture, and its stratum is withheld from the pooled figure rather than counted as a tie. The breadth aggregate is a compatibility figure, not a speed figure, and is never quoted as one.",
	"nzbfast parks an identical queued NZB as a held duplicate instead of running it. The harness now classifies that refusal as that client not finishing the fixture, so the phase still produces a result with the refusal recorded against the client; before this, the same refusal failed the whole phase.",
	"An NFS storage profile moves the payload over the network, so the container's block-io counter does not see it. The device-write figure for such a lane says so and is not comparable with a local-storage lane's.",
}

func report(args []string) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var summaryPath string
	flags.StringVar(&summaryPath, "summary", "", "a summary JSON file written by `nntpbench summarize` in any of its modes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if summaryPath == "" {
		return fmt.Errorf("--summary is required")
	}
	contents, err := os.ReadFile(summaryPath)
	if err != nil {
		return fmt.Errorf("read summary: %w", err)
	}
	return renderReport(contents, os.Stdout)
}

// renderReport picks the renderer by what the summary actually contains. The
// three summary shapes are distinct enough to tell apart, and a file that is
// none of them is refused rather than rendered as an empty report.
func renderReport(contents []byte, out io.Writer) error {
	var probe struct {
		Comparisons []json.RawMessage `json:"comparisons"`
		Arms        []json.RawMessage `json:"arms"`
		Lanes       []json.RawMessage `json:"lanes"`
	}
	if err := json.Unmarshal(contents, &probe); err != nil {
		return fmt.Errorf("decode summary: %w", err)
	}
	switch {
	case len(probe.Comparisons) > 0:
		var summary summaryReport
		if err := json.Unmarshal(contents, &summary); err != nil {
			return fmt.Errorf("decode paired summary: %w", err)
		}
		return renderSequentialReport(summary, out)
	case len(probe.Arms) > 0:
		var summary interleavedReport
		if err := json.Unmarshal(contents, &summary); err != nil {
			return fmt.Errorf("decode interleaved summary: %w", err)
		}
		return renderInterleavedReport(summary, out)
	case len(probe.Lanes) > 0:
		var summary queueDrainReport
		if err := json.Unmarshal(contents, &summary); err != nil {
			return fmt.Errorf("decode queue-drain summary: %w", err)
		}
		return renderQueueDrainReport(summary, out)
	default:
		return fmt.Errorf("this file has no comparisons, arms or lanes; it is not a summary this command renders")
	}
}

// printProvenanceHeader prints the conditions before anything else, and says
// so plainly when it cannot establish one of them.
func printProvenanceHeader(out io.Writer, provenance *reportProvenance) {
	fmt.Fprintln(out, "CONDITIONS")
	fmt.Fprintln(out)
	if provenance == nil {
		fmt.Fprintln(out, "  This summary carries no provenance block: it was produced before the harness")
		fmt.Fprintln(out, "  recorded one, or from artifacts without their execution manifest. What ran, on")
		fmt.Fprintln(out, "  what, cannot be stated from this file alone.")
		fmt.Fprintln(out)
		return
	}
	fmt.Fprintf(out, "  Harness commit      %s\n", provenance.HarnessCommit)
	fmt.Fprintf(out, "  Generated           %s\n", provenance.GeneratedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(out, "  Host                %s, %s/%s, %s, %d logical CPUs%s\n",
		orUnknown(provenance.Host.Hostname), provenance.Host.OS, provenance.Host.Arch,
		orUnknown(provenance.Host.CPUModel), provenance.Host.LogicalCPUs, memorySuffix(provenance.Host.MemoryBytes))
	fmt.Fprintf(out, "  Kernel              %s\n", orUnknown(provenance.Host.Kernel))
	if provenance.Host.DockerVersion != "" {
		fmt.Fprintf(out, "  Docker              %s\n", provenance.Host.DockerVersion)
	}
	connections := "not recorded"
	if provenance.Lane.Connections > 0 {
		connections = fmt.Sprintf("%d per client", provenance.Lane.Connections)
		if !provenance.Lane.ConnectionsEqual {
			connections += " (NOT EQUAL ACROSS CLIENTS)"
		}
	}
	fmt.Fprintf(out, "  Lane                %s, profile %s, transports %s, connections %s\n",
		orUnknown(string(provenance.Lane.Target)), orUnknown(provenance.Lane.Profile),
		strings.Join(provenance.Lane.Transports, "+"), connections)
	fmt.Fprintf(out, "  Server link         %s (rtt %.0f ms, egress %s, burst %d B)\n",
		orUnknown(provenance.Lane.ServerLinkID), float64(provenance.Lane.RTTMicros)/1000,
		bitRate(provenance.Lane.EgressBPS), provenance.Lane.BurstBytes)
	fmt.Fprintf(out, "  Storage             %s\n", orUnknown(provenance.Lane.StorageProfileID))
	fmt.Fprintf(out, "  Corpus              %d %s, digest %s, articles %s (%d B raw)\n",
		len(provenance.Corpus.FixtureIDs), plural(len(provenance.Corpus.FixtureIDs), "fixture"), shortDigest(provenance.Corpus.FixturesDigest),
		orUnknown(provenance.Corpus.ArticleProfileID), provenance.Corpus.ArticleRawBytes)
	fmt.Fprintln(out)
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "  CLIENT\tVERSION\tBUILD\tIMAGE DIGEST\tTLS\tCONNS")
	for _, client := range provenance.Clients {
		fmt.Fprintf(writer, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			client.Client, orUnknown(client.Version), orUnknown(client.Identity),
			shortDigest(client.ImageDigest), orUnknown(string(client.TLSValidation)), countOrDash(client.Connections))
	}
	writer.Flush()
	fmt.Fprintln(out)
	if parity := provenance.DockerParity; parity != nil {
		fmt.Fprintln(out, "  Container limits, read back from the daemon after start:")
		writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "  CLIENT\tCPUS\tMEMORY\tPIDS\tWORKDIR MOUNT\tNETWORK")
		for _, container := range parity.Containers {
			fmt.Fprintf(writer, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				container.Client, container.CPUs, container.MemoryLimitBytes,
				container.PidsLimit, container.WorkingDirMount, container.NetworkMode)
		}
		writer.Flush()
		if parity.Equal {
			fmt.Fprintln(out, "  Every client container ran with the same CPU and memory ceiling.")
		} else {
			for _, finding := range parity.Findings {
				fmt.Fprintf(out, "  WARN %s\n", finding)
			}
			fmt.Fprintln(out, "  WARN the clients were not given equal machines; these figures compare the")
			fmt.Fprintln(out, "       containers as much as the clients.")
		}
		fmt.Fprintln(out)
	}
	for _, warning := range provenance.Warnings {
		fmt.Fprintf(out, "  WARN %s\n", warning)
	}
	if len(provenance.Warnings) > 0 {
		fmt.Fprintln(out)
	}
}

func renderSequentialReport(summary summaryReport, out io.Writer) error {
	printProvenanceHeader(out, summary.Provenance)
	fmt.Fprintf(out, "PAIRED COMPARISON: %s against %s\n\n", summary.Candidate, summary.Baseline)
	fmt.Fprintf(out, "  Metric %s. A ratio below 1 means %s spent less than %s.\n\n",
		summary.Metric, summary.Candidate, summary.Baseline)
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "  FIXTURE\tBLOCKS\tWALL\tCPU\tPEAK RSS\tDEVICE WRITES\tNOTE")
	for _, comparison := range summary.Comparisons {
		note := comparison.ComparisonWithheld
		if note == "" && (comparison.Completion.BaselineDidNotFinish > 0 || comparison.Completion.CandidateDidNotFinish > 0) {
			note = fmt.Sprintf("%s DNF %d, %s DNF %d", summary.Baseline, comparison.Completion.BaselineDidNotFinish,
				summary.Candidate, comparison.Completion.CandidateDidNotFinish)
		}
		fmt.Fprintf(writer, "  %s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			comparison.Stratum.FixtureID, comparison.Completion.PairedBlocks,
			ratioLabel(comparison.Summary), counterLabel(comparison.CPUTime),
			counterLabel(comparison.PeakRSS), counterLabel(comparison.DeviceWrites), note)
	}
	writer.Flush()
	fmt.Fprintln(out)
	if len(summary.Aggregates) > 0 {
		fmt.Fprintln(out, "  Pooled by fixture class, equal weight per fixture:")
		writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "  CLASS\tFIXTURES\tWALL\tNOTE")
		for _, aggregate := range summary.Aggregates {
			fmt.Fprintf(writer, "  %s\t%d\t%s\t%s\n", aggregate.Stratum.FixtureClass,
				len(aggregate.FixturesCompared), crossRatioLabel(aggregate.Summary), aggregate.AggregateWithheld)
		}
		writer.Flush()
		fmt.Fprintln(out)
	}
	printReportTail(out)
	return nil
}

func renderInterleavedReport(summary interleavedReport, out io.Writer) error {
	printProvenanceHeader(out, summary.Provenance)
	fmt.Fprintf(out, "REAL-PROVIDER LEG: %d interleaved passes, forward then reversed\n\n", summary.Passes)
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "  CLIENT\tFIXTURE\tARMS\tMEDIAN\tMIN\tMAX\tSPREAD\tNOTE")
	for _, client := range summary.Clients {
		note := client.MedianWithheld
		if note == "" && client.DidNotFinishArms > 0 {
			note = fmt.Sprintf("%d arms did not finish", client.DidNotFinishArms)
		}
		spread := ""
		if client.WallClockSpreadOverMedianPct != "" {
			spread = client.WallClockSpreadOverMedianPct + "%"
		}
		fmt.Fprintf(writer, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			client.Client, client.FixtureID, client.FinishedArms,
			secondsLabel(client.WallClockMedianNanoseconds), secondsLabel(client.WallClockMinNanoseconds),
			secondsLabel(client.WallClockMaxNanoseconds), spread, note)
	}
	writer.Flush()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Every arm, in the order it ran:")
	armWriter := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(armWriter, "  #\tPASS\tCLIENT\tSTARTED (LOCAL)\tWALL\tOUTCOME\tPAYLOAD\tHOST NIC\tNIC/PAYLOAD")
	for _, arm := range summary.Arms {
		nic := arm.NICUnavailable
		ratio := ""
		if nic == "" {
			nic = bytesLabel(arm.HostNICReceiveBytes)
			if arm.NICOverPayload > 0 {
				ratio = fmt.Sprintf("%.2fx", arm.NICOverPayload)
				if arm.Contamination != "" {
					ratio += " FLAGGED"
				}
			}
		}
		fmt.Fprintf(armWriter, "  %d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			arm.Order, arm.Pass, arm.Client, arm.StartedAtLocal,
			secondsLabel(arm.WallClockNanoseconds), arm.Outcome,
			bytesLabel(arm.NZBArticleBytes), nic, ratio)
	}
	armWriter.Flush()
	fmt.Fprintln(out)
	for _, warning := range summary.Warnings {
		fmt.Fprintf(out, "  WARN %s\n", warning)
	}
	if len(summary.Warnings) > 0 {
		fmt.Fprintln(out)
	}
	printReportTail(out)
	return nil
}

func renderQueueDrainReport(summary queueDrainReport, out io.Writer) error {
	printProvenanceHeader(out, summary.Provenance)
	fmt.Fprintln(out, "QUEUE DRAIN")
	fmt.Fprintf(out, "\n  Metric %s.\n\n", summary.Metric)
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "  CLIENT\tFIXTURE\tCOPIES\tDRAIN\tVERIFIED\tSTATUS\tNOTE")
	for _, lane := range summary.Lanes {
		note := lane.Error
		if note == "" && lane.CopiesDidNotFinish > 0 {
			note = fmt.Sprintf("%d copies did not finish", lane.CopiesDidNotFinish)
		}
		fmt.Fprintf(writer, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			lane.Client, lane.FixtureID, lane.Copies,
			secondsLabel(lane.QueueWallClockNanoseconds), secondsLabel(lane.VerifiedWallClockNanoseconds),
			lane.Status, note)
	}
	writer.Flush()
	fmt.Fprintln(out)
	printReportTail(out)
	return nil
}

// printReportTail prints the fixed text every report ends with: how the
// measurement was taken, why one client appears in one lane only, and what
// this harness knows it does not measure.
func printReportTail(out io.Writer) {
	fmt.Fprintln(out, reportMethod)
	fmt.Fprintln(out)
	fmt.Fprintln(out, reportNZBFastPlacement)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "KNOWN GAPS")
	fmt.Fprintln(out)
	gaps := append([]string(nil), reportKnownGaps...)
	sort.Strings(gaps)
	for _, gap := range gaps {
		fmt.Fprintf(out, "  - %s\n", wrapIndented(gap, 76, "    "))
	}
}

func ratioLabel(summary *benchmark.PairedSummary) string {
	if summary == nil {
		return "withheld"
	}
	return fmt.Sprintf("%.3f", summary.GeometricMeanRatio)
}

func crossRatioLabel(summary *benchmark.CrossFixtureSummary) string {
	if summary == nil {
		return "withheld"
	}
	return fmt.Sprintf("%.3f", summary.GeometricMeanRatio)
}

func counterLabel(comparison counterComparison) string {
	if comparison.Summary == nil {
		return "withheld"
	}
	return fmt.Sprintf("%.3f", comparison.Summary.GeometricMeanRatio)
}

func secondsLabel(nanoseconds int64) string {
	if nanoseconds <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1fs", float64(nanoseconds)/1e9)
}

func bytesLabel(value uint64) string {
	if value == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f GiB", float64(value)/(1<<30))
}

func bitRate(bitsPerSecond uint64) string {
	if bitsPerSecond == 0 {
		return "unshaped"
	}
	return fmt.Sprintf("%.0f Mbit/s", float64(bitsPerSecond)/1e6)
}

func memorySuffix(bytes uint64) string {
	if bytes == 0 {
		return ""
	}
	return fmt.Sprintf(", %.1f GiB RAM", float64(bytes)/(1<<30))
}

func plural(count int, noun string) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func countOrDash(value int) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprint(value)
}

func shortDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return "-"
	}
	if trimmed := strings.TrimPrefix(digest, "sha256:"); len(trimmed) > 16 {
		return "sha256:" + trimmed[:16]
	}
	return digest
}

// wrapIndented wraps a known-gap paragraph so a terminal reader gets whole
// sentences instead of one long line.
func wrapIndented(text string, width int, indent string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n"+indent)
}
