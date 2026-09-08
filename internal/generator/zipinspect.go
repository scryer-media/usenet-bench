package generator

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// The ZIP structures this reads. A client's zip64 support is not a property of
// the switch the writer was given, it is a property of the bytes the writer
// produced, so the zip lanes are parsed back off disk and the manifest records
// what was found. A lane whose archive does not carry the structure its matrix
// set declared is refused before it is ever posted, because a zip64 lane that
// quietly degraded to an ordinary zip would report full zip64 support for a
// client that has none.
const (
	zipEndOfCentralDirectorySignature          uint32 = 0x06054b50
	zip64EndOfCentralDirectorySignature        uint32 = 0x06064b50
	zip64EndOfCentralDirectoryLocatorSignature uint32 = 0x07064b50
	zipCentralFileHeaderSignature              uint32 = 0x02014b50
	zipLocalFileHeaderSignature                uint32 = 0x04034b50

	zipEndOfCentralDirectoryLength   = 22
	zip64EndOfCentralDirectoryLength = 56
	zip64LocatorLength               = 20
	zipCentralFileHeaderLength       = 46
	zipLocalFileHeaderLength         = 30

	// zip64ExtraFieldHeaderID is the extra field that carries the 64-bit sizes
	// and offset when the 32-bit fields cannot.
	zip64ExtraFieldHeaderID uint16 = 0x0001
	// zipDataDescriptorFlag is general purpose bit 3: the member's CRC and
	// sizes trail its data instead of preceding it, because the writer could
	// not seek back to patch the local header it had already emitted.
	zipDataDescriptorFlag uint16 = 1 << 3

	// zip32Max and zip16Max are the values a 32- or 16-bit ZIP field carries
	// when the real number lives in a zip64 record instead.
	zip32Max = 0xFFFFFFFF
	zip16Max = 0xFFFF

	// zipMaxCommentLength bounds the backwards search for the end-of-central-
	// directory record: its comment length field is 16 bits wide.
	zipMaxCommentLength = 0xFFFF
)

// inspectZipStructure parses a posted zip and reports what it contains. It is
// deliberately a reader of its own rather than archive/zip: the standard
// library resolves zip64 for the caller and hides exactly the distinctions
// these lanes exist to make — whether the zip64 end-of-central-directory
// record is there at all, and whether a local header set general purpose
// bit 3.
func inspectZipStructure(path, inspectedFile string) (fixture.ZipStructureDetails, error) {
	file, err := os.Open(path)
	if err != nil {
		return fixture.ZipStructureDetails{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fixture.ZipStructureDetails{}, err
	}
	details := fixture.ZipStructureDetails{InspectedFile: inspectedFile}
	size := info.Size()

	eocdOffset, eocd, err := readEndOfCentralDirectory(file, size)
	if err != nil {
		return fixture.ZipStructureDetails{}, fmt.Errorf("%s: %w", inspectedFile, err)
	}
	centralDirectoryDisk := uint32(binary.LittleEndian.Uint16(eocd[6:]))
	entries := int64(binary.LittleEndian.Uint16(eocd[10:]))
	centralDirectorySize := int64(binary.LittleEndian.Uint32(eocd[12:]))
	centralDirectoryOffset := int64(binary.LittleEndian.Uint32(eocd[16:]))
	sawZip64Marker := binary.LittleEndian.Uint16(eocd[10:]) == zip16Max ||
		binary.LittleEndian.Uint32(eocd[12:]) == zip32Max ||
		binary.LittleEndian.Uint32(eocd[16:]) == zip32Max

	if locator, ok, err := readZip64Locator(file, eocdOffset); err != nil {
		return fixture.ZipStructureDetails{}, fmt.Errorf("%s: %w", inspectedFile, err)
	} else if ok {
		details.Zip64EOCDLocator = true
		record, found, err := readZip64EndOfCentralDirectory(file, size, int64(binary.LittleEndian.Uint64(locator[8:])))
		if err != nil {
			return fixture.ZipStructureDetails{}, fmt.Errorf("%s: %w", inspectedFile, err)
		}
		if found {
			details.Zip64EOCDRecord = true
			centralDirectoryDisk = binary.LittleEndian.Uint32(record[20:])
			entries = int64(binary.LittleEndian.Uint64(record[32:]))
			centralDirectorySize = int64(binary.LittleEndian.Uint64(record[40:]))
			centralDirectoryOffset = int64(binary.LittleEndian.Uint64(record[48:]))
		}
	}
	if sawZip64Marker && !details.Zip64EOCDRecord {
		// This is not a hypothetical. Info-ZIP `zip -fz` writing into a pipe
		// emits exactly this: an end-of-central-directory record whose central
		// directory offset is 0xFFFFFFFF, and no zip64 record for it to point
		// at. Both pinned readers refuse the result, so the corpus refuses it
		// too rather than posting an archive no client can open.
		return fixture.ZipStructureDetails{}, fmt.Errorf(
			"%s: the end-of-central-directory record marks a field as 64-bit but the archive carries no zip64 end-of-central-directory record; the writer produced a zip no reader can open",
			inspectedFile)
	}

	if err := readCentralDirectory(file, size, centralDirectoryOffset, centralDirectorySize, entries, centralDirectoryDisk, &details); err != nil {
		return fixture.ZipStructureDetails{}, fmt.Errorf("%s: %w", inspectedFile, err)
	}
	details.Zip64 = details.Zip64EOCDRecord || details.Zip64EOCDLocator || details.Zip64ExtraFieldEntries > 0
	return details, nil
}

// readEndOfCentralDirectory finds the last end-of-central-directory record
// whose comment length reaches exactly the end of the file, which is what
// distinguishes the record from the same four bytes occurring inside stored
// member data.
func readEndOfCentralDirectory(file *os.File, size int64) (int64, []byte, error) {
	tailLength := int64(zipEndOfCentralDirectoryLength + zipMaxCommentLength)
	if tailLength > size {
		tailLength = size
	}
	if tailLength < zipEndOfCentralDirectoryLength {
		return 0, nil, fmt.Errorf("the file is too short to be a zip archive")
	}
	tail := make([]byte, tailLength)
	if _, err := file.ReadAt(tail, size-tailLength); err != nil {
		return 0, nil, err
	}
	for index := len(tail) - zipEndOfCentralDirectoryLength; index >= 0; index-- {
		if binary.LittleEndian.Uint32(tail[index:]) != zipEndOfCentralDirectorySignature {
			continue
		}
		comment := int(binary.LittleEndian.Uint16(tail[index+20:]))
		if index+zipEndOfCentralDirectoryLength+comment != len(tail) {
			continue
		}
		return size - tailLength + int64(index), tail[index : index+zipEndOfCentralDirectoryLength], nil
	}
	return 0, nil, fmt.Errorf("the archive has no end-of-central-directory record")
}

func readZip64Locator(file *os.File, eocdOffset int64) ([]byte, bool, error) {
	if eocdOffset < zip64LocatorLength {
		return nil, false, nil
	}
	locator := make([]byte, zip64LocatorLength)
	if _, err := file.ReadAt(locator, eocdOffset-zip64LocatorLength); err != nil {
		return nil, false, err
	}
	if binary.LittleEndian.Uint32(locator) != zip64EndOfCentralDirectoryLocatorSignature {
		return nil, false, nil
	}
	return locator, true, nil
}

func readZip64EndOfCentralDirectory(file *os.File, size, offset int64) ([]byte, bool, error) {
	if offset < 0 || offset+zip64EndOfCentralDirectoryLength > size {
		return nil, false, nil
	}
	record := make([]byte, zip64EndOfCentralDirectoryLength)
	if _, err := file.ReadAt(record, offset); err != nil {
		return nil, false, err
	}
	if binary.LittleEndian.Uint32(record) != zip64EndOfCentralDirectorySignature {
		return nil, false, nil
	}
	return record, true, nil
}

// readCentralDirectory walks every entry and, for the entries whose data lives
// in this same file, reads their local headers too. A spanned set keeps most of
// its local headers on earlier parts, so those are counted as not inspected
// rather than read from the wrong offset.
func readCentralDirectory(file *os.File, size, offset, length, entries int64, centralDirectoryDisk uint32, details *fixture.ZipStructureDetails) error {
	if entries < 0 || offset < 0 || length < 0 || offset+length > size {
		return fmt.Errorf("the central directory is not inside the archive (offset %d, length %d, file %d bytes)", offset, length, size)
	}
	directory := make([]byte, length)
	if length > 0 {
		if _, err := file.ReadAt(directory, offset); err != nil {
			return err
		}
	}
	position := 0
	for index := int64(0); index < entries; index++ {
		if position+zipCentralFileHeaderLength > len(directory) {
			return fmt.Errorf("the central directory ends after %d of its %d entries", index, entries)
		}
		header := directory[position:]
		if binary.LittleEndian.Uint32(header) != zipCentralFileHeaderSignature {
			return fmt.Errorf("central directory entry %d has no central file header signature", index)
		}
		uncompressed32 := binary.LittleEndian.Uint32(header[24:])
		nameLength := int(binary.LittleEndian.Uint16(header[28:]))
		extraLength := int(binary.LittleEndian.Uint16(header[30:]))
		commentLength := int(binary.LittleEndian.Uint16(header[32:]))
		diskStart := binary.LittleEndian.Uint16(header[34:])
		localOffset32 := binary.LittleEndian.Uint32(header[42:])
		entryLength := zipCentralFileHeaderLength + nameLength + extraLength + commentLength
		if position+entryLength > len(directory) {
			return fmt.Errorf("central directory entry %d runs past the end of the directory", index)
		}
		extra := directory[position+zipCentralFileHeaderLength+nameLength : position+zipCentralFileHeaderLength+nameLength+extraLength]
		zip64, err := parseZip64CentralExtra(extra, uncompressed32, binary.LittleEndian.Uint32(header[20:]), localOffset32, diskStart)
		if err != nil {
			return fmt.Errorf("central directory entry %d: %w", index, err)
		}
		details.Entries++
		if zip64.present {
			details.Zip64ExtraFieldEntries++
		}
		if zip64.uncompressed > details.LargestMemberBytes {
			details.LargestMemberBytes = zip64.uncompressed
		}
		if zip64.diskStart == centralDirectoryDisk {
			descriptor, err := localHeaderUsesDataDescriptor(file, size, zip64.localHeaderOffset, index)
			if err != nil {
				return err
			}
			details.LocalHeadersInspected++
			if descriptor {
				details.DataDescriptorEntries++
			}
		}
		position += entryLength
	}
	return nil
}

// zip64CentralFields is one central directory entry's sizes and offset with
// any zip64 extra field resolved.
type zip64CentralFields struct {
	present           bool
	uncompressed      int64
	localHeaderOffset int64
	diskStart         uint32
}

// parseZip64CentralExtra resolves the 32-bit fields against a zip64 extra
// field if there is one. The extra field carries only the members whose 32-bit
// counterpart holds the marker value, in a fixed order, so which of them are
// present depends on the header that precedes it.
func parseZip64CentralExtra(extra []byte, uncompressed32, compressed32, localOffset32 uint32, diskStart16 uint16) (zip64CentralFields, error) {
	fields := zip64CentralFields{
		uncompressed:      int64(uncompressed32),
		localHeaderOffset: int64(localOffset32),
		diskStart:         uint32(diskStart16),
	}
	for position := 0; position+4 <= len(extra); {
		headerID := binary.LittleEndian.Uint16(extra[position:])
		dataLength := int(binary.LittleEndian.Uint16(extra[position+2:]))
		if position+4+dataLength > len(extra) {
			return fields, fmt.Errorf("extra field %#04x runs past the end of the header", headerID)
		}
		data := extra[position+4 : position+4+dataLength]
		position += 4 + dataLength
		if headerID != zip64ExtraFieldHeaderID {
			continue
		}
		fields.present = true
		cursor := 0
		take64 := func() (int64, bool) {
			if cursor+8 > len(data) {
				return 0, false
			}
			value := int64(binary.LittleEndian.Uint64(data[cursor:]))
			cursor += 8
			return value, true
		}
		if uncompressed32 == zip32Max {
			value, ok := take64()
			if !ok {
				return fields, fmt.Errorf("the zip64 extra field is too short for its uncompressed size")
			}
			fields.uncompressed = value
		}
		if compressed32 == zip32Max {
			if _, ok := take64(); !ok {
				return fields, fmt.Errorf("the zip64 extra field is too short for its compressed size")
			}
		}
		if localOffset32 == zip32Max {
			value, ok := take64()
			if !ok {
				return fields, fmt.Errorf("the zip64 extra field is too short for its local header offset")
			}
			fields.localHeaderOffset = value
		}
		if diskStart16 == zip16Max {
			if cursor+4 > len(data) {
				return fields, fmt.Errorf("the zip64 extra field is too short for its starting disk")
			}
			fields.diskStart = binary.LittleEndian.Uint32(data[cursor:])
		}
	}
	return fields, nil
}

// localHeaderUsesDataDescriptor reports whether this member's local header sets
// general purpose bit 3.
func localHeaderUsesDataDescriptor(file *os.File, size, offset int64, index int64) (bool, error) {
	if offset < 0 || offset+zipLocalFileHeaderLength > size {
		return false, fmt.Errorf("central directory entry %d points at local header offset %d, which is outside the archive", index, offset)
	}
	header := make([]byte, zipLocalFileHeaderLength)
	if _, err := file.ReadAt(header, offset); err != nil {
		return false, err
	}
	if binary.LittleEndian.Uint32(header) != zipLocalFileHeaderSignature {
		return false, fmt.Errorf("central directory entry %d points at offset %d, which is not a local file header", index, offset)
	}
	return binary.LittleEndian.Uint16(header[6:])&zipDataDescriptorFlag != 0, nil
}

// requireZipStructure refuses a fixture whose archive does not carry the
// structure its matrix set declared. The check is against the parsed archive,
// never against the switch the writer was given, so a writer that silently
// stopped honouring a flag fails the corpus build instead of producing a lane
// that measures the wrong thing.
func requireZipStructure(archiveCase fixture.ArchiveCase, details fixture.ZipStructureDetails) error {
	// The size check comes first for the large lane. A member shrunk below
	// 4 GiB makes the writer stop producing zip64 records at all, so the
	// generic check below would report the symptom while this one names the
	// cause.
	if archiveCase.ZipStructure == fixture.LargeZip64Structure && details.LargestMemberBytes <= zip32Max {
		return fmt.Errorf(
			"fixture %q declares the %q zip structure, but the largest member in %s is %d bytes, which fits in the 32-bit size fields: this lane exists for a member past 4 GiB, so regenerate it without a reduced --zip64-large-file-bytes",
			archiveCase.ID, archiveCase.ZipStructure, details.InspectedFile, details.LargestMemberBytes)
	}
	if archiveCase.ZipStructure.RequiresZip64() {
		if !details.Zip64EOCDRecord || !details.Zip64EOCDLocator {
			return fmt.Errorf(
				"fixture %q declares the %q zip structure, but %s carries no zip64 end-of-central-directory record and locator: the writer produced an ordinary zip",
				archiveCase.ID, archiveCase.ZipStructure, details.InspectedFile)
		}
		if details.Zip64ExtraFieldEntries < 1 {
			return fmt.Errorf(
				"fixture %q declares the %q zip structure, but no entry in %s carries a zip64 extra field",
				archiveCase.ID, archiveCase.ZipStructure, details.InspectedFile)
		}
	}
	if archiveCase.ZipStructure == fixture.ForcedZip64Structure && details.Zip64ExtraFieldEntries != details.Entries {
		return fmt.Errorf(
			"fixture %q declares the %q zip structure, but only %d of the %d entries in %s carry a zip64 extra field: -fz puts one on every entry, so the writer did not honour it",
			archiveCase.ID, archiveCase.ZipStructure, details.Zip64ExtraFieldEntries, details.Entries, details.InspectedFile)
	}
	if archiveCase.ZipStructure.RequiresDataDescriptors() {
		if details.Entries < 1 || details.LocalHeadersInspected != details.Entries {
			return fmt.Errorf(
				"fixture %q declares the %q zip structure, but only %d of its %d local headers are in %s",
				archiveCase.ID, archiveCase.ZipStructure, details.LocalHeadersInspected, details.Entries, details.InspectedFile)
		}
		if details.DataDescriptorEntries != details.Entries {
			return fmt.Errorf(
				"fixture %q declares the %q zip structure, but only %d of the %d local headers in %s set general purpose bit 3: the writer's output was seekable and it patched the sizes back into the headers, so this is an ordinary zip",
				archiveCase.ID, archiveCase.ZipStructure, details.DataDescriptorEntries, details.Entries, details.InspectedFile)
		}
	}
	return nil
}
