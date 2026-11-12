package fixture

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// NestedChain declares the containers between the payload and the posted
// archive, innermost first, separated by ">". Posts do nest: a scene release
// repacked into a second RAR set, a zip of RAR parts, a tarball stored in a
// RAR set. The census reads NZBs and PAR2 packets and cannot see inside a
// volume, so these lanes are stress shapes for a client's unpack graph, not
// shapes with a measured frequency.
//
// Each layer is "format:compression[:volume][*archives][+par2]":
//
//	rar5:store:15m        one stored RAR5 set cut into 15 MiB volumes
//	zip:deflate*6         the layer's input files spread over six sibling zips
//	rar5:store:15m+par2   a PAR2 set over the layer's files, one of them damaged
//
// The posted archive is the outermost container and is not part of the chain,
// so a chain of four layers is a post five containers deep.
type NestedChain string

// NestedLayer is one parsed layer of a NestedChain.
type NestedLayer struct {
	Format      ArchiveFormat
	Compression Compression
	VolumeSize  string
	// Archives is how many sibling archives the layer's inputs are divided
	// among. More than one makes the post broad as well as deep.
	Archives int
	// PAR2 writes a PAR2 set over the layer's archive files and damages one
	// of them, so the repair can only happen after the layer above is
	// unpacked.
	PAR2 bool
}

// NestedPAR2Percent is the redundancy of a nested layer's PAR2 set.
const NestedPAR2Percent = 10

// Layers parses the chain. An empty chain has no layers.
func (n NestedChain) Layers() ([]NestedLayer, error) {
	if strings.TrimSpace(string(n)) == "" {
		return nil, nil
	}
	var layers []NestedLayer
	for _, token := range strings.Split(string(n), ">") {
		token = strings.TrimSpace(token)
		layer := NestedLayer{Archives: 1}
		if rest, ok := strings.CutSuffix(token, "+par2"); ok {
			layer.PAR2 = true
			token = rest
		}
		if rest, count, ok := strings.Cut(token, "*"); ok {
			archives, err := strconv.Atoi(count)
			if err != nil || archives < 2 {
				return nil, fmt.Errorf("nested layer %q has invalid archive count %q", token, count)
			}
			layer.Archives = archives
			token = rest
		}
		fields := strings.Split(token, ":")
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("nested layer %q is not format:compression[:volume]", token)
		}
		layer.Format = ArchiveFormat(fields[0])
		layer.Compression = Compression(fields[1])
		if len(fields) == 3 {
			layer.VolumeSize = fields[2]
			if _, err := ByteSize(layer.VolumeSize); err != nil {
				return nil, fmt.Errorf("nested layer %q has invalid volume size: %w", token, err)
			}
		}
		if err := layer.validate(); err != nil {
			return nil, err
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

func (l NestedLayer) validate() error {
	ok := false
	switch l.Format {
	case RAR5:
		ok = l.Compression == Store || l.Compression == Normal
	case SevenZip:
		ok = l.Compression == Store || l.Compression == Normal
	case Zip:
		ok = l.Compression == Store || l.Compression == DeflateCompression
	case Tar:
		ok = l.Compression == Store || l.Compression == GzipCompression || l.Compression == XZCompression
	default:
		return fmt.Errorf("nested layer format %q is not one of rar5, 7z, zip, tar", l.Format)
	}
	if !ok {
		return fmt.Errorf("nested %s layer cannot use compression %q", l.Format, l.Compression)
	}
	if l.VolumeSize != "" && l.Format != RAR5 && l.Format != SevenZip {
		return fmt.Errorf("nested %s layer cannot be cut into volumes", l.Format)
	}
	return nil
}

// Compressed reports whether a reader has to decode the layer's members
// rather than copy them out.
func (l NestedLayer) Compressed() bool {
	return l.Compression != Store
}

// ArchiveName is the file name of one of the layer's archives before the
// writer adds any volume suffix. level counts from one at the innermost
// layer and index from one within the layer.
func (l NestedLayer) ArchiveName(level, index int) string {
	stem := NestedArchiveStem(level, index)
	switch l.Format {
	case RAR5:
		return stem + ".rar"
	case SevenZip:
		return stem + ".7z"
	case Zip:
		return stem + ".zip"
	default:
		switch l.Compression {
		case GzipCompression:
			return stem + ".tar.gz"
		case XZCompression:
			return stem + ".tar.xz"
		}
		return stem + ".tar"
	}
}

// NestedArchiveStem is the common prefix of every file one nested archive is
// written as, volumes included.
func NestedArchiveStem(level, index int) string {
	return fmt.Sprintf("level%d-%02d", level, index)
}

// NestedPAR2Name is the index file of a nested layer's PAR2 set.
func NestedPAR2Name(level int) string {
	return fmt.Sprintf("level%d.par2", level)
}

// Case is the layer as a writer sees it: an unencrypted, non-solid archive
// case of the layer's own format, so a nested layer is written with exactly
// the arguments a posted archive of that format gets.
func (l NestedLayer) Case(parent ArchiveCase, level int) ArchiveCase {
	return ArchiveCase{
		ID:            fmt.Sprintf("%s nested layer %d", parent.ID, level),
		ArchiveFormat: l.Format,
		Compression:   l.Compression,
		Encryption:    NoEncryption,
		VolumeSize:    l.VolumeSize,
	}
}

// SevenZipArgs is the 7-Zip invocation for a 7z or zip layer. A posted 7z set
// must declare a volume size; a nested one need not, so the layer builds its
// own arguments around the same explicit codec selection.
func (l NestedLayer) SevenZipArgs(parent ArchiveCase, level int, archive string, inputs []string) ([]string, error) {
	if l.Format != SevenZip && l.Format != Zip {
		return nil, fmt.Errorf("nested %s layer is not written by 7-Zip", l.Format)
	}
	if archive == "" || len(inputs) == 0 {
		return nil, fmt.Errorf("nested layer %d has no archive path or no inputs", level)
	}
	args := []string{"a", "-y", "-bso0", "-bsp0"}
	if l.Format == Zip {
		args = append(args, "-tzip")
	} else {
		args = append(args, "-t7z")
	}
	method, err := l.Case(parent, level).sevenZipMethodArgs()
	if err != nil {
		return nil, err
	}
	args = append(args, method...)
	if l.Format == SevenZip {
		args = append(args, "-ms=off")
	}
	if l.VolumeSize != "" {
		args = append(args, "-v"+l.VolumeSize)
	}
	args = append(args, filepath.ToSlash(archive))
	for _, input := range inputs {
		args = append(args, filepath.ToSlash(input))
	}
	return args, nil
}

// Depth is how many containers deep the post is: the posted archive plus
// every nested layer.
func (c ArchiveCase) Depth() int {
	layers, err := c.Nested.Layers()
	if err != nil || len(layers) == 0 {
		return 1
	}
	return len(layers) + 1
}

func (s FixtureSet) validateNested() error {
	layers, err := s.Nested.Layers()
	if err != nil {
		return fmt.Errorf("fixture set %q: %w", s.ID, err)
	}
	if len(layers) == 0 {
		return nil
	}
	if s.InnerArchive != NoInnerArchive {
		return fmt.Errorf("fixture set %q cannot set both nested and inner_archive", s.ID)
	}
	// The nested RAR layers are written by the set's own RARLAB image, and
	// the generator unpacks the posted archive with it to prove the chain.
	if s.ArchiveFormat != RAR5 {
		return fmt.Errorf("fixture set %q cannot nest: only a RAR5 set carries nested layers", s.ID)
	}
	layout := s.PayloadLayout
	if layout != "" && layout != UniformPayloadLayout {
		return fmt.Errorf("fixture set %q cannot nest a %q payload", s.ID, layout)
	}
	for _, encryption := range s.Encryptions {
		if encryption != NoEncryption {
			return fmt.Errorf("fixture set %q cannot nest inside an encrypted archive", s.ID)
		}
	}
	files := s.FileCount
	for index, layer := range layers {
		if layer.Archives > files {
			return fmt.Errorf("fixture set %q nested layer %d spreads %d files over %d archives", s.ID, index+1, files, layer.Archives)
		}
		// Every later layer is handed this layer's archive files; a layer cut
		// into volumes hands on more than its archive count, never fewer.
		files = layer.Archives
		if layer.PAR2 && layer.VolumeSize == "" && layer.Archives < 2 {
			return fmt.Errorf("fixture set %q nested layer %d needs volumes or sibling archives for its PAR2 set to repair", s.ID, index+1)
		}
	}
	return nil
}
