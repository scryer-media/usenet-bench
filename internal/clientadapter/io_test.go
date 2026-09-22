package clientadapter

import "testing"

// io.stat carries one line per device and several counters per line. Summing
// the wrong key, or stopping at the first device, would understate a client
// that spread its writes over more than one disk.
func TestCgroupV2WriteBytesSumsEveryDevice(t *testing.T) {
	contents := `8:0 rbytes=1000 wbytes=2048 rios=10 wios=20 dbytes=0 dios=0
259:0 rbytes=500 wbytes=1024 rios=5 wios=10 dbytes=0 dios=0
`
	written, err := parseCgroupV2WriteBytes(contents)
	if err != nil {
		t.Fatal(err)
	}
	if written != 3072 {
		t.Fatalf("wbytes = %d, want both devices summed", written)
	}
}

// A container that has not touched a disk yet is a real zero, not a parse
// failure; a file that is not io.stat at all is a failure and must not be
// read as a zero.
func TestCgroupV2WriteBytesSeparatesAnEmptyFileFromAWrongOne(t *testing.T) {
	written, err := parseCgroupV2WriteBytes("")
	if err != nil || written != 0 {
		t.Fatalf("an untouched container: %d %v", written, err)
	}
	if _, err := parseCgroupV2WriteBytes("total 1234\n"); err == nil {
		t.Fatal("a file without device lines was read as a counter")
	}
}

// The v1 file repeats each device as Read, Write, Sync, Async and Total.
// Counting anything but the Write rows would double the figure.
func TestCgroupV1WriteBytesCountsWriteRowsOnly(t *testing.T) {
	contents := `8:0 Read 1000
8:0 Write 2048
8:0 Sync 3048
8:0 Async 0
8:0 Total 3048
259:0 Write 1024
Total 6120
`
	written, err := parseCgroupV1WriteBytes(contents)
	if err != nil {
		t.Fatal(err)
	}
	if written != 3072 {
		t.Fatalf("write bytes = %d, want the Write rows only", written)
	}
	if _, err := parseCgroupV1WriteBytes("8:0 Read 1000\n"); err == nil {
		t.Fatal("a file with no Write row was read as a zero counter")
	}
}
