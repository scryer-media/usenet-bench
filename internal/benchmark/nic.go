package benchmark

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The host NIC receive counter is the independent witness for a lane with no
// shaper. Every shaped lane can say what it delivered, because the shaper
// counted it; a real-provider run has nothing between the client and the
// internet, so the only check on "the client says it downloaded N bytes" is
// what the machine's interfaces actually received while that client, and
// nothing else, was running. A delta far above the payload means the arm was
// measured over somebody else's traffic -- a backup, an update, a second
// tenant -- and the arm is worth less than its wall clock suggests.
//
// It is a whole-host counter, so it is an upper bound on one client's
// traffic and never a substitute for the client's own figure. Both are
// reported; neither is derived from the other.

// NICExcessTolerance is how far the host's received bytes may exceed the
// payload before an arm is flagged. Protocol overhead alone puts the NIC a
// few percent above application bytes -- NNTP command traffic, TLS records,
// TCP and IP headers, ACKs on the other direction's segments -- so a small
// excess is expected and a large one is contamination.
const NICExcessTolerance = 0.05

// NICReceiveDelta is the host's received byte count across one measured arm.
type NICReceiveDelta struct {
	// Available is false when the platform has no counter this harness reads;
	// Unavailable then says so and the byte fields mean nothing.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`

	Collector   string    `json:"collector,omitempty"`
	BeforeBytes uint64    `json:"before_bytes,omitempty"`
	AfterBytes  uint64    `json:"after_bytes,omitempty"`
	DeltaBytes  uint64    `json:"delta_bytes,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
}

// ExceedsPayload reports whether the host received more than the tolerance
// above the payload the arm was supposed to move, and by what factor. It
// answers false when either side of the comparison is missing: an absent
// counter is not evidence of a clean arm.
func (d NICReceiveDelta) ExceedsPayload(payloadBytes uint64) (float64, bool) {
	if !d.Available || payloadBytes == 0 || d.DeltaBytes == 0 {
		return 0, false
	}
	ratio := float64(d.DeltaBytes) / float64(payloadBytes)
	return ratio, ratio > 1+NICExcessTolerance
}

// NICReceiveCounter is one reading of the host's total received bytes.
type NICReceiveCounter struct {
	Bytes     uint64
	Collector string
}

// ReadHostNICReceiveBytes sums received bytes over the host's interfaces.
// Loopback is excluded: a client talking to a local shaper would otherwise be
// counted twice and a real provider's bytes would be diluted by whatever else
// uses the loopback.
func ReadHostNICReceiveBytes(ctx context.Context) (NICReceiveCounter, error) {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/net/dev")
		if err != nil {
			return NICReceiveCounter{}, fmt.Errorf("read /proc/net/dev: %w", err)
		}
		total, err := parseProcNetDevReceiveBytes(string(raw))
		if err != nil {
			return NICReceiveCounter{}, err
		}
		return NICReceiveCounter{Bytes: total, Collector: "proc-net-dev"}, nil
	case "darwin":
		output, err := runHostCommand(ctx, "netstat", "-ibn")
		if err != nil {
			return NICReceiveCounter{}, fmt.Errorf("read netstat -ibn: %w", err)
		}
		total, err := parseNetstatReceiveBytes(output)
		if err != nil {
			return NICReceiveCounter{}, err
		}
		return NICReceiveCounter{Bytes: total, Collector: "netstat-ibn"}, nil
	default:
		return NICReceiveCounter{}, fmt.Errorf("host NIC receive counters are not collected on %s", runtime.GOOS)
	}
}

func runHostCommand(ctx context.Context, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// parseProcNetDevReceiveBytes sums the receive-bytes column of every
// non-loopback interface.
func parseProcNetDevReceiveBytes(contents string) (uint64, error) {
	var total uint64
	counted := 0
	for _, line := range strings.Split(contents, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" || strings.HasPrefix(name, "lo:") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 1 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		total += value
		counted++
	}
	if counted == 0 {
		return 0, fmt.Errorf("/proc/net/dev named no non-loopback interface")
	}
	return total, nil
}

// parseNetstatReceiveBytes sums macOS `netstat -ibn` Ibytes over one row per
// interface. netstat prints a row per address family, all carrying the same
// per-interface totals, so only the first row of each interface is counted.
func parseNetstatReceiveBytes(contents string) (uint64, error) {
	var header []string
	seen := make(map[string]bool)
	var total uint64
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Name" {
			header = fields
			continue
		}
		if header == nil {
			continue
		}
		column := -1
		for index, name := range header {
			if name == "Ibytes" {
				column = index
				break
			}
		}
		if column < 0 {
			return 0, fmt.Errorf("netstat output has no Ibytes column")
		}
		name := strings.TrimSuffix(fields[0], "*")
		if name == "lo0" || strings.HasPrefix(name, "lo") || seen[name] {
			seen[name] = true
			continue
		}
		if column >= len(fields) {
			continue
		}
		value, err := strconv.ParseUint(fields[column], 10, 64)
		if err != nil {
			continue
		}
		seen[name] = true
		total += value
	}
	if len(seen) == 0 {
		return 0, fmt.Errorf("netstat named no interface")
	}
	return total, nil
}

// MeasureNICReceive brackets a measured arm. The returned function is called
// once the arm has ended; it reads the counter again and returns the delta,
// or an unavailable record with the reason.
func MeasureNICReceive(ctx context.Context) func(context.Context) NICReceiveDelta {
	before, err := ReadHostNICReceiveBytes(ctx)
	startedAt := time.Now().UTC()
	if err != nil {
		reason := err.Error()
		return func(context.Context) NICReceiveDelta {
			return NICReceiveDelta{Unavailable: reason}
		}
	}
	return func(endCtx context.Context) NICReceiveDelta {
		after, afterErr := ReadHostNICReceiveBytes(endCtx)
		if afterErr != nil {
			return NICReceiveDelta{Unavailable: afterErr.Error()}
		}
		if after.Collector != before.Collector || after.Bytes < before.Bytes {
			// A counter that moved backwards wrapped or was reset; a delta
			// taken across that is a fabrication.
			return NICReceiveDelta{Unavailable: "host NIC receive counter moved backwards or changed collector during the arm"}
		}
		return NICReceiveDelta{
			Available:   true,
			Collector:   before.Collector,
			BeforeBytes: before.Bytes,
			AfterBytes:  after.Bytes,
			DeltaBytes:  after.Bytes - before.Bytes,
			StartedAt:   startedAt,
			EndedAt:     time.Now().UTC(),
		}
	}
}
