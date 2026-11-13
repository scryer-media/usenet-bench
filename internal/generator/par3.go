package generator

import (
	"context"
	"fmt"
	"math/bits"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

const (
	par3BlockSize = 1 << 20
	par3IndexName = "fixture.par3"
	// par3InsideFaults is how many separate places the embedded lane damages.
	// Each lands in a different quarter of the member data, so the repair has
	// to rebuild several independent blocks rather than one.
	par3InsideFaults = 3
)

func selectedCasesRequirePAR3(cases []fixture.ArchiveCase, selected map[string]bool) bool {
	for _, archiveCase := range cases {
		if len(selected) > 0 && !selected[archiveCase.ID] {
			continue
		}
		if archiveCase.RepairProfile.EmbedsPAR3() {
			return true
		}
	}
	return false
}

// selectedCasesRequireRarpar reports whether any selected case posts a
// standalone PAR3 set, which rarpar writes.
func selectedCasesRequireRarpar(cases []fixture.ArchiveCase, selected map[string]bool) bool {
	for _, archiveCase := range cases {
		if len(selected) > 0 && !selected[archiveCase.ID] {
			continue
		}
		if profile := archiveCase.RepairProfile; profile.UsesPAR3() && !profile.EmbedsPAR3() {
			return true
		}
	}
	return false
}

func buildRarparImage(ctx context.Context, config Config, toolchain RarparToolchain) error {
	args := []string{
		"build", "--platform", toolchain.Platform,
		"--tag", toolchain.Image,
		"--file", config.RarparDockerfilePath,
		"--build-arg", "RARPAR_URL=" + toolchain.URL,
		"--build-arg", "RARPAR_SHA256=" + toolchain.SHA256,
		filepath.Dir(config.RarparDockerfilePath),
	}
	if err := runCommand(ctx, config.DockerBinary, args...); err != nil {
		return fmt.Errorf("build rarpar image %s: %w", toolchain.ID, err)
	}
	return nil
}

// runRarpar runs rarpar inside the case directory's archive folder, for the
// reason runPAR3 gives: a PAR3 set records its inputs by their path relative
// to the working directory, and a client downloads the post into one flat
// folder.
func runRarpar(ctx context.Context, dockerBinary string, toolchain RarparToolchain, caseDir string, rarparArgs ...string) error {
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		"--user", callerDockerUser(),
		"--mount", "type=bind,src=" + caseDir + ",dst=/work",
		"--workdir", "/work/archive",
		toolchain.Image,
	}
	args = append(args, rarparArgs...)
	return runCommand(ctx, dockerBinary, args...)
}

// fftCapacityLog2 is the recovery capacity an FFT set has to declare when it
// is created: the smallest power of two that holds one cohort's recovery
// blocks. The exponent is part of the set's identity, so it is derived from
// the post and never guessed. A cohort's source blocks and its capacity
// together have to fit the 65536-point transform.
func fftCapacityLog2(sources []fixture.FileDigest, blockSize int64, recoveryBlocks, redundancyPercent, cohorts int) (int, error) {
	if blockSize < 1 {
		return 0, fmt.Errorf("PAR3 block size %d is invalid", blockSize)
	}
	cohorts = max(cohorts, 1)
	var blocks int64
	for _, source := range sources {
		blocks += (source.Size + blockSize - 1) / blockSize
	}
	recovery := int64(recoveryBlocks)
	if recovery == 0 {
		recovery = (blocks*int64(redundancyPercent) + 99) / 100
	}
	perCohort := (recovery + int64(cohorts) - 1) / int64(cohorts)
	if blocks < 1 || perCohort < 1 {
		return 0, fmt.Errorf("a PAR3 set of %d source blocks and %d recovery blocks is empty", blocks, recovery)
	}
	capacityLog2 := bits.Len64(uint64(perCohort - 1))
	inputs := (blocks + int64(cohorts) - 1) / int64(cohorts)
	if capacityLog2 > 15 || inputs+int64(1)<<capacityLog2 > 65536 {
		return 0, fmt.Errorf("%d source blocks and %d recovery blocks in %d FFT cohort(s) exceed the 65536-point transform", blocks, recovery, cohorts)
	}
	return capacityLog2, nil
}

func buildPAR3Image(ctx context.Context, config Config, toolchain PAR3Toolchain) error {
	args := []string{
		"build", "--platform", toolchain.Platform,
		"--tag", toolchain.Image,
		"--file", config.PAR3DockerfilePath,
		"--build-arg", "PAR3_URL=" + toolchain.URL,
		"--build-arg", "PAR3_SHA256=" + toolchain.SHA256,
		filepath.Dir(config.PAR3DockerfilePath),
	}
	if err := runCommand(ctx, config.DockerBinary, args...); err != nil {
		return fmt.Errorf("build PAR3 image %s: %w", toolchain.ID, err)
	}
	return nil
}

// runPAR3 runs the reference inside the case directory's archive folder. The
// reference records every input by its path relative to the working
// directory, so it has to run where the posted files sit: run from the case
// root, it would name every file archive/... and a client that downloads the
// post into one flat folder would find none of them by name.
func runPAR3(ctx context.Context, dockerBinary string, toolchain PAR3Toolchain, caseDir string, par3Args ...string) error {
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		"--user", callerDockerUser(),
		"--mount", "type=bind,src=" + caseDir + ",dst=/work",
		"--workdir", "/work/archive",
		toolchain.Image,
	}
	args = append(args, par3Args...)
	return runCommand(ctx, dockerBinary, args...)
}

// par3Parameters is the creation recipe and fault for each PAR3 profile. The
// light and heavy-withheld recipes are the PAR2 ones on purpose; see the
// profile declarations. Every standalone set is written with the FFT
// Reed-Solomon code; only the embedded lane, which the reference writes,
// leaves the code to its writer.
type par3Recipe struct {
	code       string
	redundancy int
	withheld   int
	flips      int
	// articlesPerMille is set for the scattered profiles, whose blocks are
	// one article each and whose fault is withheld articles.
	articlesPerMille int
	// lostArticles is set for the article-loss profiles, whose fault is
	// whole articles withheld from a post with a declared shape.
	lostArticles bool
	// cohorts, when above one, asks rarpar for that many interleaved
	// FFT cohorts.
	cohorts int
}

func par3Parameters(profile fixture.RepairProfile) (par3Recipe, error) {
	switch profile {
	case fixture.PAR3LightRepairProfile:
		return par3Recipe{code: "fft", redundancy: 10, flips: 1}, nil
	case fixture.PAR3HeavyWithheldProfile:
		return par3Recipe{code: "fft", redundancy: 35, withheld: 1}, nil
	case fixture.PAR3FFTHeavyWithheldProfile:
		return par3Recipe{code: "fft", redundancy: 35, withheld: 1, flips: 1}, nil
	case fixture.PAR3InsideLightProfile:
		// insert chooses its own block size and code, so only the redundancy
		// is the harness's choice.
		return par3Recipe{code: "reference-default", redundancy: 10, flips: par3InsideFaults}, nil
	case fixture.PAR3FFTScatteredLightProfile, fixture.PAR3FFTScatteredHeavyProfile:
		perMille, redundancy, _ := profile.ScatteredDamage()
		return par3Recipe{code: "fft", redundancy: redundancy, articlesPerMille: perMille}, nil
	case fixture.PAR3ArticlesLightProfile, fixture.PAR3SilentArticlesLightProfile, fixture.PAR3CorruptArticlesLightProfile:
		return par3Recipe{code: "fft", lostArticles: true}, nil
	case fixture.PAR3FFTArticlesHeavyProfile, fixture.PAR3FFTCorruptArticlesHeavyProfile:
		return par3Recipe{code: "fft", lostArticles: true}, nil
	case fixture.PAR3FFTPastPAR2CapProfile:
		perMille, redundancy, _ := profile.ScatteredDamage()
		return par3Recipe{code: "fft", redundancy: redundancy, articlesPerMille: perMille, cohorts: 2}, nil
	default:
		return par3Recipe{}, fmt.Errorf("repair profile %q is not a PAR3 profile", profile)
	}
}

// applyPAR3Profile writes the PAR3 material, injects the declared fault and
// proves its writer can repair exactly what a client will receive.
func applyPAR3Profile(ctx context.Context, in repairInputs) (*fixture.PAR3Details, []fixture.CorruptionDetail, []fixture.FileDigest, error) {
	profile := in.Case.RepairProfile
	recipe, err := par3Parameters(profile)
	if err != nil {
		return nil, nil, nil, err
	}
	details := &fixture.PAR3Details{
		Code:              recipe.code,
		RedundancyPercent: recipe.redundancy,
	}
	if profile.EmbedsPAR3() {
		if in.PAR3Toolchain == nil {
			return nil, nil, nil, fmt.Errorf("embedded PAR3 repair fixture requires the pinned PAR3 reference toolchain")
		}
		details.Toolchain = in.PAR3Toolchain.ManifestID()
		faults, err := embedPAR3(ctx, in, recipe, details)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := verifyPAR3Repair(ctx, in, nil, nil); err != nil {
			return nil, nil, nil, err
		}
		return details, faults, nil, nil
	}

	if in.Writers.Rarpar == nil {
		return nil, nil, nil, fmt.Errorf("PAR3 repair fixture requires the pinned rarpar toolchain")
	}
	rarpar := *in.Writers.Rarpar
	details.Toolchain = rarpar.ManifestID()
	names := make([]string, 0, len(in.SourceArchives))
	for _, source := range in.SourceArchives {
		name, err := archiveRelativeName(source.Path)
		if err != nil {
			return nil, nil, nil, err
		}
		names = append(names, name)
	}
	details.BlockSize = par3BlockSize
	if recipe.articlesPerMille > 0 {
		details.BlockSize = fixture.ScatteredArticleBytes
	}
	recovery := []string{"--recovery-percent", fmt.Sprint(recipe.redundancy)}
	shape := in.Case.RecoveryShape
	if shape.Declared() {
		// A declared shape is a real post's block size and recovery block
		// count, which replace the fixed block and the percentage.
		details.BlockSize = shape.SliceBytes
		details.RecoveryBlocks = shape.RecoveryBlocks
		details.RedundancyPercent = 0
		recovery = []string{"--recovery-count", fmt.Sprint(shape.RecoveryBlocks)}
	} else if recipe.lostArticles {
		return nil, nil, nil, fmt.Errorf("repair profile %q needs a recovery_shape", profile)
	}
	capacityLog2, err := fftCapacityLog2(in.SourceArchives, details.BlockSize, details.RecoveryBlocks, recipe.redundancy, recipe.cohorts)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("repair profile %q: %w", profile, err)
	}
	details.Arguments = append([]string{
		"par3", "create",
		"--codec", "fft", "--capacity-log2", fmt.Sprint(capacityLog2),
		"--block-size", fmt.Sprint(details.BlockSize),
	}, recovery...)
	if recipe.cohorts > 1 {
		// rarpar counts the extra cohorts, so N cohorts is --interleave N-1.
		details.Cohorts = recipe.cohorts
		details.Arguments = append(details.Arguments, "--interleave", fmt.Sprint(recipe.cohorts-1))
	}
	details.Arguments = append(append(details.Arguments, par3IndexName), names...)
	if profile.ExceedsPAR2BlockLimit() {
		// Prove PAR2 cannot serve this post before spending the creation and
		// repair time on it.
		faults, err := scatteredArticles(in.SourceArchives, in.Case.SetID, recipe.articlesPerMille)
		if err != nil {
			return nil, nil, nil, err
		}
		comparison, err := par2LimitComparison(in.SourceArchives, faults, recipe.redundancy)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("repair profile %q: %w", profile, err)
		}
		details.PAR2AtLimit = &comparison
	}
	if err := runRarpar(ctx, in.Config.DockerBinary, rarpar, in.CaseDir, details.Arguments...); err != nil {
		return nil, nil, nil, fmt.Errorf("create PAR3 recovery material: %w", err)
	}
	if recipe.articlesPerMille > 0 {
		faults, err := scatteredArticles(in.SourceArchives, in.Case.SetID, recipe.articlesPerMille)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := verifyPAR3Repair(ctx, in, nil, faults); err != nil {
			return nil, nil, nil, err
		}
		return details, faults, nil, nil
	}

	if recipe.lostArticles {
		faults, damaged, err := lostArticles(in.SourceArchives, profile, shape)
		if err != nil {
			return nil, nil, nil, err
		}
		if faults, err = damageArticles(in.CaseDir, profile, faults); err != nil {
			return nil, nil, nil, err
		}
		if profile.SilentlyCorruptsArticles() {
			damaged = damagedSlices(faults, shape.SliceBytes)
		}
		details.DamagedBlocks = damaged
		details.ArticleBytes = shape.ArticleBytes
		if err := verifyPAR3Repair(ctx, in, nil, faults); err != nil {
			return nil, nil, nil, err
		}
		return details, faults, nil, nil
	}

	// The faulted files are chosen the same way the PAR2 lanes choose theirs:
	// from the middle of the set and never the first file. When a profile
	// withholds one file and damages another, the withheld file comes first.
	targets, err := repairTargets(in.SourceArchives, recipe.withheld+recipe.flips)
	if err != nil {
		return nil, nil, nil, err
	}
	var faults []fixture.CorruptionDetail
	var withheld []fixture.FileDigest
	if recipe.withheld > 0 {
		held, files, err := withholdArchiveFiles(in.CaseDir, targets[:recipe.withheld])
		if err != nil {
			return nil, nil, nil, err
		}
		faults = append(faults, held...)
		withheld = files
	}
	for _, target := range targets[recipe.withheld:] {
		fault, err := flipArchiveBytes(in.CaseDir, target)
		if err != nil {
			return nil, nil, nil, err
		}
		faults = append(faults, fault)
	}
	if err := verifyPAR3Repair(ctx, in, withheld, nil); err != nil {
		return nil, nil, nil, err
	}
	return details, faults, withheld, nil
}

// embedPAR3 inserts the PAR3 packets into the container and then damages the
// member data. The reference appends its packets after the original data and
// rewrites only the trailing directory, so offsets inside the original file
// length stay inside the stored member rather than inside the parity.
func embedPAR3(ctx context.Context, in repairInputs, recipe par3Recipe, details *fixture.PAR3Details) ([]fixture.CorruptionDetail, error) {
	var original fixture.FileDigest
	for _, source := range in.SourceArchives {
		if source.Path == in.FirstVolume {
			original = source
		}
	}
	if original.Path == "" || len(in.SourceArchives) != 1 {
		return nil, fmt.Errorf("embedded PAR3 needs exactly one container file, have %d", len(in.SourceArchives))
	}
	name, err := archiveRelativeName(original.Path)
	if err != nil {
		return nil, err
	}
	details.Embedded = true
	details.Arguments = []string{"insert", "-q", fmt.Sprintf("-r%d", recipe.redundancy), name}
	if err := runPAR3(ctx, in.Config.DockerBinary, *in.PAR3Toolchain, in.CaseDir, details.Arguments...); err != nil {
		return nil, fmt.Errorf("embed PAR3 recovery material: %w", err)
	}
	info, err := os.Stat(filepath.Join(in.CaseDir, filepath.FromSlash(original.Path)))
	if err != nil {
		return nil, err
	}
	if info.Size() <= original.Size {
		return nil, fmt.Errorf("the PAR3 reference did not grow %s; no packets were embedded", original.Path)
	}
	faults := make([]fixture.CorruptionDetail, 0, recipe.flips)
	for index := 1; index <= recipe.flips; index++ {
		offset := original.Size * int64(index) / int64(recipe.flips+1)
		fault, err := flipArchiveBytesAt(in.CaseDir, original, offset)
		if err != nil {
			return nil, err
		}
		faults = append(faults, fault)
	}
	return faults, nil
}

func verifyPAR3Repair(ctx context.Context, in repairInputs, withheld []fixture.FileDigest, articles []fixture.CorruptionDetail) error {
	for _, blankCorrupt := range articleStagings(articles) {
		if err := verifyPAR3RepairStaged(ctx, in, withheld, articles, blankCorrupt); err != nil {
			return fmt.Errorf("%w%s", err, stagingName(articles, blankCorrupt))
		}
	}
	return nil
}

func verifyPAR3RepairStaged(ctx context.Context, in repairInputs, withheld []fixture.FileDigest, articles []fixture.CorruptionDetail, blankCorrupt bool) error {
	verifyDir, err := copyArchiveForRepairVerification(in.CaseDir, withheld)
	if err != nil {
		return err
	}
	defer os.RemoveAll(verifyDir)
	if err := stageArticleFaults(verifyDir, articles, blankCorrupt); err != nil {
		return err
	}
	if in.Case.RepairProfile.EmbedsPAR3() {
		name, err := archiveRelativeName(in.FirstVolume)
		if err != nil {
			return err
		}
		if err := runPAR3(ctx, in.Config.DockerBinary, *in.PAR3Toolchain, verifyDir, "rs", "-q", name); err != nil {
			return fmt.Errorf("embedded PAR3 repair verification: %w", err)
		}
		// Info-ZIP reports the embedded packets as unexpected bytes inside the
		// archive, so the repaired container is read back with 7-Zip, which
		// locates the members from the trailing directory the reference kept
		// intact.
		if in.Writers.SevenZip == nil {
			return fmt.Errorf("embedded PAR3 verification requires the pinned 7-Zip toolchain")
		}
		if err := verifySevenZipArchive(ctx, in.Config, in.Case, *in.Writers.SevenZip, verifyDir, in.FirstVolume, in.ExpectedFiles); err != nil {
			return fmt.Errorf("verify PAR3-repaired %s container: %w", in.Case.ArchiveFormat, err)
		}
		return nil
	}
	if in.Writers.Rarpar == nil {
		return fmt.Errorf("PAR3 repair verification requires the pinned rarpar toolchain")
	}
	if err := runRarpar(ctx, in.Config.DockerBinary, *in.Writers.Rarpar, verifyDir, "par3", "repair", par3IndexName); err != nil {
		return fmt.Errorf("PAR3 repair verification: %w", err)
	}
	if err := verifyArchive(ctx, in.Config, in.Case, in.Writers, verifyDir, in.FirstVolume, in.ExpectedFiles); err != nil {
		return fmt.Errorf("verify PAR3-repaired %s fixture: %w", in.Case.ArchiveFormat, err)
	}
	return nil
}

// archiveRelativeName turns a case-relative posted path into the name the
// reference sees from inside the archive folder.
func archiveRelativeName(casePath string) (string, error) {
	name := strings.TrimPrefix(casePath, "archive/")
	if name == casePath || name == "" || path.IsAbs(name) || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("posted file %q is not inside the archive folder", casePath)
	}
	return name, nil
}
