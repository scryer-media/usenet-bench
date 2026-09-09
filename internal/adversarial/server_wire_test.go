package adversarial

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A recording connection preserves Write boundaries. It never interprets the
// bytes with the responder's framing helpers, so assertions below have an
// independent literal wire oracle rather than comparing a function to itself.
type wireRecorder struct {
	bytes.Buffer
	chunks     []int
	fail       bool
	afterWrite func()
}

func (w *wireRecorder) Write(p []byte) (int, error) {
	if w.fail {
		return 0, io.ErrClosedPipe
	}
	w.chunks = append(w.chunks, len(p))
	if w.afterWrite != nil {
		w.afterWrite()
	}
	return w.Buffer.Write(p)
}
func (*wireRecorder) Close() error                     { return nil }
func (*wireRecorder) LocalAddr() net.Addr              { return nil }
func (*wireRecorder) RemoteAddr() net.Addr             { return nil }
func (*wireRecorder) SetDeadline(time.Time) error      { return nil }
func (*wireRecorder) SetReadDeadline(time.Time) error  { return nil }
func (*wireRecorder) SetWriteDeadline(time.Time) error { return nil }

func wireTestServer(kind string, chunk int) *Server {
	b := Bundle{Data: map[string][]byte{"body": []byte("=ybegin size=3 name=payload.bin\r\n.AB\r\n=yend size=3\r\n")}}
	b.Manifest.Case = Case{Family: "nntp"}
	b.Manifest.Fault = Fault{Kind: kind, ChunkBytes: chunk}
	return &Server{Bundle: b}
}

const expectedBody = "=ybegin size=3 name=payload.bin\r\n..AB\r\n=yend size=3\r\n.\r\n"
const expectedStatus = "222 0 <fixture@adversarial.invalid> follows\r\n"

var wireArticle = Article{ID: "fixture@adversarial.invalid", Body: "body"}

func TestExactFaultWireAndTranscript(t *testing.T) {
	normal := expectedStatus + expectedBody
	cases := []struct {
		kind, want string
		eof        bool
	}{
		{"", normal, false},
		{"short-status", "2\r\n" + expectedBody, false},
		{"nonnumeric-status", "XYZ follows\r\n" + expectedBody, false},
		{"overlong-status", "222 " + strings.Repeat("A", 1<<20) + "\r\n" + expectedBody, false},
		{"wrong-status", "211 1 1 1 unexpected\r\n" + expectedBody, false},
		{"wrong-id", "222 0 <foreign@adversarial.invalid> follows\r\n" + expectedBody, false},
		{"status-html", "222 <svg onload=alert('adversarial-canary')>\r\n" + expectedBody, false},
		{"status-terminal", "222 \x1b]52;c;Y2FuYXJ5\x07\r\n" + expectedBody, false},
		{"early-terminator", expectedStatus + ".\r\n", false},
		{"double-terminator", normal + ".\r\n", false},
		{"extra-response", normal + "222 0 <unsolicited@adversarial.invalid> follows\r\n.\r\n", false},
		{"lf-only", strings.ReplaceAll(normal, "\r\n", "\n"), false},
		{"truncated", normal[:len(normal)/2], true},
		{"always-missing", "430 no such article\r\n", false},
		{"first-missing", "430 no such article\r\n", false},
		{"first-corrupt", expectedStatus + strings.Replace(expectedBody, "..AB", "/.AB", 1), false},
		{"temporary-failure", "400 temporarily unavailable\r\n", true},
		{"400-without-close", "400 deliberately nonconforming keepalive\r\n", false},
		{"auth-loop", "480 authentication required\r\n", false},
		{"disconnect", "", true},
		{"lying-capabilities", normal, false},
		{"oversized-body", expectedStatus + "=ybegin size=3 name=payload.bin\r\n" + strings.Repeat("AAAAAAAAAAAAAAAA\r\n", 1<<19) + ".\r\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			s := wireTestServer(tc.kind, 0)
			w := &wireRecorder{}
			err := s.article(context.Background(), w, "BODY", wireArticle)
			if tc.eof && !errors.Is(err, io.EOF) || !tc.eof && err != nil {
				t.Fatal(err)
			}
			if w.String() != tc.want {
				t.Fatalf("wire mismatch: got %d bytes, want %d", w.Len(), len(tc.want))
			}
			stats := s.Stats()
			if len(stats.Transcript) != 1 {
				t.Fatal(stats)
			}
			event := stats.Transcript[0]
			digest := sha256.Sum256([]byte(tc.want))
			if event.SHA256 != hex.EncodeToString(digest[:]) || event.WireBytes != int64(len(tc.want)) || event.Attempt != 1 || event.Completed == tc.eof {
				t.Fatal(event)
			}
			wantFault := int64(1)
			if tc.kind == "" || tc.kind == "lying-capabilities" {
				wantFault = 0
			}
			if stats.FaultDeliveries != wantFault {
				t.Fatalf("delivered %d, want %d", stats.FaultDeliveries, wantFault)
			}
		})
	}
}

func TestExactFragmentedWire(t *testing.T) {
	for _, chunk := range []int{1, 15, 16, 17, 31, 32, 33, 63, 64, 65} {
		s := wireTestServer("", chunk)
		w := &wireRecorder{}
		if err := s.article(context.Background(), w, "ARTICLE", wireArticle); err != nil {
			t.Fatal(err)
		}
		want := "220 0 <fixture@adversarial.invalid> follows\r\nMessage-ID: <fixture@adversarial.invalid>\r\nNewsgroups: alt.binaries.test\r\n\r\n" + expectedBody
		if w.String() != want {
			t.Fatalf("chunk %d changed bytes", chunk)
		}
		for i, n := range w.chunks {
			if n > chunk || i < len(w.chunks)-1 && n != chunk {
				t.Fatalf("chunk %d: %v", chunk, w.chunks)
			}
		}
		if len(w.chunks) < 2 || s.Stats().FaultDeliveries != 1 {
			t.Fatal("segmentation was not delivered")
		}
	}
}

func TestFaultEvidenceDoesNotCountFailedWritesOrSuccessfulRetries(t *testing.T) {
	for _, kind := range []string{"first-missing", "first-corrupt"} {
		s := wireTestServer(kind, 0)
		if err := s.article(context.Background(), &wireRecorder{}, "BODY", wireArticle); err != nil {
			t.Fatal(err)
		}
		w := &wireRecorder{}
		if err := s.article(context.Background(), w, "BODY", wireArticle); err != nil {
			t.Fatal(err)
		}
		if w.String() != expectedStatus+expectedBody || s.Stats().FaultDeliveries != 1 {
			t.Fatal("retry counted as another fault")
		}
	}
	s := wireTestServer("wrong-id", 0)
	if err := s.article(context.Background(), &wireRecorder{fail: true}, "BODY", wireArticle); err == nil {
		t.Fatal("write should fail")
	}
	if s.Stats().FaultDeliveries != 0 {
		t.Fatal("failed write counted as delivered fault")
	}
}

func TestMissingTerminatorStaysOpenUntilCancellation(t *testing.T) {
	s := wireTestServer("missing-terminator", 0)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.article(ctx, server, "BODY", wireArticle) }()
	want := expectedStatus + strings.TrimSuffix(expectedBody, ".\r\n")
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil || string(got) != want {
		t.Fatal("wrong incomplete body", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	var one [1]byte
	if _, err := client.Read(one[:]); err == nil {
		t.Fatal("unexpected terminator")
	} else if n, ok := err.(net.Error); !ok || !n.Timeout() {
		t.Fatalf("premature closure: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Stats().FaultDeliveries != 1 {
		t.Fatal("missing-terminator delivery not recorded")
	}
}

func TestSlowFaultCountsDeliveredDelayDespiteCancellation(t *testing.T) {
	s := wireTestServer("slow-body", 1)
	s.Bundle.Manifest.Fault.DelayMilliseconds = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &wireRecorder{}
	w.afterWrite = func() {
		if len(w.chunks) == 2 {
			cancel()
		}
	}
	err := s.article(ctx, w, "BODY", wireArticle)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Stats().FaultDeliveries != 1 || w.Len() == 0 {
		t.Fatal("completed delay was not recorded")
	}
}
