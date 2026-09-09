package adversarial

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
)

var le = binary.LittleEndian

func put16(b []byte, at int, v uint16) { le.PutUint16(b[at:], v) }
func put32(b []byte, at int, v uint32) { le.PutUint32(b[at:], v) }
func put64(b []byte, at int, v uint64) { le.PutUint64(b[at:], v) }

func zipArchive(files []namedBytes) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Store}
		h.SetMode(0600)
		out, err := w.CreateHeader(h)
		if err != nil {
			panic(err)
		}
		if _, err = out.Write(f.data); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

type zipModeEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func zipArchiveWithModes(files []zipModeEntry) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Store}
		h.SetMode(f.mode)
		out, err := w.CreateHeader(h)
		if err != nil {
			panic(err)
		}
		if _, err = out.Write(f.data); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func zipSymlinkPivot(replacement bool) []byte {
	entries := []zipModeEntry{{"pivot", []byte("/scratch"), os.ModeSymlink | 0777}}
	if replacement {
		entries = []zipModeEntry{
			{"pivot/", nil, os.ModeDir | 0755},
			{"pivot/harmless", payload, 0600},
			{"pivot", []byte("/scratch"), os.ModeSymlink | 0777},
		}
	}
	entries = append(entries, zipModeEntry{"pivot/escape.canary", []byte("ZIP LINK PIVOT\n"), 0600})
	return zipArchiveWithModes(entries)
}

func mutateZip(v string) []byte {
	files := []namedBytes{{"payload.bin", payload}}
	switch v {
	case "duplicate-name":
		files = append(files, namedBytes{"payload.bin", []byte("replacement")})
	case "file-directory-collision":
		files = append(files, namedBytes{"payload.bin/child", payload})
	case "symlink":
		files = []namedBytes{{"payload.bin", []byte("../escape.canary")}}
	case "many-entries":
		for i := 0; i < 4097; i++ {
			files = append(files, namedBytes{fmt.Sprintf("file-%05d", i), nil})
		}
	}
	d := zipArchive(files)
	central := bytes.Index(d, []byte{'P', 'K', 1, 2})
	eocd := bytes.LastIndex(d, []byte{'P', 'K', 5, 6})
	switch v {
	case "duplicate-name", "file-directory-collision", "many-entries":
		return d
	case "bad-magic":
		d[0] ^= 255
	case "truncated":
		d = d[:len(d)/2]
	case "bad-crc":
		put32(d, central+16, 0)
	case "local-name-mismatch":
		copy(d[30:], []byte("foreign.bin"))
	case "local-size-mismatch":
		put32(d, 18, 1)
		put32(d, 22, 1)
		put16(d, 6, le.Uint16(d[6:])&^8)
	case "local-method-mismatch":
		put16(d, 8, 8)
	case "local-flag-mismatch":
		put16(d, 6, le.Uint16(d[6:])|1)
	case "size-overflow":
		put32(d, central+24, 0xfffffffe)
	case "overlap-offset":
		put32(d, central+42, uint32(central))
	case "offset-beyond-eof":
		put32(d, central+42, 0xfffffff0)
	case "missing-eocd":
		d = d[:eocd]
	case "duplicate-eocd":
		tail := append([]byte{}, d[eocd:]...)
		put32(tail, 16, 0)
		d = append(d, tail...)
	case "zip64-without-record":
		put32(d, central+20, 0xffffffff)
		put32(d, central+24, 0xffffffff)
		put32(d, eocd+16, 0xffffffff)
	case "unknown-method":
		put16(d, 8, 65535)
		put16(d, central+10, 65535)
	case "symlink":
		put16(d, central+4, 3<<8|20)
		put32(d, central+38, 0120777<<16)
	case "unicode-override":
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		name := "payload.bin"
		extra := make([]byte, 9)
		put16(extra, 0, 0x7075)
		put16(extra, 2, uint16(5+len("../escape.canary")))
		extra[4] = 1
		put32(extra, 5, crc32.ChecksumIEEE([]byte(name)))
		extra = append(extra, []byte("../escape.canary")...)
		out, err := w.CreateHeader(&zip.FileHeader{Name: name, Extra: extra, Method: zip.Store})
		if err != nil {
			panic(err)
		}
		_, _ = out.Write(payload)
		if err := w.Close(); err != nil {
			panic(err)
		}
		return b.Bytes()
	case "prefix-polyglot":
		d = append([]byte("MZ\x00ADVERSARIAL-INERT-SFX\x00"), d...)
	case "trailing-archive":
		d = append(d, zipArchive([]namedBytes{{"../escape.canary", payload}})...)
	case "descriptor-mismatch":
		at := bytes.Index(d, []byte{'P', 'K', 7, 8})
		put32(d, at+4, 0)
		put32(d, at+12, 1)
	case "output-bomb":
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		out, err := w.Create("expanded.bin")
		if err != nil {
			panic(err)
		}
		chunk := make([]byte, 64<<10)
		for i := 0; i < 1025; i++ {
			if _, err := out.Write(chunk); err != nil {
				panic(err)
			}
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
		return b.Bytes()
	default:
		panic("unknown ZIP mutation " + v)
	}
	return d
}

// Raw USTAR writing deliberately permits paths and entry types rejected by
// safe high-level writers. Header checksum is recomputed after semantic edits.
func tarHeader(name string, size int64, kind byte, link string) []byte {
	h := make([]byte, 512)
	copy(h, name)
	copy(h[100:], "0000600\x00")
	copy(h[108:], "0000000\x00")
	copy(h[116:], "0000000\x00")
	copy(h[124:], fmt.Sprintf("%011o\x00", size))
	copy(h[136:], "00000000000\x00")
	h[156] = kind
	copy(h[157:257], link)
	copy(h[257:], "ustar\x0000")
	tarChecksum(h)
	return h
}
func tarChecksum(h []byte) {
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	sum := 0
	for _, v := range h {
		sum += int(v)
	}
	copy(h[148:], fmt.Sprintf("%06o\x00 ", sum))
}
func tarEntry(name string, data []byte, kind byte, link string) []byte {
	h := tarHeader(name, int64(len(data)), kind, link)
	h = append(h, data...)
	padding := (512 - len(data)%512) % 512
	return append(h, make([]byte, padding)...)
}
func tarArchive(files []namedBytes) []byte {
	var d []byte
	for _, f := range files {
		d = append(d, tarEntry(f.name, f.data, '0', "")...)
	}
	return append(d, make([]byte, 1024)...)
}

func tarSymlinkPivot(replacement bool) []byte {
	var d []byte
	if replacement {
		d = append(d, tarEntry("pivot", nil, '5', "")...)
		d = append(d, tarEntry("pivot/harmless", payload, '0', "")...)
	}
	d = append(d, tarEntry("pivot", nil, '2', "/scratch")...)
	d = append(d, tarEntry("pivot/escape.canary", []byte("TAR LINK PIVOT\n"), '0', "")...)
	return append(d, make([]byte, 1024)...)
}

func tarHardlinkOverwritePivot() []byte {
	d := tarEntry("pivot", nil, '1', "/scratch/escape.canary")
	d = append(d, tarEntry("pivot", []byte("TAR HARDLINK PIVOT\n"), '0', "")...)
	return append(d, make([]byte, 1024)...)
}
func paxRecord(key, value string) []byte {
	body := key + "=" + value + "\n"
	n := len(body) + 2
	for {
		s := fmt.Sprintf("%d %s", n, body)
		if len(s) == n {
			return []byte(s)
		}
		n = len(s)
	}
}

func mutateTar(v string) []byte {
	d := tarArchive([]namedBytes{{"payload.bin", payload}})
	fix := false
	switch v {
	case "bad-checksum":
		d[0] ^= 1
	case "truncated-header":
		return d[:127]
	case "truncated-data":
		return d[:515]
	case "size-lie":
		copy(d[124:], "77777777777\x00")
		fix = true
	case "negative-size":
		copy(d[124:], "-0000000001\x00")
		fix = true
	case "base256-overflow":
		for i := 124; i < 136; i++ {
			d[i] = 255
		}
		d[124] = 0x80
		fix = true
	case "missing-end":
		return d[:len(d)-1024]
	case "trailing-archive":
		return append(d, tarArchive([]namedBytes{{"../escape.canary", payload}})...)
	case "duplicate-name":
		return tarArchive([]namedBytes{{"payload.bin", payload}, {"payload.bin", []byte("replacement")}})
	case "file-directory-collision":
		return tarArchive([]namedBytes{{"payload.bin", payload}, {"payload.bin/child", payload}})
	case "symlink", "hardlink", "fifo", "char-device", "block-device", "sparse":
		kind := map[string]byte{"symlink": '2', "hardlink": '1', "fifo": '6', "char-device": '3', "block-device": '4', "sparse": 'S'}[v]
		return append(tarEntry("payload.bin", nil, kind, "../escape.canary"), make([]byte, 1024)...)
	case "parent-symlink":
		return append(tarEntry("parent", nil, '2', "../"), tarArchive([]namedBytes{{"parent/escape.canary", payload}})...)
	case "link-chain":
		return append(append(tarEntry("a", nil, '2', "b"), tarEntry("b", nil, '2', "../")...), tarArchive([]namedBytes{{"a/escape.canary", payload}})...)
	case "pax-path":
		return append(tarEntry("PaxHeader", paxRecord("path", "../escape.canary"), 'x', ""), d...)
	case "pax-linkpath":
		return append(append(tarEntry("PaxHeader", paxRecord("linkpath", "../escape.canary"), 'x', ""), tarEntry("payload.bin", nil, '1', "safe.bin")...), make([]byte, 1024)...)
	case "gnu-longname":
		return append(tarEntry("././@LongLink", []byte("../escape.canary\x00"), 'L', ""), d...)
	case "gnu-longlink":
		return append(append(tarEntry("././@LongLink", []byte("../escape.canary\x00"), 'K', ""), tarEntry("payload.bin", nil, '2', "safe")...), make([]byte, 1024)...)
	case "privileged-mode":
		copy(d[100:], "0006777\x00")
		fix = true
	case "hostile-owner":
		copy(d[108:], "7777777\x00")
		copy(d[116:], "7777777\x00")
		fix = true
	case "hostile-time":
		copy(d[136:], "77777777777\x00")
		fix = true
	case "many-entries":
		var files []namedBytes
		for i := 0; i < 4097; i++ {
			files = append(files, namedBytes{fmt.Sprintf("empty-%05d", i), nil})
		}
		return tarArchive(files)
	case "deep-path":
		return append(tarEntry("PaxHeader", paxRecord("path", strings.Repeat("d/", 4097)+"payload.bin"), 'x', ""), d...)
	default:
		panic("unknown tar mutation " + v)
	}
	if fix {
		tarChecksum(d[:512])
	}
	return d
}

func gzipBytes(data []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(data); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}
func deflateBytes(data []byte) []byte {
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, flate.BestCompression)
	if err != nil {
		panic(err)
	}
	_, _ = w.Write(data)
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}
func compressedBomb(gz bool) []byte {
	var b bytes.Buffer
	if gz {
		w, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
		if err != nil {
			panic(err)
		}
		chunk := make([]byte, 64<<10)
		for i := 0; i < 1025; i++ {
			_, _ = w.Write(chunk)
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
	} else {
		w, err := flate.NewWriter(&b, flate.BestCompression)
		if err != nil {
			panic(err)
		}
		chunk := make([]byte, 64<<10)
		for i := 0; i < 1025; i++ {
			_, _ = w.Write(chunk)
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
	}
	return b.Bytes()
}
func mutateCompressed(kind, v string) (string, []byte) {
	name := "payload.bin.gz"
	d := gzipBytes(payload)
	if kind == "deflate" {
		name = "payload.bin.deflate"
		d = deflateBytes(payload)
	}
	switch v {
	case "truncated":
		d = d[:len(d)/2]
	case "invalid-block":
		d = []byte{7}
	case "bad-crc":
		d[len(d)-8] ^= 1
	case "size-lie":
		put32(d, len(d)-4, 0xffffffff)
	case "trailing-junk":
		d = append(d, []byte("UNEXPECTED-TAIL")...)
	case "concatenated":
		d = append(d, gzipBytes([]byte("other stream"))...)
	case "filename-traversal", "huge-comment":
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		if v == "filename-traversal" {
			w.Name = "../escape.canary"
		} else {
			w.Comment = strings.Repeat("X", 1<<20)
		}
		if _, err := w.Write(payload); err != nil {
			panic(err)
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
		d = b.Bytes()
	case "output-bomb":
		d = compressedBomb(kind == "gzip")
	case "nested-zip":
		name = "fixture.zip.gz"
		d = gzipBytes(zipArchive([]namedBytes{{"../escape.canary", payload}}))
	default:
		panic("unknown compression mutation " + v)
	}
	return name, d
}

func uuencode(name string, data []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "begin 600 %s\r\n", name)
	enc := func(v byte) byte {
		if v == 0 {
			return '`'
		}
		return v + 32
	}
	for len(data) > 0 {
		n := len(data)
		if n > 45 {
			n = 45
		}
		chunk := append([]byte{}, data[:n]...)
		data = data[n:]
		b.WriteByte(enc(byte(n)))
		for len(chunk)%3 != 0 {
			chunk = append(chunk, 0)
		}
		for i := 0; i < len(chunk); i += 3 {
			a, c, d := chunk[i], chunk[i+1], chunk[i+2]
			b.Write([]byte{enc(a >> 2), enc((a<<4 | c>>4) & 63), enc((c<<2 | d>>6) & 63), enc(d & 63)})
		}
		b.WriteString("\r\n")
	}
	b.WriteString("`\r\nend\r\n")
	return b.Bytes()
}
func mutateUU(v string) []byte {
	d := uuencode("payload.bin", payload)
	row := bytes.Index(d, []byte("\r\n")) + 2
	switch v {
	case "invalid-mode":
		return replace(d, "600", "999999999")
	case "traversal-name":
		return replace(d, "payload.bin", "../escape.canary")
	case "invalid-length":
		d[row] = 255
	case "truncated-row":
		return d[:row+3]
	case "missing-end":
		return d[:len(d)-5]
	case "nested-begin":
		return append([]byte("begin 600 foreign.bin\r\n"), d...)
	case "overlong-row":
		return append(append(d[:row:row], bytes.Repeat([]byte("M"), 1<<20)...), d[row:]...)
	case "mixed-yenc":
		return append(d, yenc("payload.bin", payload, 1, 1, 1, len(payload))...)
	default:
		panic("unknown UU mutation " + v)
	}
	return d
}

func sfv(v string) []byte {
	line := fmt.Sprintf("payload.bin %08X\r\n", crc32.ChecksumIEEE(payload))
	switch v {
	case "bad-crc":
		line = "payload.bin 00000000\r\n"
	case "invalid-crc":
		line = "payload.bin GGGGGGGG\r\n"
	case "missing-file":
		line = "missing.bin 00000000\r\n"
	case "duplicate-conflict":
		line += "payload.bin 00000000\r\n"
	case "traversal":
		line = "../escape.canary 00000000\r\n"
	case "absolute":
		line = "/adversarial-canary/secret.txt 00000000\r\n"
	case "leading-option":
		line = "--help 00000000\r\n"
	case "unicode-collision":
		line = "caf\u00e9.bin 00000000\r\ncafe\u0301.bin 11111111\r\n"
	case "long-line":
		line = strings.Repeat("A", 1<<20) + " 00000000\r\n"
	case "shell", "html", "terminal":
		line = pathValue(v) + " 00000000\r\n"
	default:
		panic("unknown SFV mutation " + v)
	}
	return []byte(line)
}

func chain(b *Bundle, v string) {
	switch v {
	case "par2-renames-archive-escape":
		d := zipArchive([]namedBytes{{"../escape.canary", payload}})
		b.post(namedBytes{"obfuscated", d}, namedBytes{"fixture.par2", par2("fixture.zip", d, "")})
	case "sab-cve-2021-29488-par2-parent-escape":
		b.post(
			namedBytes{"obfuscated", payload},
			namedBytes{"fixture.par2", par2("../escape.canary", payload, "")},
		)
	case "sab-ghsa-75g3-unpacked-par2-parent-escape":
		index := par2("../escape.canary", payload, "")
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{
			{"obfuscated", payload},
			{"fixture.par2", index},
		})})
	case "sab-ghsa-mjwj-symlink-dotdot-par2-escape":
		index := par2("pivot/../escape.canary", payload, "")
		b.post(namedBytes{"fixture.zip", zipArchiveWithModes([]zipModeEntry{
			{"pivot", []byte("."), os.ModeSymlink | 0777},
			{"obfuscated", payload, 0600},
			{"fixture.par2", index, 0600},
		})})
	case "zip-local-central-traversal":
		d := zipArchive([]namedBytes{{"payload.bin", payload}})
		copy(d[30:], []byte("../evil.bin"))
		b.post(namedBytes{"fixture.zip", d})
	case "nested-tar-link":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"inner.tar", mutateTar("parent-symlink")}})})
	case "nested-gzip-bomb":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"expanded.bin.gz", compressedBomb(true)}})})
	case "safe-member-before-traversal":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"safe.bin", payload}, {"../escape.canary", payload}})})
	case "traversal-before-safe-member":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"../escape.canary", payload}, {"safe.bin", payload}})})
	case "yenc-name-zip-traversal":
		d := zipArchive([]namedBytes{{"../escape.canary", payload}})
		b.post(namedBytes{"fixture.zip", d})
		b.Data["article-000.body"] = yenc("../fixture.zip", d, 1, 1, 1, len(d))
	case "corrupt-first-unsafe-retry":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"../escape.canary", payload}})})
		b.Manifest.Fault.Kind = "first-corrupt"
	case "par2-shell-name":
		b.post(namedBytes{"fixture.par2", par2(pathValue("shell"), payload, "recovery-only")})
	case "stored-html-metadata":
		b.post(namedBytes{pathValue("html"), payload})
		b.Data["input.nzb"] = mutateNZB(b.Data["input.nzb"], "html")
	case "archive-sfx-canary":
		b.post(namedBytes{"fixture.exe", mutateZip("prefix-polyglot")})
	case "zip-symlink-absolute-pivot":
		b.post(namedBytes{"fixture.zip", zipSymlinkPivot(false)})
	case "zip-symlink-replacement-pivot":
		b.post(namedBytes{"fixture.zip", zipSymlinkPivot(true)})
	case "tar-symlink-absolute-pivot":
		b.post(namedBytes{"fixture.tar", tarSymlinkPivot(false)})
	case "tar-hardlink-overwrite-pivot":
		b.post(namedBytes{"fixture.tar", tarHardlinkOverwritePivot()})
	case "tar-symlink-replacement-pivot":
		b.post(namedBytes{"fixture.tar", tarSymlinkPivot(true)})
	case "nested-zip-tar-symlink-pivot":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"inner.tar", tarSymlinkPivot(false)}})})
	case "nested-depth-tar-symlink-pivot":
		d := tarSymlinkPivot(false)
		name := "inner.tar"
		for i := 0; i < 8; i++ {
			d = zipArchive([]namedBytes{{name, d}})
			name = fmt.Sprintf("layer-%02d.zip", i)
		}
		b.post(namedBytes{"fixture.zip", d})
	case "nested-extension-magic-confusion":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"inner.zip", tarSymlinkPivot(false)}})})
	case "reserved-staging-nested-archive":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{".weaver-staging/0/inner.tar", tarSymlinkPivot(false)}})})
	case "shell-helper-archive-name":
		b.post(namedBytes{"$(adversarial-canary).zip", zipArchive([]namedBytes{{"payload.bin", payload}})})
	case "shell-helper-member-name":
		b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{"$(adversarial-canary)", payload}})})
	case "par2-helper-filename":
		b.post(namedBytes{"fixture.par2", par2("$(adversarial-canary)", payload, "recovery-only")})
	case "rar4-symlink-absolute-record":
		b.post(namedBytes{"fixture.rar", rar4("pivot", []byte("/scratch/escape.canary"), "symlink-absolute-record")})
	case "rar5-symlink-absolute-record":
		b.post(namedBytes{"fixture.rar", rar5("pivot", payload, "symlink-absolute-record")})
	case "archive-option-filename":
		b.post(namedBytes{"--checkpoint-action=exec=adversarial-canary.tar", tarArchive([]namedBytes{{"payload.bin", payload}})})
	case "parallel-zip-symlink-pivot":
		b.post(
			namedBytes{"a.zip", zipArchive([]namedBytes{{"pivot/harmless", payload}})},
			namedBytes{"b.zip", zipSymlinkPivot(false)},
		)
	case "parallel-tar-symlink-pivot":
		b.post(
			namedBytes{"a.tar", tarArchive([]namedBytes{{"pivot/harmless", payload}})},
			namedBytes{"b.tar", tarSymlinkPivot(false)},
		)
	case "parallel-cross-format-symlink-pivot":
		b.post(
			namedBytes{"a.zip", zipArchive([]namedBytes{{"pivot/harmless", payload}})},
			namedBytes{"b.tar", tarSymlinkPivot(false)},
		)
	case "parallel-file-directory-replacement":
		b.post(
			namedBytes{"a.zip", zipArchive([]namedBytes{{"pivot", payload}})},
			namedBytes{"b.zip", zipArchive([]namedBytes{{"pivot/escape.canary", payload}})},
		)
	default:
		panic("unknown chain " + v)
	}
}
