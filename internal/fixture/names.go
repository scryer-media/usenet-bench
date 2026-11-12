package fixture

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DisplayName describes the workload from its declared axes, never from its
// opaque identifier. IDs remain the stable keys used by plans and scripts.
func (c ArchiveCase) DisplayName() string {
	format := map[ArchiveFormat]string{RAR4: "RAR4", RAR5: "RAR5", SevenZip: "7-Zip", Tar: "TAR", XZCompressed: "XZ", Zip: "ZIP", Media: "Direct media"}[c.ArchiveFormat]
	if format == "" {
		format = "Custom workload"
	}
	parts := []string{format}
	writer := c.ArchiveWriter
	if writer == "" && (c.ArchiveFormat == RAR4 || c.ArchiveFormat == RAR5) {
		writer = c.GeneratorToolchain
	}
	if writer != "" && c.ArchiveFormat != Media {
		writer = strings.NewReplacer("rarlab-", "RAR ", "sevenzip-", "7-Zip ", "gnutools-bookworm", "GNU tools (Debian Bookworm)").Replace(writer)
		parts[0] += " (writer " + writer + ")"
	}
	compression := map[Compression]string{Store: "stored without compression", Normal: "normal compression", Best: "maximum compression", GzipCompression: "gzip compression", XZCompression: "XZ compression", DeflateCompression: "Deflate compression", LZMACompression: "LZMA compression", LZMA2Compression: "LZMA2 compression", PPMdCompression: "PPMd compression", BZip2Compression: "bzip2 compression"}[c.Compression]
	if compression != "" {
		parts = append(parts, compression)
	}
	if c.Solid {
		parts = append(parts, "solid archive")
	}
	switch c.Encryption {
	case DataEncryption:
		parts = append(parts, "encrypted contents")
	case HeaderEncryption:
		parts = append(parts, "encrypted contents and filenames")
	case NoEncryption:
		parts = append(parts, "unencrypted")
	}
	repair := map[RepairProfile]string{PAR2LightRepairProfile: "PAR2 repair, light damage", PAR2HeavyRepairProfile: "PAR2 repair, heavy damage", PAR2HeavyWithheldProfile: "PAR2 repair, missing server articles", PAR2ArticlesLightProfile: "PAR2 repair, a few lost articles", PAR2ArticlesHeavyProfile: "PAR2 repair, many lost articles", PAR3ArticlesLightProfile: "PAR3 repair, a few lost articles", PAR3FFTArticlesHeavyProfile: "PAR3 FFT repair, many lost articles", RARRecoveryVolumeLightProfile: "RAR recovery-volume repair, light damage", RARRecoveryVolumeHeavyProfile: "RAR recovery-volume repair, heavy damage", RARRecoveryRecordLightProfile: "RAR recovery-record repair, a few lost articles", PAR3LightRepairProfile: "PAR3 repair, light damage", PAR3HeavyWithheldProfile: "PAR3 repair, missing server articles", PAR3FFTHeavyWithheldProfile: "PAR3 FFT repair, missing server articles", PAR3InsideLightProfile: "PAR3 repair inside the archive, light damage", PAR2ScatteredLightProfile: "PAR2 repair, light scattered damage", PAR2ScatteredHeavyProfile: "PAR2 repair, heavy scattered damage", PAR3FFTScatteredLightProfile: "PAR3 FFT repair, light scattered damage", PAR3FFTScatteredHeavyProfile: "PAR3 FFT repair, heavy scattered damage", PAR3FFTPastPAR2CapProfile: "PAR3 FFT repair past the PAR2 block cap"}[c.RepairProfile]
	if repair != "" {
		parts = append(parts, repair)
	}
	if c.PayloadLayout == BluRayDiscPayloadLayout {
		parts = append(parts, "Blu-ray directory tree")
	}
	if c.PayloadLayout == Zip64LargePayloadLayout {
		parts = append(parts, "large-file ZIP64 layout")
	}
	if c.InnerArchive != "" {
		parts = append(parts, "nested "+strings.ReplaceAll(string(c.InnerArchive), "-", " "))
	}
	if layers, err := c.Nested.Layers(); err == nil && len(layers) > 0 {
		broad, repair := 0, false
		for _, layer := range layers {
			broad = max(broad, layer.Archives)
			repair = repair || layer.PAR2
		}
		text := fmt.Sprintf("nested %d containers deep", len(layers)+1)
		if broad > 1 {
			text += fmt.Sprintf(", up to %d sibling archives", broad)
		}
		if repair {
			text += ", PAR2 repair inside"
		}
		// Lanes of one depth differ only in what the layers are, so the name
		// spells them out, innermost first.
		var inside []string
		for _, layer := range layers {
			described := fmt.Sprintf("%s %s", layer.Format, layer.Compression)
			if layer.VolumeSize != "" {
				described += " in " + layer.VolumeSize + " volumes"
			}
			if layer.Archives > 1 {
				described += fmt.Sprintf(" ×%d", layer.Archives)
			}
			if layer.PAR2 {
				described += " with PAR2"
			}
			inside = append(inside, described)
		}
		text += " (innermost first: " + strings.Join(inside, ", ") + ")"
		parts = append(parts, text)
	}
	if c.QuickOpen {
		parts = append(parts, "quick-open index")
	}
	if c.TextCompression {
		parts = append(parts, "PPMd text compression")
	}
	if c.ZipStructure != "" {
		parts = append(parts, strings.ReplaceAll(string(c.ZipStructure), "-", " "))
	}
	if c.Sidecar == SFVSidecar {
		parts = append(parts, "SFV checksums")
	}
	if c.Encoding == UUEncodeEncoding {
		parts = append(parts, "uuencoded articles")
	}
	if c.NZBOrder == ScatteredNZBOrder {
		parts = append(parts, "shuffled file order")
	}
	if c.Payload != "" {
		parts = append(parts, string(c.Payload)+" payload")
	}
	if c.FileCount > 0 {
		parts = append(parts, fmt.Sprintf("%d payload files", c.FileCount))
	}
	if c.BytesPerFile != "" {
		if size, err := ByteSize(c.BytesPerFile); err == nil {
			parts = append(parts, fmt.Sprintf("%d bytes per file", size))
		}
	}
	if shape := c.RecoveryShape; shape.Declared() && (c.RepairProfile.UsesRARRecoveryVolumes() || c.RepairProfile.UsesRARRecoveryRecord()) {
		parts = append(parts, fmt.Sprintf("recovery sized from a PAR2 post with %d recovery blocks over %d-byte slices", shape.RecoveryBlocks, shape.SliceBytes), fmt.Sprintf("%d-byte articles", shape.ArticleBytes))
	} else if shape.Declared() {
		parts = append(parts, fmt.Sprintf("recovery in %d-byte slices with %d recovery blocks", shape.SliceBytes, shape.RecoveryBlocks), fmt.Sprintf("%d-byte articles", shape.ArticleBytes))
	}
	if c.VolumeSize != "" {
		if size, err := ByteSize(c.VolumeSize); err == nil {
			parts = append(parts, fmt.Sprintf("%d-byte volumes", size))
		}
	}
	return strings.Join(parts, " · ")
}

// Including the derived label in serialized cases makes saved catalogs and
// manifests readable without shipping a second, potentially stale name table.
func (c ArchiveCase) MarshalJSON() ([]byte, error) {
	type plain ArchiveCase
	return json.Marshal(struct {
		plain
		DisplayName string `json:"display_name"`
	}{plain(c), c.DisplayName()})
}
