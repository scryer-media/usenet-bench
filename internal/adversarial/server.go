package adversarial

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ServerStats struct {
	RunID           string          `json:"run_id"`
	ManifestSHA256  string          `json:"manifest_sha256"`
	FaultDeliveries int64           `json:"fault_deliveries"`
	StartedAt       time.Time       `json:"started_at"`
	StoppedAt       time.Time       `json:"stopped_at"`
	StopReason      string          `json:"stop_reason"`
	Commands        map[string]int  `json:"commands"`
	Transcript      []ResponseEvent `json:"transcript"`
	DroppedEvents   int             `json:"dropped_events"`
	Connections     int64           `json:"connections"`
	Requests        int64           `json:"requests"`
	BodyRequests    int64           `json:"body_requests"`
	WireBytes       int64           `json:"wire_bytes"`
}

type Server struct {
	RunID           string
	faultDeliveries atomic.Int64
	startedAt       time.Time
	stoppedAt       time.Time
	stopReason      string
	commands        map[string]int
	transcript      []ResponseEvent
	droppedEvents   int
	Bundle          Bundle
	mu              sync.Mutex
	attempts        map[string]int
	connections     atomic.Int64
	requests        atomic.Int64
	bodyRequests    atomic.Int64
	wireBytes       atomic.Int64
	wireReserved    atomic.Int64
}

func (s *Server) Stats() ServerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	commands := map[string]int{}
	for k, v := range s.commands {
		commands[k] = v
	}
	return ServerStats{RunID: s.RunID, ManifestSHA256: ManifestDigest(s.Bundle.Manifest), FaultDeliveries: s.faultDeliveries.Load(), StartedAt: s.startedAt, StoppedAt: s.stoppedAt, StopReason: s.stopReason, Commands: commands, Transcript: append([]ResponseEvent(nil), s.transcript...), DroppedEvents: s.droppedEvents, Connections: s.connections.Load(), Requests: s.requests.Load(), BodyRequests: s.bodyRequests.Load(), WireBytes: s.wireBytes.Load()}
}

// Serve owns listener and all accepted sockets. Canceling ctx closes each
// socket and joins every worker. The responder never dials another service.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.mu.Lock()
	s.startedAt = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stoppedAt = time.Now()
		s.stopReason = "listener_stopped"
		if ctx.Err() != nil {
			s.stopReason = ctx.Err().Error()
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	slots := make(chan struct{}, 64)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if s.connections.Add(1) > 2048 {
			_ = conn.Close()
			return fmt.Errorf("responder connection budget exceeded")
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer conn.Close()
			stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stopConn()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) write(c net.Conn, data []byte) error {
	for {
		reserved := s.wireReserved.Load()
		if int64(len(data)) > (512<<20)-reserved {
			return fmt.Errorf("responder byte budget exceeded")
		}
		if s.wireReserved.CompareAndSwap(reserved, reserved+int64(len(data))) {
			break
		}
	}
	n, err := c.Write(data)
	s.wireBytes.Add(int64(n))
	if n != len(data) && err == nil {
		return io.ErrShortWrite
	}
	return err
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	// The responder lifetime, not an unrelated 30-second socket timer, owns
	// termination. Slow/missing-terminator fixtures must not acquire an EOF
	// mutation before the client's declared 60-second budget expires.
	deadline := time.Now().Add(5 * time.Minute)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = c.SetDeadline(deadline)
	if s.write(c, []byte("200 usenet-bench adversarial responder\r\n")) != nil {
		return
	}
	r := bufio.NewReaderSize(c, 4096)
	for i := 0; i < 512; i++ {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return
		}
		if s.requests.Add(1) > 8192 {
			return
		}
		line = bytes.TrimRight(line, "\r\n")
		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			return
		}
		cmd := strings.ToUpper(fields[0])
		s.mu.Lock()
		if s.commands == nil {
			s.commands = map[string]int{}
		}
		if len(cmd) <= 16 {
			s.commands[cmd]++
		} else {
			s.commands["OVERLONG_COMMAND"]++
		}
		s.mu.Unlock()
		arg := ""
		if len(fields) > 1 {
			arg = fields[len(fields)-1]
		}
		var response string
		switch cmd {
		case "CAPABILITIES":
			response = "101 capabilities\r\nVERSION 2\r\nREADER\r\nPIPELINING\r\nAUTHINFO USER\r\n.\r\n"
			if s.Bundle.Manifest.Fault.Kind == "lying-capabilities" {
				response = "101 capabilities\r\nVERSION 2\r\nREADER\r\nPIPELINING\r\nSTARTTLS\r\nCOMPRESS DEFLATE\r\n.\r\n"
			}
		case "MODE":
			response = "200 reader mode\r\n"
		case "DATE":
			response = "111 20260101000000\r\n"
		case "AUTHINFO":
			if len(fields) > 1 && strings.EqualFold(fields[1], "USER") {
				response = "381 password required\r\n"
			} else {
				response = "281 authentication accepted\r\n"
			}
			if s.Bundle.Manifest.Fault.Kind == "auth-loop" {
				response = "480 authentication required\r\n"
			}
		case "GROUP":
			response = "211 1 1 1 alt.binaries.test\r\n"
		case "QUIT":
			_ = s.write(c, []byte("205 closing connection\r\n"))
			return
		case "STAT", "BODY", "ARTICLE", "HEAD":
			id := strings.Trim(arg, "<>")
			var article *Article
			for _, a := range s.Bundle.Manifest.Articles {
				if a.ID == id {
					copy := a
					article = &copy
					break
				}
			}
			if article == nil {
				response = "430 no such article\r\n"
				break
			}
			if cmd == "STAT" {
				response = "223 0 <" + id + ">\r\n"
				break
			}
			if cmd == "HEAD" {
				response = "221 0 <" + id + "> headers\r\nMessage-ID: <" + id + ">\r\nNewsgroups: alt.binaries.test\r\n.\r\n"
				break
			}
			s.bodyRequests.Add(1)
			if err := s.article(ctx, c, cmd, *article); err != nil {
				return
			}
			continue
		default:
			response = "500 unsupported command\r\n"
		}
		if s.write(c, []byte(response)) != nil {
			return
		}
		if (cmd == "CAPABILITIES" && s.Bundle.Manifest.Fault.Kind == "lying-capabilities") || (cmd == "AUTHINFO" && s.Bundle.Manifest.Fault.Kind == "auth-loop") {
			s.faultDeliveries.Add(1)
		}
	}
}

func wireBody(data []byte) []byte {
	var b bytes.Buffer
	start := true
	for _, v := range data {
		if start && v == '.' {
			b.WriteByte('.')
		}
		b.WriteByte(v)
		start = v == '\n'
	}
	if !bytes.HasSuffix(data, []byte("\r\n")) {
		b.WriteString("\r\n")
	}
	b.WriteString(".\r\n")
	return b.Bytes()
}

func (s *Server) article(ctx context.Context, c net.Conn, cmd string, a Article) (resultErr error) {
	s.mu.Lock()
	if s.attempts == nil {
		s.attempts = map[string]int{}
	}
	s.attempts[a.ID]++
	attempt := s.attempts[a.ID]
	s.mu.Unlock()
	observed := &responseCapture{Conn: c, digest: sha256.New()}
	c = observed
	started := time.Now()
	f := s.Bundle.Manifest.Fault
	kind := f.Kind
	articleFault := kind != "" && kind != "lying-capabilities" && kind != "slow-body" && kind != "missing-terminator"
	if (kind == "first-missing" || kind == "first-corrupt") && attempt != 1 {
		articleFault = false
	}
	if f.ChunkBytes > 0 && kind == "" {
		articleFault = true
	}
	defer func() {
		if s.Bundle.Manifest.Case.Family == "nntp" && articleFault && (resultErr == nil || resultErr == io.EOF) {
			s.faultDeliveries.Add(1)
		}
		e := ResponseEvent{Command: cmd, ArticleID: a.ID, Attempt: attempt, StartedAt: started, FinishedAt: time.Now(), WireBytes: observed.bytes, SHA256: hex.EncodeToString(observed.digest.Sum(nil)), Completed: resultErr == nil}
		if resultErr != nil {
			e.Error = resultErr.Error()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.transcript) < 8192 {
			s.transcript = append(s.transcript, e)
		} else {
			s.droppedEvents++
		}
	}()
	if kind == "always-missing" || kind == "first-missing" && attempt == 1 {
		return s.write(c, []byte("430 no such article\r\n"))
	}
	if kind == "temporary-failure" {
		if err := s.write(c, []byte("400 temporarily unavailable\r\n")); err != nil {
			return err
		}
		return io.EOF
	}
	if kind == "400-without-close" {
		return s.write(c, []byte("400 deliberately nonconforming keepalive\r\n"))
	}
	if kind == "auth-loop" {
		return s.write(c, []byte("480 authentication required\r\n"))
	}
	if kind == "disconnect" {
		return io.EOF
	}
	code := "222"
	if cmd == "ARTICLE" {
		code = "220"
	}
	status := code + " 0 <" + a.ID + "> follows\r\n"
	switch kind {
	case "short-status":
		status = "2\r\n"
	case "nonnumeric-status":
		status = "XYZ follows\r\n"
	case "overlong-status":
		status = code + " " + strings.Repeat("A", 1<<20) + "\r\n"
	case "wrong-status":
		status = "211 1 1 1 unexpected\r\n"
	case "wrong-id":
		status = code + " 0 <foreign@adversarial.invalid> follows\r\n"
	case "status-html":
		status = code + " <svg onload=alert('adversarial-canary')>\r\n"
	case "status-terminal":
		status = code + " \x1b]52;c;Y2FuYXJ5\x07\r\n"
	}
	body := wireBody(s.Bundle.Data[a.Body])
	if kind == "first-corrupt" && attempt == 1 {
		body = append([]byte{}, body...)
		start := bytes.Index(body, []byte("\r\n")) + 2
		if start < len(body) {
			body[start] ^= 1
		}
	}
	if kind == "early-terminator" {
		body = []byte(".\r\n")
	}
	if kind == "missing-terminator" {
		body = body[:len(body)-3]
	}
	if kind == "double-terminator" {
		body = append(body, []byte(".\r\n")...)
	}
	if kind == "extra-response" {
		body = append(body, []byte("222 0 <unsolicited@adversarial.invalid> follows\r\n.\r\n")...)
	}
	if kind == "oversized-body" {
		headerEnd := bytes.Index(body, []byte("\r\n")) + 2
		body = append(append([]byte{}, body[:headerEnd]...), bytes.Repeat([]byte("AAAAAAAAAAAAAAAA\r\n"), 1<<19)...)
		body = append(body, []byte(".\r\n")...)
	}
	if cmd == "ARTICLE" {
		body = append([]byte("Message-ID: <"+a.ID+">\r\nNewsgroups: alt.binaries.test\r\n\r\n"), body...)
	}
	wire := append([]byte(status), body...)
	if kind == "lf-only" {
		wire = bytes.ReplaceAll(wire, []byte("\r\n"), []byte("\n"))
	}
	if kind == "truncated" {
		if err := s.write(c, wire[:len(wire)/2]); err != nil {
			return err
		}
		return io.EOF
	}
	chunk := f.ChunkBytes
	if chunk <= 0 {
		chunk = len(wire)
	}
	delayDelivered := false
	for len(wire) > 0 {
		n := min(chunk, len(wire))
		if err := s.write(c, wire[:n]); err != nil {
			return err
		}
		wire = wire[n:]
		if f.DelayMilliseconds > 0 {
			timer := time.NewTimer(time.Duration(f.DelayMilliseconds) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				if kind == "slow-body" && !delayDelivered {
					s.faultDeliveries.Add(1)
					delayDelivered = true
				}
			}
		}
	}
	if kind == "missing-terminator" {
		s.faultDeliveries.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

type ResponseEvent struct {
	Command    string    `json:"command"`
	ArticleID  string    `json:"article_id"`
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	WireBytes  int64     `json:"wire_bytes"`
	SHA256     string    `json:"wire_sha256"`
	Completed  bool      `json:"completed"`
	Error      string    `json:"error,omitempty"`
}
type responseCapture struct {
	net.Conn
	digest hash.Hash
	bytes  int64
}

func (c *responseCapture) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	_, _ = c.digest.Write(data[:n])
	c.bytes += int64(n)
	return n, err
}

func LoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address must be a loopback IP")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("invalid port")
	}
	return nil
}
