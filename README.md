# Usocket

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
