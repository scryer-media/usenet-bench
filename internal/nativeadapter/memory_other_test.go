//go:build !windows

package nativeadapter

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tree walk is the whole measurement on this lane, so it is exercised
// against a real process table rather than a fixture: this test process is
// resident, so a walk rooted at it must find memory.
func TestResidentBytesBelowFindsThisProcess(t *testing.T) {
	total, err := residentBytesBelow(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("the running test process was reported as holding no resident memory")
	}
}

// A client that has already exited is an ordinary end-of-run state. It must
// contribute nothing rather than fail the collection, or a run whose client
// exits between two samples would lose the peak it had already recorded.
func TestResidentBytesBelowTreatsADeadRootAsNothing(t *testing.T) {
	command := exec.Command("sleep", "30")
	configureNativeProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	if err := killNativeProcessTree(command.Process); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	total, err := residentBytesBelow(pid)
	if err != nil {
		t.Fatalf("a departed root must not be a collection failure: %v", err)
	}
	if total != 0 {
		t.Fatalf("a departed root reported %d resident bytes", total)
	}
}

// A child counts. This is the requirement the whole tree walk exists for: the
// unpackers SABnzbd and NZBGet shell out to are separate processes, and a
// figure that missed them would understate exactly the clients that delegate
// the most work.
func TestResidentBytesBelowCountsAChild(t *testing.T) {
	// A shell that holds a child of its own, so the walk has two levels to find
	// rather than one. It is launched the way the adapter launches a client, in
	// its own process group, so the group kill below cannot reach the test
	// runner.
	command := exec.Command("sh", "-c", "sleep 30 & wait")
	configureNativeProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = killNativeProcessTree(command.Process)
		_ = command.Wait()
	}()

	root := command.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if children, err := childrenOf(root); err != nil {
			t.Fatal(err)
		} else if len(children) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	children, err := childrenOf(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) == 0 {
		t.Skip("the shell never forked a visible child on this host")
	}

	total, err := residentBytesBelow(root)
	if err != nil {
		t.Fatal(err)
	}
	alone, err := residentBytesOf(root)
	if err != nil {
		t.Fatal(err)
	}
	if alone == 0 {
		t.Fatal("the shell itself was reported as holding no resident memory")
	}
	// The only way the subtree total can exceed the root's own resident set is
	// if the descendant was walked and added.
	if total <= alone {
		t.Fatalf("a tree of %d processes summed to %d bytes, no more than the root's own %d", len(children)+1, total, alone)
	}
}

// childrenOf reports the pids the process table currently shows as direct
// children of root.
func childrenOf(root int) ([]int, error) {
	rows, err := processTableRows("pid=,ppid=")
	if err != nil {
		return nil, err
	}
	var children []int
	for _, row := range rows {
		if len(row) != 2 {
			continue
		}
		if row[1] == root {
			children = append(children, row[0])
		}
	}
	return children, nil
}

// residentBytesOf reports one process's own resident set, with no descendants.
func residentBytesOf(pid int) (uint64, error) {
	rows, err := processTableRows("pid=,rss=")
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if len(row) == 2 && row[0] == pid {
			return uint64(row[1]) * 1024, nil
		}
	}
	return 0, nil
}

func processTableRows(format string) ([][]int, error) {
	command := exec.Command("ps", "-axo", format)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var rows [][]int
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		row := make([]int, 0, len(fields))
		valid := true
		for _, field := range fields {
			value, err := strconv.Atoi(field)
			if err != nil {
				valid = false
				break
			}
			row = append(row, value)
		}
		if valid {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
