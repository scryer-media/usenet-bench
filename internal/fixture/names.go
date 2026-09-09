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
	repair := map[RepairProfile]string{PAR2LightRepairProfile: "PAR2 repair, light damage", PAR2HeavyRepairProfile: "PAR2 repair, heavy damage", PAR2HeavyWithheldProfile: "PAR2 repair, missing server articles", RARRecoveryVolumeLightProfile: "RAR recovery-volume repair, light damage", RARRecoveryVolumeHeavyProfile: "RAR recovery-volume repair, heavy damage"}[c.RepairProfile]
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
