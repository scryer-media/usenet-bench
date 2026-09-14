package adversarial

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEveryRecipeIsDeterministicBoundedAndExportable(t *testing.T) {
	seen := map[string]bool{}
	catalog := Catalog()
	if len(catalog) < 400 {
		t.Fatalf("unexpectedly small catalog: %d", len(catalog))
	}
	t.Logf("%d generated adversarial recipes", len(catalog))
	for _, c := range catalog {
		t.Run(c.ID, func(t *testing.T) {
			if seen[c.ID] {
				t.Fatal("duplicate ID")
			}
			seen[c.ID] = true
			b, err := Generate(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Generate(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(b, again) {
				t.Fatal("nondeterministic recipe")
			}
			if _, ok := b.Data["input.nzb"]; !ok {
				t.Fatal("missing NZB")
			}
			ids := map[string]bool{}
			for _, a := range b.Manifest.Articles {
				if ids[a.ID] {
					t.Fatal("duplicate article ID")
				}
				ids[a.ID] = true
				if _, ok := b.Data[a.Body]; !ok {
					t.Fatal("article body absent")
				}
			}
			parent := t.TempDir()
			dir, err := Export(parent, b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := Export(parent, b); err == nil {
				t.Fatal("export overwrote existing bundle")
			}
		})
	}
}

func TestRejectUnknownCase(t *testing.T) {
	if _, err := Generate("../escape"); err == nil {
		t.Fatal("accepted unknown case")
	}
}

func TestVerifyDetectsArtifactAndManifestTampering(t *testing.T) {
	for _, what := range []string{"article", "manifest", "symlink", "oversized"} {
		t.Run(what, func(t *testing.T) {
			b, _ := Generate("control-yenc")
			dir, err := Export(t.TempDir(), b)
			if err != nil {
				t.Fatal(err)
			}
			switch what {
			case "article":
				err = os.WriteFile(filepath.Join(dir, "article-000.body"), []byte("changed"), 0600)
			case "manifest":
				b.Manifest.Case.Expectation = Reject
				raw, _ := json.Marshal(b.Manifest)
				err = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600)
			case "symlink":
				err = os.Remove(filepath.Join(dir, "article-000.body"))
				if err == nil {
					err = os.Symlink("input.nzb", filepath.Join(dir, "article-000.body"))
				}
			case "oversized":
				f, e := os.OpenFile(filepath.Join(dir, "article-000.body"), os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				err = f.Truncate(MaxArtifactBytes + 1)
				_ = f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(dir); err == nil {
				t.Fatal("tampered bundle accepted")
			}
		})
	}
}

func TestNZBAndYencControls(t *testing.T) {
	b, _ := Generate("control-yenc")
	d := xml.NewDecoder(bytes.NewReader(b.Data["input.nzb"]))
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range [][]byte{payload, {}, bytes.Repeat([]byte{0, 1, 19, 214, 224, 227, 255}, 1000)} {
		encoded := yenc("payload.bin", data, 1, 1, 1, len(data))
		start := bytes.Index(encoded, []byte("\r\n")) + 2
		end := bytes.LastIndex(encoded, []byte("\r\n=yend"))
		body := encoded[start:end]
		var decoded []byte
		for i := 0; i < len(body); i++ {
			v := body[i]
			if v == '\r' || v == '\n' {
				continue
			}
			if v == '=' {
				i++
				v = body[i] - 64
			}
			decoded = append(decoded, v-42)
		}
		if !bytes.Equal(decoded, data) {
			t.Fatal("yEnc roundtrip failed")
		}
		if !bytes.Contains(encoded, []byte(fmt.Sprintf("crc32=%08x", crc32.ChecksumIEEE(data)))) {
			t.Fatal("wrong independent CRC")
		}
	}
}

func TestStandardLibraryControls(t *testing.T) {
	for _, id := range []string{"control-zip", "control-tar", "control-gzip", "control-deflate"} {
		t.Run(id, func(t *testing.T) {
			b, _ := Generate(id)
			raw := b.Data["payload-000.bin"]
			var r io.Reader
			switch id {
			case "control-zip":
				z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
				if err != nil {
					t.Fatal(err)
				}
				if len(z.File) != 1 || z.File[0].Name != "payload.bin" {
					t.Fatal("bad ZIP directory")
				}
				f, err := z.File[0].Open()
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				r = f
			case "control-tar":
				tr := tar.NewReader(bytes.NewReader(raw))
				h, err := tr.Next()
				if err != nil || h.Name != "payload.bin" {
					t.Fatalf("bad tar: %v", err)
				}
				r = tr
			case "control-gzip":
				gz, err := gzip.NewReader(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				r = gz
			case "control-deflate":
				f := flate.NewReader(bytes.NewReader(raw))
				defer f.Close()
				r = f
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("control bytes mismatch")
			}
		})
	}
}

func TestPAR2PacketChecksumsAndRecoveryIdentity(t *testing.T) {
	b, _ := Generate("repair-exactly-sufficient")
	d := b.Data["payload-001.bin"]
	packets := 0
	recovery := false
	for len(d) > 0 {
		if len(d) < 64 || string(d[:8]) != "PAR2\x00PKT" {
			t.Fatal("bad packet framing")
		}
		n := int(le.Uint64(d[8:16]))
		if n < 64 || n > len(d) || n%4 != 0 {
			t.Fatal("bad packet length")
		}
		sum := md5.Sum(d[32:n])
		if !bytes.Equal(sum[:], d[16:32]) {
			t.Fatal("bad packet hash")
		}
		if strings.HasPrefix(string(d[48:64]), "PAR 2.0\x00RecvSlic") {
			recovery = true
			if !bytes.Equal(d[68:68+len(payload)], payload) {
				t.Fatal("exponent zero must equal sole source slice")
			}
		}
		d = d[n:]
		packets++
	}
	if packets != 5 || !recovery {
		t.Fatal("missing recovery packet")
	}
}

// Optional positive-control validation uses installed independent tools. No
// hostile archive is passed to them, no packages are installed, and all writes
// remain in test-owned directories. These tests must pass when explicitly on.
func TestInstalledIndependentOracles(t *testing.T) {
	if os.Getenv("ADVERSARIAL_ORACLES") != "1" {
		t.Skip("set ADVERSARIAL_ORACLES=1 to require installed 7zz and par2 positive-control oracles")
	}
	seven, err := exec.LookPath("7zz")
	if err != nil {
		t.Fatal(err)
	}
	par, err := exec.LookPath("par2")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"control-rar4", "control-rar5", "control-7z"} {
		t.Run(id, func(t *testing.T) {
			b, _ := Generate(id)
			dir := t.TempDir()
			archive := filepath.Join(dir, "fixture.archive")
			if err := os.WriteFile(archive, b.Data["payload-000.bin"], 0600); err != nil {
				t.Fatal(err)
			}
			outdir := filepath.Join(dir, "out")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, seven, "x", "-y", "-bd", "-o"+outdir, archive)
			log := &boundedOracleOutput{}
			cmd.Stdout = log
			cmd.Stderr = log
			cmd.WaitDelay = time.Second
			err := cmd.Run()
			raw := log.data
			if err != nil {
				t.Fatalf("independent extractor: %v\n%s", err, raw)
			}
			got, err := os.ReadFile(filepath.Join(outdir, "payload.bin"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("control extraction mismatch: %v", err)
			}
		})
	}
	for _, v := range []string{"one-slice", "missing-file", "exactly-sufficient", "damaged-recovery-packet", "renamed-file"} {
		t.Run("repair-"+v, func(t *testing.T) {
			b, _ := Generate("repair-" + v)
			dir := t.TempDir()
			parIndex := 1
			if v == "missing-file" {
				parIndex = 0
			} else {
				name := "payload.bin"
				if v == "renamed-file" {
					name = "obfuscated.bin"
				}
				if err := os.WriteFile(filepath.Join(dir, name), b.Data["payload-000.bin"], 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "fixture.par2"), b.Data[fmt.Sprintf("payload-%03d.bin", parIndex)], 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, par, "r", "-q", "-t1", "fixture.par2")
			cmd.Dir = dir
			log := &boundedOracleOutput{}
			cmd.Stdout = log
			cmd.Stderr = log
			cmd.WaitDelay = time.Second
			err := cmd.Run()
			raw := log.data
			if err != nil {
				t.Fatalf("independent repairer: %v\n%s", err, raw)
			}
			got, err := os.ReadFile(filepath.Join(dir, "payload.bin"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("repaired bytes mismatch: %v", err)
			}
		})
	}
}

func TestTargetedMutationsPreserveOuterChecksums(t *testing.T) {
	control7z := sevenZip("payload.bin", payload, "")
	crc := fixed32(crc32.ChecksumIEEE(payload))
	if !bytes.Contains(control7z, append([]byte{0, 8, 10, 1}, append(crc, 0, 0, 5)...)) {
		t.Fatal("7z control has no single-stream SubStreamsInfo section")
	}
	for _, v := range []string{"packed-size-lie", "unpacked-size-lie", "unknown-method", "extra-size-lie", "high-dictionary"} {
		d := rar5("payload.bin", payload, v)
		pos := 8
		for i := 0; i < 2; i++ {
			want := le.Uint32(d[pos:])
			j := pos + 4
			n := uint64(0)
			shift := uint(0)
			for {
				x := d[j]
				j++
				n |= uint64(x&127) << shift
				shift += 7
				if x&128 == 0 {
					break
				}
			}
			end := j + int(n)
			if crc32.ChecksumIEEE(d[pos+4:end]) != want {
				t.Fatal("semantic mutation lost outer RAR checksum", v)
			}
			pos = end
		}
	}
	for _, v := range []string{"next-offset-overflow", "next-size-overflow", "high-dictionary", "unpack-size-lie"} {
		d := sevenZip("payload.bin", payload, v)
		if crc32.ChecksumIEEE(d[12:32]) != le.Uint32(d[8:12]) {
			t.Fatal("semantic mutation lost start header CRC", v)
		}
	}
}

func TestBombsExceedBudgetWithoutLargeGeneratedArtifacts(t *testing.T) {
	for _, kind := range []string{"zip", "gzip", "deflate"} {
		t.Run(kind, func(t *testing.T) {
			b, err := Generate(kind + "-output-bomb")
			if err != nil {
				t.Fatal(err)
			}
			data := b.Data["payload-000.bin"]
			if len(data) > 128<<10 {
				t.Fatal("bomb fixture needlessly large")
			}
			var r io.ReadCloser
			switch kind {
			case "zip":
				z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				r, err = z.File[0].Open()
				if err != nil {
					t.Fatal(err)
				}
			case "gzip":
				r, err = gzip.NewReader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
			case "deflate":
				r = flate.NewReader(bytes.NewReader(data))
			}
			defer r.Close()
			n, err := io.Copy(io.Discard, io.LimitReader(r, b.Manifest.Limits.WrittenBytes+1))
			if err != nil || n <= b.Manifest.Limits.WrittenBytes {
				t.Fatalf("fixture did not exercise resource threshold: %d, %v", n, err)
			}
		})
	}
}
