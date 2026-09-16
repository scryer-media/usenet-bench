package nntp

import (
	"math/rand"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/scryer-media/usenet-bench/internal/uucodec"
)

// The concatenated article bodies must be byte-for-byte the single-part
// stream. That is the whole contract of the split: a client that fetches
// every part in order and joins them has a stream the format's own reference
// decoder accepts, with no harness-specific reassembly rule.
func TestSplitUUArticlesConcatenatesToTheWholeStream(t *testing.T) {
	source := rand.New(rand.NewSource(20260908))
	for _, size := range []int{45, 45*10 + 3, 45 * 1000} {
		payload := make([]byte, size)
		source.Read(payload)
		articles := splitUUArticles("payload-01.mkv", payload, 100)
		var joined strings.Builder
		for index, article := range articles {
			if article.partNumber != index+1 {
				t.Fatalf("article %d claims part %d", index, article.partNumber)
			}
			for _, line := range article.lines {
				joined.WriteString(line)
				joined.WriteString("\n")
			}
		}
		if joined.String() != string(uucodec.Encode("payload-01.mkv", payload)) {
			t.Fatalf("joined articles are not the single-part stream at size %d", size)
		}
	}
}

func TestSplitUUArticlesKeepsEveryLineButTheLastFull(t *testing.T) {
	payload := make([]byte, 45*250+11)
	articles := splitUUArticles("payload-01.mkv", payload, 100)
	if len(articles) != 3 {
		t.Fatalf("split into %d articles, want 3", len(articles))
	}
	// The begin header rides on the first article and the terminator on the
	// last, so the middle article is pure payload.
	if articles[0].lines[0] != "begin 644 payload-01.mkv" {
		t.Fatalf("first article does not open the stream: %q", articles[0].lines[0])
	}
	last := articles[len(articles)-1].lines
	if last[len(last)-2] != "`" || last[len(last)-1] != "end" {
		t.Fatalf("last article does not close the stream: %q", last[len(last)-2:])
	}
	if articles[0].rawBytes != 45*100 || articles[1].rawBytes != 45*100 {
		t.Fatalf("full articles carry %d and %d bytes, want %d each", articles[0].rawBytes, articles[1].rawBytes, 45*100)
	}
	if articles[2].rawBytes != 45*50+11 {
		t.Fatalf("final article carries %d bytes, want %d", articles[2].rawBytes, 45*50+11)
	}
	if got, want := articles[1].rawOffset, int64(45*100); got != want {
		t.Fatalf("second article starts at %d, want %d", got, want)
	}
}

func TestUUWireBytesCountsCRLFTerminators(t *testing.T) {
	// The NZB reports the article as it goes on the wire, which is the same
	// convention Nyuu follows for the yEnc lanes, so a client sizing buffers
	// from the NZB sees the two alike.
	if got := uuWireBytes([]string{"ab", "cde"}); got != 9 {
		t.Fatalf("uuWireBytes = %d, want 9", got)
	}
}

func TestUUSubjectIsTheClassicMultiPartShape(t *testing.T) {
	got := uuSubject("payload-01.mkv", 2, 7, 234881024)
	if got != `"payload-01.mkv" (2/7) 234881024` {
		t.Fatalf("uuSubject = %q", got)
	}
	name, err := nzbFileName(got)
	if err != nil || name != "payload-01.mkv" {
		t.Fatalf("the order assertion cannot read the subject back: %q, %v", name, err)
	}
}

func TestUUMessageIDsMatchTheirTemplate(t *testing.T) {
	// The seed image fingerprints the template, and the runner materialises
	// concrete ids from the same rule; if the two drift, a restored image
	// serves articles no NZB refers to.
	template := UUMessageIDTemplate("seed-run-1", "media-uuencode-store-nonsolid-none-incompressible")
	id := uuMessageID("seed-run-1", "media-uuencode-store-nonsolid-none-incompressible", 1, 42)
	expected := strings.NewReplacer("{0filenum}", "001", "{0part}", "00042").Replace(template)
	if id != expected {
		t.Fatalf("message id %q does not match its template expansion %q", id, expected)
	}
}

// Posting two articles on one session must not hang: the 340 and the 240 both
// answer one POST, and a session that gives up its response slot between them
// never reads the 240.
func TestPostReadsBothRepliesOfEveryArticle(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	received := make(chan []string, 2)
	go func() {
		defer server.Close()
		text := textproto.NewConn(server)
		for range 2 {
			if line, err := text.ReadLine(); err != nil || line != "POST" {
				t.Errorf("server read %q, %v; want POST", line, err)
				return
			}
			if err := text.PrintfLine("340 send article"); err != nil {
				return
			}
			lines, err := text.ReadDotLines()
			if err != nil {
				t.Errorf("server read article: %v", err)
				return
			}
			received <- lines
			if err := text.PrintfLine("240 article received"); err != nil {
				return
			}
		}
	}()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	session := &nntpSession{conn: client, text: textproto.NewConn(client)}
	for part := 1; part <= 2; part++ {
		if err := session.post([]string{"Subject: part"}, []string{".begins with a dot", "plain"}); err != nil {
			t.Fatalf("post part %d: %v", part, err)
		}
		lines := <-received
		if want := []string{"Subject: part", "", ".begins with a dot", "plain"}; strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Fatalf("part %d arrived as %q, want %q", part, lines, want)
		}
	}
}
