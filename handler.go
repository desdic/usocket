//go:build darwin || linux

package usocket

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sync"
)

const BUFSIZE = 512

type Request struct {
	vars    map[string]string
	rawLine string
}

func (r Request) Get(key string) string {
	return r.vars[key]
}

func (r Request) GetRawLine() string {
	return r.rawLine
}

type HandlerFunc func(w *Connection, r *Request)

type ServeMux struct {
	reg            map[*regexp.Regexp]HandlerFunc
	defaultHandler HandlerFunc
	mu             sync.RWMutex
	bufsize        int
}

func NewRouter() ServeMux {
	return ServeMux{
		reg:     make(map[*regexp.Regexp]HandlerFunc),
		bufsize: BUFSIZE,
	}
}

func (mux *ServeMux) HandleDefaultFunc(handler HandlerFunc) {
	mux.mu.Lock()
	defer mux.mu.Unlock()

	mux.defaultHandler = handler
}

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

func (mux *ServeMux) handleConnection(ctx context.Context, conn net.Conn, wg *sync.WaitGroup) {
	defer wg.Done()

	err := ctx.Err()
	if err != nil {
		return
	}

	cmdLine, err := readLine(conn, mux.bufsize)
	if err != nil {
		return
	}

	var (
		matches   []string
		rematch   *regexp.Regexp
		funcMatch HandlerFunc
		r         Request
	)

	r.rawLine = cmdLine

	mux.mu.RLock()
	for re, f := range mux.reg {
		matches = re.FindStringSubmatch(cmdLine)
		if len(matches) > 0 {
			rematch = re
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
	mux.mu.RUnlock()

	w := Connection{conn: conn}

	if rematch != nil {
		funcMatch(&w, &r)
		return
	}

	if mux.defaultHandler != nil {
		mux.defaultHandler(&w, &r)
	}
}

func (mux *ServeMux) ListenAndServe(ctx context.Context, socketpath string) error {
	var (
		wg       sync.WaitGroup
		err      error
		listener net.Listener
	)

	// Setup default handler to just close the connection
	if mux.defaultHandler != nil {
		mux.defaultHandler = func(c *Connection, _ *Request) {
			_ = c.Close()
		}
	}

	if mux.bufsize == 0 {
		mux.bufsize = BUFSIZE
	}

	listener, err = net.Listen("unix", socketpath)
	if err != nil {
		return fmt.Errorf("failed to open socket: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		if err := ctx.Err(); err != nil {
			break
		}

		conn, err := listener.Accept()
		if err != nil {
			break
		}

		wg.Add(1)

		go mux.handleConnection(ctx, conn, &wg)
	}

	return err
}
