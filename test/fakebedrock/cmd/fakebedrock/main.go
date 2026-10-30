// Command fakebedrock serves a local Bedrock endpoint for container checks.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Victoroide/axiom-engine/test/fakebedrock"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	address := flag.String("addr", "127.0.0.1:9090", "HTTP listen address")
	mode := flag.String("mode", "ok", "Response mode")
	flag.Parse()

	backend := fakebedrock.NewHandler(*mode)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", *address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	fmt.Println(listener.Addr().String())
	server := &http.Server{Handler: backend, ReadHeaderTimeout: 5 * time.Second}
	return server.Serve(listener)
}
