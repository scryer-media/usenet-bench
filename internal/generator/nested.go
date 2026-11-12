package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// nestedStageDir is where a layer's archives are written before they replace
// the layer's inputs in the staging directory.
const nestedStageDir = "nested-stage"

func caseNestedLayers(archiveCase fixture.ArchiveCase) []fixture.NestedLayer {
	layers, _ := archiveCase.Nested.Layers()
	return layers
}

// caseNestsFormat reports whether any nested layer of the case is written in
// one of the given formats.
func caseNestsFormat(archiveCase fixture.ArchiveCase, formats ...fixture.ArchiveFormat) bool {
	for _, layer := range caseNestedLayers(archiveCase) {
		for _, format := range formats {
			if layer.Format == format {
				return true
			}
		}
	}
	return false
}

func caseNestsPAR2(archiveCase fixture.ArchiveCase) bool {
	for _, layer := range caseNestedLayers(archiveCase) {
		if layer.PAR2 {
			return true
		}
	}
	return false
}

// writeNestedLayers wraps the staged payload in every nested layer, innermost
// first, and returns the outer writer's inputs: the files of the outermost
// nested layer, staged under input/ in place of the payload.
func writeNestedLayers(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir string, inputs []string) ([]string, []fixture.NestedLayerDetails, error) {
	layers, err := archiveCase.Nested.Layers()
	if err != nil {
		return nil, nil, err
	}
	var details []fixture.NestedLayerDetails
	for index, layer := range layers {
		level := index + 1
		produced, detail, err := writeNestedLayer(ctx, config, archiveCase, writers, caseDir, layer, level, inputs)
		if err != nil {
			return nil, nil, fmt.Errorf("fixture %q nested layer %d: %w", archiveCase.ID, level, err)
		}
		inputs = produced
		details = append(details, detail)
	}
	return inputs, details, nil
}

func writeNestedLayer(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir string, layer fixture.NestedLayer, level int, inputs []string) ([]string, fixture.NestedLayerDetails, error) {
	none := fixture.NestedLayerDetails{}
	if len(inputs) < layer.Archives {
		return nil, none, fmt.Errorf("%d input files cannot fill %d archives", len(inputs), layer.Archives)
	}
	stage := filepath.Join(caseDir, nestedStageDir)
	if err := os.Mkdir(stage, 0o755); err != nil {
		return nil, none, err
	}
	sorted := append([]string(nil), inputs...)
	sort.Strings(sorted)
	layerCase := layer.Case(archiveCase, level)
	for group := 0; group < layer.Archives; group++ {
		// Contiguous runs of the sorted inputs, so the volumes of one inner
		// set stay together in one archive of the next layer.
		members := sorted[group*len(sorted)/layer.Archives : (group+1)*len(sorted)/layer.Archives]
		relative, err := inputRelativePaths(members)
		if err != nil {
			return nil, none, err
		}
		name := layer.ArchiveName(level, group+1)
		switch layer.Format {
		case fixture.RAR5:
			args, err := layerCase.RARArgs(filepath.ToSlash(filepath.Join(nestedStageDir, name)), members)
			if err != nil {
				return nil, none, err
			}
			if err := runRAR(ctx, config.DockerBinary, writers.RAR, caseDir, args...); err != nil {
				return nil, none, fmt.Errorf("write %s: %w", name, err)
			}
		case fixture.SevenZip, fixture.Zip:
			if writers.SevenZip == nil {
				return nil, none, fmt.Errorf("a %s layer requires the pinned 7-Zip toolchain", layer.Format)
			}
			args, err := layer.SevenZipArgs(archiveCase, level, "../"+nestedStageDir+"/"+name, relative)
			if err != nil {
				return nil, none, err
			}
			if err := runSevenZip(ctx, config, *writers.SevenZip, caseDir, "input", args...); err != nil {
				return nil, none, fmt.Errorf("write %s: %w", name, err)
			}
		case fixture.Tar:
			if writers.GNUTools == nil {
				return nil, none, fmt.Errorf("a tar layer requires the pinned GNU tools toolchain")
			}
			args, err := layerCase.TarArgs(filepath.ToSlash(filepath.Join(nestedStageDir, name)), "input", relative)
			if err != nil {
				return nil, none, err
			}
			if err := runTarCreate(ctx, config, *writers.GNUTools, caseDir, ".", append([]string{"tar"}, args...)...); err != nil {
				return nil, none, fmt.Errorf("write %s: %w", name, err)
			}
		default:
			return nil, none, fmt.Errorf("no writer for a %s layer", layer.Format)
		}
	}
	archives, err := digestDirectory(stage, caseDir)
	if err != nil {
		return nil, none, err
	}
	if len(archives) < layer.Archives {
		return nil, none, fmt.Errorf("the writers produced %d files for %d archives", len(archives), layer.Archives)
	}
	detail := fixture.NestedLayerDetails{
		Level:       level,
		Format:      layer.Format,
		Compression: layer.Compression,
		Archives:    layer.Archives,
		Files:       len(archives),
		Bytes:       totalDigestBytes(archives),
	}
	if layer.PAR2 {
		if writers.PAR2 == nil {
			return nil, none, fmt.Errorf("a nested PAR2 set requires the PAR2 toolchain")
		}
		args := []string{"create", "-q", fmt.Sprintf("-r%d", fixture.NestedPAR2Percent), fmt.Sprintf("-s%d", par2BlockSize),
			filepath.ToSlash(filepath.Join(nestedStageDir, fixture.NestedPAR2Name(level)))}
		for _, archive := range archives {
			args = append(args, archive.Path)
		}
		if err := runPAR2(ctx, config.DockerBinary, *writers.PAR2, caseDir, args...); err != nil {
			return nil, none, fmt.Errorf("create the nested PAR2 set: %w", err)
		}
		// The damage goes into the middle file, clear of the leading one.
		fault, err := flipArchiveBytes(caseDir, archives[len(archives)/2])
		if err != nil {
			return nil, none, err
		}
		all, err := digestDirectory(stage, caseDir)
		if err != nil {
			return nil, none, err
		}
		for _, file := range all {
			if strings.HasSuffix(file.Path, ".par2") {
				file.Path = filepath.Base(file.Path)
				detail.RecoveryFiles = append(detail.RecoveryFiles, file)
			}
		}
		fault.Path = filepath.Base(fault.Path)
		detail.PAR2RedundancyPercent = fixture.NestedPAR2Percent
		detail.Corruptions = []fixture.CorruptionDetail{fault}
	}
	// The layer's files replace its inputs: the next writer is handed only
	// what this one produced.
	for _, input := range inputs {
		if err := os.Remove(filepath.Join(caseDir, filepath.FromSlash(input))); err != nil {
			return nil, none, err
		}
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, none, err
	}
	var produced []string
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(stage, entry.Name()), filepath.Join(caseDir, "input", entry.Name())); err != nil {
			return nil, none, err
		}
		produced = append(produced, "input/"+entry.Name())
	}
	if err := os.Remove(stage); err != nil {
		return nil, none, err
	}
	sort.Strings(produced)
	return produced, detail, nil
}

// digestDirectory digests every file directly inside dir, in name order,
// with paths relative to caseDir.
func digestDirectory(dir, caseDir string) ([]fixture.FileDigest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var digests []fixture.FileDigest
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected directory %s in %s", entry.Name(), dir)
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		digest, err := hashFile(path)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(caseDir, path)
		if err != nil {
			return nil, err
		}
		digests = append(digests, fixture.FileDigest{Path: filepath.ToSlash(relative), Size: info.Size(), BLAKE3: digest})
	}
	return digests, nil
}

// verifyNestedArchive proves the whole chain from the posted bytes: the outer
// set is extracted with the pinned RAR, then every layer from the outermost
// in is repaired where it carries a PAR2 set and extracted with its own
// pinned reader, and what the innermost layer yields must be the payload.
func verifyNestedArchive(ctx context.Context, config Config, archiveCase fixture.ArchiveCase, writers writerToolchains, caseDir, primary string, expected []fixture.FileDigest) error {
	layers, err := archiveCase.Nested.Layers()
	if err != nil {
		return err
	}
	current, relative, err := newVerificationDir(caseDir, "nested-outer-")
	if err != nil {
		return err
	}
	defer func() { os.RemoveAll(current) }()
	if err := runRAR(ctx, config.DockerBinary, writers.RAR, caseDir, "x", "-idq", "-y", filepath.ToSlash(primary), filepath.ToSlash(relative)+"/"); err != nil {
		return fmt.Errorf("extract the outer archive %s: %w", primary, err)
	}
	for index := len(layers) - 1; index >= 0; index-- {
		layer, level := layers[index], index+1
		if layer.PAR2 {
			if writers.PAR2 == nil {
				return fmt.Errorf("a nested PAR2 set requires the PAR2 toolchain")
			}
			if err := runPAR2(ctx, config.DockerBinary, *writers.PAR2, caseDir, "repair", "-q", filepath.ToSlash(filepath.Join(relative, fixture.NestedPAR2Name(level)))); err != nil {
				return fmt.Errorf("repair nested layer %d: %w", level, err)
			}
		}
		next, nextRelative, err := newVerificationDir(caseDir, fmt.Sprintf("nested-level%d-", level))
		if err != nil {
			return err
		}
		for group := 1; group <= layer.Archives; group++ {
			first, err := nestedPrimary(current, fixture.NestedArchiveStem(level, group))
			if err != nil {
				os.RemoveAll(next)
				return fmt.Errorf("nested layer %d: %w", level, err)
			}
			source := filepath.ToSlash(filepath.Join(relative, first))
			switch layer.Format {
			case fixture.RAR5:
				err = runRAR(ctx, config.DockerBinary, writers.RAR, caseDir, "x", "-idq", "-y", source, filepath.ToSlash(nextRelative)+"/")
			case fixture.SevenZip, fixture.Zip:
				err = runSevenZip(ctx, config, *writers.SevenZip, caseDir, ".", "x", "-y", "-bso0", "-bsp0", "-o/work/"+filepath.ToSlash(nextRelative), source)
			case fixture.Tar:
				err = runGNUTools(ctx, config, *writers.GNUTools, caseDir, ".", "tar", "--extract", "--file", source, "--directory", filepath.ToSlash(nextRelative))
			default:
				err = fmt.Errorf("no reader for a %s layer", layer.Format)
			}
			if err != nil {
				os.RemoveAll(next)
				return fmt.Errorf("extract nested layer %d archive %s: %w", level, first, err)
			}
		}
		os.RemoveAll(current)
		current, relative = next, nextRelative
	}
	return checkExtractedFiles(current, expected, "nested chain")
}

// nestedPrimary names the file a reader opens for one nested archive: the
// archive itself, or the first of its volumes in name order.
func nestedPrimary(dir, stem string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stem+".") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("the layer above did not contain %s", stem)
	}
	sort.Strings(names)
	return names[0], nil
}
