// Package generator creates large, deterministic RAR fixture sets without
// putting their binary output in Git.
package generator

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/scryer-media/usenet-bench/internal/uucodec"
	"github.com/zeebo/blake3"
)

const (
	// Every benchmark fixture must post at least fixture.MinimumPostedBytes
	// (100 MiB) of archive; smaller downloads finish in the time the clients
	// spend starting up and settling, and the comparison then measures
	// process launch rather than the pipeline. Ordinary fixtures contain one
	// 150 MiB movie whose archive clears that floor on its own, including
	// the withheld-volume sets that post one 32 MiB volume fewer; the
	// fixtures with four input movies use the per-movie size below so that
	// their four archives together clear it.
	defaultBytesPerFile            int64 = 150 << 20
	defaultMultiVolumeBytesPerFile int64 = 40 << 20
	// A compressible payload shrinks to roughly 62% (LZMA2) to 70% (RAR -m5)
	// of its size at compressibleNoiseBits, so it starts larger to post an
	// archive that clears the floor with margin: 270 MiB posts about 167 MiB
	// through 7-Zip and about 190 MiB through RAR.
	defaultCompressibleBytesPerFile int64 = 270 << 20
	defaultBluRayLargeFile          int64 = 5 << 30
	defaultBluRayMediumFile         int64 = 96 << 20
	defaultBluRayMediumFileCount          = 8
	defaultBluRaySmallFile          int64 = 128 << 10
	defaultBluRaySmallFileCount           = 512
	defaultGenerationWorkers              = 4

	// The zip64 large lane exists to put one member past the 32-bit size
	// fields, so its default has to clear 4 GiB with room to spare. It has its
	// own knob rather than sharing the Blu-ray one because a smoke run wants
	// to shrink the Blu-ray disc without silently turning the zip64 lane into
	// an ordinary zip — which the structure check would then refuse.
	defaultZip64LargeFile int64 = 5 << 30
)

var canonicalFileTime = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

type Config struct {
	MatrixPath               string
	ToolchainsPath           string
	DockerfilePath           string
	PAR2ToolchainPath        string
	PAR2DockerfilePath       string
	PAR3ToolchainPath        string
	PAR3DockerfilePath       string
	SevenZipToolchainPath    string
	SevenZipDockerfilePath   string
	GNUToolsToolchainPath    string
	GNUToolsDockerfilePath   string
	UUToolchainPath          string
	UUDockerfilePath         string
	OutputDir                string
	DockerBinary             string
	BytesPerFile             int64
	MultiVolumeBytesPerFile  int64
	CompressibleBytesPerFile int64
	BluRayLargeFileBytes     int64
	Zip64LargeFileBytes      int64
	BluRayMediumFileBytes    int64
	BluRayMediumFileCount    int
	BluRaySmallFileBytes     int64
	BluRaySmallFileCount     int
	Workers                  int
	CaseIDs                  map[string]bool
	BuildImages              bool
}

func (c Config) withDefaults() Config {
	if c.MatrixPath == "" {
		c.MatrixPath = "fixtures/matrix.json"
	}
	if c.ToolchainsPath == "" {
		c.ToolchainsPath = "docker/rarlab/toolchains.json"
	}
	if c.DockerfilePath == "" {
		c.DockerfilePath = "docker/rarlab/Dockerfile"
	}
	if c.PAR2ToolchainPath == "" {
		c.PAR2ToolchainPath = "docker/par2/toolchain.json"
	}
	if c.PAR2DockerfilePath == "" {
		c.PAR2DockerfilePath = "docker/par2/Dockerfile"
	}
	if c.PAR3ToolchainPath == "" {
		c.PAR3ToolchainPath = "docker/par3/toolchain.json"
	}
	if c.PAR3DockerfilePath == "" {
		c.PAR3DockerfilePath = "docker/par3/Dockerfile"
	}
	if c.SevenZipToolchainPath == "" {
		c.SevenZipToolchainPath = "docker/sevenzip/toolchain.json"
	}
	if c.SevenZipDockerfilePath == "" {
		c.SevenZipDockerfilePath = "docker/sevenzip/Dockerfile"
	}
	if c.GNUToolsToolchainPath == "" {
		c.GNUToolsToolchainPath = "docker/gnutools/toolchain.json"
	}
	if c.GNUToolsDockerfilePath == "" {
		c.GNUToolsDockerfilePath = "docker/gnutools/Dockerfile"
	}
	if c.UUToolchainPath == "" {
		c.UUToolchainPath = "docker/uudeview/toolchain.json"
	}
	if c.UUDockerfilePath == "" {
		c.UUDockerfilePath = "docker/uudeview/Dockerfile"
	}
	if c.OutputDir == "" {
		c.OutputDir = "generated"
	}
	if c.DockerBinary == "" {
		c.DockerBinary = "docker"
	}
	if c.BytesPerFile == 0 {
		c.BytesPerFile = defaultBytesPerFile
	}
	if c.MultiVolumeBytesPerFile == 0 {
		c.MultiVolumeBytesPerFile = defaultMultiVolumeBytesPerFile
	}
	if c.CompressibleBytesPerFile == 0 {
		c.CompressibleBytesPerFile = defaultCompressibleBytesPerFile
	}
	if c.BluRayLargeFileBytes == 0 {
		c.BluRayLargeFileBytes = defaultBluRayLargeFile
	}
	if c.Zip64LargeFileBytes == 0 {
		c.Zip64LargeFileBytes = defaultZip64LargeFile
	}
	if c.BluRayMediumFileBytes == 0 {
		c.BluRayMediumFileBytes = defaultBluRayMediumFile
	}
	if c.BluRayMediumFileCount == 0 {
		c.BluRayMediumFileCount = defaultBluRayMediumFileCount
	}
	if c.BluRaySmallFileBytes == 0 {
		c.BluRaySmallFileBytes = defaultBluRaySmallFile
	}
	if c.BluRaySmallFileCount == 0 {
		c.BluRaySmallFileCount = defaultBluRaySmallFileCount
	}
	if c.Workers == 0 {
		c.Workers = defaultGenerationWorkers
	}
	return c
}

func (c Config) Validate() error {
	if c.BytesPerFile <= 0 || c.MultiVolumeBytesPerFile <= 0 || c.CompressibleBytesPerFile <= 0 {
		return fmt.Errorf("movie sizes must be positive")
	}
	if c.BluRayLargeFileBytes <= 0 || c.BluRaySmallFileBytes <= 0 || c.BluRaySmallFileCount < 1 {
		return fmt.Errorf("Blu-ray layout sizes and file count must be positive")
	}
	if c.BluRayMediumFileBytes <= 0 || c.BluRayMediumFileCount < 1 {
		return fmt.Errorf("Blu-ray extra-stream size and count must be positive")
	}
	if c.Zip64LargeFileBytes <= 0 {
		return fmt.Errorf("the zip64 large-member size must be positive")
	}
	if c.Workers < 1 {
		return fmt.Errorf("generator workers must be positive")
	}
	if strings.TrimSpace(c.OutputDir) == "" {
		return fmt.Errorf("output directory is required")
	}
	return nil
}

// Generate builds the requested pinned RARLAB images, creates the expanded
// matrix, verifies every archive using RARLAB, and writes a durable manifest.
// It never overwrites an existing case directory.
func Generate(ctx context.Context, config Config) ([]fixture.GeneratedManifest, error) {
	config = config.withDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	matrix, err := fixture.LoadMatrix(config.MatrixPath)
	if err != nil {
		return nil, err
	}
	cases, err := matrix.Expand()
	if err != nil {
		return nil, err
	}
	lock, err := LoadToolchainLock(config.ToolchainsPath)
	if err != nil {
		return nil, err
	}
	var par2Toolchain *PAR2Toolchain
	if selectedCasesRequirePAR2(cases, config.CaseIDs) {
		loaded, err := LoadPAR2Toolchain(config.PAR2ToolchainPath)
		if err != nil {
			return nil, err
		}
		if config.BuildImages {
			if err := buildPAR2Image(ctx, config, loaded); err != nil {
				return nil, err
			}
		}
		par2Toolchain = &loaded
	}
	var par3Toolchain *PAR3Toolchain
	if selectedCasesRequirePAR3(cases, config.CaseIDs) {
		loaded, err := LoadPAR3Toolchain(config.PAR3ToolchainPath)
		if err != nil {
			return nil, err
		}
		if config.BuildImages {
			if err := buildPAR3Image(ctx, config, loaded); err != nil {
				return nil, err
			}
		}
		par3Toolchain = &loaded
	}
	var sevenZipToolchain *SevenZipToolchain
	if selectedCasesRequireSevenZip(cases, config.CaseIDs) {
		loaded, err := LoadSevenZipToolchain(config.SevenZipToolchainPath)
		if err != nil {
			return nil, err
		}
		if config.BuildImages {
			if err := buildSevenZipImage(ctx, config, loaded); err != nil {
				return nil, err
			}
		}
		sevenZipToolchain = &loaded
	}
	var gnuToolsToolchain *GNUToolsToolchain
	if selectedCasesRequireGNUTools(cases, config.CaseIDs) {
		loaded, err := LoadGNUToolsToolchain(config.GNUToolsToolchainPath)
		if err != nil {
			return nil, err
		}
		if config.BuildImages {
			if err := buildGNUToolsImage(ctx, config, loaded); err != nil {
				return nil, err
			}
		}
		// The distribution writers have no upstream release tarball to pin by
		// hash, so the pin is the base image digest plus the exact package
		// versions. Checking them against the built image is what makes that
		// pin mean something.
		if err := verifyGNUToolsPackages(ctx, config, loaded); err != nil {
			return nil, err
		}
		gnuToolsToolchain = &loaded
	}
	var uuToolchain *uucodec.Toolchain
	if selectedCasesRequireUUCodec(cases, config.CaseIDs) {
		loaded, err := uucodec.LoadToolchain(config.UUToolchainPath)
		if err != nil {
			return nil, err
		}
		if config.BuildImages {
			if err := uucodec.BuildImage(ctx, config.DockerBinary, config.UUDockerfilePath, loaded); err != nil {
				return nil, err
			}
		}
		uuToolchain = &loaded
	}
	writers := writerToolchains{
		PAR2:     par2Toolchain,
		PAR3:     par3Toolchain,
		SevenZip: sevenZipToolchain,
		GNUTools: gnuToolsToolchain,
		UUCodec:  uuToolchain,
	}

	if err := os.MkdirAll(config.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	type generationJob struct {
		index       int
		archiveCase fixture.ArchiveCase
		toolchain   Toolchain
	}
	jobs := make([]generationJob, 0, len(cases))
	for _, archiveCase := range cases {
		if len(config.CaseIDs) > 0 && !config.CaseIDs[archiveCase.ID] {
			continue
		}
		toolchain, ok := lock.Find(archiveCase.GeneratorToolchain)
		if !ok {
			return nil, fmt.Errorf("fixture %q references unknown toolchain %q", archiveCase.ID, archiveCase.GeneratorToolchain)
		}
		jobs = append(jobs, generationJob{index: len(jobs), archiveCase: archiveCase, toolchain: toolchain})
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("fixture selection did not match any cases")
	}
	if config.BuildImages {
		built := make(map[string]bool)
		for _, job := range jobs {
			if built[job.toolchain.ID] {
				continue
			}
			if err := buildImage(ctx, config, job.toolchain); err != nil {
				return nil, err
			}
			built[job.toolchain.ID] = true
		}
	}

	workers := config.Workers
	if workers > len(jobs) {
		workers = len(jobs)
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan generationJob)
	results := make([]fixture.GeneratedManifest, len(jobs))
	errs := make(chan error, 1)
	var workerGroup sync.WaitGroup
	worker := func() {
		defer workerGroup.Done()
		for job := range queue {
			manifest, err := generateCase(workCtx, config, job.archiveCase, job.toolchain, writers)
			if err != nil {
				select {
				case errs <- fmt.Errorf("generate fixture %q: %w", job.archiveCase.ID, err):
					cancel()
				default:
				}
				return
			}
			results[job.index] = manifest
		}
	}
	workerGroup.Add(workers)
	for range workers {
		go worker()
	}
enqueue:
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-workCtx.Done():
			break enqueue
		}
	}
	close(queue)
	workerGroup.Wait()
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	return results, nil
}

func buildImage(ctx context.Context, config Config, toolchain Toolchain) error {
	args := []string{
		"build", "--platform", toolchain.Platform,
		"--tag", toolchain.Image,
		"--file", config.DockerfilePath,
		"--build-arg", "RAR_URL=" + toolchain.URL,
		"--build-arg", "RAR_SHA256=" + toolchain.SHA256,
		"--build-arg", "RAR_BINARY=" + toolchain.Binary,
		filepath.Dir(config.DockerfilePath),
	}
	if err := runCommand(ctx, config.DockerBinary, args...); err != nil {
		return fmt.Errorf("build RARLAB image %s: %w", toolchain.ID, err)
	}
	return nil
}

func generateCase(
	ctx context.Context,
	config Config,
	archiveCase fixture.ArchiveCase,
	toolchain Toolchain,
	writers writerToolchains,
) (fixture.GeneratedManifest, error) {
	writers.RAR = toolchain
	caseDir, err := filepath.Abs(filepath.Join(config.OutputDir, archiveCase.ID))
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("resolve fixture directory: %w", err)
	}
	if _, err := os.Stat(caseDir); err == nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("fixture directory already exists: %s (use a new output directory to preserve prior evidence)", caseDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fixture.GeneratedManifest{}, fmt.Errorf("inspect fixture directory %s: %w", caseDir, err)
	}
	inputDir := filepath.Join(caseDir, "input")
	archiveDir := filepath.Join(caseDir, "archive")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("create input directory: %w", err)
	}
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("create archive directory: %w", err)
	}

	expected, inputs, recipe, err := writePayloadFiles(ctx, inputDir, caseDir, archiveCase, config, toolchain)
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("write fixture %q payload: %w", archiveCase.ID, err)
	}
	payloadBytes := totalDigestBytes(expected)
	var innerArchive *fixture.InnerArchiveDetails
	if archiveCase.InnerArchive != fixture.NoInnerArchive {
		// The wrapped lanes hand the outer writer a single inner container
		// instead of the media files, which is what a post that ships a
		// .tar.xz inside a stored RAR set actually looks like.
		if inputs, innerArchive, err = writeInnerArchive(ctx, config, archiveCase, writers, caseDir, inputs); err != nil {
			return fixture.GeneratedManifest{}, err
		}
	}
	if err := createArchive(ctx, config, archiveCase, writers, caseDir, inputs); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("create fixture %q: %w", archiveCase.ID, err)
	}

	archives, firstVolume, err := digestArchiveFiles(archiveDir, caseDir, archiveCase.ArchiveFormat)
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("inspect fixture %q archive: %w", archiveCase.ID, err)
	}
	if requiresMultiVolumeArchive(archiveCase) && len(archives) < 2 {
		return fixture.GeneratedManifest{}, fmt.Errorf("fixture %q did not create a multi-volume archive; increase bytes per file or reduce volume size", archiveCase.ID)
	}
	if firstVolume, err = primaryArchiveFile(archives, archiveCase.ArchiveFormat); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("fixture %q: %w", archiveCase.ID, err)
	}
	// Every zip is parsed back off disk before it is read back, so what goes
	// into the manifest is what the archive contains rather than what the
	// writer was asked for. A lane whose declared structure is missing is
	// refused here: it would otherwise post as an ordinary zip and report full
	// zip64 support for a client that had never been asked for any.
	var zipStructure *fixture.ZipStructureDetails
	if archiveCase.ArchiveFormat == fixture.Zip {
		details, err := inspectZipStructure(filepath.Join(caseDir, filepath.FromSlash(firstVolume)), firstVolume)
		if err != nil {
			return fixture.GeneratedManifest{}, fmt.Errorf("inspect fixture %q zip structure: %w", archiveCase.ID, err)
		}
		details.Declared = archiveCase.ZipStructure
		if err := requireZipStructure(archiveCase, details); err != nil {
			return fixture.GeneratedManifest{}, err
		}
		zipStructure = &details
	}
	if err := verifyArchive(ctx, config, archiveCase, writers, caseDir, firstVolume, expected); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("verify fixture %q: %w", archiveCase.ID, err)
	}
	archiveBytes := totalDigestBytes(archives)
	var sidecars []fixture.SidecarDetails
	if archiveCase.Sidecar != fixture.NoSidecar {
		// The sidecar describes the archive files, so it can only be written
		// once they exist and their digests are known.
		sidecar, err := writeSFVSidecar(ctx, config, archiveCase, writers, caseDir, archives)
		if err != nil {
			return fixture.GeneratedManifest{}, err
		}
		sidecars = append(sidecars, sidecar)
		if archives, _, err = digestArchiveFiles(archiveDir, caseDir, archiveCase.ArchiveFormat); err != nil {
			return fixture.GeneratedManifest{}, fmt.Errorf("inspect fixture %q archive: %w", archiveCase.ID, err)
		}
	}
	repair, postedFiles, withheld, err := applyRepairProfile(ctx, repairInputs{
		Config:            config,
		Case:              archiveCase,
		RARToolchain:      toolchain,
		PAR2Toolchain:     writers.PAR2,
		SevenZipToolchain: writers.SevenZip,
		PAR3Toolchain:     writers.PAR3,
		Writers:           writers,
		CaseDir:           caseDir,
		SourceArchives:    archives,
		FirstVolume:       firstVolume,
		ExpectedFiles:     expected,
	})
	if err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("apply repair profile to fixture %q: %w", archiveCase.ID, err)
	}

	archiveWriter, err := archiveWriterManifestID(archiveCase, writers)
	if err != nil {
		return fixture.GeneratedManifest{}, err
	}
	manifest := fixture.GeneratedManifest{
		SchemaVersion:          fixture.GeneratedManifestSchemaVersion,
		Case:                   archiveCase,
		Toolchain:              toolchain.ManifestID(),
		ArchiveWriterToolchain: archiveWriter,
		PayloadRecipe:          recipe,
		ExpectedFiles:          expected,
		SourceArchiveFiles:     archives,
		ArchiveFiles:           postedFiles,
		WithheldFiles:          withheld,
		Repair:                 repair,
		Compression: fixture.CompressionDetails{
			Method:       archiveCase.Compression,
			PayloadBytes: payloadBytes,
			ArchiveBytes: archiveBytes,
			Ratio:        compressionRatio(payloadBytes, archiveBytes),
		},
		Sidecars:     sidecars,
		InnerArchive: innerArchive,
		ZipStructure: zipStructure,
		Encoding:     archiveCase.PostEncodingOrDefault(),
	}
	if manifest.NZBFileOrder, manifest.NZBOrderSeed, err = fixture.OrderedNZBFiles(
		archiveCase.NZBOrder,
		archiveCase.ID,
		postedPathList(manifest.PostedFiles()),
	); err != nil {
		return fixture.GeneratedManifest{}, err
	}
	if err := manifest.ValidatePostedSize(); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("fixture %q: %w", archiveCase.ID, err)
	}
	if err := writeManifest(filepath.Join(caseDir, "fixture-manifest.json"), manifest); err != nil {
		return fixture.GeneratedManifest{}, err
	}
	// Input data is only a deterministic staging source. The expected digests
	// above are the verification oracle, so retaining it would double fixture
	// storage without increasing reproducibility.
	if err := os.RemoveAll(inputDir); err != nil {
		return fixture.GeneratedManifest{}, fmt.Errorf("remove fixture staging input: %w", err)
	}
	return manifest, nil
}

// requiresMultiVolumeArchive holds for every case that asked the writer to
// split. A declared volume size that produces one file means the payload was
// smaller than the split threshold, which is a broken lane, not a small one.
func requiresMultiVolumeArchive(archiveCase fixture.ArchiveCase) bool {
	return strings.TrimSpace(archiveCase.VolumeSize) != ""
}

func totalDigestBytes(files []fixture.FileDigest) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}

func compressionRatio(payloadBytes, archiveBytes int64) float64 {
	if payloadBytes <= 0 {
		return 0
	}
	return float64(archiveBytes) / float64(payloadBytes)
}

func runRAR(ctx context.Context, dockerBinary string, toolchain Toolchain, caseDir string, rarArgs ...string) error {
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		"--user", callerDockerUser(),
		"--mount", "type=bind,src=" + caseDir + ",dst=/work",
		"--workdir", "/work",
		toolchain.Image,
	}
	args = append(args, rarArgs...)
	return runCommand(ctx, dockerBinary, args...)
}

func callerDockerUser() string {
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writePayloadFiles(ctx context.Context, dir, caseDir string, archiveCase fixture.ArchiveCase, config Config, toolchain Toolchain) ([]fixture.FileDigest, []string, fixture.PayloadRecipe, error) {
	layout := archiveCase.PayloadLayout
	if layout == "" {
		layout = fixture.UniformPayloadLayout
	}
	switch layout {
	case fixture.UniformPayloadLayout:
		return writeUniformPayloadFiles(ctx, caseDir, archiveCase, config, toolchain)
	case fixture.BluRayDiscPayloadLayout:
		return writeBluRayDiscPayloadFiles(ctx, dir, caseDir, archiveCase, config, toolchain)
	case fixture.Zip64LargePayloadLayout:
		return writeZip64LargePayloadFiles(ctx, caseDir, archiveCase, config, toolchain)
	default:
		return nil, nil, fixture.PayloadRecipe{}, fmt.Errorf("fixture %q has unsupported payload layout %q", archiveCase.ID, layout)
	}
}

func writeUniformPayloadFiles(ctx context.Context, caseDir string, archiveCase fixture.ArchiveCase, config Config, toolchain Toolchain) ([]fixture.FileDigest, []string, fixture.PayloadRecipe, error) {
	bytesPerFile := uniformMovieBytes(archiveCase, config)
	digests := make([]fixture.FileDigest, 0, archiveCase.FileCount)
	inputs := make([]string, 0, archiveCase.FileCount)
	for index := 1; index <= archiveCase.FileCount; index++ {
		name := fmt.Sprintf("payload-%02d%s", index, videoExtension(archiveCase.Payload))
		digest, err := renderVideo(ctx, config, toolchain, caseDir, filepath.ToSlash(filepath.Join("input", name)), archiveCase.Payload, bytesPerFile, uint64(index))
		if err != nil {
			return nil, nil, fixture.PayloadRecipe{}, err
		}
		digests = append(digests, fixture.FileDigest{Path: filepath.ToSlash(name), Size: digest.Size, BLAKE3: digest.BLAKE3})
		inputs = append(inputs, filepath.ToSlash(filepath.Join("input", name)))
	}
	recipe := fixture.PayloadRecipe{
		Layout:              fixture.UniformPayloadLayout,
		UniformBytesPerFile: bytesPerFile,
	}
	if archiveCase.Payload == fixture.CompressiblePayload {
		recipe.SampleNoiseBits = compressibleNoiseBits
	}
	return digests, inputs, recipe, nil
}

func uniformMovieBytes(archiveCase fixture.ArchiveCase, config Config) int64 {
	// A case may name its own payload size when neither default suits it: the
	// breadth formats post the container the writer produces, and a lane that
	// compresses well needs more input than one that stores to clear the
	// posted-size floor. A local reduced-size run still caps it, so the
	// declaration never overrides an operator asking for a small corpus.
	if declared := archiveCase.BytesPerFile; strings.TrimSpace(declared) != "" {
		if bytes, err := fixture.ByteSize(declared); err == nil && bytes > 0 {
			return scaleDeclaredBytes(bytes, config)
		}
	}
	if archiveCase.FileCount > 1 {
		return config.MultiVolumeBytesPerFile
	}
	if archiveCase.Payload == fixture.CompressiblePayload {
		return config.CompressibleBytesPerFile
	}
	return config.BytesPerFile
}

// scaleDeclaredBytes keeps a case's declared payload size in proportion to
// whatever the operator asked the run for. On a full run the scale is one and
// the declaration is used verbatim; a reduced-size smoke run that shrinks
// --bytes-per-file shrinks the declared lanes by the same factor, so a small
// corpus stays small in every lane rather than only in the ones that took a
// default.
func scaleDeclaredBytes(declared int64, config Config) int64 {
	if config.BytesPerFile == defaultBytesPerFile {
		return declared
	}
	scaled := declared * config.BytesPerFile / defaultBytesPerFile
	if scaled < 1 {
		return 1
	}
	return scaled
}

// writeZip64LargePayloadFiles writes the single member the zip64 large lane
// posts. It is its own layout rather than a uniform payload with a bigger
// bytes_per_file because the size is the whole point of the lane: the member
// has to exceed what a 32-bit ZIP size field can hold, so it must not be
// scaled by --bytes-per-file the way every other lane's payload is.
func writeZip64LargePayloadFiles(ctx context.Context, caseDir string, archiveCase fixture.ArchiveCase, config Config, toolchain Toolchain) ([]fixture.FileDigest, []string, fixture.PayloadRecipe, error) {
	if archiveCase.FileCount != 1 {
		return nil, nil, fixture.PayloadRecipe{}, fmt.Errorf("fixture %q uses the zip64 large layout, which posts exactly one member, but declares %d", archiveCase.ID, archiveCase.FileCount)
	}
	name := "payload-01" + videoExtension(archiveCase.Payload)
	digest, err := renderVideo(ctx, config, toolchain, caseDir, filepath.ToSlash(filepath.Join("input", name)), archiveCase.Payload, config.Zip64LargeFileBytes, 1)
	if err != nil {
		return nil, nil, fixture.PayloadRecipe{}, err
	}
	return []fixture.FileDigest{{Path: name, Size: digest.Size, BLAKE3: digest.BLAKE3}},
		[]string{filepath.ToSlash(filepath.Join("input", name))},
		fixture.PayloadRecipe{
			Layout:         fixture.Zip64LargePayloadLayout,
			LargeFileBytes: config.Zip64LargeFileBytes,
		}, nil
}

// writeBluRayDiscPayloadFiles makes an intentionally declared disc-layout
// workload: one large media stream, a few small menu/extra streams, and many
// tiny playlist, clip-info, BD-J, and presentation members. It does not claim
// to be a byte-for-byte authored Blu-ray image.
func writeBluRayDiscPayloadFiles(ctx context.Context, dir, caseDir string, archiveCase fixture.ArchiveCase, config Config, toolchain Toolchain) ([]fixture.FileDigest, []string, fixture.PayloadRecipe, error) {
	digests := make([]fixture.FileDigest, 0, config.BluRaySmallFileCount+1)
	var smallStream fixture.FileDigest
	for index := 1; index <= config.BluRaySmallFileCount; index++ {
		relative := bluRaySmallPath(index)
		if isTransportStream(relative) {
			if smallStream.Path == "" {
				digest, err := renderVideo(ctx, config, toolchain, caseDir, filepath.ToSlash(filepath.Join("input", relative)), fixture.CompressiblePayload, config.BluRaySmallFileBytes, 10_001)
				if err != nil {
					return nil, nil, fixture.PayloadRecipe{}, err
				}
				smallStream = fixture.FileDigest{Path: relative, Size: digest.Size, BLAKE3: digest.BLAKE3}
			} else if err := copyVideoFile(filepath.Join(dir, filepath.FromSlash(smallStream.Path)), filepath.Join(dir, filepath.FromSlash(relative))); err != nil {
				return nil, nil, fixture.PayloadRecipe{}, err
			}
			digest := smallStream
			digest.Path = relative
			digests = append(digests, digest)
			continue
		}
		if _, err := writePayloadAt(dir, relative, fixture.CompressiblePayload, bluRayMetadataBytes(relative, config.BluRaySmallFileBytes), uint64(index)); err != nil {
			return nil, nil, fixture.PayloadRecipe{}, err
		}
		digest, err := digestFile(relative, filepath.Join(dir, filepath.FromSlash(relative)))
		if err != nil {
			return nil, nil, fixture.PayloadRecipe{}, err
		}
		digests = append(digests, digest)
	}
	// The extra streams are the menu, trailer and bonus-feature members a real
	// disc folder carries: too big to be metadata, far smaller than the main
	// feature. One is rendered and copied, so the mix costs one encode.
	var mediumStream fixture.FileDigest
	for index := 1; index <= config.BluRayMediumFileCount; index++ {
		relative := bluRayMediumPath(index)
		if mediumStream.Path == "" {
			digest, err := renderVideo(ctx, config, toolchain, caseDir, filepath.ToSlash(filepath.Join("input", relative)), archiveCase.Payload, config.BluRayMediumFileBytes, 20_001)
			if err != nil {
				return nil, nil, fixture.PayloadRecipe{}, err
			}
			mediumStream = fixture.FileDigest{Path: relative, Size: digest.Size, BLAKE3: digest.BLAKE3}
		} else if err := copyVideoFile(filepath.Join(dir, filepath.FromSlash(mediumStream.Path)), filepath.Join(dir, filepath.FromSlash(relative))); err != nil {
			return nil, nil, fixture.PayloadRecipe{}, err
		}
		digest := mediumStream
		digest.Path = relative
		digests = append(digests, digest)
	}
	largeRelative := "BDMV/STREAM/00000.m2ts"
	largeDigest, err := renderVideo(ctx, config, toolchain, caseDir, filepath.ToSlash(filepath.Join("input", largeRelative)), archiveCase.Payload, config.BluRayLargeFileBytes, 1)
	if err != nil {
		return nil, nil, fixture.PayloadRecipe{}, err
	}
	digests = append(digests, fixture.FileDigest{Path: largeRelative, Size: largeDigest.Size, BLAKE3: largeDigest.BLAKE3})
	return digests, presentBluRayInputRoots(digests), fixture.PayloadRecipe{
		Layout:          fixture.BluRayDiscPayloadLayout,
		LargeFileBytes:  config.BluRayLargeFileBytes,
		MediumFileCount: config.BluRayMediumFileCount,
		MediumFileBytes: config.BluRayMediumFileBytes,
		SmallFileCount:  config.BluRaySmallFileCount,
		SmallFileBytes:  config.BluRaySmallFileBytes,
	}, nil
}

// bluRayMediumPath numbers the extra streams from 00101 so they cannot
// collide with the main feature at 00000 or the small menu streams at
// 00001-00004.
func bluRayMediumPath(index int) string {
	return fmt.Sprintf("BDMV/STREAM/%05d.m2ts", 100+index)
}

// bluRayArchiveInputRoots preserves the archive's disc tree. RAR's -ep1
// removes the input/ prefix while retaining BDMV/ and CERTIFICATE/ beneath
// these roots. Passing individual nested files would instead flatten each
// file to its basename and make duplicate legitimate member names collide.
func bluRayArchiveInputRoots() []string {
	return []string{"input/BDMV", "input/CERTIFICATE"}
}

// presentBluRayInputRoots keeps only the roots the payload actually wrote.
// The CERTIFICATE members sit at the tail of the small-file numbering, so a
// reduced-size local run asks for fewer files than the full disc and never
// creates that directory; naming it anyway makes the archiver fail on a path
// that does not exist. Root order is preserved so the archive member order
// stays stable for a given file count.
func presentBluRayInputRoots(digests []fixture.FileDigest) []string {
	roots := make([]string, 0, 2)
	for _, root := range bluRayArchiveInputRoots() {
		prefix := strings.TrimPrefix(root, "input/") + "/"
		for _, digest := range digests {
			if strings.HasPrefix(digest.Path, prefix) {
				roots = append(roots, root)
				break
			}
		}
	}
	return roots
}

func bluRaySmallPath(index int) string {
	switch {
	case index <= 4:
		return fmt.Sprintf("BDMV/STREAM/%05d.m2ts", index)
	case index <= 164:
		return fmt.Sprintf("BDMV/PLAYLIST/%05d.mpls", index-5)
	case index <= 324:
		return fmt.Sprintf("BDMV/CLIPINF/%05d.clpi", index-165)
	case index <= 388:
		return fmt.Sprintf("BDMV/BDJO/%05d.bdjo", index-325)
	case index <= 452:
		return fmt.Sprintf("BDMV/META/DL/Composite%03d_BT2020_HDR.png", index-389)
	case index <= 484:
		return fmt.Sprintf("BDMV/META/DL/metadata-%03d.xml", index-453)
	case index == 485:
		return "BDMV/index.bdmv"
	case index == 486:
		return "BDMV/MovieObject.bdmv"
	case index == 487:
		return "BDMV/JAR/00000.jar"
	case index == 488:
		return "BDMV/AUXDATA/00000.otf"
	case index == 489:
		return "CERTIFICATE/id.bdmv"
	case index == 490:
		return "CERTIFICATE/BACKUP/id.bdmv"
	case index == 491:
		// Disc trees are upper-case throughout; a second CERTIFICATE/backup
		// spelled differently is not something a real disc carries, and it
		// makes a case-insensitive extractor see two directories as one.
		return "CERTIFICATE/app.discroot.crt"
	default:
		return fmt.Sprintf("BDMV/META/DL/locale-%03d.txt", index-492)
	}
}

func bluRayMetadataBytes(relative string, limit int64) int64 {
	requested := int64(8 << 10)
	switch {
	case strings.HasSuffix(relative, ".clpi"):
		requested = 16 << 10
	case strings.HasSuffix(relative, ".bdjo"):
		requested = 24 << 10
	case strings.HasSuffix(relative, ".png"):
		requested = 48 << 10
	case strings.HasSuffix(relative, ".jar"):
		requested = 64 << 10
	case strings.HasSuffix(relative, ".otf"):
		requested = 96 << 10
	}
	if requested > limit {
		return limit
	}
	return requested
}

func writePayloadAt(root, relative string, kind fixture.PayloadKind, size int64, stream uint64) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return writePayload(path, kind, size, stream)
}

func writePayload(path string, kind fixture.PayloadKind, size int64, stream uint64) (string, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	hash := blake3.New()
	writer := io.MultiWriter(file, hash)
	var writeErr error
	switch kind {
	case fixture.IncompressiblePayload:
		writeErr = writeIncompressible(writer, size, stream)
	case fixture.CompressiblePayload:
		writeErr = writeModeratelyCompressible(writer, size, stream)
	default:
		writeErr = fmt.Errorf("unsupported payload kind %q", kind)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chtimes(path, canonicalFileTime, canonicalFileTime); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeIncompressible(writer io.Writer, size int64, stream uint64) error {
	var counter uint64
	var written int64
	for written < size {
		var seed [16]byte
		binary.BigEndian.PutUint64(seed[:8], stream)
		binary.BigEndian.PutUint64(seed[8:], counter)
		block := blake3.Sum256(seed[:])
		remaining := size - written
		chunk := block[:]
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		if _, err := writer.Write(chunk); err != nil {
			return err
		}
		written += int64(len(chunk))
		counter++
	}
	return nil
}

// writeModeratelyCompressible emits four fresh pseudorandom 32 KiB blocks and
// repeats one. The resulting 20% redundancy is enough to exercise RAR
// compression while keeping the archive close to the payload size.
func writeModeratelyCompressible(writer io.Writer, size int64, stream uint64) error {
	const (
		blockSize    = 32 << 10
		uniqueBlocks = 4
	)
	var counter uint64
	var written int64
	for written < size {
		blocks := make([][]byte, uniqueBlocks)
		for index := range blocks {
			block := make([]byte, blockSize)
			for offset := 0; offset < len(block); offset += 32 {
				var seed [16]byte
				binary.BigEndian.PutUint64(seed[:8], stream)
				binary.BigEndian.PutUint64(seed[8:], counter)
				digest := blake3.Sum256(seed[:])
				copy(block[offset:], digest[:])
				counter++
			}
			blocks[index] = block
		}
		for index := 0; index <= uniqueBlocks && written < size; index++ {
			chunk := blocks[index%uniqueBlocks]
			if remaining := size - written; int64(len(chunk)) > remaining {
				chunk = chunk[:remaining]
			}
			if _, err := writer.Write(chunk); err != nil {
				return err
			}
			written += int64(len(chunk))
		}
	}
	return nil
}

// digestArchiveFiles collects the archive volumes the writer produced, in the
// order a client would read them. RAR volumes all carry the .rar suffix;
// 7-Zip multi-volume members are fixture.7z.001, .002, ... so they are matched
// by name prefix and sort into numeric order on their zero-padded suffix.
func digestArchiveFiles(archiveDir, caseDir string, format fixture.ArchiveFormat) ([]fixture.FileDigest, string, error) {
	var paths []string
	if err := filepath.WalkDir(archiveDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if isArchiveVolume(entry.Name(), format) {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, "", err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, "", fmt.Errorf("the pinned %s writer did not produce an archive volume", format)
	}
	digests := make([]fixture.FileDigest, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", err
		}
		digest, err := hashFile(path)
		if err != nil {
			return nil, "", err
		}
		relative, err := filepath.Rel(caseDir, path)
		if err != nil {
			return nil, "", err
		}
		digests = append(digests, fixture.FileDigest{
			Path:   filepath.ToSlash(relative),
			Size:   info.Size(),
			BLAKE3: digest,
		})
	}
	first, err := filepath.Rel(caseDir, paths[0])
	if err != nil {
		return nil, "", err
	}
	return digests, first, nil
}

// isArchiveVolume decides which files in a fixture's archive directory are
// posted material. Every format names its parts differently, and the media
// lane posts the payload itself, so the rule is per-format rather than a
// single suffix test. The SFV sidecar is posted for the lanes that carry one,
// so it is archive material too.
func isArchiveVolume(name string, format fixture.ArchiveFormat) bool {
	if name == sfvSidecarName {
		return true
	}
	switch format {
	case fixture.SevenZip:
		return strings.HasPrefix(name, sevenZipArchiveName)
	case fixture.Tar:
		return strings.HasPrefix(name, tarArchiveBaseName)
	case fixture.XZCompressed:
		return strings.EqualFold(filepath.Ext(name), ".xz")
	case fixture.Zip:
		// A spanned Info-ZIP set is fixture.z01, fixture.z02, ..., fixture.zip;
		// the common prefix covers both the parts and the final member.
		return strings.HasPrefix(name, "fixture.z")
	case fixture.Media:
		// Nothing wrapped the payload, so every file staged for posting is
		// posted material.
		return true
	default:
		return strings.EqualFold(filepath.Ext(name), ".rar")
	}
}

// primaryArchiveFile names the file a reader is handed to open the set. For
// every format but zip that is the first file in read order; a spanned zip
// puts its central directory in the last member, fixture.zip, and Info-ZIP
// refuses to be pointed at a .z01.
func primaryArchiveFile(archives []fixture.FileDigest, format fixture.ArchiveFormat) (string, error) {
	candidates := make([]fixture.FileDigest, 0, len(archives))
	for _, file := range archives {
		if filepath.Base(file.Path) == sfvSidecarName {
			continue
		}
		candidates = append(candidates, file)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("the pinned %s writer did not produce an archive file", format)
	}
	if format == fixture.Zip {
		for _, file := range candidates {
			if filepath.Base(file.Path) == zipArchiveName {
				return file.Path, nil
			}
		}
		return "", fmt.Errorf("the pinned zip writer did not produce %s", zipArchiveName)
	}
	return candidates[0].Path, nil
}

func postedPathList(files []fixture.FileDigest) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := blake3.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeManifest(path string, manifest fixture.GeneratedManifest) error {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("write fixture manifest %s: %w", path, err)
	}
	return nil
}
