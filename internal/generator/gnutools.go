package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// GNUToolsToolchain pins the writers that come from a distribution rather
// than from an upstream release tarball: GNU tar, gzip, xz-utils, bzip2,
// Info-ZIP zip/unzip and cksfv.
//
// The other writers in this corpus pin a URL and a SHA-256, because RARLAB and
// 7-Zip publish one. These do not: their upstream is a distribution package.
// The pin is therefore the digest-pinned Debian base every writer image is
// built on, plus the exact package version each tool resolves to, declared
// here and checked against the built image before a single fixture byte is
// written. A Debian point release that moves any of them stops the corpus
// build with the old and new versions named, instead of quietly writing
// fixtures whose manifest claims a writer that did not produce them.
type GNUToolsToolchain struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	// BaseImage is the digest-pinned Debian the tools are installed into. It
	// is the whole of the supply-chain pin for this image, so it is recorded
	// rather than left implicit in the Dockerfile.
	BaseImage string            `json:"base_image"`
	Packages  map[string]string `json:"packages"`
}

// gnuToolsRequiredPackages is every tool the generator invokes from this
// image. The toolchain file must declare a version for each, so a missing
// entry is an authoring error rather than an unpinned writer.
var gnuToolsRequiredPackages = []string{"bzip2", "cksfv", "gzip", "tar", "unzip", "xz-utils", "zip"}

func LoadGNUToolsToolchain(path string) (GNUToolsToolchain, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return GNUToolsToolchain{}, fmt.Errorf("read GNU tools toolchain %s: %w", path, err)
	}
	var toolchain GNUToolsToolchain
	if err := json.Unmarshal(contents, &toolchain); err != nil {
		return GNUToolsToolchain{}, fmt.Errorf("decode GNU tools toolchain %s: %w", path, err)
	}
	if err := toolchain.Validate(); err != nil {
		return GNUToolsToolchain{}, err
	}
	return toolchain, nil
}

func (t GNUToolsToolchain) Validate() error {
	if t.SchemaVersion != 1 {
		return fmt.Errorf("GNU tools toolchain %q has unsupported schema version %d", t.ID, t.SchemaVersion)
	}
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Image) == "" || strings.TrimSpace(t.Platform) == "" {
		return fmt.Errorf("GNU tools toolchain must include id, image, and platform")
	}
	if !strings.Contains(t.BaseImage, "@sha256:") {
		return fmt.Errorf("GNU tools toolchain %q must pin its base image by digest, got %q", t.ID, t.BaseImage)
	}
	for _, name := range gnuToolsRequiredPackages {
		if strings.TrimSpace(t.Packages[name]) == "" {
			return fmt.Errorf("GNU tools toolchain %q declares no version for %s", t.ID, name)
		}
	}
	return nil
}

// ManifestID records the image and the exact package versions inside it, so a
// fixture manifest names the writer that produced its bytes as precisely as
// the RAR and 7-Zip lanes do.
func (t GNUToolsToolchain) ManifestID() fixture.ToolchainID {
	packages := make(map[string]string, len(t.Packages))
	for name, version := range t.Packages {
		packages[name] = version
	}
	return fixture.ToolchainID{
		ID:       t.ID,
		Image:    t.Image,
		Platform: t.Platform,
		// There is no single upstream URL or tarball hash for a set of
		// distribution packages; the digest-pinned base plus the versions
		// below are the pin, and they are what the generator checks.
		Version:  t.BaseImage,
		Packages: packages,
	}
}

func selectedCasesRequireGNUTools(cases []fixture.ArchiveCase, selected map[string]bool) bool {
	for _, archiveCase := range cases {
		if len(selected) > 0 && !selected[archiveCase.ID] {
			continue
		}
		if caseUsesGNUTools(archiveCase) {
			return true
		}
	}
	return false
}

// caseUsesGNUTools reports whether generating this fixture invokes the
// distribution writers at all — as the container writer, as the writer of an
// inner archive, or as the checker of a checksum sidecar.
func caseUsesGNUTools(archiveCase fixture.ArchiveCase) bool {
	switch archiveCase.ArchiveFormat {
	case fixture.Tar, fixture.XZCompressed:
		return true
	case fixture.Zip:
		// A zip written by 7-Zip is still read back by Info-ZIP's unzip: the
		// point of the two-writer lane is that neither writer marks its own
		// homework.
		return true
	}
	return archiveCase.InnerArchive != fixture.NoInnerArchive || archiveCase.Sidecar != fixture.NoSidecar
}

func buildGNUToolsImage(ctx context.Context, config Config, toolchain GNUToolsToolchain) error {
	args := []string{
		"build", "--platform", toolchain.Platform,
		"--tag", toolchain.Image,
		"--file", config.GNUToolsDockerfilePath,
		filepath.Dir(config.GNUToolsDockerfilePath),
	}
	if err := runCommand(ctx, config.DockerBinary, args...); err != nil {
		return fmt.Errorf("build GNU tools image %s: %w", toolchain.ID, err)
	}
	return nil
}

// verifyGNUToolsPackages reads the versions recorded inside the built image
// and refuses to generate anything if they differ from the toolchain file.
// This is the whole of the pin for these writers, so it fails closed.
func verifyGNUToolsPackages(ctx context.Context, config Config, toolchain GNUToolsToolchain) error {
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		toolchain.Image, "versions",
	}
	output, err := exec.CommandContext(ctx, config.DockerBinary, args...).Output()
	if err != nil {
		return fmt.Errorf("read package versions from %s: %w", toolchain.Image, err)
	}
	installed := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			installed[fields[0]] = fields[1]
		}
	}
	var drift []string
	for _, name := range gnuToolsRequiredPackages {
		if installed[name] != toolchain.Packages[name] {
			drift = append(drift, fmt.Sprintf("%s: image has %q, %s declares %q",
				name, installed[name], config.GNUToolsToolchainPath, toolchain.Packages[name]))
		}
	}
	if len(drift) > 0 {
		sort.Strings(drift)
		return fmt.Errorf(
			"the %s image does not contain the writers it is pinned to; Debian has moved these packages, so update %s (and regenerate every tar, xz, zip and SFV fixture, because the writer changed):\n  %s",
			toolchain.ID, config.GNUToolsToolchainPath, strings.Join(drift, "\n  "))
	}
	return nil
}

// gnuToolsDockerArgs builds the docker invocation for one tool from the pinned
// image with workdir set inside the mounted case directory.
func gnuToolsDockerArgs(toolchain GNUToolsToolchain, caseDir, workdir string, toolArgs ...string) []string {
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		"--user", callerDockerUser(),
		"--mount", "type=bind,src=" + caseDir + ",dst=/work",
		"--workdir", "/work/" + filepath.ToSlash(workdir),
		toolchain.Image,
	}
	return append(args, toolArgs...)
}

// runGNUTools invokes one tool from the pinned image with workdir set inside
// the mounted case directory.
func runGNUTools(ctx context.Context, config Config, toolchain GNUToolsToolchain, caseDir, workdir string, toolArgs ...string) error {
	return runCommand(ctx, config.DockerBinary, gnuToolsDockerArgs(toolchain, caseDir, workdir, toolArgs...)...)
}

// runGNUToolsToFile is runGNUTools with the tool's standard output captured
// into a file on the host. The streamed zip lane needs it: the point of that
// lane is an archive the writer produced without ever seeking back, so the
// archive has to arrive down a pipe. The redirection is done here rather than
// inside the container because the pinned image has no shell — its entrypoint
// dispatches straight to the requested tool, which is what keeps the recorded
// package versions the whole story of how a fixture was written.
func runGNUToolsToFile(ctx context.Context, config Config, toolchain GNUToolsToolchain, caseDir, workdir, outputPath string, toolArgs ...string) error {
	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, config.DockerBinary, gnuToolsDockerArgs(toolchain, caseDir, workdir, toolArgs...)...)
	cmd.Stdout = output
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := output.Close()
	if runErr != nil {
		os.Remove(outputPath)
		return fmt.Errorf("%s %s: %w\n%s", config.DockerBinary, strings.Join(toolArgs, " "), runErr, strings.TrimSpace(stderr.String()))
	}
	if closeErr != nil {
		os.Remove(outputPath)
		return closeErr
	}
	return nil
}
