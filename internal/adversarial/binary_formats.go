package adversarial

import (
	"bytes"
	"crypto/md5"
	"hash/crc32"
	"unicode/utf16"
)

func vint(v uint64) []byte {
	var out []byte
	for v >= 128 {
		out = append(out, byte(v)|128)
		v >>= 7
	}
	return append(out, byte(v))
}
func fixed32(v uint32) []byte { b := make([]byte, 4); put32(b, 0, v); return b }
func fixed64(v uint64) []byte { b := make([]byte, 8); put64(b, 0, v); return b }

func rar4Block(kind byte, flags uint16, body []byte) []byte {
	h := make([]byte, 7)
	h[2] = kind
	put16(h, 3, flags)
	put16(h, 5, uint16(len(h)+len(body)))
	h = append(h, body...)
	put16(h, 0, uint16(crc32.ChecksumIEEE(h[2:])))
	return h
}

func rar4(name string, data []byte, v string) []byte {
	main := rar4Block(0x73, 0, make([]byte, 6))
	f := make([]byte, 25)
	put32(f, 0, uint32(len(data)))
	put32(f, 4, uint32(len(data)))
	f[8] = 3
	put32(f, 9, crc32.ChecksumIEEE(data))
	f[17] = 20
	f[18] = 0x30
	put16(f, 19, uint16(len(name)))
	put32(f, 21, 0100600)
	f = append(f, []byte(name)...)
	flags := uint16(0x8000)
	kind := byte(0x74)
	switch v {
	case "", "bad-magic", "bad-header-crc", "truncated-header", "truncated-data", "header-size-lie":
	case "packed-size-lie":
		put32(f, 0, 0xfffffff0)
	case "unpacked-size-lie":
		put32(f, 4, 0xfffffff0)
	case "bad-data-crc":
		put32(f, 9, 0)
	case "unknown-method":
		f[18] = 255
	case "split-before":
		flags |= 1
	case "split-after":
		flags |= 2
	case "solid-without-predecessor":
		flags |= 0x10
	case "unknown-block":
		kind = 0x79
	case "high-dictionary":
		flags |= 0xc0
		f[18] = 0x35
		f[17] = 29
	case "symlink-absolute-record":
		put32(f, 21, 0120777)
	default:
		panic("unknown RAR4 mutation " + v)
	}
	h := rar4Block(kind, flags, f)
	if v == "header-size-lie" {
		put16(h, 5, 65535)
		put16(h, 0, uint16(crc32.ChecksumIEEE(h[2:])))
	}
	if v == "bad-header-crc" {
		h[0] ^= 1
	}
	d := append([]byte{'R', 'a', 'r', '!', 0x1a, 7, 0}, main...)
	d = append(d, h...)
	if v == "truncated-header" {
		return d[:7+len(main)+9]
	}
	if v == "truncated-data" {
		return append(d, data[:len(data)/2]...)
	}
	d = append(d, data...)
	d = append(d, rar4Block(0x7b, 0, nil)...)
	if v == "bad-magic" {
		d[0] ^= 255
	}
	return d
}

func rar5Block(body []byte) []byte {
	rest := append(vint(uint64(len(body))), body...)
	return append(fixed32(crc32.ChecksumIEEE(rest)), rest...)
}
func rar5(name string, data []byte, v string) []byte {
	main := rar5Block([]byte{1, 0, 0})
	packed, unpacked := uint64(len(data)), uint64(len(data))
	flags, fileflags := uint64(2), uint64(4)
	compression := uint64(0)
	kind := uint64(2)
	crc := crc32.ChecksumIEEE(data)
	var extra []byte
	switch v {
	case "", "bad-magic", "bad-header-crc", "truncated-header", "truncated-data", "header-size-lie", "unterminated-vint", "overflow-vint", "kdf-extreme", "encryption-truncated", "quick-open-conflict":
	case "packed-size-lie":
		packed = 0xfffffffffffffff0
	case "unpacked-size-lie":
		unpacked = 0xfffffffffffffff0
	case "bad-data-crc":
		crc = 0
	case "unknown-method":
		compression = 7 << 7
	case "split-before":
		flags |= 8
	case "split-after":
		flags |= 16
	case "solid-without-predecessor":
		compression = 0x40
	case "unknown-block":
		kind = 127
	case "extra-size-lie":
		flags |= 1
	case "high-dictionary":
		compression = (1 << 7) | (15 << 10)
	case "unknown-size":
		fileflags |= 8
		unpacked = 0xffffffffffffffff
	case "symlink-record", "symlink-absolute-record":
		target := []byte("../escape.canary")
		if v == "symlink-absolute-record" {
			target = []byte("/scratch/escape.canary")
		}
		record := []byte{5, 1, 0}
		record = append(record, vint(uint64(len(target)))...)
		record = append(record, target...)
		extra = append(vint(uint64(len(record))), record...)
		flags |= 1
	default:
		panic("unknown RAR5 mutation " + v)
	}
	body := append(vint(kind), vint(flags)...)
	if flags&1 != 0 {
		n := uint64(len(extra))
		if v == "extra-size-lie" {
			n = 0xffffffffffffffff
		}
		body = append(body, vint(n)...)
	}
	body = append(body, vint(packed)...)
	body = append(body, vint(fileflags)...)
	body = append(body, vint(unpacked)...)
	body = append(body, 0)
	body = append(body, fixed32(crc)...)
	body = append(body, vint(compression)...)
	body = append(body, 1)
	body = append(body, vint(uint64(len([]byte(name))))...)
	body = append(body, []byte(name)...)
	body = append(body, extra...)
	h := rar5Block(body)
	if v == "header-size-lie" {
		rest := append(vint(0x1fffff), body...)
		h = append(fixed32(crc32.ChecksumIEEE(rest)), rest...)
	}
	if v == "unterminated-vint" {
		rest := bytes.Repeat([]byte{0x80}, 32)
		h = append(fixed32(crc32.ChecksumIEEE(rest)), rest...)
	}
	if v == "overflow-vint" {
		rest := append(bytes.Repeat([]byte{0xff}, 10), 1)
		rest = append(rest, body...)
		h = append(fixed32(crc32.ChecksumIEEE(rest)), rest...)
	}
	if v == "bad-header-crc" {
		h[0] ^= 1
	}
	d := []byte{'R', 'a', 'r', '!', 0x1a, 7, 1, 0}
	if v == "kdf-extreme" || v == "encryption-truncated" {
		enc := []byte{4, 0, 0, 0, 255}
		enc = append(enc, make([]byte, 16)...)
		if v == "encryption-truncated" {
			enc = enc[:7]
		}
		d = append(d, rar5Block(enc)...)
		return d
	}
	d = append(d, main...)
	d = append(d, h...)
	if v == "truncated-header" {
		return d[:8+len(main)+7]
	}
	if v == "truncated-data" {
		return append(d, data[:len(data)/2]...)
	}
	d = append(d, data...)
	if v == "quick-open-conflict" {
		service := []byte{3, 2, 4, 0, 4, 0, 0, 1, 2, 'Q', 'O'}
		d = append(d, rar5Block(service)...)
		d = append(d, []byte{255, 255, 255, 255}...)
	}
	d = append(d, rar5Block([]byte{5, 0, 0})...)
	if v == "bad-magic" {
		d[0] ^= 255
	}
	return d
}

// 7z UINT64 is a prefix-length encoding, unlike RAR5's base-128 integers.
func sevenUint(v uint64) []byte {
	for n := 0; n < 8; n++ {
		if v < uint64(1)<<uint(7+7*n) {
			prefix := byte(0xff << uint(8-n))
			first := prefix | byte(v>>uint(8*n))
			b := []byte{first}
			for j := 0; j < n; j++ {
				b = append(b, byte(v>>uint(8*j)))
			}
			return b
		}
	}
	return append([]byte{255}, fixed64(v)...)
}
func sevenZip(name string, data []byte, v string) []byte {
	packed, unpacked := uint64(len(data)), uint64(len(data))
	coder := []byte{1, 1, 0}
	count := uint64(1)
	switch v {
	case "", "bad-magic", "truncated-start", "bad-start-crc", "bad-next-crc", "next-offset-overflow", "next-offset-beyond-eof", "next-size-overflow", "overlapping-header", "truncated-data", "encoded-header", "trailing-data":
	case "unpack-size-lie":
		unpacked = 0xffffffffffffffff
	case "file-count-lie":
		count = 0xffffffffffffffff
	case "unknown-coder":
		coder = []byte{1, 1, 255}
	case "high-dictionary":
		coder = []byte{1, 0x23, 3, 1, 1, 5, 0x5d, 255, 255, 255, 127}
	case "cyclic-coder":
		coder = []byte{2, 0x11, 0, 1, 1, 0x11, 0, 1, 1, 0, 0}
	default:
		panic("unknown 7z mutation " + v)
	}
	h := []byte{1, 4, 6, 0, 1, 9}
	h = append(h, sevenUint(packed)...)
	h = append(h, 0, 7, 11, 1, 0)
	h = append(h, coder...)
	h = append(h, 12)
	h = append(h, sevenUint(unpacked)...)
	// A folder with a data stream still needs SubStreamsInfo, even when it has
	// exactly one implicit substream. Omitting the section produces a header
	// that some readers reject as "streams but no SubStreamsInfo".
	h = append(h, 0, 8, 10, 1)
	h = append(h, fixed32(crc32.ChecksumIEEE(data))...)
	h = append(h, 0, 0, 5)
	h = append(h, sevenUint(count)...)
	encoded := []byte{0}
	for _, r := range utf16.Encode([]rune(name)) {
		encoded = append(encoded, byte(r), byte(r>>8))
	}
	encoded = append(encoded, 0, 0)
	h = append(h, 17)
	h = append(h, sevenUint(uint64(len(encoded)))...)
	h = append(h, encoded...)
	h = append(h, 0, 0)
	if v == "encoded-header" {
		h[0] = 23
	}
	start := make([]byte, 32)
	copy(start, []byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c, 0, 4})
	put64(start, 12, uint64(len(data)))
	put64(start, 20, uint64(len(h)))
	put32(start, 28, crc32.ChecksumIEEE(h))
	switch v {
	case "next-offset-overflow":
		put64(start, 12, 0xffffffffffffffff)
	case "next-offset-beyond-eof":
		put64(start, 12, 1<<40)
	case "next-size-overflow":
		put64(start, 20, 0xffffffffffffffff)
	case "overlapping-header":
		put64(start, 12, 0)
	case "bad-next-crc":
		start[28] ^= 1
	}
	put32(start, 8, crc32.ChecksumIEEE(start[12:]))
	if v == "bad-start-crc" {
		start[8] ^= 1
	}
	if v == "bad-magic" {
		start[0] ^= 255
	}
	if v == "truncated-start" {
		return start[:17]
	}
	d := append(start, data...)
	d = append(d, h...)
	if v == "truncated-data" {
		return d[:32+len(data)/2]
	}
	if v == "trailing-data" {
		d = append(d, []byte("UNEXPECTED-TAIL")...)
	}
	return d
}

func md5Bytes(b []byte) []byte { h := md5.Sum(b); return h[:] }
func packet(kind string, set, body []byte) []byte {
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	p := make([]byte, 64)
	copy(p, "PAR2\x00PKT")
	put64(p, 8, uint64(64+len(body)))
	copy(p[32:48], set)
	copy(p[48:64], "PAR 2.0\x00"+kind)
	p = append(p, body...)
	copy(p[16:32], md5Bytes(p[32:]))
	return p
}
func rehashPacket(p []byte) { copy(p[16:32], md5Bytes(p[32:])) }

// A single protected slice makes exponent-zero recovery an independently
// checkable identity operation: its recovery bytes equal the padded source.
func par2(name string, data []byte, v string) []byte {
	sliceSize := uint64((len(data) + 3) / 4 * 4)
	if sliceSize == 0 {
		sliceSize = 4
	}
	first := data
	if len(first) > 16384 {
		first = first[:16384]
	}
	idInput := append(md5Bytes(first), fixed64(uint64(len(data)))...)
	idInput = append(idInput, []byte(name)...)
	id := md5Bytes(idInput)
	mainBody := append(fixed64(sliceSize), fixed32(1)...)
	mainBody = append(mainBody, id...)
	desc := append(append(append(append([]byte{}, id...), md5Bytes(data)...), md5Bytes(first)...), fixed64(uint64(len(data)))...)
	desc = append(desc, []byte(name)...)
	block := append([]byte{}, data...)
	for uint64(len(block)) < sliceSize {
		block = append(block, 0)
	}
	ifsc := append(append(append([]byte{}, id...), md5Bytes(block)...), fixed32(crc32.ChecksumIEEE(block))...)
	rec := append(fixed32(0), block...)
	switch v {
	case "zero-slice":
		put64(mainBody, 0, 0)
	case "unaligned-slice":
		put64(mainBody, 0, 3)
	case "huge-slice":
		put64(mainBody, 0, 0xfffffffffffffffc)
	case "file-count-lie":
		put32(mainBody, 8, 0xffffffff)
	case "duplicate-file-id":
		mainBody = append(mainBody, id...)
		put32(mainBody, 8, 2)
	case "bad-file-id":
		desc[0] ^= 1
	case "bad-full-hash":
		desc[16] ^= 1
	case "bad-first16k-hash":
		desc[32] ^= 1
	case "file-length-lie":
		put64(desc, 48, 0xffffffffffffffff)
	case "consistent-file-length-overflow":
		put64(desc, 48, 0xffffffffffffffff)
	case "consistent-zero-file-length":
		put64(desc, 48, 0)
	case "consistent-first16k-mismatch":
		desc[32] ^= 1
	case "ifsc-short":
		ifsc = ifsc[:len(ifsc)-4]
	case "ifsc-extra":
		ifsc = append(ifsc, make([]byte, 20)...)
	case "recovery-short":
		rec = rec[:len(rec)-4]
	case "recovery-long":
		rec = append(rec, make([]byte, 4)...)
	case "max-exponent":
		put32(rec, 0, 0xffffffff)
	}
	// These variants must reach metadata/size validation instead of stopping
	// at an unrelated File ID mismatch. File ID covers first-16K hash, length
	// and unpadded name; changing it also changes Main, the set ID and IFSC.
	if v == "consistent-file-length-overflow" || v == "consistent-zero-file-length" || v == "consistent-first16k-mismatch" {
		id = md5Bytes(desc[32:])
		copy(desc[:16], id)
		copy(mainBody[12:28], id)
		copy(ifsc[:16], id)
	}
	set := md5Bytes(mainBody)
	main := packet("Main", set, mainBody)
	description := packet("FileDesc", set, desc)
	checks := packet("IFSC", set, ifsc)
	recovery := packet("RecvSlic", set, rec)
	creator := packet("Creator", set, []byte("usenet-bench recipe 1"))
	packets := [][]byte{main, description, checks, creator}
	switch v {
	case "", "zero-slice", "unaligned-slice", "huge-slice", "file-count-lie", "duplicate-file-id", "bad-file-id", "bad-full-hash", "bad-first16k-hash", "file-length-lie", "ifsc-short", "ifsc-extra":
	case "consistent-file-length-overflow", "consistent-zero-file-length", "consistent-first16k-mismatch":
	case "recovery-only":
		packets = append(packets, recovery)
	case "bad-magic":
		main[0] ^= 255
	case "truncated-header":
		return main[:31]
	case "zero-length":
		put64(main, 8, 0)
	case "short-length":
		put64(main, 8, 16)
	case "unaligned-length":
		put64(main, 8, uint64(len(main)-1))
	case "overflow-length":
		put64(main, 8, 0xffffffffffffffff)
	case "length-beyond-eof":
		put64(main, 8, 1<<40)
	case "bad-packet-hash":
		main[16] ^= 1
	case "unknown-packet":
		packets = append(packets, packet("Unknown!", set, bytes.Repeat([]byte{255}, 4096)))
	case "duplicate-packets":
		packets = append(packets, main, description, checks)
	case "conflicting-main":
		m := append([]byte{}, main...)
		put64(m, 64, sliceSize*2)
		rehashPacket(m)
		packets = append(packets, m)
	case "reverse-packets":
		packets = [][]byte{creator, checks, description, main}
	case "junk-between-packets":
		packets = [][]byte{main, bytes.Repeat([]byte("PAR2\x00PKX"), 128), description, checks, creator}
	case "foreign-set":
		description[32] ^= 1
		rehashPacket(description)
	case "missing-main":
		packets = packets[1:]
	case "missing-description":
		packets = [][]byte{main, checks, creator}
	case "ifsc-conflict":
		p := append([]byte{}, checks...)
		p[80] ^= 1
		rehashPacket(p)
		packets = append(packets, p)
	case "recovery-short", "recovery-long", "max-exponent":
		packets = append(packets, recovery)
	case "duplicate-exponent":
		packets = append(packets, recovery, recovery)
	case "conflicting-exponent":
		p := append([]byte{}, recovery...)
		p[68] ^= 1
		rehashPacket(p)
		packets = append(packets, recovery, p)
	case "unicode-override":
		body := append([]byte{}, id...)
		body = append(body, 0xff, 0xfe)
		for _, r := range utf16.Encode([]rune("../escape.canary")) {
			body = append(body, byte(r), byte(r>>8))
		}
		packets = append(packets, packet("UniFileN", set, body))
	case "many-packets":
		for i := 0; i < 8193; i++ {
			packets = append(packets, creator)
		}
	case "many-sets":
		for i := 0; i < 4097; i++ {
			body := append([]byte{}, mainBody...)
			put64(body, 0, uint64(i+1)*4)
			packets = append(packets, packet("Main", md5Bytes(body), body))
		}
	case "filename-count-lie":
		packets = append(packets, packet("FileDesc", set, append(desc, []byte("\x00second-file.bin")...)))
	default:
		panic("unknown PAR2 mutation " + v)
	}
	return bytes.Join(packets, nil)
}

func repair(b *Bundle, v string) {
	original := payload
	source := append([]byte{}, original...)
	source[0] ^= 1
	par := par2("payload.bin", original, "recovery-only")
	if v == "insufficient" {
		par = par2("payload.bin", original, "")
	}
	if v == "lying-recovery" {
		start := bytes.LastIndex(par, []byte("PAR2\x00PKT"))
		par[start+68] ^= 1
		rehashPacket(par[start:])
	}
	if v == "damaged-recovery-packet" {
		start := bytes.LastIndex(par, []byte("PAR2\x00PKT"))
		damaged := append([]byte{}, par[start:]...)
		damaged[16] ^= 1
		par = append(damaged, par...)
	}
	if v == "missing-file" {
		b.post(namedBytes{"fixture.par2", par})
	} else {
		name := "payload.bin"
		if v == "renamed-file" {
			name = "obfuscated.bin"
			source = original
		}
		b.post(namedBytes{name, source}, namedBytes{"fixture.par2", par})
	}
	if b.Manifest.Case.Expectation == Recover {
		b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(original)}
	}
}
