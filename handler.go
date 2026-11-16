//go:build darwin || linux

// Package usocket implement a mux for handleing sockets in a similar way as go handles http
package usocket

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sync"
	"syscall"
	"time"
)

const (
	// BUFSIZE default buffer size when using NewRouter
	BUFSIZE = 512

	// DefaultReadTimeout is the read timeout used when none is set
	DefaultReadTimeout = 10 * time.Second

	// DefaultWriteTimeout is the write timeout used when none is set
	DefaultWriteTimeout = 10 * time.Second

	minAcceptBackoff = 5 * time.Millisecond
	maxAcceptBackoff = time.Second
)

type Request struct {
	vars    map[string]string
	rawLine string
}

// Get returns a matching capture group from the request
func (r Request) Get(key string) string {
	return r.vars[key]
}

// GetRawLine returns the full line from the request
// usefull in logging in the default handler when no match is found
func (r Request) GetRawLine() string {
	return r.rawLine
}

// HandlerFunc is the definition of a function for the HandlerFunc/HandleDefaultFunc
type HandlerFunc func(w *Connection, r *Request)

type ServeMux struct {
	reg            map[*regexp.Regexp]HandlerFunc
	defaultHandler HandlerFunc
	mu             sync.RWMutex
	bufsize        int
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxConns       int
}

// NewRouter creates a new socket router
func NewRouter() ServeMux {
	return ServeMux{
		reg:     make(map[*regexp.Regexp]HandlerFunc),
		bufsize: BUFSIZE,
	}
}

// SetReadTimeout sets how long each read from a connection may wait for data,
// including reading the request line. Zero uses DefaultReadTimeout and a
// negative value disables the timeout.
func (mux *ServeMux) SetReadTimeout(d time.Duration) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	mux.readTimeout = d
}

// SetWriteTimeout sets how long each write to a connection may block. Zero
// uses DefaultWriteTimeout and a negative value disables the timeout.
func (mux *ServeMux) SetWriteTimeout(d time.Duration) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	mux.writeTimeout = d
}

// SetMaxConnections limits how many connections are handled at once. When the
// limit is reached, new connections wait in the listen backlog until a slot
// frees up. Zero or a negative value means no limit.
func (mux *ServeMux) SetMaxConnections(n int) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	mux.maxConns = n
}

// HandleDefaultFunc sets the default handling of request not matching
// any patterns added by HandleFunc
func (mux *ServeMux) HandleDefaultFunc(handler HandlerFunc) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	mux.defaultHandler = handler
}

// HandleFunc adds a HandleFunc to a specific pattern (regex)
func (mux *ServeMux) HandleFunc(pattern string, handler HandlerFunc) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	mux.mu.Lock()
	defer mux.mu.Unlock()

	if mux.reg == nil {
		mux.reg = make(map[*regexp.Regexp]HandlerFunc)
	}

	mux.reg[re] = handler

	return nil
}

// resolveTimeout maps a configured timeout to the effective one:
// zero means the default, negative means disabled (0).
func resolveTimeout(d, def time.Duration) time.Duration {
	switch {
	case d == 0:
		return def
	case d < 0:
		return 0
	default:
		return d
	}
}

func (mux *ServeMux) handleConnection(ctx context.Context, conn net.Conn, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() { _ = conn.Close() }()

	err := ctx.Err()
	if err != nil {
		return
	}

	mux.mu.RLock()
	w := Connection{
		conn:         conn,
		readTimeout:  resolveTimeout(mux.readTimeout, DefaultReadTimeout),
		writeTimeout: resolveTimeout(mux.writeTimeout, DefaultWriteTimeout),
	}
	bufsize := mux.bufsize
	mux.mu.RUnlock()

	if bufsize <= 0 {
		bufsize = BUFSIZE
	}

	cmdLine, err := w.ReadLine(bufsize)
	if err != nil {
		return
	}

	var (
		matches   []string
		funcMatch HandlerFunc
		r         Request
	)

	r.rawLine = cmdLine

	mux.mu.RLock()
	for re, f := range mux.reg {
		matches = re.FindStringSubmatch(cmdLine)
		if len(matches) > 0 {
			funcMatch = f
			r.vars = make(map[string]string)
			subexpNames := re.SubexpNames()
			for i, name := range subexpNames {
				if i != 0 && name != "" {
					r.vars[name] = matches[i]
				}
			}
			break
		}
	}
	if funcMatch == nil {
		funcMatch = mux.defaultHandler
	}
	mux.mu.RUnlock()

	if funcMatch != nil {
		funcMatch(&w, &r)
	}
}

// isTemporaryAcceptErr reports whether an Accept error is transient, such as
// running out of file descriptors, so the accept loop should back off and
// retry instead of stopping.
func isTemporaryAcceptErr(err error) bool {
	for _, errno := range []syscall.Errno{
		syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS,
		syscall.ENOMEM, syscall.ECONNABORTED, syscall.EINTR,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}

	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// ListenAndServe creates a unix socket at socketpath and serves it until ctx
// is cancelled. See Serve.
func (mux *ServeMux) ListenAndServe(ctx context.Context, socketpath string) error {
	listener, err := net.Listen("unix", socketpath)
	if err != nil {
		return fmt.Errorf("failed to open socket: %w", err)
	}

	return mux.Serve(ctx, listener)
}

// Serve accepts connections on listener and dispatches them to handlers until
// ctx is cancelled or the listener fails. Transient accept errors (such as
// running out of file descriptors) are retried with backoff.
//
// When ctx is cancelled, the listener and all open connections are closed and
// Serve waits for running handlers to return before returning nil. If the
// listener fails with a non-transient error, Serve shuts down the same way and
// returns that error.
func (mux *ServeMux) Serve(ctx context.Context, listener net.Listener) error {
	mux.mu.Lock()
	// Setup default handler to just close the connection
	if mux.defaultHandler == nil {
		mux.defaultHandler = func(c *Connection, _ *Request) {
			_ = c.Close()
		}
	}
	if mux.bufsize == 0 {
		mux.bufsize = BUFSIZE
	}
	maxConns := mux.maxConns
	mux.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		connsMu sync.Mutex
		conns   = make(map[net.Conn]struct{})
		closing bool
		sem     chan struct{}
		retErr  error
		backoff time.Duration
	)

	if maxConns > 0 {
		sem = make(chan struct{}, maxConns)
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		_ = listener.Close()

		connsMu.Lock()
		closing = true
		for c := range conns {
			_ = c.Close()
		}
		connsMu.Unlock()
	}()

	for {
		if sem != nil {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
			}
		}

		if ctx.Err() != nil {
			break
		}

		conn, err := listener.Accept()
		if err != nil {
			if sem != nil {
				<-sem
			}

			if ctx.Err() != nil {
				break
			}

			if isTemporaryAcceptErr(err) {
				if backoff == 0 {
					backoff = minAcceptBackoff
				} else {
					backoff = min(backoff*2, maxAcceptBackoff)
				}

				select {
				case <-time.After(backoff):
				case <-ctx.Done():
				}
				continue
			}

			retErr = fmt.Errorf("failed to accept connection: %w", err)
			break
		}
		backoff = 0

		connsMu.Lock()
		if closing {
			connsMu.Unlock()
			_ = conn.Close()
			if sem != nil {
				<-sem
			}
			break
		}
		conns[conn] = struct{}{}
		connsMu.Unlock()

		wg.Add(1)
		go func() {
			defer func() {
				connsMu.Lock()
				delete(conns, conn)
				connsMu.Unlock()

				if sem != nil {
					<-sem
				}
			}()

			mux.handleConnection(ctx, conn, &wg)
		}()
	}

	cancel()
	<-stopped
	wg.Wait()

	return retErr
}
