// Package uucodec is the corpus's uuencode implementation and the pinned
// decoder it is checked against.
//
// Nyuu cannot write uuencode, so the harness encodes and posts that lane
// itself. That would ordinarily be a problem: a hand-rolled encoder is
// exactly the kind of thing that produces bytes only its own author's decoder
// accepts, and a fixture that only this harness can read measures nothing. So
// the encoder here never ships a fixture on its own word. Every uuencoded
// corpus is decoded by UUDeview — a source-pinned build of the reference
// implementation of the format, compiled from a SHA-256-verified upstream
// tarball inside a digest-pinned Debian image — and the recovered bytes are
// checked against the payload's BLAKE3 digest before the fixture is accepted.
// The encoder is fast and deterministic; the oracle is what makes it true.
package uucodec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// BytesPerLine is fixed by the encoding: one uuencode line carries exactly 45
// input bytes as 60 output characters, plus a leading length character.
const BytesPerLine = 45

// Toolchain pins the UUDeview release the corpus is checked against. It has
// the same shape as the 7-Zip pin for the same reason: an upstream source
// tarball, verified by SHA-256 before anything is compiled.
type Toolchain struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	Version       string `json:"version"`
}

func LoadToolchain(path string) (Toolchain, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Toolchain{}, fmt.Errorf("read uuencode toolchain %s: %w", path, err)
	}
	var toolchain Toolchain
	if err := json.Unmarshal(contents, &toolchain); err != nil {
		return Toolchain{}, fmt.Errorf("decode uuencode toolchain %s: %w", path, err)
	}
	if err := toolchain.Validate(); err != nil {
		return Toolchain{}, err
	}
	return toolchain, nil
}

func (t Toolchain) Validate() error {
	if t.SchemaVersion != 1 {
		return fmt.Errorf("uuencode toolchain %q has unsupported schema version %d", t.ID, t.SchemaVersion)
	}
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Image) == "" || strings.TrimSpace(t.Platform) == "" {
		return fmt.Errorf("uuencode toolchain must include id, image, and platform")
	}
	if strings.TrimSpace(t.Version) == "" {
		return fmt.Errorf("uuencode toolchain %q must declare the upstream version it installs", t.ID)
	}
	parsed, err := url.Parse(t.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("uuencode toolchain %q must use an https URL", t.ID)
	}
	if len(t.SHA256) != 64 || strings.Trim(t.SHA256, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("uuencode toolchain %q has invalid SHA-256", t.ID)
	}
	return nil
}

func (t Toolchain) ManifestID() fixture.ToolchainID {
	return fixture.ToolchainID{
		ID:       t.ID,
		Image:    t.Image,
		URL:      t.URL,
		SHA256:   t.SHA256,
		Platform: t.Platform,
		Binary:   "uudeview",
		Version:  t.Version,
	}
}

// BuildImage compiles the pinned UUDeview release. The URL and hash are build
// arguments, so the image cannot contain a different release than the
// toolchain file names.
func BuildImage(ctx context.Context, dockerBinary, dockerfile string, toolchain Toolchain) error {
	args := []string{
		"build", "--platform", toolchain.Platform,
		"--tag", toolchain.Image,
		"--file", dockerfile,
		"--build-arg", "UUDEVIEW_URL=" + toolchain.URL,
		"--build-arg", "UUDEVIEW_SHA256=" + toolchain.SHA256,
		filepath.Dir(dockerfile),
	}
	if err := run(ctx, dockerBinary, args...); err != nil {
		return fmt.Errorf("build UUDeview image %s: %w", toolchain.ID, err)
	}
	return nil
}

// Encode writes the complete uuencoded stream for one file: the begin header,
// the data lines, the terminating backquote and the end line. Lines are
// separated by bare newlines; the poster is what turns them into CRLF wire
// lines, because that is a property of the transport and not of the encoding.
func Encode(name string, payload []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(payload)/BytesPerLine*62 + 64)
	fmt.Fprintf(&out, "begin 644 %s\n", name)
	for offset := 0; offset < len(payload); offset += BytesPerLine {
		end := offset + BytesPerLine
		if end > len(payload) {
			end = len(payload)
		}
		out.Write(EncodeLine(payload[offset:end]))
		out.WriteByte('\n')
	}
	out.WriteString("`\nend\n")
	return out.Bytes()
}

// EncodeLine encodes up to BytesPerLine bytes as one uuencode line, without a
// terminator. A zero six-bit value is written as a backquote rather than a
// space: both are legal and every decoder accepts either, but a trailing
// space is exactly the character a mail or news path is most likely to strip.
func EncodeLine(chunk []byte) []byte {
	if len(chunk) == 0 || len(chunk) > BytesPerLine {
		panic(fmt.Sprintf("uucodec: line carries %d bytes, want 1..%d", len(chunk), BytesPerLine))
	}
	line := make([]byte, 0, 1+((len(chunk)+2)/3)*4)
	line = append(line, encodeSixBits(byte(len(chunk))))
	for offset := 0; offset < len(chunk); offset += 3 {
		var group [3]byte
		copy(group[:], chunk[offset:])
		line = append(line,
			encodeSixBits(group[0]>>2),
			encodeSixBits((group[0]&0x03)<<4|group[1]>>4),
			encodeSixBits((group[1]&0x0f)<<2|group[2]>>6),
			encodeSixBits(group[2]&0x3f),
		)
	}
	return line
}

func encodeSixBits(value byte) byte {
	if value == 0 {
		return '`'
	}
	return value + 32
}

// LinesPerArticle is how many uuencode lines an article of the declared raw
// size carries. Every line but the file's very last must carry a full 45
// bytes, or the concatenation of the articles is not a decodable stream, so
// an article carries the largest whole number of lines that fits rather than
// the declared size exactly.
func LinesPerArticle(rawArticleBytes int) (int, error) {
	lines := rawArticleBytes / BytesPerLine
	if lines < 1 {
		return 0, fmt.Errorf("article size %d bytes is smaller than one uuencode line", rawArticleBytes)
	}
	return lines, nil
}

// Decode runs the pinned UUDeview decoder over a uuencoded stream and returns
// the recovered bytes. workDir is bind-mounted, streamName and outputName are
// relative to it, and the decoder writes the file under the name its own
// begin header carries — which is why the caller names it.
func Decode(ctx context.Context, dockerBinary string, toolchain Toolchain, workDir, streamName, outputName string) ([]byte, error) {
	decodeDir, err := os.MkdirTemp(workDir, "uudecode-")
	if err != nil {
		return nil, fmt.Errorf("create UUDeview output directory: %w", err)
	}
	defer os.RemoveAll(decodeDir)
	relative, err := filepath.Rel(workDir, decodeDir)
	if err != nil {
		return nil, err
	}
	args := []string{
		"run", "--rm", "--platform", toolchain.Platform,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--mount", "type=bind,src=" + workDir + ",dst=/work",
		"--workdir", "/work",
		toolchain.Image,
		// -i never overwrites, -q stays quiet, -o allows the one output file
		// this call expects, and -p names the directory to write into.
		"uudeview", "-i", "-q", "-o", "-p", filepath.ToSlash(relative), filepath.ToSlash(streamName),
	}
	if err := run(ctx, dockerBinary, args...); err != nil {
		return nil, fmt.Errorf("decode uuencoded stream with %s: %w", toolchain.ID, err)
	}
	decoded, err := os.ReadFile(filepath.Join(decodeDir, outputName))
	if err != nil {
		return nil, fmt.Errorf("the pinned UUDeview decoder produced no %s: %w", outputName, err)
	}
	return decoded, nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
