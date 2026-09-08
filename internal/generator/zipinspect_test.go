package generator

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// testZipEntry is one member of a hand-built archive. The zips these tests
// read are assembled byte by byte rather than with archive/zip, because the
// distinctions the inspector exists to make — a zip64 record that is missing,
// a local header that does or does not set bit 3 — are exactly the ones a
// writing library makes for you.
type testZipEntry struct {
	name           string
	data           []byte
	dataDescriptor bool
	zip64Extra     bool
}

type testZipOptions struct {
	entries []testZipEntry
	// zip64Records writes the zip64 end-of-central-directory record and its
	// locator.
	zip64Records bool
	// truncatedZip64 marks the end-of-central-directory record's fields as
	// 64-bit without writing the record that would hold them, which is what
	// Info-ZIP produces when it is asked to force zip64 into a pipe.
	truncatedZip64 bool
}

func buildTestZip(options testZipOptions) []byte {
	var archive []byte
	offsets := make([]int64, len(options.entries))
	for index, entry := range options.entries {
		offsets[index] = int64(len(archive))
		var flags uint16
		if entry.dataDescriptor {
			flags |= zipDataDescriptorFlag
		}
		size := uint32(len(entry.data))
		sum := crc32.ChecksumIEEE(entry.data)
		var extra []byte
		if entry.zip64Extra {
			extra = make([]byte, 20)
			binary.LittleEndian.PutUint16(extra, zip64ExtraFieldHeaderID)
			binary.LittleEndian.PutUint16(extra[2:], 16)
			binary.LittleEndian.PutUint64(extra[4:], uint64(size))
			binary.LittleEndian.PutUint64(extra[12:], uint64(size))
			size = zip32Max
		}
		header := make([]byte, zipLocalFileHeaderLength)
		binary.LittleEndian.PutUint32(header, zipLocalFileHeaderSignature)
		binary.LittleEndian.PutUint16(header[4:], 45)
		binary.LittleEndian.PutUint16(header[6:], flags)
		binary.LittleEndian.PutUint32(header[14:], sum)
		binary.LittleEndian.PutUint32(header[18:], size)
		binary.LittleEndian.PutUint32(header[22:], size)
		binary.LittleEndian.PutUint16(header[26:], uint16(len(entry.name)))
		binary.LittleEndian.PutUint16(header[28:], uint16(len(extra)))
		archive = append(archive, header...)
		archive = append(archive, entry.name...)
		archive = append(archive, extra...)
		archive = append(archive, entry.data...)
		if entry.dataDescriptor {
			descriptor := make([]byte, 16)
			binary.LittleEndian.PutUint32(descriptor, 0x08074b50)
			binary.LittleEndian.PutUint32(descriptor[4:], sum)
			binary.LittleEndian.PutUint32(descriptor[8:], uint32(len(entry.data)))
			binary.LittleEndian.PutUint32(descriptor[12:], uint32(len(entry.data)))
			archive = append(archive, descriptor...)
		}
	}

	centralDirectoryOffset := int64(len(archive))
	for index, entry := range options.entries {
		var flags uint16
		if entry.dataDescriptor {
			flags |= zipDataDescriptorFlag
		}
		size := uint32(len(entry.data))
		offset := uint32(offsets[index])
		var extra []byte
		if entry.zip64Extra {
			extra = make([]byte, 32)
			binary.LittleEndian.PutUint16(extra, zip64ExtraFieldHeaderID)
			binary.LittleEndian.PutUint16(extra[2:], 28)
			binary.LittleEndian.PutUint64(extra[4:], uint64(len(entry.data)))
			binary.LittleEndian.PutUint64(extra[12:], uint64(len(entry.data)))
			binary.LittleEndian.PutUint64(extra[20:], uint64(offsets[index]))
			binary.LittleEndian.PutUint32(extra[28:], 0)
			size = zip32Max
			offset = zip32Max
		}
		header := make([]byte, zipCentralFileHeaderLength)
		binary.LittleEndian.PutUint32(header, zipCentralFileHeaderSignature)
		binary.LittleEndian.PutUint16(header[8:], flags)
		binary.LittleEndian.PutUint32(header[16:], crc32.ChecksumIEEE(entry.data))
		binary.LittleEndian.PutUint32(header[20:], size)
		binary.LittleEndian.PutUint32(header[24:], size)
		binary.LittleEndian.PutUint16(header[28:], uint16(len(entry.name)))
		binary.LittleEndian.PutUint16(header[30:], uint16(len(extra)))
		binary.LittleEndian.PutUint32(header[42:], offset)
		archive = append(archive, header...)
		archive = append(archive, entry.name...)
		archive = append(archive, extra...)
	}
	centralDirectorySize := int64(len(archive)) - centralDirectoryOffset

	if options.zip64Records {
		zip64Offset := int64(len(archive))
		record := make([]byte, zip64EndOfCentralDirectoryLength)
		binary.LittleEndian.PutUint32(record, zip64EndOfCentralDirectorySignature)
		binary.LittleEndian.PutUint64(record[4:], uint64(zip64EndOfCentralDirectoryLength-12))
		binary.LittleEndian.PutUint16(record[12:], 45)
		binary.LittleEndian.PutUint16(record[14:], 45)
		binary.LittleEndian.PutUint64(record[24:], uint64(len(options.entries)))
		binary.LittleEndian.PutUint64(record[32:], uint64(len(options.entries)))
		binary.LittleEndian.PutUint64(record[40:], uint64(centralDirectorySize))
		binary.LittleEndian.PutUint64(record[48:], uint64(centralDirectoryOffset))
		archive = append(archive, record...)
		locator := make([]byte, zip64LocatorLength)
		binary.LittleEndian.PutUint32(locator, zip64EndOfCentralDirectoryLocatorSignature)
		binary.LittleEndian.PutUint64(locator[8:], uint64(zip64Offset))
		binary.LittleEndian.PutUint32(locator[16:], 1)
		archive = append(archive, locator...)
	}

	end := make([]byte, zipEndOfCentralDirectoryLength)
	binary.LittleEndian.PutUint32(end, zipEndOfCentralDirectorySignature)
	binary.LittleEndian.PutUint16(end[8:], uint16(len(options.entries)))
	binary.LittleEndian.PutUint16(end[10:], uint16(len(options.entries)))
	binary.LittleEndian.PutUint32(end[12:], uint32(centralDirectorySize))
	binary.LittleEndian.PutUint32(end[16:], uint32(centralDirectoryOffset))
	if options.zip64Records || options.truncatedZip64 {
		binary.LittleEndian.PutUint16(end[8:], zip16Max)
		binary.LittleEndian.PutUint16(end[10:], zip16Max)
		binary.LittleEndian.PutUint32(end[16:], zip32Max)
	}
	return append(archive, end...)
}

func writeTestZip(t *testing.T, options testZipOptions) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.zip")
	if err := os.WriteFile(path, buildTestZip(options), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestZipInspectorReadsTheStructuresApart(t *testing.T) {
	classic := writeTestZip(t, testZipOptions{entries: []testZipEntry{
		{name: "payload-01.mkv", data: []byte("silver horizon")},
	}})
	details, err := inspectZipStructure(classic, "archive/fixture.zip")
	if err != nil {
		t.Fatal(err)
	}
	if details.Zip64 || details.Zip64EOCDRecord || details.Zip64EOCDLocator {
		t.Errorf("an ordinary zip should not read as zip64: %+v", details)
	}
	if details.Entries != 1 || details.LocalHeadersInspected != 1 || details.DataDescriptorEntries != 0 {
		t.Errorf("ordinary zip = %+v, want one entry, one local header, no data descriptors", details)
	}
	if details.LargestMemberBytes != int64(len("silver horizon")) {
		t.Errorf("largest member = %d, want %d", details.LargestMemberBytes, len("silver horizon"))
	}

	forced := writeTestZip(t, testZipOptions{
		zip64Records: true,
		entries: []testZipEntry{
			{name: "payload-01.mkv", data: []byte("silver horizon"), zip64Extra: true},
		},
	})
	if details, err = inspectZipStructure(forced, "archive/fixture.zip"); err != nil {
		t.Fatal(err)
	}
	if !details.Zip64 || !details.Zip64EOCDRecord || !details.Zip64EOCDLocator {
		t.Errorf("forced zip64 = %+v, want the record and its locator", details)
	}
	if details.Zip64ExtraFieldEntries != 1 || details.Entries != 1 {
		t.Errorf("forced zip64 = %+v, want a zip64 extra field on its one entry", details)
	}
	// The 32-bit size fields hold the marker, so the real size can only come
	// from the extra field.
	if details.LargestMemberBytes != int64(len("silver horizon")) {
		t.Errorf("largest member = %d, want the size from the zip64 extra field", details.LargestMemberBytes)
	}

	streamed := writeTestZip(t, testZipOptions{entries: []testZipEntry{
		{name: "payload-01.mkv", data: []byte("silver horizon"), dataDescriptor: true},
		{name: "payload-02.mkv", data: []byte("copper meridian"), dataDescriptor: true},
	}})
	if details, err = inspectZipStructure(streamed, "archive/fixture.zip"); err != nil {
		t.Fatal(err)
	}
	if details.Entries != 2 || details.DataDescriptorEntries != 2 {
		t.Errorf("streamed zip = %+v, want bit 3 on both local headers", details)
	}
	if details.Zip64 {
		t.Errorf("streamed zip = %+v, want no zip64 records", details)
	}
}

// A zip whose end-of-central-directory record points at a zip64 record that
// was never written is the shape Info-ZIP produces when `-fz` is aimed at a
// pipe. Neither pinned reader can open it, so the inspector refuses it rather
// than reporting an archive with no entries.
func TestZipInspectorRefusesATruncatedZip64Archive(t *testing.T) {
	path := writeTestZip(t, testZipOptions{
		truncatedZip64: true,
		entries: []testZipEntry{
			{name: "payload-01.mkv", data: []byte("silver horizon")},
		},
	})
	_, err := inspectZipStructure(path, "archive/fixture.zip")
	if err == nil {
		t.Fatal("a zip64 marker with no zip64 record should be refused")
	}
	if !strings.Contains(err.Error(), "no zip64 end-of-central-directory record") {
		t.Errorf("error = %v, want it to name the missing zip64 record", err)
	}
}

func TestZipInspectorRefusesSomethingThatIsNotAZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.zip")
	if err := os.WriteFile(path, []byte("this is not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectZipStructure(path, "archive/fixture.zip"); err == nil {
		t.Error("a file with no end-of-central-directory record should be refused")
	}
}

func TestRequireZipStructureRefusesADegradedLane(t *testing.T) {
	for name, testCase := range map[string]struct {
		structure fixture.ZipStructure
		details   fixture.ZipStructureDetails
		want      string
	}{
		"forced lane that came out an ordinary zip": {
			structure: fixture.ForcedZip64Structure,
			details:   fixture.ZipStructureDetails{Entries: 1, LocalHeadersInspected: 1},
			want:      "the writer produced an ordinary zip",
		},
		"forced lane missing an entry's extra field": {
			structure: fixture.ForcedZip64Structure,
			details: fixture.ZipStructureDetails{
				Zip64EOCDRecord: true, Zip64EOCDLocator: true,
				Entries: 2, Zip64ExtraFieldEntries: 1,
			},
			want: "did not honour it",
		},
		"large lane shrunk below 4 GiB": {
			structure: fixture.LargeZip64Structure,
			details: fixture.ZipStructureDetails{
				Zip64EOCDRecord: true, Zip64EOCDLocator: true,
				Entries: 1, Zip64ExtraFieldEntries: 1, LargestMemberBytes: 200 << 20,
			},
			want: "--zip64-large-file-bytes",
		},
		"streamed lane with patched local headers": {
			structure: fixture.StreamedZipStructure,
			details: fixture.ZipStructureDetails{
				Entries: 2, LocalHeadersInspected: 2, DataDescriptorEntries: 1,
			},
			want: "set general purpose bit 3",
		},
	} {
		archiveCase := fixture.ArchiveCase{ID: "zip-lane", ArchiveFormat: fixture.Zip, ZipStructure: testCase.structure}
		testCase.details.Declared = testCase.structure
		err := requireZipStructure(archiveCase, testCase.details)
		if err == nil {
			t.Errorf("%s was accepted, want a refusal mentioning %q", name, testCase.want)
			continue
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s error = %v, want it to mention %q", name, err, testCase.want)
		}
	}
}

func TestRequireZipStructureAcceptsTheLanesAsWritten(t *testing.T) {
	for name, testCase := range map[string]struct {
		structure fixture.ZipStructure
		details   fixture.ZipStructureDetails
	}{
		"ordinary zip": {
			structure: fixture.ClassicZipStructure,
			details:   fixture.ZipStructureDetails{Entries: 1, LocalHeadersInspected: 1},
		},
		"forced zip64": {
			structure: fixture.ForcedZip64Structure,
			details: fixture.ZipStructureDetails{
				Zip64: true, Zip64EOCDRecord: true, Zip64EOCDLocator: true,
				Entries: 1, Zip64ExtraFieldEntries: 1, LocalHeadersInspected: 1,
			},
		},
		"member past 4 GiB": {
			structure: fixture.LargeZip64Structure,
			details: fixture.ZipStructureDetails{
				Zip64: true, Zip64EOCDRecord: true, Zip64EOCDLocator: true,
				Entries: 1, Zip64ExtraFieldEntries: 1, LocalHeadersInspected: 1,
				LargestMemberBytes: 5 << 30,
			},
		},
		"streamed": {
			structure: fixture.StreamedZipStructure,
			details: fixture.ZipStructureDetails{
				Entries: 2, LocalHeadersInspected: 2, DataDescriptorEntries: 2,
			},
		},
	} {
		archiveCase := fixture.ArchiveCase{ID: "zip-lane", ArchiveFormat: fixture.Zip, ZipStructure: testCase.structure}
		testCase.details.Declared = testCase.structure
		if err := requireZipStructure(archiveCase, testCase.details); err != nil {
			t.Errorf("%s should be accepted: %v", name, err)
		}
	}
}
