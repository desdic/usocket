//go:build darwin || linux

package usocket

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// socketPath returns a short socket path; unix socket paths are limited to
// ~104 bytes and t.TempDir() can exceed that on macOS.
func socketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "us")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return filepath.Join(dir, "s")
}

// startServer runs mux on a fresh socket and returns its path and a function
// that cancels the server and returns Serve's result.
func startServer(t *testing.T, mux *ServeMux) (string, func() error) {
	t.Helper()

	path := socketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mux.Serve(ctx, listener) }()

	var once sync.Once
	var result error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				result = errors.New("Serve did not return after cancel")
			}
		})
		return result
	}
	t.Cleanup(func() { _ = stop() })

	return path, stop
}

func dial(t *testing.T, path string) net.Conn {
	t.Helper()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func echoMux() *ServeMux {
	mux := NewRouter()
	_ = mux.HandleFunc("^ping$", func(c *Connection, _ *Request) {
		_, _ = c.Write([]byte("pong\n"))
	})
	return &mux
}

func TestIdleClientIsDisconnected(t *testing.T) {
	t.Parallel()

	mux := echoMux()
	mux.SetReadTimeout(100 * time.Millisecond)
	path, _ := startServer(t, mux)

	conn := dial(t, path)

	// Send nothing; the server should hang up once the read timeout passes.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected the server to close the idle connection")
	}

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("server did not close the idle connection within 3s")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle connection closed after %v, expected ~100ms", elapsed)
	}
}

func TestWriteTimeout(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	// net.Pipe is unbuffered, so a write blocks until the client reads,
	// which it never does.
	c := Connection{conn: server, writeTimeout: 50 * time.Millisecond}

	start := time.Now()
	_, err := c.Write([]byte("hello"))

	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected a timeout error, got %v", err)
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("write returned after %v, expected ~50ms", elapsed)
	}
}

// flakyListener returns `fails` transient EMFILE errors from Accept before
// handing out the connections queued in conns.
type flakyListener struct {
	mu     sync.Mutex
	fails  int
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.fails > 0 {
		l.fails--
		l.mu.Unlock()
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	}
	l.mu.Unlock()

	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *flakyListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *flakyListener) Addr() net.Addr { return nil }

func TestServeSurvivesTooManyOpenFiles(t *testing.T) {
	t.Parallel()

	listener := &flakyListener{
		fails:  3,
		conns:  make(chan net.Conn, 1),
		closed: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- echoMux().Serve(ctx, listener) }()

	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	listener.conns <- server

	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("ping\n")); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("expected a reply after transient accept errors, got %v", err)
	}
	if line != "pong\n" {
		t.Fatalf("expected pong, got %q", line)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on shutdown, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

type failingListener struct{}

func (failingListener) Accept() (net.Conn, error) { return nil, errors.New("boom") }
func (failingListener) Close() error              { return nil }
func (failingListener) Addr() net.Addr            { return nil }

func TestServeReturnsPermanentAcceptError(t *testing.T) {
	t.Parallel()

	err := echoMux().Serve(context.Background(), failingListener{})
	if err == nil {
		t.Fatal("expected Serve to return the accept error")
	}
}

func TestShutdownClosesActiveConnections(t *testing.T) {
	t.Parallel()

	handlerDone := make(chan struct{})

	mux := NewRouter()
	mux.SetReadTimeout(-1) // no timeout: only shutdown can unblock the handler
	_ = mux.HandleFunc("^hold$", func(c *Connection, _ *Request) {
		defer close(handlerDone)
		_, _ = c.ReadLine(BUFSIZE)
	})
	path, stop := startServer(t, &mux)

	conn := dial(t, path)
	if _, err := conn.Write([]byte("hold\n")); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Give the handler time to start blocking in ReadLine.
	time.Sleep(100 * time.Millisecond)

	if err := stop(); err != nil {
		t.Fatalf("expected nil on shutdown, got %v", err)
	}

	select {
	case <-handlerDone:
	default:
		t.Fatal("Serve returned before the handler finished")
	}
}

func TestMaxConnections(t *testing.T) {
	t.Parallel()

	mux := NewRouter()
	mux.SetMaxConnections(1)
	_ = mux.HandleFunc("^hold$", func(c *Connection, _ *Request) {
		_, _ = c.ReadLine(BUFSIZE)
	})
	_ = mux.HandleFunc("^ping$", func(c *Connection, _ *Request) {
		_, _ = c.Write([]byte("pong\n"))
	})
	path, _ := startServer(t, &mux)

	first := dial(t, path)
	if _, err := first.Write([]byte("hold\n")); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	second := dial(t, path)
	if _, err := second.Write([]byte("ping\n")); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	reader := bufio.NewReader(second)

	// The only slot is taken, so the second connection must not be served yet.
	_ = second.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := reader.ReadString('\n'); err == nil {
		t.Fatal("second connection was served while the limit was reached")
	}

	// Free the slot.
	_ = first.Close()

	_ = second.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("second connection not served after slot freed: %v", err)
	}
	if line != "pong\n" {
		t.Fatalf("expected pong, got %q", line)
	}
}

func TestIsTemporaryAcceptErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want bool
	}{
		{&net.OpError{Op: "accept", Err: os.NewSyscallError("accept", syscall.EMFILE)}, true},
		{&net.OpError{Op: "accept", Err: os.NewSyscallError("accept", syscall.ENFILE)}, true},
		{fmt.Errorf("wrapped: %w", syscall.ECONNABORTED), true},
		{net.ErrClosed, false},
		{errors.New("boom"), false},
	}

	for _, tt := range tests {
		if got := isTemporaryAcceptErr(tt.err); got != tt.want {
			t.Errorf("isTemporaryAcceptErr(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
