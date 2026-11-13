package nntp

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestYEncLinesRoundTripEveryByteAndKeepTheLineRules(t *testing.T) {
	var data []byte
	for repeat := 0; repeat < 4; repeat++ {
		for value := 0; value < 256; value++ {
			data = append(data, byte(value))
		}
	}
	lines := encodeYEncLines(data, 128)
	var decoded []byte
	for _, line := range lines {
		if len(line) > 129 {
			t.Fatalf("line of %d characters, want at most 129", len(line))
		}
		if strings.ContainsAny(line, "\x00\r\n") {
			t.Fatalf("line carries a NUL, CR or LF: %q", line)
		}
		if line[0] == '.' || line[0] == ' ' || line[0] == '\t' {
			t.Fatalf("line starts with an unescaped %q", line[0])
		}
		if last := line[len(line)-1]; last == ' ' || last == '\t' {
			t.Fatalf("line ends with an unescaped %q", last)
		}
		decoded = appendYEncDecoded(decoded, line)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("the encoded lines do not decode to the data")
	}
}

// fakeArticleServer stores articles as lines, as the server keeps them.
type fakeArticleServer struct {
	headers map[string][]string
	bodies  map[string][]string
	posted  []string
}

func (s *fakeArticleServer) head(messageID string) ([]string, error) {
	headers, ok := s.headers[messageID]
	if !ok {
		return nil, fmt.Errorf("430 no article %s", messageID)
	}
	return headers, nil
}

func (s *fakeArticleServer) body(messageID string) ([]byte, error) {
	lines, ok := s.bodies[messageID]
	if !ok {
		return nil, fmt.Errorf("430 no article %s", messageID)
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n"), nil
}

func (s *fakeArticleServer) post(headers, body []string) error {
	var id string
	for _, header := range headers {
		if value, ok := strings.CutPrefix(header, "Message-ID: "); ok {
			id = strings.Trim(value, "<>")
		}
	}
	if id == "" {
		return fmt.Errorf("441 no Message-ID")
	}
	if _, exists := s.bodies[id]; exists {
		return fmt.Errorf("441 duplicate %s", id)
	}
	s.headers[id] = headers
	s.bodies[id] = body
	s.posted = append(s.posted, id)
	return nil
}

const corruptTestArticleBytes = 1024

// corruptTestSeed writes fixture.part03.rar to disk and puts its two articles
// on a fake server the way Nyuu posts them.
func corruptTestSeed(t *testing.T) (string, []byte, *fakeArticleServer) {
	t.Helper()
	dir := t.TempDir()
	contents := make([]byte, 2*corruptTestArticleBytes)
	for index := range contents {
		contents[index] = byte(index*31 + 7)
	}
	if err := os.MkdirAll(filepath.Join(dir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "archive", "fixture.part03.rar"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	server := &fakeArticleServer{headers: map[string][]string{}, bodies: map[string][]string{}}
	for part, id := range []string{"a1", "a2"} {
		begin := part * corruptTestArticleBytes
		data := contents[begin : begin+corruptTestArticleBytes]
		body := []string{
			fmt.Sprintf("=ybegin part=%d total=2 line=128 size=%d name=fixture.part03.rar", part+1, len(contents)),
			fmt.Sprintf("=ypart begin=%d end=%d", begin+1, begin+corruptTestArticleBytes),
		}
		body = append(body, encodeYEncLines(data, 128)...)
		body = append(body, fmt.Sprintf("=yend size=%d part=%d pcrc32=%08x", len(data), part+1, crc32.ChecksumIEEE(data)))
		server.headers[id] = []string{
			"Path: nntp-bench",
			"From: nntp-bench@example.invalid",
			"Newsgroups: alt.binaries.test",
			"Subject: fixture.part03.rar yEnc",
			"Date: Tue, 14 Nov 2023 22:13:20 +0000",
			"Message-ID: <" + id + ">",
		}
		server.bodies[id] = body
	}
	return dir, contents, server
}

func corruptArticle(path string, part int64) fixture.CorruptionDetail {
	return fixture.CorruptionDetail{Kind: fixture.CorruptArticleFault, Path: path, Offset: part * corruptTestArticleBytes, Length: corruptTestArticleBytes}
}

func TestCorruptArticlesOnServerPostsADamagedCopyAndPointsTheNZBAtIt(t *testing.T) {
	dir, contents, server := corruptTestSeed(t)
	faults := []fixture.CorruptionDetail{
		{Kind: fixture.ByteFlipFault, Path: "archive/fixture.part01.rar", Offset: 7, Length: 128},
		corruptArticle("archive/fixture.part03.rar", 1),
	}
	document, corrupted, err := corruptArticlesOnServer(server, postedDocument(), postedOnlyPlan(t), faults, dir, "run1", "fixture1", corruptTestArticleBytes)
	if err != nil {
		t.Fatal(err)
	}
	copyID := corruptArticleMessageID("run1", "fixture1", 1, 2)
	if corrupted != 1 || len(server.posted) != 1 || server.posted[0] != copyID {
		t.Fatalf("corrupted %d, posted %v", corrupted, server.posted)
	}
	untouched := postedDocument()
	for file := range document.Files {
		for segment, got := range document.Files[file].Segments {
			want := untouched.Files[file].Segments[segment]
			if file == 0 && segment == 1 {
				if got.MessageID != copyID {
					t.Fatalf("the faulted segment names %s, want the copy", got.MessageID)
				}
				continue
			}
			if got != want {
				t.Fatalf("segment %d of file %d changed: %+v", segment, file, got)
			}
		}
	}

	posted, err := server.body(copyID)
	if err != nil {
		t.Fatal(err)
	}
	article, err := parseYEncArticle(posted)
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), contents[corruptTestArticleBytes:]...)
	fixture.CorruptArticle(damaged)
	if !bytes.Equal(article.data, damaged) {
		t.Fatal("the copy does not decode to the shared article damage")
	}
	if article.partCRC != crc32.ChecksumIEEE(contents[corruptTestArticleBytes:]) || crc32.ChecksumIEEE(article.data) == article.partCRC {
		t.Fatal("the copy does not carry the original part CRC, or passes it")
	}
	original, _ := parseYEncArticle([]byte(strings.Join(server.bodies["a2"], "\n")))
	if want := untouched.Files[0].Segments[1].Bytes + wireBytes(article.lines) - wireBytes(original.lines); document.Files[0].Segments[1].Bytes != want {
		t.Fatalf("segment bytes %d, want %d", document.Files[0].Segments[1].Bytes, want)
	}
	headers := strings.Join(server.headers[copyID], "\n")
	for _, header := range []string{"From: nntp-bench@example.invalid", "Newsgroups: alt.binaries.test", "Subject: fixture.part03.rar yEnc", "Message-ID: <" + copyID + ">"} {
		if !strings.Contains(headers, header) {
			t.Fatalf("the copy lacks %q", header)
		}
	}
	if strings.Contains(headers, "Path:") || strings.Contains(headers, "<a2>") {
		t.Fatal("the copy carries the original's server headers")
	}
}

func TestCorruptArticlesOnServerRefusesAnArticleThatIsNotTheBytesOnDisk(t *testing.T) {
	dir, _, server := corruptTestSeed(t)
	server.bodies["a2"] = server.bodies["a1"]
	_, _, err := corruptArticlesOnServer(server, postedDocument(), postedOnlyPlan(t), []fixture.CorruptionDetail{corruptArticle("archive/fixture.part03.rar", 1)}, dir, "run1", "fixture1", corruptTestArticleBytes)
	if err == nil {
		t.Fatal("a posted article covering other bytes was damaged anyway")
	}
	if len(server.posted) != 0 {
		t.Fatalf("posted %v after refusing", server.posted)
	}
}

func TestCorruptArticlesOnServerRefusesAMismatchedArticleSize(t *testing.T) {
	dir, _, server := corruptTestSeed(t)
	_, _, err := corruptArticlesOnServer(server, postedDocument(), postedOnlyPlan(t), []fixture.CorruptionDetail{corruptArticle("archive/fixture.part03.rar", 1)}, dir, "run1", "fixture1", 2*corruptTestArticleBytes)
	if err == nil || len(server.posted) != 0 {
		t.Fatalf("a fault chosen for another article size was posted: %v", err)
	}
}
