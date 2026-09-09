package adversarial

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPathRecipesPreserveHostileBytes(t *testing.T) {
	b, err := Generate("nzb-path-nul")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b.Data["input.nzb"], []byte{0}) {
		t.Fatal("XML writer neutralized NUL attack")
	}
	b, err = Generate("nzb-path-newline")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b.Data["input.nzb"], []byte("&#10;")) {
		t.Fatal("newline did not survive attribute normalization")
	}
	b, err = Generate("tar-pax-path")
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(b.Data["payload-000.bin"]))
	h, err := r.Next()
	if err != nil || h.Name != "../escape.canary" {
		t.Fatalf("PAX attack did not override name: %+v %v", h, err)
	}
}

func TestZipLinkIsChecksumValid(t *testing.T) {
	b, err := Generate("zip-symlink")
	if err != nil {
		t.Fatal(err)
	}
	raw := b.Data["payload-000.bin"]
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if z.File[0].Mode()&os.ModeSymlink == 0 {
		t.Fatal("not a symlink entry")
	}
	r, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "../escape.canary" {
		t.Fatalf("link failed checksum before target could be exercised: %q %v", got, err)
	}
}

func TestOutputRejectsHardlinkWithoutReadingIt(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(outside, payload, 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(out, "payload.bin")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutput(out, map[string]string{"payload.bin": Digest(payload)}, DefaultLimits()); err == nil {
		t.Fatal("accepted outside hardlink even with matching content")
	}
}

// A recording connection observes writes without exercising any real network.
type countConn struct {
	mu sync.Mutex
	n  int
}

func (c *countConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n += len(p)
	return len(p), nil
}
func (*countConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*countConn) Close() error                     { return nil }
func (*countConn) LocalAddr() net.Addr              { return nil }
func (*countConn) RemoteAddr() net.Addr             { return nil }
func (*countConn) SetDeadline(time.Time) error      { return nil }
func (*countConn) SetReadDeadline(time.Time) error  { return nil }
func (*countConn) SetWriteDeadline(time.Time) error { return nil }

func TestResponseBudgetIsReservedBeforeConcurrentWrites(t *testing.T) {
	s := &Server{}
	s.wireReserved.Store((512 << 20) - 100)
	c := &countConn{}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.write(c, make([]byte, 10)) }()
	}
	wg.Wait()
	if c.n != 100 || s.Stats().WireBytes != 100 {
		t.Fatalf("byte cap overshoot: %d %+v", c.n, s.Stats())
	}
}
