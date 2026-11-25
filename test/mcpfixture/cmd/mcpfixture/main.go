// Command mcpfixture runs a local HTTPS issuer, Redis and synthetic tokens
// for container checks of the remote MCP endpoint.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Victoroide/axiom-engine/test/mcpfixture"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "", "Directory for the certificate, key and CA files")
	listen := flag.String("listen", "0.0.0.0", "Interface for the issuer and Redis")
	hosts := flag.String("issuer-hosts", "host.docker.internal", "Comma-separated names the issuer certificate covers")
	mcpHost := flag.String("mcp-host", "mcp.example.test", "Public host name of the MCP endpoint")
	mcpAudience := flag.String("mcp-audience", "", "Audience of MCP tokens, the endpoint's public URL")
	restAudience := flag.String("rest-audience", "https://api.example.test/axiom", "Audience of API tokens")
	flag.Parse()
	if *dir == "" || *mcpAudience == "" {
		return fmt.Errorf("usage: mcpfixture -dir DIR -mcp-audience URL [-listen ADDR] [-issuer-hosts LIST]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	f, err := mcpfixture.New(ctx, mcpfixture.Options{
		Dir: *dir, IssuerHosts: strings.Split(*hosts, ","), MCPHost: *mcpHost, ListenHost: *listen,
	})
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	tokens := map[string]string{}
	for name, spec := range map[string][3]string{
		"full":   {*mcpAudience, "graph:read plan:generate", "smoke-caller"},
		"graph":  {*mcpAudience, "graph:read", "smoke-caller"},
		"api":    {*restAudience, "graph:read plan:generate", "smoke-caller"},
		"second": {*mcpAudience, "graph:read plan:generate", "other-caller"},
	} {
		if tokens[name], err = f.Token(spec[0], "smoke-tenant", spec[2], spec[1]); err != nil {
			return err
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"issuer": f.Issuer(), "keys_url": f.KeysURL(), "issuer_port": f.IssuerPort, "redis_port": f.RedisPort, "tokens": tokens,
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
