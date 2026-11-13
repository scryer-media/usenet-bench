package nntp

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// articleServer is what posting a corrupt article needs from the server: the
// original article's headers and body, and a POST. nntpSession is one.
type articleServer interface {
	head(messageID string) ([]string, error)
	body(messageID string) ([]byte, error)
	post(headers, body []string) error
}

// corruptArticleMessageID names the damaged copy of one posted article. Its
// "corrupt" segment keeps it apart from the posted identifiers, whose file
// field is always the poster's decimal file number, and from the never-posted
// "missing" and "withheld" ones.
func corruptArticleMessageID(runID, fixtureID string, fileIndex, part int) string {
	return fmt.Sprintf("bench-%s-%s-corrupt%03d-%05d@nntp-bench", safeID(runID), safeID(fixtureID), fileIndex, part)
}

func hasCorruptArticles(faults []fixture.CorruptionDetail) bool {
	for _, fault := range faults {
		if fault.Kind == fixture.CorruptArticleFault {
			return true
		}
	}
	return false
}

// corruptArticlesOnServer posts a damaged copy of every corrupt-article fault
// and points its NZB segment at the copy. The copy is the article Nyuu posted
// with its decoded bytes changed by fixture.CorruptArticle and re-encoded,
// under the original =ybegin, =ypart and =yend lines; its declared size is
// still right, and its declared part CRC is the original's, which the changed
// bytes no longer match. The original stays on the server where nothing names
// it, as a withheld article's does.
//
// Nothing is taken on trust: the original must decode to exactly the bytes the
// fault names in the file on disk, and the copy is read back and must decode
// to the changed bytes and fail its CRC, or the seed is refused.
func corruptArticlesOnServer(server articleServer, document NZBDocument, plan postingPlan, faults []fixture.CorruptionDetail, fixtureDir, runID, fixtureID string, segmentBytes int) (NZBDocument, int, error) {
	positions := make(map[string]int, len(plan.Order))
	for index, entry := range plan.Order {
		positions[entry] = index
	}
	corrupted := 0
	for _, fault := range faults {
		if fault.Kind != fixture.CorruptArticleFault {
			continue
		}
		if fault.Length != segmentBytes {
			return NZBDocument{}, 0, fmt.Errorf("corrupt articles were chosen for %d-byte articles, but this seed posts %d-byte articles", fault.Length, segmentBytes)
		}
		if len(document.Files) != len(plan.Order) {
			return NZBDocument{}, 0, fmt.Errorf("NZB lists %d files, expected %d", len(document.Files), len(plan.Order))
		}
		index, listed := positions[fault.Path]
		if !listed || plan.Withheld[fault.Path].Path != "" {
			return NZBDocument{}, 0, fmt.Errorf("corrupt article names %s, which is not a posted file", fault.Path)
		}
		if fault.Offset < 0 || fault.Offset%int64(segmentBytes) != 0 {
			return NZBDocument{}, 0, fmt.Errorf("corrupt article at %d in %s does not start on an article boundary", fault.Offset, fault.Path)
		}
		part := int(fault.Offset/int64(segmentBytes)) + 1
		segments := document.Files[index].Segments
		position := -1
		for segment := range segments {
			if segments[segment].Number == part {
				position = segment
				break
			}
		}
		if position < 0 {
			return NZBDocument{}, 0, fmt.Errorf("NZB has no article %d for %s", part, fault.Path)
		}
		original := segments[position].MessageID
		if strings.Contains(original, "-missing") || strings.Contains(original, "-corrupt") {
			return NZBDocument{}, 0, fmt.Errorf("article %d of %s is faulted twice", part, fault.Path)
		}
		expected, err := readFileRange(filepath.Join(fixtureDir, filepath.FromSlash(fault.Path)), fault.Offset, fault.Length)
		if err != nil {
			return NZBDocument{}, 0, err
		}
		copyID := corruptArticleMessageID(runID, fixtureID, index+1, part)
		delta, err := postCorruptCopy(server, original, copyID, fault, expected)
		if err != nil {
			return NZBDocument{}, 0, fmt.Errorf("article %d of %s: %w", part, fault.Path, err)
		}
		segments[position].MessageID = copyID
		segments[position].Bytes += delta
		corrupted++
	}
	return document, corrupted, nil
}

// postCorruptCopy posts the damaged copy of one article and proves it, and
// returns how many more wire bytes its body has than the original's.
func postCorruptCopy(server articleServer, originalID, copyID string, fault fixture.CorruptionDetail, expected []byte) (int64, error) {
	body, err := server.body(originalID)
	if err != nil {
		return 0, fmt.Errorf("read the posted article: %w", err)
	}
	article, err := parseYEncArticle(body)
	if err != nil {
		return 0, fmt.Errorf("parse the posted article: %w", err)
	}
	if article.begin != fault.Offset+1 || article.end != fault.Offset+int64(fault.Length) {
		return 0, fmt.Errorf("the posted article covers bytes %d-%d, but the fault names %d-%d", article.begin, article.end, fault.Offset+1, fault.Offset+int64(fault.Length))
	}
	if !bytes.Equal(article.data, expected) {
		return 0, fmt.Errorf("the posted article does not decode to the bytes on disk")
	}
	if crc32.ChecksumIEEE(article.data) != article.partCRC {
		return 0, fmt.Errorf("the posted article fails its own part CRC")
	}
	damaged := append([]byte(nil), article.data...)
	fixture.CorruptArticle(damaged)
	if crc32.ChecksumIEEE(damaged) == article.partCRC {
		return 0, fmt.Errorf("the damaged article still matches its part CRC")
	}
	lines := encodeYEncLines(damaged, article.lineLength)
	copyBody := append(append([]string{article.beginLine, article.partLine}, lines...), article.endLine)

	headers, err := server.head(originalID)
	if err != nil {
		return 0, fmt.Errorf("read the posted article's headers: %w", err)
	}
	copyHeaders, err := corruptCopyHeaders(headers, copyID)
	if err != nil {
		return 0, err
	}
	if err := server.post(copyHeaders, copyBody); err != nil {
		return 0, fmt.Errorf("post the damaged copy: %w", err)
	}

	readBack, err := server.body(copyID)
	if err != nil {
		return 0, fmt.Errorf("read back the damaged copy: %w", err)
	}
	posted, err := parseYEncArticle(readBack)
	if err != nil {
		return 0, fmt.Errorf("parse the damaged copy: %w", err)
	}
	if !bytes.Equal(posted.data, damaged) || posted.partCRC != article.partCRC || crc32.ChecksumIEEE(posted.data) == posted.partCRC {
		return 0, fmt.Errorf("the damaged copy on the server is not the article that was posted")
	}
	return wireBytes(lines) - wireBytes(article.lines), nil
}

// corruptCopyHeaders keeps the headers a reader of the original sees and
// names the copy.
func corruptCopyHeaders(headers []string, copyID string) ([]string, error) {
	keep := map[string]bool{"from": true, "newsgroups": true, "subject": true, "date": true}
	var out []string
	found := map[string]bool{}
	for _, header := range headers {
		name, _, ok := strings.Cut(header, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		if keep[key] && !found[key] {
			found[key] = true
			out = append(out, header)
		}
	}
	for _, key := range []string{"from", "newsgroups", "subject"} {
		if !found[key] {
			return nil, fmt.Errorf("the posted article has no %s header", key)
		}
	}
	return append(out, "Message-ID: <"+copyID+">"), nil
}

// yencArticle is one single-part-of-many yEnc article as the server returns
// it: its control lines verbatim, its data lines, and what they decode to.
type yencArticle struct {
	beginLine, partLine, endLine string
	lineLength                      int
	begin, end                      int64
	partCRC                         uint32
	lines                           []string
	data                            []byte
}

func parseYEncArticle(body []byte) (yencArticle, error) {
	var article yencArticle
	all := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	stage := 0
	for _, raw := range all {
		line := strings.TrimRight(raw, "\r")
		switch {
		case stage == 0 && strings.HasPrefix(line, "=ybegin "):
			article.beginLine = line
			value, err := yencField(line, "line", true)
			if err != nil {
				return yencArticle{}, err
			}
			article.lineLength = int(value)
			stage = 1
		case stage == 1 && strings.HasPrefix(line, "=ypart "):
			article.partLine = line
			begin, err := yencField(line, "begin", true)
			if err != nil {
				return yencArticle{}, err
			}
			end, err := yencField(line, "end", true)
			if err != nil {
				return yencArticle{}, err
			}
			article.begin, article.end = begin, end
			stage = 2
		case stage == 2 && strings.HasPrefix(line, "=yend"):
			article.endLine = line
			crc, err := yencField(line, "pcrc32", false)
			if err != nil {
				return yencArticle{}, err
			}
			article.partCRC = uint32(crc)
			stage = 3
		case stage == 2:
			article.lines = append(article.lines, line)
			article.data = appendYEncDecoded(article.data, line)
		case stage == 0 && line == "":
		default:
			return yencArticle{}, fmt.Errorf("unexpected line in a multi-part yEnc article: %.40q", line)
		}
	}
	if stage != 3 {
		return yencArticle{}, fmt.Errorf("the article is not a complete multi-part yEnc article")
	}
	if article.lineLength < 2 || article.begin < 1 || article.end < article.begin {
		return yencArticle{}, fmt.Errorf("the article declares an impossible line length or part range")
	}
	if int64(len(article.data)) != article.end-article.begin+1 {
		return yencArticle{}, fmt.Errorf("the article decodes to %d bytes, but its part covers %d", len(article.data), article.end-article.begin+1)
	}
	return article, nil
}

// yencField reads one key=value field of a yEnc control line, as decimal or,
// for a CRC, hexadecimal.
func yencField(line, key string, decimal bool) (int64, error) {
	for _, field := range strings.Fields(line) {
		name, value, ok := strings.Cut(field, "=")
		if !ok || name != key {
			continue
		}
		if decimal {
			return strconv.ParseInt(value, 10, 64)
		}
		parsed, err := strconv.ParseUint(value, 16, 32)
		return int64(parsed), err
	}
	return 0, fmt.Errorf("yEnc line %.40q has no %s field", line, key)
}

func appendYEncDecoded(out []byte, line string) []byte {
	for index := 0; index < len(line); index++ {
		character := line[index]
		if character == '=' && index+1 < len(line) {
			index++
			out = append(out, line[index]-64-42)
			continue
		}
		out = append(out, character-42)
	}
	return out
}

// encodeYEncLines encodes data as yEnc data lines of about lineLength
// characters. NUL, LF, CR and '=' are always escaped, tab and space where they
// would start or end a line, and '.' where it would start one, which is the
// escaping the yEnc specification asks of an encoder.
func encodeYEncLines(data []byte, lineLength int) []string {
	var lines []string
	line := make([]byte, 0, lineLength+2)
	for index, value := range data {
		encoded := value + 42
		atStart := len(line) == 0
		atEnd := index == len(data)-1 || len(line)+1 >= lineLength
		escape := encoded == 0 || encoded == '\n' || encoded == '\r' || encoded == '='
		escape = escape || ((encoded == '\t' || encoded == ' ') && (atStart || atEnd))
		escape = escape || (encoded == '.' && atStart)
		if escape {
			line = append(line, '=', encoded+64)
		} else {
			line = append(line, encoded)
		}
		if len(line) >= lineLength {
			lines = append(lines, string(line))
			line = line[:0]
		}
	}
	if len(line) > 0 {
		lines = append(lines, string(line))
	}
	return lines
}

func wireBytes(lines []string) int64 {
	var total int64
	for _, line := range lines {
		total += int64(len(line)) + 2
	}
	return total
}

func readFileRange(path string, offset int64, length int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents := make([]byte, length)
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, int64(length)), contents); err != nil {
		return nil, fmt.Errorf("read %d bytes at %d of %s: %w", length, offset, path, err)
	}
	return contents, nil
}
