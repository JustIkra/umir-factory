package itest

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

const ioWait = 2 * time.Second

// echoServer echoes every byte back and records what it received, per
// accepted connection. It owns its goroutines and stops them on cleanup.
type echoServer struct {
	addr string
	conn chan *recording
}

type recording struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

func (r *recording) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf.Bytes()...)
}

func startEcho(t *testing.T) *echoServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &echoServer{addr: ln.Addr().String(), conn: make(chan *recording, 16)}
	var wg sync.WaitGroup
	var conns sync.Map
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Store(c, struct{}{})
			rec := &recording{done: make(chan struct{})}
			s.conn <- rec
			wg.Go(func() {
				defer close(rec.done)
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						rec.mu.Lock()
						if _, werr := rec.buf.Write(buf[:n]); werr != nil {
							rec.mu.Unlock()
							return
						}
						rec.mu.Unlock()
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			})
		}
	})
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
		conns.Range(func(k, _ any) bool {
			if c, ok := k.(net.Conn); ok {
				closeConn(t, c)
			}
			return true
		})
		wg.Wait()
	})
	return s
}

func (s *echoServer) nextConn(t *testing.T) *recording {
	t.Helper()
	select {
	case r := <-s.conn:
		return r
	case <-time.After(ioWait):
		t.Fatalf("server saw no connection")
		return nil
	}
}

// closeConn closes our end of a connection; a second close is not an error.
func closeConn(t *testing.T, c net.Conn) {
	t.Helper()
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close: %v", err)
	}
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, ioWait)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { closeConn(t, c) })
	return c
}

func write(t *testing.T, c net.Conn, data string) {
	t.Helper()
	if _, err := c.Write([]byte(data)); err != nil {
		t.Fatalf("write %q: %v", data, err)
	}
}

func readExactly(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if err := c.SetReadDeadline(time.Now().Add(ioWait)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return string(buf)
}

// readUntilClosed returns everything read until the peer closes the connection.
func readUntilClosed(t *testing.T, c net.Conn) string {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(ioWait)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	data, err := io.ReadAll(c)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("connection was not closed by the proxy")
		}
	}
	return string(data)
}

// expectSilence asserts that nothing arrives on c within a short window.
func expectSilence(t *testing.T, c net.Conn) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	var ne net.Error
	if n != 0 || !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected silence, got n=%d err=%v", n, err)
	}
}

func waitDone(t *testing.T, r *recording) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(ioWait):
		t.Fatalf("server connection did not close")
	}
}

func TestTCPProxyForwardsBothDirections(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	write(t, c, "hello")
	if got := readExactly(t, c, 5); got != "hello" {
		t.Fatalf("echo = %q", got)
	}
}

func TestTCPProxyCutAfterBytes(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	rec := srv.nextConn(t)
	write(t, c, "ab")
	readExactly(t, c, 2)
	p.CutAfterBytes(5)
	write(t, c, "cdefgh") // 6 bytes: only the first 5 may reach the server
	readUntilClosed(t, c)
	waitDone(t, rec)
	if got := string(rec.bytes()); got != "abcdefg" { // 2 bytes before the call, exactly 5 after it
		t.Fatalf("server received %q, want %q", got, "abcdefg")
	}
}

func TestTCPProxyCutAfterBytesIsOneShot(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	rec := srv.nextConn(t)
	p.CutAfterBytes(3)
	write(t, c, "abc")
	readUntilClosed(t, c)
	waitDone(t, rec)
	c2 := dial(t, p.Addr())
	write(t, c2, "again")
	if got := readExactly(t, c2, 5); got != "again" {
		t.Fatalf("second connection echo = %q", got)
	}
}

func TestTCPProxyCutAfterMatchToServer(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	rec := srv.nextConn(t)
	p.CutAfterMatch(ToServer, []byte("COMMIT"))
	write(t, c, "xxCOMMITyy")
	if got := readUntilClosed(t, c); got != "" {
		t.Fatalf("client received %q after the cut; the reply must be lost", got)
	}
	waitDone(t, rec)
	if got := string(rec.bytes()); got != "xxCOMMIT" {
		t.Fatalf("server received %q, want %q", got, "xxCOMMIT")
	}
}

func TestTCPProxyCutAfterMatchAcrossChunks(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	rec := srv.nextConn(t)
	p.CutAfterMatch(ToServer, []byte("COMMIT"))
	write(t, c, "COM")
	readExactly(t, c, 3)
	write(t, c, "MIT!")
	readUntilClosed(t, c)
	waitDone(t, rec)
	if got := string(rec.bytes()); got != "COMMIT" {
		t.Fatalf("server received %q, want %q", got, "COMMIT")
	}
}

func TestTCPProxyCutAfterMatchToClient(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	srv.nextConn(t)
	p.CutAfterMatch(ToClient, []byte("OK"))
	write(t, c, "aaOKbb")
	if got := readUntilClosed(t, c); got != "aaOK" {
		t.Fatalf("client received %q, want %q", got, "aaOK")
	}
}

func TestTCPProxyPauseAndResume(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	rec := srv.nextConn(t)
	p.Pause()
	write(t, c, "ping")
	expectSilence(t, c)
	if got := rec.bytes(); len(got) != 0 {
		t.Fatalf("server received %q while paused", got)
	}
	p.Resume()
	if got := readExactly(t, c, 4); got != "ping" {
		t.Fatalf("echo after resume = %q", got)
	}
}

func TestTCPProxyDropToClient(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	c := dial(t, p.Addr())
	rec := srv.nextConn(t)
	p.DropToClient(true)
	write(t, c, "lost")
	expectSilence(t, c)
	if got := string(rec.bytes()); got != "lost" {
		t.Fatalf("server received %q, want the request to arrive", got)
	}
	p.DropToClient(false)
	write(t, c, "kept")
	if got := readExactly(t, c, 4); got != "kept" {
		t.Fatalf("echo = %q", got)
	}
}

func TestTCPProxyCloseStopsEverything(t *testing.T) {
	srv := startEcho(t)
	p := NewTCPProxy(t, srv.addr)
	addr := p.Addr()
	c := dial(t, addr)
	srv.nextConn(t)
	p.Pause()
	write(t, c, "stuck")
	p.Close()
	p.Close() // idempotent
	readUntilClosed(t, c)
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		closeConn(t, conn)
		t.Fatalf("proxy still accepts connections after Close")
	}
}
