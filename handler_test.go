//go:build darwin || linux

package usocket

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type mockConn struct {
	bytes.Buffer
	Output []byte
}

var _ net.Conn = (*mockConn)(nil)

func NewMockConn(data string) *mockConn {
	m := &mockConn{}
	m.WriteString(data)
	return m
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	m.Output = b
	return len(m.Output), nil
}

func (m *mockConn) Close() error {
	return nil
}

func (m *mockConn) LocalAddr() net.Addr {
	return nil
}

func (m *mockConn) RemoteAddr() net.Addr {
	return nil
}

func (m *mockConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func TestServeMux_handleConnection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "defaultHandler",
			input:    "notfound",
			expected: "not found",
		},
		{
			name:     "test",
			input:    "testing",
			expected: "testing handler",
		},
		{
			name:     "groups",
			input:    "add cabcdefgh w0123456789",
			expected: "group cabcdefgh w0123456789",
		},
	}

	var wg sync.WaitGroup

	mux := NewRouter()
	if err := mux.HandleFunc(
		"^testing",
		func(c *Connection, r *Request) {
			_, _ = c.Write([]byte("testing handler"))
			_ = c.Close()
		},
	); err != nil {
		t.Fatalf("failed to setup test for testing handler: %v", err)
	}

	if err := mux.HandleFunc(
		"^add (?P<username>c[a-z0-9]{8}) (?P<group>w[a-z0-9]{10})$",
		func(c *Connection, r *Request) {
			ret := []byte("group")
			space := []byte(" ")
			username := []byte(r.Get("username"))
			group := []byte(r.Get("group"))

			ret = append(ret, space...)
			ret = append(ret, username...)
			ret = append(ret, space...)
			ret = append(ret, group...)

			_, _ = c.Write(ret)
			_ = c.Close()
		},
	); err != nil {
		t.Fatalf("failed to setup test for group handler: %v", err)
	}
	mux.HandleDefaultFunc(func(c *Connection, r *Request) {
		_, _ = c.Write([]byte("not found"))
		_ = c.Close()
	})
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wg.Add(1)

			mockConnection := NewMockConn(tt.input)

			mux.handleConnection(ctx, mockConnection, &wg)

			out := string(mockConnection.Output)

			if out != tt.expected {
				t.Fatalf("expected %s, got %s", out, tt.expected)
			}
		})
	}
	wg.Wait()
}
