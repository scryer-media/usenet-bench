package generator

import (
	"context"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/scryer-media/usenet-bench/internal/uucodec"
	"github.com/zeebo/blake3"
)

// writerToolchains is every pinned writer a generation run may need. The RAR
// image is always present because it renders the payload; the rest are loaded
// only when the selected cases use them, so a run over the RAR lanes alone
// still needs nothing else installed.
type writerToolchains struct {
	RAR      Toolchain
	PAR2     *PAR2Toolchain
	SevenZip *SevenZipToolchain
	GNUTools *GNUToolsToolchain
	UUCodec  *uucodec.Toolchain
}

const (
	tarArchiveBaseName = "fixture.tar"
	zipArchiveName     = "fixture.zip"
	sfvSidecarName     = "fixture.sfv"
)

// writerKind says which pinned image writes a fixture's container. It is
// derived from the toolchain the matrix names rather than from the format,
// because zip has two upstream writers in common use and the corpus writes it
// with both.
type writerKind int

const (
	writerRARLAB writerKind = iota
	writerSevenZip
	writerGNUTools
	// writerCopy is the container-less media lane: the payload files are the
	// posted files, so nothing writes a container at all.
	writerCopy
)

func resolveWriterKind(archiveCase fixture.ArchiveCase, writers writerToolchains) (writerKind, error) {
	switch archiveCase.ArchiveFormat {
	case fixture.RAR4, fixture.RAR5:
		return writerRARLAB, nil
	case fixture.SevenZip:
		return writerSevenZip, nil
	case fixture.Tar, fixture.XZCompressed:
		return writerGNUTools, nil
	case fixture.Media:
		return writerCopy, nil
	case fixture.Zip:
		if writers.SevenZip != nil && archiveCase.ArchiveWriter == writers.SevenZip.ID {
			return writerSevenZip, nil
		}
		if writers.GNUTools != nil && archiveCase.ArchiveWriter == writers.GNUTools.ID {
			return writerGNUTools, nil
		}
		return 0, fmt.Errorf("fixture %q names archive_writer %q, which is neither the pinned 7-Zip nor the pinned Info-ZIP toolchain", archiveCase.ID, archiveCase.ArchiveWriter)
	default:
		return 0, fmt.Errorf("fixture %q has unsupported archive_format %q", archiveCase.ID, archiveCase.ArchiveFormat)
	}
}

// requiredWriterToolchain names the pinned image that produced the container,
// for the manifest's archive_writer_toolchain field.
func archiveWriterManifestID(archiveCase fixture.ArchiveCase, writers writerToolchains) (fixture.ToolchainID, error) {
	kind, err := resolveWriterKind(archiveCase, writers)
	if err != nil {
		return fixture.ToolchainID{}, err
	}
	switch kind {
	case writerSevenZip:
		if writers.SevenZip == nil {
			return fixture.ToolchainID{}, fmt.Errorf("fixture %q requires the pinned 7-Zip toolchain", archiveCase.ID)
		}
		return writers.SevenZip.ManifestID(), nil
	case writerGNUTools:
		if writers.GNUTools == nil {
			return fixture.ToolchainID{}, fmt.Errorf("fixture %q requires the pinned GNU/Info-ZIP toolchain", archiveCase.ID)
		}
		return writers.GNUTools.ManifestID(), nil
	case writerCopy:
		// Nothing rewrote the payload, so the writer of the posted bytes is
		// the image that rendered them.
		return writers.RAR.ManifestID(), nil
	default:
		return writers.RAR.ManifestID(), nil
	}
}

// createArchive writes the fixture's posted container from the staged payload.
// inputs are case-relative staging paths under input/.
func createArchive(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir string, inputs []string) error {
	kind, err := resolveWriterKind(archiveCase, writers)
	if err != nil {
		return err
	}
	relative, err := inputRelativePaths(inputs)
	if err != nil {
		return fmt.Errorf("create fixture %q: %w", archiveCase.ID, err)
	}
	switch {
	case kind == writerRARLAB:
		args, err := archiveCase.RARArgs(filepath.ToSlash(filepath.Join("archive", "fixture.rar")), inputs)
		if err != nil {
			return err
		}
		return runRAR(ctx, config.DockerBinary, writers.RAR, caseDir, args...)
	case kind == writerSevenZip && archiveCase.ArchiveFormat == fixture.SevenZip:
		return createSevenZipArchive(ctx, config, archiveCase, *writers.SevenZip, caseDir, inputs)
	case kind == writerSevenZip && archiveCase.ArchiveFormat == fixture.Zip:
		args, err := archiveCase.SevenZipArgs("../archive/"+zipArchiveName, relative)
		if err != nil {
			return err
		}
		return runSevenZip(ctx, config, *writers.SevenZip, caseDir, "input", args...)
	case kind == writerGNUTools && archiveCase.ArchiveFormat == fixture.Tar:
		name, err := archiveCase.TarFileName()
		if err != nil {
			return err
		}
		args, err := archiveCase.TarArgs(filepath.ToSlash(filepath.Join("archive", name)), "input", relative)
		if err != nil {
			return err
		}
		return runTarCreate(ctx, config, *writers.GNUTools, caseDir, ".", append([]string{"tar"}, args...)...)
	case kind == writerGNUTools && archiveCase.ArchiveFormat == fixture.Zip:
		args, err := archiveCase.ZipArgs("../archive/"+zipArchiveName, relative)
		if err != nil {
			return err
		}
		if archiveCase.WritesToStandardOutput() {
			// ZipArgs has already named "-" as the archive, so the writer is
			// producing a stream it cannot seek back into. Capturing that
			// stream on the host is what makes the members carry data
			// descriptors instead of patched-up local headers.
			return runGNUToolsToFile(ctx, config, *writers.GNUTools, caseDir, "input",
				filepath.Join(caseDir, "archive", zipArchiveName),
				append([]string{"zip"}, args...)...)
		}
		return runGNUTools(ctx, config, *writers.GNUTools, caseDir, "input", append([]string{"zip"}, args...)...)
	case kind == writerGNUTools && archiveCase.ArchiveFormat == fixture.XZCompressed:
		if len(relative) != 1 {
			return fmt.Errorf("fixture %q compresses %d files with xz; a bare xz stream holds exactly one", archiveCase.ID, len(relative))
		}
		// xz replaces the file it is handed, so the payload is staged into the
		// archive directory and the compressor renames it to .xz there.
		if err := copyFile(filepath.Join(caseDir, "input", filepath.FromSlash(relative[0])), filepath.Join(caseDir, "archive", filepath.FromSlash(relative[0]))); err != nil {
			return err
		}
		args, err := archiveCase.XZArgs(relative[0])
		if err != nil {
			return err
		}
		return runGNUTools(ctx, config, *writers.GNUTools, caseDir, "archive", append([]string{"xz"}, args...)...)
	case kind == writerCopy:
		for _, name := range relative {
			if err := copyFile(filepath.Join(caseDir, "input", filepath.FromSlash(name)), filepath.Join(caseDir, "archive", filepath.FromSlash(name))); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("fixture %q has no writer for archive_format %q", archiveCase.ID, archiveCase.ArchiveFormat)
	}
}

// verifyArchive proves the posted container holds the payload, by reading it
// back with a pinned reader and checking every member against its BLAKE3
// digest. Only the RAR lanes take the writer's own `t` verdict; it is
// RARLAB's own integrity test over its own container, which is the strongest
// statement available for that format without a second implementation.
func verifyArchive(
	ctx context.Context,
	config Config,
	archiveCase fixture.ArchiveCase,
	writers writerToolchains,
	caseDir, primary string,
	expected []fixture.FileDigest,
) error {
	if archiveCase.InnerArchive != fixture.NoInnerArchive {
		return verifyWrappedArchive(ctx, config, archiveCase, writers, caseDir, primary, expected)
	}
	kind, err := resolveWriterKind(archiveCase, writers)
	if err != nil {
		return err
	}
	switch archiveCase.ArchiveFormat {
	case fixture.RAR4, fixture.RAR5:
		testArgs := []string{"t", "-idq", "-y"}
		if archiveCase.RequiresPassword() {
			testArgs = append(testArgs, "-p"+fixture.FixturePassword)
		}
		testArgs = append(testArgs, filepath.ToSlash(primary))
		return runRAR(ctx, config.DockerBinary, writers.RAR, caseDir, testArgs...)
	case fixture.SevenZip:
		return verifySevenZipArchive(ctx, config, archiveCase, *writers.SevenZip, caseDir, primary, expected)
	case fixture.Tar:
		return verifyTarArchive(ctx, config, archiveCase, *writers.GNUTools, caseDir, primary, expected)
	case fixture.XZCompressed:
		return verifyXZArchive(ctx, config, *writers.GNUTools, caseDir, primary, expected)
	case fixture.Zip:
		return verifyZipArchive(ctx, config, archiveCase, writers, kind, caseDir, expected)
	case fixture.Media:
		return verifyMediaFiles(ctx, config, archiveCase, writers, caseDir, expected)
	default:
		return fmt.Errorf("fixture %q has no verifier for archive_format %q", archiveCase.ID, archiveCase.ArchiveFormat)
	}
}

// verifyTarArchive extracts with GNU tar, which also runs the gzip and xz
// decompressors for the filtered lanes.
func verifyTarArchive(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, toolchain GNUToolsToolchain, caseDir, primary string, expected []fixture.FileDigest) error {
	extractDir, relative, err := newVerificationDir(caseDir, "tar-verification-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	args := []string{"tar", "--extract", "--file", filepath.ToSlash(primary), "--directory", filepath.ToSlash(relative)}
	if err := runGNUTools(ctx, config, toolchain, caseDir, ".", args...); err != nil {
		return fmt.Errorf("extract %s: %w", primary, err)
	}
	return checkExtractedFiles(extractDir, expected, "tar")
}

// verifyXZArchive decompresses to a pipe and hashes the stream, so a
// multi-hundred-megabyte member never lands on disk twice.
func verifyXZArchive(ctx context.Context, config Config, toolchain GNUToolsToolchain, caseDir, primary string, expected []fixture.FileDigest) error {
	if len(expected) != 1 {
		return fmt.Errorf("a bare xz stream holds one file, but the fixture expects %d", len(expected))
	}
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		"--user", callerDockerUser(),
		"--mount", "type=bind,src=" + caseDir + ",dst=/work",
		"--workdir", "/work",
		toolchain.Image,
		"xz", "--decompress", "--stdout", "--threads=1", filepath.ToSlash(primary),
	}
	digest, size, err := hashCommandOutput(ctx, config.DockerBinary, args...)
	if err != nil {
		return fmt.Errorf("decompress %s: %w", primary, err)
	}
	if size != expected[0].Size || digest != expected[0].BLAKE3 {
		return fmt.Errorf("the pinned xz decompressor recovered %d bytes from %s, which do not match the payload oracle for %s", size, primary, expected[0].Path)
	}
	return nil
}

// verifyZipArchive reads the container back and checks every member against
// the payload oracle. Which reader does that is decided per lane, because
// neither of the two zip readers can open all of them:
//
//   - Info-ZIP's unzip is the default. For a 7-Zip-written zip it is an
//     independent implementation; for an Info-ZIP-written one it is the
//     writer's own reader, exactly as RARLAB's `t` is for the RAR lanes.
//   - An AES-encrypted zip is read by the pinned 7-Zip. Info-ZIP 6.0 has no
//     AES support at all, so there is no second implementation to use.
//   - A spanned zip is read by the pinned 7-Zip, which opens the .z01/.zip
//     set directly. Info-ZIP can only get at a split archive by rejoining it
//     with `zip -s-`, and that prompts for the location of every disk past
//     the first, so it cannot run unattended — the pinned 7-Zip is both the
//     independent reader and the only one that works here.
func verifyZipArchive(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, kind writerKind, caseDir string, expected []fixture.FileDigest) error {
	primary := filepath.ToSlash(filepath.Join("archive", zipArchiveName))
	spanned := strings.TrimSpace(archiveCase.VolumeSize) != ""
	if spanned || archiveCase.Encryption == fixture.DataEncryption {
		if writers.SevenZip == nil {
			return fmt.Errorf("fixture %q requires the pinned 7-Zip toolchain to read its zip back", archiveCase.ID)
		}
		return verifySevenZipArchive(ctx, config, archiveCase, *writers.SevenZip, caseDir, primary, expected)
	}
	if archiveCase.ZipStructure != fixture.ClassicZipStructure {
		return verifyZipStructureLane(ctx, config, archiveCase, writers, caseDir, primary, expected)
	}
	return verifyZipWithInfoZip(ctx, config, archiveCase, *writers.GNUTools, caseDir, primary, expected)
}

// verifyZipStructureLane reads a zip64 or streamed lane back with both pinned
// readers. These lanes exist because a client's zip parser may take a
// different path through them, so a single reader agreeing with the writer
// proves less here than it does elsewhere: two independent implementations
// have to recover the same bytes before the fixture is allowed to exist.
func verifyZipStructureLane(
	ctx context.Context,
	config Config,
	archiveCase fixture.ArchiveCase,
	writers writerToolchains,
	caseDir, primary string,
	expected []fixture.FileDigest,
) error {
	if writers.SevenZip == nil || writers.GNUTools == nil {
		return fmt.Errorf("fixture %q needs both pinned zip readers to verify its %q structure", archiveCase.ID, archiveCase.ZipStructure)
	}
	if archiveCase.ZipStructure == fixture.LargeZip64Structure {
		return verifyZipByStreaming(ctx, config, archiveCase, writers, caseDir, primary, expected)
	}
	if err := verifySevenZipArchive(ctx, config, archiveCase, *writers.SevenZip, caseDir, primary, expected); err != nil {
		return err
	}
	return verifyZipWithInfoZip(ctx, config, archiveCase, *writers.GNUTools, caseDir, primary, expected)
}

// verifyZipByStreaming pipes the single member through each pinned reader and
// hashes the stream. The zip64 large lane holds one member past 4 GiB, and
// extracting it twice onto disk would cost more than eight gigabytes of
// scratch on top of the payload and the archive already there.
func verifyZipByStreaming(
	ctx context.Context,
	config Config,
	archiveCase fixture.ArchiveCase,
	writers writerToolchains,
	caseDir, primary string,
	expected []fixture.FileDigest,
) error {
	if len(expected) != 1 {
		return fmt.Errorf("fixture %q streams its zip through both readers, which needs exactly one member, but it holds %d", archiveCase.ID, len(expected))
	}
	readers := []struct {
		name string
		args []string
	}{
		{"7-Zip", sevenZipDockerArgs(*writers.SevenZip, caseDir, ".", "x", "-so", primary)},
		{"unzip", gnuToolsDockerArgs(*writers.GNUTools, caseDir, ".", "unzip", "-p", primary)},
	}
	for _, reader := range readers {
		digest, size, err := hashCommandOutput(ctx, config.DockerBinary, reader.args...)
		if err != nil {
			return fmt.Errorf("read %s back with the pinned %s: %w", primary, reader.name, err)
		}
		if size != expected[0].Size || digest != expected[0].BLAKE3 {
			return fmt.Errorf("the pinned %s recovered %d bytes from %s, which do not match the payload oracle for %s", reader.name, size, primary, expected[0].Path)
		}
	}
	return nil
}

func verifyZipWithInfoZip(
	ctx context.Context,
	config Config,
	archiveCase fixture.ArchiveCase,
	toolchain GNUToolsToolchain,
	caseDir, primary string,
	expected []fixture.FileDigest,
) error {
	extractDir, relative, err := newVerificationDir(caseDir, "zip-verification-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	args := []string{"unzip", "-qq", "-o"}
	if archiveCase.RequiresPassword() {
		args = append(args, "-P", fixture.FixturePassword)
	}
	args = append(args, primary, "-d", filepath.ToSlash(relative))
	if err := runGNUTools(ctx, config, toolchain, caseDir, ".", args...); err != nil {
		return fmt.Errorf("extract %s: %w", primary, err)
	}
	return checkExtractedFiles(extractDir, expected, "zip")
}

// verifyMediaFiles checks the posted copies against the payload oracle, and
// for the uuencode lane also proves the harness's own encoder against the
// pinned UUDeview decoder before the fixture is allowed to exist.
func verifyMediaFiles(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir string, expected []fixture.FileDigest) error {
	if err := checkExtractedFiles(filepath.Join(caseDir, "archive"), expected, "media"); err != nil {
		return err
	}
	if archiveCase.PostEncodingOrDefault() != fixture.UUEncodeEncoding {
		return nil
	}
	if writers.UUCodec == nil {
		return fmt.Errorf("fixture %q requires the pinned UUDeview toolchain", archiveCase.ID)
	}
	proofDir, relative, err := newVerificationDir(caseDir, "uuencode-verification-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(proofDir)
	for _, file := range expected {
		name := filepath.Base(file.Path)
		payload, err := os.ReadFile(filepath.Join(caseDir, "archive", filepath.FromSlash(file.Path)))
		if err != nil {
			return err
		}
		streamName := name + ".uu"
		if err := os.WriteFile(filepath.Join(proofDir, streamName), uucodec.Encode(name, payload), 0o644); err != nil {
			return fmt.Errorf("stage the uuencoded stream for %s: %w", name, err)
		}
		decoded, err := uucodec.Decode(ctx, config.DockerBinary, *writers.UUCodec, caseDir,
			filepath.ToSlash(filepath.Join(relative, streamName)), name)
		if err != nil {
			return err
		}
		sum := blake3.Sum256(decoded)
		if int64(len(decoded)) != file.Size || hex.EncodeToString(sum[:]) != file.BLAKE3 {
			return fmt.Errorf("the pinned UUDeview decoder did not recover %s from the harness's own uuencode output; the encoder and the format's reference implementation disagree", name)
		}
	}
	return nil
}

// verifyWrappedArchive is the two-level check: extract the outer container
// with its own writer's reader, then open the inner one with the pinned GNU
// tools and check the payload members. Both levels have to be right for a
// client to have any chance of producing the expected output.
func verifyWrappedArchive(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir, primary string, expected []fixture.FileDigest) error {
	extractDir, relative, err := newVerificationDir(caseDir, "wrapped-verification-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	switch archiveCase.ArchiveFormat {
	case fixture.RAR4, fixture.RAR5:
		args := []string{"x", "-idq", "-y"}
		if archiveCase.RequiresPassword() {
			args = append(args, "-p"+fixture.FixturePassword)
		}
		args = append(args, filepath.ToSlash(primary), filepath.ToSlash(relative)+"/")
		if err := runRAR(ctx, config.DockerBinary, writers.RAR, caseDir, args...); err != nil {
			return fmt.Errorf("extract the outer archive %s: %w", primary, err)
		}
	case fixture.SevenZip:
		args := []string{"x", "-y", "-bso0", "-bsp0"}
		args = append(args, archiveCase.SevenZipPasswordArgs()...)
		args = append(args, "-o/work/"+filepath.ToSlash(relative), filepath.ToSlash(primary))
		if err := runSevenZip(ctx, config, *writers.SevenZip, caseDir, ".", args...); err != nil {
			return fmt.Errorf("extract the outer archive %s: %w", primary, err)
		}
	default:
		return fmt.Errorf("fixture %q cannot wrap an inner archive in a %s container", archiveCase.ID, archiveCase.ArchiveFormat)
	}
	innerName := archiveCase.InnerArchive.FileName()
	if _, err := os.Stat(filepath.Join(extractDir, innerName)); err != nil {
		return fmt.Errorf("the outer archive did not contain %s: %w", innerName, err)
	}
	innerDir, innerRelative, err := newVerificationDir(caseDir, "wrapped-inner-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(innerDir)
	if err := runGNUTools(ctx, config, *writers.GNUTools, caseDir, ".",
		"tar", "--extract", "--file", filepath.ToSlash(filepath.Join(relative, innerName)),
		"--directory", filepath.ToSlash(innerRelative)); err != nil {
		return fmt.Errorf("extract the inner archive %s: %w", innerName, err)
	}
	return checkExtractedFiles(innerDir, expected, string(archiveCase.InnerArchive))
}

// writeInnerArchive builds the container the outer writer will store, and
// returns the outer writer's new single input plus the manifest record.
func writeInnerArchive(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir string, inputs []string) ([]string, *fixture.InnerArchiveDetails, error) {
	if archiveCase.InnerArchive != fixture.TarXZInnerArchive {
		return nil, nil, fmt.Errorf("fixture %q has unsupported inner_archive %q", archiveCase.ID, archiveCase.InnerArchive)
	}
	if writers.GNUTools == nil {
		return nil, nil, fmt.Errorf("fixture %q requires the pinned GNU tools toolchain for its inner archive", archiveCase.ID)
	}
	relative, err := inputRelativePaths(inputs)
	if err != nil {
		return nil, nil, err
	}
	name := archiveCase.InnerArchive.FileName()
	// The inner archive is staged beside the payload, so the outer writer's
	// -ep1/working-directory rules store it under its bare name.
	target := filepath.ToSlash(filepath.Join("input", name))
	args := []string{"tar", "--create", "--format=gnu", "--sort=name", "--xz",
		"--file", target, "--directory", "input"}
	args = append(args, relative...)
	if err := runTarCreate(ctx, config, *writers.GNUTools, caseDir, ".", args...); err != nil {
		return nil, nil, fmt.Errorf("create the inner archive for %q: %w", archiveCase.ID, err)
	}
	info, err := os.Stat(filepath.Join(caseDir, "input", name))
	if err != nil {
		return nil, nil, err
	}
	return []string{target}, &fixture.InnerArchiveDetails{
		Kind:      archiveCase.InnerArchive,
		Path:      name,
		Size:      info.Size(),
		Toolchain: writers.GNUTools.ManifestID(),
	}, nil
}

// writeSFVSidecar writes a Simple File Verification list over the posted
// archive files and then has the pinned cksfv confirm it.
//
// The list itself is written here rather than by cksfv: cksfv stamps its
// output with its own version and the time of day, which would make the
// sidecar's bytes — and therefore the fixture's digest and the seed image's
// fingerprint — different on every generation run. Writing the canonical
// `name CRC32` lines and having cksfv verify them keeps the pinned tool as
// the authority on whether the file is right without making the corpus
// irreproducible.
func writeSFVSidecar(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir string, archives []fixture.FileDigest) (fixture.SidecarDetails, error) {
	if writers.GNUTools == nil {
		return fixture.SidecarDetails{}, fmt.Errorf("fixture %q requires the pinned cksfv toolchain for its SFV sidecar", archiveCase.ID)
	}
	var lines strings.Builder
	for _, file := range archives {
		name := filepath.Base(file.Path)
		crc, err := crc32File(filepath.Join(caseDir, filepath.FromSlash(file.Path)))
		if err != nil {
			return fixture.SidecarDetails{}, err
		}
		fmt.Fprintf(&lines, "%s %08X\n", name, crc)
	}
	relative := filepath.ToSlash(filepath.Join("archive", sfvSidecarName))
	if err := os.WriteFile(filepath.Join(caseDir, filepath.FromSlash(relative)), []byte(lines.String()), 0o644); err != nil {
		return fixture.SidecarDetails{}, fmt.Errorf("write the SFV sidecar for %q: %w", archiveCase.ID, err)
	}
	if err := runGNUTools(ctx, config, *writers.GNUTools, caseDir, "archive", "cksfv", "-q", "-f", sfvSidecarName); err != nil {
		return fixture.SidecarDetails{}, fmt.Errorf("the pinned cksfv rejected the SFV sidecar for %q: %w", archiveCase.ID, err)
	}
	return fixture.SidecarDetails{
		Kind:       archiveCase.Sidecar,
		Path:       relative,
		VerifiedBy: writers.GNUTools.ManifestID(),
	}, nil
}

// runTarCreate writes a tarball and tolerates exactly one failure mode: GNU
// tar's "file changed as we read it".
//
// The payload lives on a directory bind-mounted into the container, and on
// some hosts the file's attributes as the container sees them settle a moment
// after the writer released it — so tar's stat before the read and its stat
// after it disagree about a file no byte of which changed, and tar exits 1.
// Suppressing the warning is not enough: tar still exits 1.
//
// Tolerating it here does not weaken the fixture. Nothing about this lane
// rests on tar's own verdict: the tarball is extracted with the pinned reader
// and every member is BLAKE3-checked against the payload oracle before the
// fixture is accepted. A file that genuinely changed mid-read produces a
// tarball whose members do not match, and that check fails. Any other tar
// failure is still fatal.
func runTarCreate(ctx context.Context, config Config, toolchain GNUToolsToolchain, caseDir, workdir string, toolArgs ...string) error {
	err := runGNUTools(ctx, config, toolchain, caseDir, workdir, toolArgs...)
	if err == nil || !onlyFileChangedWhileReading(err.Error()) {
		return err
	}
	return nil
}

// onlyFileChangedWhileReading reports whether every complaint tar made was the
// benign one. One unrecognised line and the whole failure stands.
func onlyFileChangedWhileReading(output string) bool {
	saw := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "docker "), strings.HasPrefix(line, "exit status "):
			// The wrapper's own description of the command it ran.
		case strings.HasSuffix(line, "file changed as we read it"):
			saw = true
		case line == "tar: Exiting with failure status due to previous errors":
		default:
			return false
		}
	}
	return saw
}

func newVerificationDir(caseDir, prefix string) (string, string, error) {
	dir, err := os.MkdirTemp(caseDir, prefix)
	if err != nil {
		return "", "", fmt.Errorf("create verification directory: %w", err)
	}
	relative, err := filepath.Rel(caseDir, dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return dir, relative, nil
}

func checkExtractedFiles(root string, expected []fixture.FileDigest, reader string) error {
	for _, file := range expected {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s member %s: %w", reader, file.Path, err)
		}
		if info.Size() != file.Size {
			return fmt.Errorf("%s member %s has size %d, expected %d", reader, file.Path, info.Size(), file.Size)
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		if digest != file.BLAKE3 {
			return fmt.Errorf("%s member %s does not match the payload oracle", reader, file.Path)
		}
	}
	return nil
}

func hashCommandOutput(ctx context.Context, name string, args ...string) (string, int64, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", 0, err
	}
	hash := blake3.New()
	size, copyErr := io.Copy(hash, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return "", 0, copyErr
	}
	if waitErr != nil {
		return "", 0, fmt.Errorf("%s: %w\n%s", name, waitErr, strings.TrimSpace(stderr.String()))
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// crc32File computes the IEEE CRC-32 an SFV line carries.
func crc32File(path string) (uint32, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	hash := crc32.NewIEEE()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, err
	}
	return hash.Sum32(), nil
}

// selectedCasesRequireUUCodec reports whether the run needs the pinned
// uuencode oracle, which only the uuencoded lanes do.
func selectedCasesRequireUUCodec(cases []fixture.ArchiveCase, selected map[string]bool) bool {
	for _, archiveCase := range cases {
		if len(selected) > 0 && !selected[archiveCase.ID] {
			continue
		}
		if archiveCase.PostEncodingOrDefault() == fixture.UUEncodeEncoding {
			return true
		}
	}
	return false
}
