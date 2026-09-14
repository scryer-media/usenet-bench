package adversarial

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func testConnection(t *testing.T, s *Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); s.handle(ctx, server) }()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("responder worker leaked")
		}
	})
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(client)
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "200 ") {
		t.Fatalf("bad greeting: %q %v", line, err)
	}
	return client, r
}

func TestResponderPreservesBodiesAcrossChunkSizes(t *testing.T) {
	for _, chunk := range []int{0, 1, 15, 16, 17, 31, 32, 33, 63, 64, 65} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			b, _ := Generate("control-yenc")
			b.Manifest.Fault = Fault{ChunkBytes: chunk}
			s := &Server{Bundle: b}
			c, r := testConnection(t, s)
			_, _ = fmt.Fprintf(c, "BODY <%s>\r\n", b.Manifest.Articles[0].ID)
			status, err := r.ReadString('\n')
			if err != nil || !strings.HasPrefix(status, "222 ") {
				t.Fatal(status, err)
			}
			want := wireBody(b.Data[b.Manifest.Articles[0].Body])
			got := make([]byte, len(want))
			if _, err := io.ReadFull(r, got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, got) {
				t.Fatal("wire bytes changed by segmentation")
			}
		})
	}
}

func TestResponderFirstFailureSurvivesReconnect(t *testing.T) {
	b, _ := Generate("nntp-first-missing")
	s := &Server{Bundle: b}
	for i, want := range []string{"430 ", "222 "} {
		c, r := testConnection(t, s)
		_, _ = fmt.Fprintf(c, "BODY <%s>\r\n", b.Manifest.Articles[0].ID)
		status, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(status, want) {
			t.Fatalf("attempt %d: %q %v", i, status, err)
		}
		_ = c.Close()
	}
}

func TestResponderArticleAndPipelining(t *testing.T) {
	b, _ := Generate("control-yenc")
	s := &Server{Bundle: b}
	c, r := testConnection(t, s)
	id := b.Manifest.Articles[0].ID
	done := make(chan error, 1)
	go func() { _, err := fmt.Fprintf(c, "STAT <%s>\r\nARTICLE <%s>\r\n", id, id); done <- err }()
	line, _ := r.ReadString('\n')
	if !strings.HasPrefix(line, "223 ") {
		t.Fatal(line)
	}
	line, _ = r.ReadString('\n')
	if !strings.HasPrefix(line, "220 ") {
		t.Fatal(line)
	}
	line, _ = r.ReadString('\n')
	if line != "Message-ID: <"+id+">\r\n" {
		t.Fatal(line)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDotStuffing(t *testing.T) {
	if got := string(wireBody([]byte(".\r\n..two\r\nplain\r\n"))); got != "..\r\n...two\r\nplain\r\n.\r\n" {
		t.Fatal(got)
	}
}

func TestAllProtocolFaultsDispatch(t *testing.T) {
	for _, c := range Catalog() {
		if c.Family != "nntp" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			b, _ := Generate(c.ID)
			s := &Server{Bundle: b}
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				_ = s.article(ctx, server, "BODY", b.Manifest.Articles[0])
			}()
			_ = client.SetDeadline(time.Now().Add(50 * time.Millisecond))
			_, _ = io.Copy(io.Discard, client)
			cancel()
			_ = client.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("fault did not stop")
			}
		})
	}
}

func TestServeCancellationJoinsIdleSockets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate("control-yenc")
	s := &Server{Bundle: b}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	c, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = bufio.NewReader(c).ReadString('\n')
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve leaked worker")
	}
}

func TestListenRejectsExternalInterfaces(t *testing.T) {
	for _, s := range []string{"0.0.0.0:119", "[::]:119", "example.com:119", "192.0.2.1:119", "127.0.0.1:65536", "127.0.0.1:-1"} {
		if LoopbackAddress(s) == nil {
			t.Fatal("unsafe bind", s)
		}
	}
	for _, s := range []string{"127.0.0.1:0", "[::1]:119"} {
		if err := LoopbackAddress(s); err != nil {
			t.Fatal(err)
		}
	}
}
