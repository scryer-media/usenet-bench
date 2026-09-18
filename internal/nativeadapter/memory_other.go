//go:build !windows

package nativeadapter

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// Resident memory for the natively launched client and everything it starts.
//
// There is no cgroup here and no way to ask this kernel for a tree's resident
// memory, so the tree is walked: every sample lists the process table once,
// rebuilds the parent-child edges below the client, and sums the resident set
// of each process in that subtree. The high point of that sum over the run is
// the reported figure. It is the same quantity the container lane samples out
// of the cgroup, which is the whole point -- the number has to mean one thing
// across the three hosts this suite runs on.
//
// One `ps` per sample is deliberate. Reading another process's resident set
// on macOS otherwise needs its task port, which needs either cgo or
// privileges this harness does not take, and the cost lands on a process that
// is not the client and is not being timed.
const (
	nativeMemorySampleInterval = 250 * time.Millisecond
	nativeMemoryScope          = "client_process_tree"
)

type processTreeMemory struct {
	rootPID int

	mu      sync.Mutex
	peak    uint64
	samples uint64
	failure string

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func newMemoryAccountant(_ cpuAccountant, process *os.Process) memoryAccountant {
	if process == nil || process.Pid < 1 {
		return unavailableMemoryAccount{reason: "native client process has no pid to account memory for"}
	}
	account := &processTreeMemory{
		rootPID: process.Pid,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	account.sample()
	go account.loop()
	return account
}

func (account *processTreeMemory) loop() {
	defer close(account.done)
	ticker := time.NewTicker(nativeMemorySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-account.stop:
			return
		case <-ticker.C:
			account.sample()
		}
	}
}

func (account *processTreeMemory) sample() {
	total, err := residentBytesBelow(account.rootPID)
	account.mu.Lock()
	defer account.mu.Unlock()
	if err != nil {
		if account.failure == "" {
			account.failure = err.Error()
		}
		return
	}
	account.samples++
	if total > account.peak {
		account.peak = total
	}
}

// residentBytesBelow sums the resident set of the process and every
// descendant of it that the process table shows right now.
func residentBytesBelow(root int) (uint64, error) {
	command := exec.Command("ps", "-axo", "pid=,ppid=,rss=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return 0, fmt.Errorf("list processes for resident memory: %w", err)
	}
	type entry struct {
		parent int
		rss    uint64
	}
	table := make(map[int]entry)
	children := make(map[int][]int)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		kilobytes, rssErr := strconv.ParseUint(fields[2], 10, 64)
		if pidErr != nil || parentErr != nil || rssErr != nil {
			continue
		}
		table[pid] = entry{parent: parent, rss: kilobytes * 1024}
		children[parent] = append(children[parent], pid)
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read process table: %w", err)
	}
	if _, live := table[root]; !live {
		// The client is gone. That is an ordinary end-of-run state, not a
		// collection failure, and it contributes nothing to the peak.
		return 0, nil
	}
	var total uint64
	// Breadth-first from the client. A pid is visited once, so a recycled pid
	// pointing back up the tree cannot loop.
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		total += table[pid].rss
		for _, child := range children[pid] {
			if !seen[child] {
				seen[child] = true
				queue = append(queue, child)
			}
		}
	}
	return total, nil
}

func (account *processTreeMemory) measurement(state *os.ProcessState) (benchmark.CounterMeasurement, benchmark.CounterMeasurement) {
	account.stopOnce.Do(func() { close(account.stop) })
	<-account.done
	account.mu.Lock()
	peak, samples, failure := account.peak, account.samples, account.failure
	account.mu.Unlock()

	collector := "ps-process-tree-sampled"
	version := fmt.Sprintf("%s@%dms", runtime.GOOS, nativeMemorySampleInterval.Milliseconds())
	sampled := benchmark.MeasuredMeasurement(nativeMemoryScope, collector, version, peak)
	switch {
	case samples == 0:
		reason := failure
		if reason == "" {
			reason = "no process-tree memory sample was taken"
		}
		sampled = benchmark.UnavailableMeasurement(nativeMemoryScope, collector, version, reason)
	case peak == 0:
		sampled = benchmark.UnavailableMeasurement(nativeMemoryScope, collector, version,
			"the client process tree was never observed holding resident memory")
	}
	return sampled, maxRSSHint(state)
}

// maxRSSHint reports what wait charged the client. It is the largest single
// process in the reaped tree rather than their sum, and the kernel's units
// differ by platform, so it is reported as a hint beside the sampled figure
// and never as a substitute for it.
func maxRSSHint(state *os.ProcessState) benchmark.CounterMeasurement {
	const collector = "getrusage-ru_maxrss"
	version := runtime.GOOS
	if state == nil {
		return benchmark.UnavailableMeasurement("client_process", collector, version, "native client process did not exit before memory accounting")
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return benchmark.UnavailableMeasurement("client_process", collector, version, "the platform did not report rusage for the client process")
	}
	if usage.Maxrss < 0 {
		return benchmark.UnavailableMeasurement("client_process", collector, version, "ru_maxrss was negative")
	}
	bytes := uint64(usage.Maxrss)
	// Darwin reports ru_maxrss in bytes; Linux and the BSDs report kilobytes.
	if runtime.GOOS != "darwin" {
		if bytes > ^uint64(0)/1024 {
			return benchmark.UnavailableMeasurement("client_process", collector, version, "ru_maxrss overflows bytes")
		}
		bytes *= 1024
	}
	return benchmark.MeasuredMeasurement("client_process", collector, version, bytes)
}

func (account *processTreeMemory) close() {
	account.stopOnce.Do(func() { close(account.stop) })
	<-account.done
}
