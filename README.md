# Usocket

![Test](https://github.com/desdic/usocket/actions/workflows/ci.yaml/badge.svg)

Handle UNIX socket in a similar way go handles http

example

```go
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"github.com/desdic/usocket"
)

const MYSOCK = "./my.sock"

func main() {
	router := usocket.NewRouter()

	if err := router.HandleFunc("^add (?P<username>c[a-z0-9]{8}) (?P<group>w[a-z0-9]{10})$", func(c *usocket.Connection, r *usocket.Request) {
		fmt.Println("hello world", r.Get("username"))

		c.Write([]byte("hello world\n"))
		c.Close()
	}); err != nil {
		slog.Error("error creating function", "err", err)
	}

	ctx := context.Background()

	_ = os.Remove(MYSOCK)
	log.Fatal(router.ListenAndServe(ctx, MYSOCK))
}
```

## Timeouts and limits

Each read and write on a connection has a timeout (`DefaultReadTimeout` and
`DefaultWriteTimeout`, 10 seconds each), so idle or stalled clients cannot hold
a connection open forever.

```go
router.SetReadTimeout(5 * time.Second)  // 0 = default, negative = no timeout
router.SetWriteTimeout(5 * time.Second) // 0 = default, negative = no timeout
router.SetMaxConnections(100)           // 0 = unlimited
```

The connection is closed when the handler returns. Cancelling the context passed
to `ListenAndServe` closes the socket and all open connections, then waits for
running handlers to return.

Use `Serve` instead of `ListenAndServe` to bring your own listener, for example
to set permissions on the socket before accepting connections:

```go
listener, _ := net.Listen("unix", MYSOCK)
_ = os.Chmod(MYSOCK, 0o660)
log.Fatal(router.Serve(ctx, listener))
```
