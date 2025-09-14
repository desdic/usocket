//go:build darwin || linux

package usocket

import (
	"fmt"
	"net"
	"strings"
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

type Connection struct {
	conn net.Conn
}

func (c *Connection) Write(data []byte) (int, error) {
	return c.conn.Write(data)
}

func (c *Connection) ReadLine(bufsize int) (string, error) {
	return readLine(c.conn, bufsize)
}

func (c *Connection) Read(bufsize int) (int, []byte, error) {
	return read(c.conn, bufsize)
}

func (c *Connection) Close() error {
	return c.conn.Close()
}
