// Package buildinfo reports the harness build a result came from. A published
// number is only auditable against the code that produced it, so every report
// carries the commit rather than leaving a reader to guess which checkout was
// on the bench host that week.
package buildinfo

import (
	"runtime/debug"
	"strings"
	"sync"
)

// Commit is stamped at link time:
//
//	go build -ldflags "-X github.com/scryer-media/usenet-bench/internal/buildinfo.Commit=$(git describe --always --dirty)"
//
// It is left empty in an ordinary `go build`, where the Go tool's own VCS
// stamping answers instead.
var Commit string

// UnknownCommit is what a report records when neither the linker stamp nor the
// Go tool's VCS stamp is present. It is never left blank: a blank field reads
// like an omission, and this one is a stated fact about the build.
const UnknownCommit = "unknown"

// UnknownCommitWarning explains an unknown commit in the report itself, so a
// reviewer is told that the build is unidentified rather than discovering it.
const UnknownCommitWarning = "harness commit is unknown: the binary carries neither an -X buildinfo.Commit stamp nor Go VCS build information (a build from an archive, or with -buildvcs=false)"

var (
	once     sync.Once
	resolved string
	known    bool
)

// HarnessCommit returns the harness revision and whether it is known. An
// unknown build returns UnknownCommit and false.
func HarnessCommit() (string, bool) {
	once.Do(func() {
		resolved, known = resolveCommit()
	})
	return resolved, known
}

func resolveCommit() (string, bool) {
	if value := strings.TrimSpace(Commit); value != "" {
		return value, true
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return UnknownCommit, false
	}
	revision, modified := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return UnknownCommit, false
	}
	if modified {
		// A dirty tree is a different build from the commit it sits on, and
		// saying so is the whole point of recording the commit.
		return revision + "-dirty", true
	}
	return revision, true
}
