//go:build darwin || linux

package usocket

import (
	"fmt"
	"net"
	"strings"
	"time"
)

func read(conn net.Conn, bufsize int) (int, []byte, error) {
	buf := make([]byte, bufsize)
	numBytes, err := conn.Read(buf)
	if err != nil {
		return 0, buf, fmt.Errorf("failed to read line: %w", err)
	}
	buf = buf[:numBytes]

	return numBytes, buf, err
}

func readLine(conn net.Conn, bufsize int) (string, error) {
	_, buf, err := read(conn, bufsize)
	return strings.TrimSuffix(string(buf), "\n"), err
}

// Connection is the socket used for reading/writing
type Connection struct {
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
}

func (c *Connection) setReadDeadline() error {
	if c.readTimeout <= 0 {
		return nil
	}
	return c.conn.SetReadDeadline(time.Now().Add(c.readTimeout))
}

// Write to a connection. If a write timeout is configured, the write fails
// when it cannot complete within that duration.
func (c *Connection) Write(data []byte) (int, error) {
	if c.writeTimeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			return 0, err
		}
	}
	return c.conn.Write(data)
}

// ReadLine reads until a newline or specific buffer size. If a read timeout
// is configured, the read fails when no data arrives within that duration.
func (c *Connection) ReadLine(bufsize int) (string, error) {
	if err := c.setReadDeadline(); err != nil {
		return "", err
	}
	return readLine(c.conn, bufsize)
}

// Read reads a specific buffer size. If a read timeout is configured, the
// read fails when no data arrives within that duration.
func (c *Connection) Read(bufsize int) (int, []byte, error) {
	if err := c.setReadDeadline(); err != nil {
		return 0, nil, err
	}
	return read(c.conn, bufsize)
}

// Close closes the connection
func (c *Connection) Close() error {
	return c.conn.Close()
}
