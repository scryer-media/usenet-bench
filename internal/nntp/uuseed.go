package nntp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/scryer-media/usenet-bench/internal/fixture"
	"github.com/scryer-media/usenet-bench/internal/uucodec"
	"github.com/zeebo/blake3"
)

// UUSeedConfig is what the uuencode lane needs that the Nyuu lane does not.
//
// Nyuu posts from inside the server's Docker network; this poster runs in the
// seeding process, so it needs an address reachable from here. The shaper
// stack publishes the plaintext port on the loopback interface by default and
// passes it through to the same server Nyuu reaches as `nntp-upstream`, so
// that is the default — but it is stated explicitly rather than derived,
// because posting to the wrong server would produce a corpus that seeds and
// then fails every run.
type UUSeedConfig struct {
	DockerBinary string
	// UUToolchain and UUImagePlatform pin the decoder that proves the post.
	UUToolchain uucodec.Toolchain
	FixtureDir  string
	RunID       string
	NZBPath     string
	// Addr is host:port of the NNTP server as reachable from this process.
	Addr     string
	TLS      bool
	Username string
	Password string
	Group    string
	// ArticleBytes is the declared raw article size. uuencode rounds it down
	// to a whole number of 45-byte lines; see uucodec.LinesPerArticle.
	ArticleBytes int
	// Timeout bounds the whole posting conversation.
	Timeout time.Duration
}

const (
	defaultUUTimeout = 30 * time.Minute
	uuFromHeader     = "nntp-bench@example.invalid"
)

// UUMessageIDTemplate is the message-id scheme the uuencode poster expands
// per article. It is deliberately distinct from the yEnc scheme rather than a
// reuse of it: the two posters run over the same server in the same seed run,
// and an accidental collision would silently replace one lane's article with
// the other's.
func UUMessageIDTemplate(runID, fixtureID string) string {
	return fmt.Sprintf("bench-%s-%s-uu-{0filenum}-{0part}@nntp-bench", safeID(runID), safeID(fixtureID))
}

func uuMessageID(runID, fixtureID string, fileNumber, part int) string {
	return fmt.Sprintf("bench-%s-%s-uu-%03d-%05d@nntp-bench", safeID(runID), safeID(fixtureID), fileNumber, part)
}

// SeedUUEncoded posts one uuencoded fixture over plain NNTP and writes its
// NZB. It refuses to accept the seed until the articles have been read back
// off the server and decoded by the pinned UUDeview build into bytes whose
// BLAKE3 digest matches the payload the generator recorded — so a corpus that
// posted successfully but encoded, split, dot-stuffed or ordered wrongly
// fails here rather than as an unexplainable client failure hours later.
func SeedUUEncoded(ctx context.Context, config UUSeedConfig) (SeedResult, error) {
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return SeedResult{}, err
	}
	fixtureDir, err := filepath.Abs(config.FixtureDir)
	if err != nil {
		return SeedResult{}, fmt.Errorf("resolve fixture directory: %w", err)
	}
	manifest, err := fixture.LoadGeneratedManifest(filepath.Join(fixtureDir, "fixture-manifest.json"))
	if err != nil {
		return SeedResult{}, err
	}
	if manifest.Case.PostEncodingOrDefault() != fixture.UUEncodeEncoding {
		return SeedResult{}, fmt.Errorf("fixture %q is %s-encoded; seed it with Nyuu", manifest.Case.ID, manifest.Case.PostEncodingOrDefault())
	}
	if len(manifest.WithheldFiles) > 0 {
		return SeedResult{}, fmt.Errorf("fixture %q withholds files; the uuencode lane posts a complete file", manifest.Case.ID)
	}
	if err := verifyArchiveFiles(fixtureDir, manifest.ArchiveFiles); err != nil {
		return SeedResult{}, err
	}
	plan, err := newPostingPlan(manifest)
	if err != nil {
		return SeedResult{}, err
	}
	nzbPath, err := resolveSeedNZBPath(fixtureDir, config.NZBPath, manifest.Case.ID)
	if err != nil {
		return SeedResult{}, err
	}
	linesPerArticle, err := uucodec.LinesPerArticle(config.ArticleBytes)
	if err != nil {
		return SeedResult{}, err
	}

	files := make([]NZBFile, 0, len(plan.Posted))
	articles := 0
	posted := time.Now().UTC()
	for index, relative := range plan.Posted {
		payload, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(relative)))
		if err != nil {
			return SeedResult{}, fmt.Errorf("read posted file %s: %w", relative, err)
		}
		name := path.Base(relative)
		parts := splitUUArticles(name, payload, linesPerArticle)
		if len(parts) == 0 {
			return SeedResult{}, fmt.Errorf("posted file %s produced no articles", relative)
		}
		file, err := postUUFile(ctx, config, manifest.Case.ID, name, index, parts, posted)
		if err != nil {
			return SeedResult{}, err
		}
		files = append(files, file)
		articles += len(file.Segments)
	}
	if articles == 0 {
		return SeedResult{}, fmt.Errorf("fixture %q produced no article segments", manifest.Case.ID)
	}

	// Prove the post from the server's own copy before the NZB is written: an
	// NZB on disk is what every later step trusts, so it must not exist until
	// the articles behind it have been read back and decoded.
	if err := proveUUPost(ctx, config, fixtureDir, manifest, files); err != nil {
		return SeedResult{}, err
	}

	contents, err := MarshalNZB(files)
	if err != nil {
		return SeedResult{}, fmt.Errorf("render NZB for %q: %w", manifest.Case.ID, err)
	}
	if err := os.WriteFile(nzbPath, contents, 0o644); err != nil {
		return SeedResult{}, fmt.Errorf("write NZB %s: %w", nzbPath, err)
	}
	document, err := UnmarshalNZB(contents)
	if err != nil {
		return SeedResult{}, fmt.Errorf("parse the NZB just written to %s: %w", nzbPath, err)
	}
	if err := assertNZBFileOrder(document, plan.Order); err != nil {
		return SeedResult{}, fmt.Errorf("NZB %s: %w", nzbPath, err)
	}
	if err := WriteArticleAttestation(nzbPath, manifest, config.ArticleBytes, "builtin-uuencode"); err != nil {
		return SeedResult{}, err
	}
	return SeedResult{
		FixtureID:    manifest.Case.ID,
		NZBPath:      nzbPath,
		Files:        len(document.Files),
		Articles:     articles,
		NZBOrder:     manifest.Case.NZBOrder,
		NZBOrderSeed: manifest.NZBOrderSeed,
		NZBFileOrder: plan.Order,
	}, nil
}

// uuArticle is one posted article: the exact body lines, and the payload
// range they encode.
type uuArticle struct {
	lines      []string
	rawOffset  int64
	rawBytes   int64
	wireBytes  int64
	partNumber int
}

// splitUUArticles cuts one file's uuencoded stream into articles at line
// boundaries. The concatenation of every article's body, in part order, is
// exactly the stream uucodec.Encode produces for the whole file — which is
// what makes an ordinary uudecode of the reassembled post work.
func splitUUArticles(name string, payload []byte, linesPerArticle int) []uuArticle {
	articles := make([]uuArticle, 0, len(payload)/(linesPerArticle*uucodec.BytesPerLine)+1)
	var current uuArticle
	current.partNumber = 1
	current.rawOffset = 0
	// The begin header belongs to the first article, exactly where a
	// single-part post would put it.
	current.lines = append(current.lines, fmt.Sprintf("begin 644 %s", name))
	lines := 0
	for offset := 0; offset < len(payload); offset += uucodec.BytesPerLine {
		end := offset + uucodec.BytesPerLine
		if end > len(payload) {
			end = len(payload)
		}
		current.lines = append(current.lines, string(uucodec.EncodeLine(payload[offset:end])))
		current.rawBytes += int64(end - offset)
		lines++
		if lines == linesPerArticle && end < len(payload) {
			current.wireBytes = uuWireBytes(current.lines)
			articles = append(articles, current)
			current = uuArticle{partNumber: len(articles) + 1, rawOffset: int64(end)}
			lines = 0
		}
	}
	// The terminating backquote and end line belong to the last article.
	current.lines = append(current.lines, "`", "end")
	current.wireBytes = uuWireBytes(current.lines)
	return append(articles, current)
}

// uuWireBytes is the size an NZB segment reports: the article body as it goes
// on the wire, CRLF terminators included and dot-stuffing excluded. It is the
// same convention Nyuu's segment sizes follow, so a client sizing its buffers
// from the NZB sees the two lanes alike.
func uuWireBytes(lines []string) int64 {
	var total int64
	for _, line := range lines {
		total += int64(len(line)) + 2
	}
	return total
}

// uuSubject is the classic multi-part binary subject: the quoted file name,
// the part counter, and the file's decoded size. It is the shape a uuencoded
// post carried before yEnc existed, and it is what every client's subject
// parser keys on.
func uuSubject(name string, part, parts int, size int64) string {
	return fmt.Sprintf("%q (%d/%d) %d", name, part, parts, size)
}

// postUUFile posts every article of one file and returns the NZB entry that
// describes it.
func postUUFile(ctx context.Context, config UUSeedConfig, fixtureID, name string, fileIndex int, parts []uuArticle, posted time.Time) (NZBFile, error) {
	session, err := dialNNTP(ctx, config)
	if err != nil {
		return NZBFile{}, err
	}
	defer session.Close()

	file := NZBFile{
		Poster:   uuFromHeader,
		Date:     posted.Unix(),
		Subject:  uuSubject(name, 1, len(parts), totalRawBytes(parts)),
		Groups:   []NZBGroup{NZBGroup(config.Group)},
		Segments: make([]NZBSegment, 0, len(parts)),
	}
	for _, article := range parts {
		messageID := uuMessageID(config.RunID, fixtureID, fileIndex+1, article.partNumber)
		headers := []string{
			"From: " + uuFromHeader,
			"Newsgroups: " + config.Group,
			"Subject: " + uuSubject(name, article.partNumber, len(parts), totalRawBytes(parts)),
			"Message-ID: <" + messageID + ">",
			"Date: " + posted.Format(time.RFC1123Z),
		}
		if err := session.post(headers, article.lines); err != nil {
			return NZBFile{}, fmt.Errorf("post article %d of %s: %w", article.partNumber, name, err)
		}
		file.Segments = append(file.Segments, NZBSegment{
			Bytes:     article.wireBytes,
			Number:    article.partNumber,
			MessageID: messageID,
		})
	}
	return file, nil
}

func totalRawBytes(parts []uuArticle) int64 {
	var total int64
	for _, part := range parts {
		total += part.rawBytes
	}
	return total
}

// proveUUPost reads every posted article back off the server, reassembles the
// stream and hands it to the pinned decoder. Only if the decoder recovers
// bytes matching the payload's recorded BLAKE3 digest is the seed accepted.
func proveUUPost(ctx context.Context, config UUSeedConfig, fixtureDir string, manifest fixture.GeneratedManifest, files []NZBFile) error {
	session, err := dialNNTP(ctx, config)
	if err != nil {
		return err
	}
	defer session.Close()

	digests := make(map[string]fixture.FileDigest, len(manifest.ArchiveFiles))
	for _, file := range manifest.ArchiveFiles {
		digests[path.Base(file.Path)] = file
	}
	proofDir, err := os.MkdirTemp(fixtureDir, "uu-post-proof-")
	if err != nil {
		return fmt.Errorf("create uuencode proof directory: %w", err)
	}
	defer os.RemoveAll(proofDir)
	relativeProof, err := filepath.Rel(fixtureDir, proofDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		name, err := nzbFileName(file.Subject)
		if err != nil {
			return err
		}
		expected, ok := digests[name]
		if !ok {
			return fmt.Errorf("posted file %q is not in the fixture manifest", name)
		}
		var stream bytes.Buffer
		for _, segment := range file.Segments {
			body, err := session.body(segment.MessageID)
			if err != nil {
				return fmt.Errorf("read back article %d of %s: %w", segment.Number, name, err)
			}
			stream.Write(body)
		}
		streamName := name + ".uu"
		if err := os.WriteFile(filepath.Join(proofDir, streamName), stream.Bytes(), 0o644); err != nil {
			return fmt.Errorf("stage the read-back stream for %s: %w", name, err)
		}
		decoded, err := uucodec.Decode(ctx, config.DockerBinary, config.UUToolchain, fixtureDir,
			filepath.ToSlash(filepath.Join(relativeProof, streamName)), name)
		if err != nil {
			return fmt.Errorf("prove the post of %s: %w", name, err)
		}
		if int64(len(decoded)) != expected.Size {
			return fmt.Errorf("the pinned decoder recovered %d bytes of %s from the server, expected %d", len(decoded), name, expected.Size)
		}
		sum := blake3.Sum256(decoded)
		if hex.EncodeToString(sum[:]) != expected.BLAKE3 {
			return fmt.Errorf("the pinned decoder recovered %s from the server, but the bytes do not match the payload oracle", name)
		}
	}
	return nil
}

// nntpSession is a minimal NNTP client: enough to authenticate, post an
// article and read one back. It exists because Nyuu cannot write uuencode,
// and it does nothing a benchmark measures.
type nntpSession struct {
	conn net.Conn
	text *textproto.Conn
}

func dialNNTP(ctx context.Context, config UUSeedConfig) (*nntpSession, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	var err error
	if config.TLS {
		host, _, splitErr := net.SplitHostPort(config.Addr)
		if splitErr != nil {
			return nil, fmt.Errorf("parse NNTP address %q: %w", config.Addr, splitErr)
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", config.Addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", config.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial NNTP server %s: %w", config.Addr, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(config.Timeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, err
	}
	session := &nntpSession{conn: conn, text: textproto.NewConn(conn)}
	if _, _, err := session.text.ReadCodeLine(20); err != nil {
		session.Close()
		return nil, fmt.Errorf("NNTP greeting from %s: %w", config.Addr, err)
	}
	if config.Username != "" {
		if err := session.authenticate(config.Username, config.Password); err != nil {
			session.Close()
			return nil, err
		}
	}
	return session, nil
}

func (s *nntpSession) authenticate(username, password string) error {
	id, err := s.text.Cmd("AUTHINFO USER %s", username)
	if err != nil {
		return err
	}
	s.text.StartResponse(id)
	code, message, err := s.text.ReadCodeLine(-1)
	s.text.EndResponse(id)
	if err != nil {
		return fmt.Errorf("AUTHINFO USER: %w", err)
	}
	if code == 281 {
		return nil
	}
	if code != 381 {
		return fmt.Errorf("AUTHINFO USER refused: %d %s", code, message)
	}
	id, err = s.text.Cmd("AUTHINFO PASS %s", password)
	if err != nil {
		return err
	}
	s.text.StartResponse(id)
	code, message, err = s.text.ReadCodeLine(-1)
	s.text.EndResponse(id)
	if err != nil {
		return fmt.Errorf("AUTHINFO PASS: %w", err)
	}
	if code != 281 {
		return fmt.Errorf("NNTP authentication refused: %d %s", code, message)
	}
	return nil
}

// post writes one article. Every body line is dot-stuffed: a uuencode line
// carrying exactly 14 bytes begins with '.', and an unstuffed one would
// silently terminate the article mid-file.
func (s *nntpSession) post(headers, body []string) error {
	id, err := s.text.Cmd("POST")
	if err != nil {
		return err
	}
	s.text.StartResponse(id)
	code, message, err := s.text.ReadCodeLine(-1)
	s.text.EndResponse(id)
	if err != nil {
		return err
	}
	if code != 340 {
		return fmt.Errorf("POST refused: %d %s", code, message)
	}
	writer := bufio.NewWriterSize(s.conn, 1<<16)
	for _, header := range headers {
		if _, err := fmt.Fprintf(writer, "%s\r\n", header); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString("\r\n"); err != nil {
		return err
	}
	for _, line := range body {
		if strings.HasPrefix(line, ".") {
			if err := writer.WriteByte('.'); err != nil {
				return err
			}
		}
		if _, err := writer.WriteString(line); err != nil {
			return err
		}
		if _, err := writer.WriteString("\r\n"); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString(".\r\n"); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	s.text.StartResponse(id)
	code, message, err = s.text.ReadCodeLine(-1)
	s.text.EndResponse(id)
	if err != nil {
		return err
	}
	if code != 240 {
		return fmt.Errorf("article rejected: %d %s", code, message)
	}
	return nil
}

// body reads one article's body back, undoing dot-stuffing, and returns it
// with bare newline terminators — the form the decoder is handed.
func (s *nntpSession) body(messageID string) ([]byte, error) {
	id, err := s.text.Cmd("BODY <%s>", messageID)
	if err != nil {
		return nil, err
	}
	s.text.StartResponse(id)
	defer s.text.EndResponse(id)
	code, message, err := s.text.ReadCodeLine(-1)
	if err != nil {
		return nil, err
	}
	if code != 222 {
		return nil, fmt.Errorf("BODY refused: %d %s", code, message)
	}
	lines, err := s.text.ReadDotLines()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, line := range lines {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func (s *nntpSession) Close() {
	if s.text != nil {
		_, _ = s.text.Cmd("QUIT")
		_ = s.text.Close()
		return
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (c UUSeedConfig) withDefaults() UUSeedConfig {
	if c.DockerBinary == "" {
		c.DockerBinary = "docker"
	}
	if c.Group == "" {
		c.Group = "alt.binaries.test"
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:119"
	}
	if c.ArticleBytes == 0 {
		c.ArticleBytes = defaultSegmentBytes
	}
	if c.Timeout == 0 {
		c.Timeout = defaultUUTimeout
	}
	return c
}

func (c UUSeedConfig) validate() error {
	if c.FixtureDir == "" || c.RunID == "" {
		return fmt.Errorf("fixture directory and run id are required")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("uuencode poster address %q must be host:port: %w", c.Addr, err)
	}
	if c.ArticleBytes < 1024 {
		return fmt.Errorf("article bytes must be at least 1024")
	}
	if err := c.UUToolchain.Validate(); err != nil {
		return err
	}
	return nil
}

// resolveSeedNZBPath is the shared rule for where a seed writes its NZB, and
// the shared refusal to overwrite one that already exists.
func resolveSeedNZBPath(fixtureDir, requested, fixtureID string) (string, error) {
	nzbPath := requested
	if nzbPath == "" {
		nzbPath = filepath.Join(fixtureDir, fixtureID+".nzb")
	}
	nzbPath, err := filepath.Abs(nzbPath)
	if err != nil {
		return "", fmt.Errorf("resolve NZB output path: %w", err)
	}
	relative, err := filepath.Rel(fixtureDir, nzbPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("NZB output path must be inside fixture directory %s", fixtureDir)
	}
	if _, err := os.Stat(nzbPath); err == nil {
		return "", fmt.Errorf("NZB output already exists: %s (use a new run id/path to preserve prior evidence)", nzbPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect NZB output %s: %w", nzbPath, err)
	}
	return nzbPath, nil
}
