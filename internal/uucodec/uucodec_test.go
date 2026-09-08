package uucodec

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

// decodeReference is an independent decoder written straight from the format
// description, used to check the encoder inside the unit suite. It is not a
// substitute for the pinned UUDeview oracle, which is what actually clears a
// fixture for posting; it is here so a mistake in the encoder is caught
// without Docker.
func decodeReference(t *testing.T, stream []byte) (string, []byte) {
	t.Helper()
	lines := strings.Split(string(stream), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "begin 644 ") {
		t.Fatalf("stream does not begin with a uuencode header: %q", lines[0])
	}
	name := strings.TrimPrefix(lines[0], "begin 644 ")
	var out []byte
	for index := 1; index < len(lines); index++ {
		line := lines[index]
		if line == "`" {
			if index+1 >= len(lines) || lines[index+1] != "end" {
				t.Fatal("the terminating backquote is not followed by end")
			}
			return name, out
		}
		if line == "" {
			continue
		}
		length := int(sixBits(line[0]))
		var decoded []byte
		for offset := 1; offset+3 < len(line)+1 && offset < len(line); offset += 4 {
			var group [4]byte
			copy(group[:], line[offset:])
			first := sixBits(group[0])<<2 | sixBits(group[1])>>4
			second := sixBits(group[1])<<4 | sixBits(group[2])>>2
			third := sixBits(group[2])<<6 | sixBits(group[3])
			decoded = append(decoded, first, second, third)
		}
		if len(decoded) < length {
			t.Fatalf("line %d decoded to %d bytes, header claims %d", index, len(decoded), length)
		}
		out = append(out, decoded[:length]...)
	}
	t.Fatal("stream has no terminator")
	return "", nil
}

func sixBits(character byte) byte {
	if character == '`' {
		return 0
	}
	return (character - 32) & 0x3f
}

func TestEncodeRoundTripsThroughAnIndependentDecoder(t *testing.T) {
	source := rand.New(rand.NewSource(20260908))
	// Sizes either side of a line boundary, so the short final line is
	// exercised in all three of its shapes.
	for _, size := range []int{1, 2, 3, 44, 45, 46, 45*7 + 1, 45 * 200} {
		payload := make([]byte, size)
		source.Read(payload)
		name, decoded := decodeReference(t, Encode("payload-01.mkv", payload))
		if name != "payload-01.mkv" {
			t.Fatalf("decoded name = %q", name)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("round trip lost bytes at size %d", size)
		}
	}
}

func TestEncodeLineNeverEndsInASpace(t *testing.T) {
	// A trailing space is exactly the character a news path is most likely to
	// strip, and a stripped one silently corrupts the article.
	for value := 0; value < 256; value++ {
		line := EncodeLine([]byte{byte(value), byte(value), byte(value)})
		if bytes.ContainsRune(line, ' ') {
			t.Fatalf("EncodeLine(%d) = %q, which contains a space", value, line)
		}
	}
	if got := EncodeLine([]byte{0, 0, 0}); string(got) != "#````" {
		t.Fatalf("EncodeLine(zeroes) = %q, want a length of three then backquotes", got)
	}
}

func TestEncodeLinesAreFullExceptTheLast(t *testing.T) {
	payload := make([]byte, 45*3+7)
	stream := string(Encode("payload-01.mkv", payload))
	lines := strings.Split(strings.TrimSuffix(stream, "\n"), "\n")
	// begin, three full lines, one short line, backquote, end
	if len(lines) != 7 {
		t.Fatalf("stream has %d lines: %q", len(lines), lines)
	}
	for _, line := range lines[1:4] {
		if len(line) != 61 {
			t.Fatalf("full line has %d characters, want 61: %q", len(line), line)
		}
	}
	if lines[5] != "`" || lines[6] != "end" {
		t.Fatalf("stream does not terminate correctly: %q", lines[5:])
	}
}

func TestLinesPerArticleFitsWholeLinesOnly(t *testing.T) {
	for _, testCase := range []struct {
		raw   int
		lines int
	}{
		{768000, 17066},
		{393216, 8738},
	} {
		lines, err := LinesPerArticle(testCase.raw)
		if err != nil {
			t.Fatal(err)
		}
		if lines != testCase.lines {
			t.Fatalf("LinesPerArticle(%d) = %d, want %d", testCase.raw, lines, testCase.lines)
		}
		if lines*BytesPerLine > testCase.raw {
			t.Fatalf("LinesPerArticle(%d) overflows the article", testCase.raw)
		}
	}
	if _, err := LinesPerArticle(44); err == nil {
		t.Fatal("an article smaller than one line should be refused")
	}
}

func TestToolchainRequiresItsUpstreamPin(t *testing.T) {
	valid := Toolchain{
		SchemaVersion: 1,
		ID:            "uudeview-0.5.20",
		Image:         "weaver-nntp-bench-uudeview:0.5.20",
		Platform:      "linux/amd64",
		URL:           "https://deb.debian.org/debian/pool/main/u/uudeview/uudeview_0.5.20.orig.tar.gz",
		SHA256:        strings.Repeat("a", 64),
		Version:       "0.5.20",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a complete pin should validate: %v", err)
	}
	for name, mutate := range map[string]func(*Toolchain){
		"no sha256":    func(t *Toolchain) { t.SHA256 = "" },
		"short sha":    func(t *Toolchain) { t.SHA256 = "abcd" },
		"no url":       func(t *Toolchain) { t.URL = "" },
		"bad scheme":   func(t *Toolchain) { t.URL = "ftp://example.invalid/uudeview.tar.gz" },
		"no version":   func(t *Toolchain) { t.Version = "" },
		"wrong schema": func(t *Toolchain) { t.SchemaVersion = 2 },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("%s should not validate", name)
		}
	}
}
