package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

const mcpHTTPChild = "AXIOM_MCP_HTTP_CHILD"

type mcpHTTPFiles struct {
	cert, key string
	pool      *x509.CertPool
}

func writeMCPHTTPFiles(t *testing.T) mcpHTTPFiles {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mcp.example.test"}, DNSNames: []string{"mcp.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := mcpHTTPFiles{cert: filepath.Join(dir, "tls.crt"), key: filepath.Join(dir, "tls.key"), pool: x509.NewCertPool()}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(files.cert, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	files.pool.AppendCertsFromPEM(certPEM)
	return files
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// mcpHTTPSettings is a complete, valid configuration. The issuer is never
// contacted unless a request presents a token.
func mcpHTTPSettings(redisAddr, bindAddr string, files mcpHTTPFiles) map[string]string {
	return map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": "https://issuer.example.test/tenant",
		"AXIOM_AUTH_AUDIENCE": "https://api.example.test/axiom", "AXIOM_AUTH_JWKS_URL": "https://issuer.example.test/keys",
		"AXIOM_TENANT_QUOTA_ENABLED": "true", "REDIS_URL": "redis://" + redisAddr,
		"MCP_PUBLIC_URL": "https://mcp.example.test/mcp", "MCP_BIND_ADDR": bindAddr,
		"MCP_TLS_CERT_FILE": files.cert, "MCP_TLS_KEY_FILE": files.key,
		"AWS_MAX_ATTEMPTS": "3", "AWS_RETRY_MODE": "standard", "BAML_LOG": "off", "GRAPH_FILE": "",
	}
}

// clearedEnv drops every setting these tests control from the inherited environment.
func clearedEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return isHTTPAuthSetting(key) || strings.HasPrefix(key, "MCP_") || strings.HasPrefix(key, "AXIOM_TENANT_") || key == "REDIS_URL" || key == "PORT"
	})
}

func clearRemoteSettings(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if isHTTPAuthSetting(key) || strings.HasPrefix(key, "MCP_") || strings.HasPrefix(key, "AXIOM_TENANT_") || key == "REDIS_URL" {
			t.Setenv(key, "")
		}
	}
}

func TestMCPHTTPRefusesMisconfigurationBeforeBinding(t *testing.T) {
	redis := miniredis.RunT(t)
	files := writeMCPHTTPFiles(t)
	held, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	base := mcpHTTPSettings(redis.Addr(), held.Addr().String(), files)
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{"shared tokens", "AXIOM_AUTH_MODE", "shared", "AXIOM_AUTH_MODE=verified"},
		{"no tenant quota", "AXIOM_TENANT_QUOTA_ENABLED", "false", "AXIOM_TENANT_QUOTA_ENABLED=true"},
		{"no public URL", "MCP_PUBLIC_URL", "", "MCP_PUBLIC_URL is required"},
		{"plain HTTP public URL", "MCP_PUBLIC_URL", "http://mcp.example.test/mcp", "MCP_PUBLIC_URL"},
		{"API audience reused", "AXIOM_AUTH_AUDIENCE", "https://mcp.example.test/mcp", "AXIOM_AUTH_AUDIENCE must differ"},
		{"wildcard bind", "MCP_BIND_ADDR", "0.0.0.0:" + freePort(t), "MCP_BIND_ADDR"},
		{"no certificate", "MCP_TLS_CERT_FILE", "", "plaintext"},
		{"unreadable key", "MCP_TLS_KEY_FILE", "/nonexistent/tls.key", "MCP_TLS_KEY_FILE"},
		{"wildcard origin", "MCP_ALLOWED_ORIGINS", "*", "MCP_ALLOWED_ORIGINS"},
		{"unreachable Redis", "REDIS_URL", "redis://127.0.0.1:" + freePort(t), "redis startup ping failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRemoteSettings(t)
			for key, value := range base {
				t.Setenv(key, value)
			}
			t.Setenv(tc.key, tc.value)
			var out bytes.Buffer
			err := runMCPHTTP(nil, &out)
			// The address is already in use, so any attempt to bind would say so.
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "listen: ") {
				t.Fatalf("error = %v, want %q before binding", err, tc.want)
			}
			if !strings.Contains(out.String(), `"msg":"server.failed"`) || strings.Contains(out.String(), "mcp.listening") {
				t.Fatalf("output = %s", out.String())
			}
		})
	}
	t.Run("a valid configuration reaches the bind", func(t *testing.T) {
		clearRemoteSettings(t)
		for key, value := range base {
			t.Setenv(key, value)
		}
		var out bytes.Buffer
		if err := runMCPHTTP(nil, &out); err == nil || !strings.Contains(err.Error(), "listen") {
			t.Fatalf("error = %v, want a bind failure on the held address", err)
		}
	})
	t.Run("arguments", func(t *testing.T) {
		if err := runMCPHTTP([]string{"--port"}, io.Discard); err == nil || !strings.Contains(err.Error(), "usage: axiom-server mcp-http") {
			t.Fatalf("error = %v", err)
		}
	})
}

func startChild(t *testing.T, mode string, env []string, stdin io.Reader) (*exec.Cmd, <-chan map[string]any) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // Reexecute this test binary to exercise the entry point.
	cmd.Env = slices.Concat(env, []string{mcpHTTPChild + "=" + mode, "NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1"})
	cmd.Stdin = stdin
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	records := make(chan map[string]any, 256)
	go func() {
		defer close(records)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var record map[string]any
			if json.Unmarshal(scanner.Bytes(), &record) == nil {
				select {
				case records <- record:
				default:
				}
			}
		}
	}()
	return cmd, records
}

func waitForRecord(t *testing.T, records <-chan map[string]any, message string) map[string]any {
	t.Helper()
	timeout := time.After(60 * time.Second)
	var seen []string
	for {
		select {
		case record, ok := <-records:
			if !ok {
				t.Fatalf("the process ended without logging %q after %v", message, seen)
			}
			if record["msg"] == message {
				return record
			}
			seen = append(seen, fmt.Sprint(record["msg"], ": ", record["error"]))
		case <-timeout:
			t.Fatalf("timed out waiting for %q after %v", message, seen)
		}
	}
}

func TestMCPHTTPProcessServesMetadataAndStops(t *testing.T) {
	if os.Getenv(mcpHTTPChild) == "http" {
		if err := run([]string{"mcp-http"}, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	redis := miniredis.RunT(t)
	files := writeMCPHTTPFiles(t)
	port := freePort(t)
	restPort := freePort(t)
	settings := mcpHTTPSettings(redis.Addr(), "127.0.0.1:"+port, files)
	settings["PORT"], settings["SHUTDOWN_TIMEOUT"], settings["MCP_PUBLIC_URL"] = restPort, "15s", "https://mcp.example.test:"+port+"/mcp"
	env := clearedEnv()
	for key, value := range settings {
		env = append(env, key+"="+value)
	}
	cmd, records := startChild(t, "http", env, nil)
	listening := waitForRecord(t, records, "mcp.listening")
	if listening["address"] != "127.0.0.1:"+port || listening["resource"] != settings["MCP_PUBLIC_URL"] {
		t.Fatalf("listening record = %v", listening)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: files.pool},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+port)
		},
	}}
	get := func(method, path string) (int, http.Header, string) {
		req, err := http.NewRequestWithContext(t.Context(), method, "https://mcp.example.test:"+port+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(body)
	}
	status, _, body := get(http.MethodGet, "/.well-known/oauth-protected-resource/mcp")
	if status != http.StatusOK || !strings.Contains(body, `"resource":"`+settings["MCP_PUBLIC_URL"]+`"`) || !strings.Contains(body, `"authorization_servers":["https://issuer.example.test/tenant"]`) {
		t.Fatalf("metadata status=%d body=%s", status, body)
	}
	status, header, _ := get(http.MethodPost, "/mcp")
	if status != http.StatusUnauthorized || !strings.HasPrefix(header.Get("WWW-Authenticate"), `Bearer resource_metadata="https://mcp.example.test:`+port) {
		t.Fatalf("challenge status=%d header=%q", status, header.Get("WWW-Authenticate"))
	}
	// The command serves only the MCP listener, not the HTTP API.
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", "127.0.0.1:"+restPort); err == nil {
		_ = conn.Close()
		t.Fatal("mcp-http opened the HTTP API port")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitForRecord(t, records, "server.shutdown_started")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit after SIGTERM: %v", err)
	}
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", "127.0.0.1:"+port); err == nil {
		_ = conn.Close()
		t.Fatal("the endpoint kept listening after the command stopped")
	}
}

func TestOrdinaryCommandsOpenNoRemoteMCPPort(t *testing.T) {
	if mode := os.Getenv(mcpHTTPChild); mode != "" {
		switch mode {
		case "default":
			if err := run(nil, os.Stdout); err != nil {
				os.Exit(1)
			}
		case "stdio":
			if err := run([]string{"mcp"}, os.Stdout); err != nil {
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	redis := miniredis.RunT(t)
	files := writeMCPHTTPFiles(t)
	for _, mode := range []string{"default", "stdio"} {
		t.Run(mode, func(t *testing.T) {
			mcpPort, restPort := freePort(t), freePort(t)
			settings := mcpHTTPSettings(redis.Addr(), "127.0.0.1:"+mcpPort, files)
			// Every remote setting is valid, yet only the new command may use them.
			env := clearedEnv()
			for key, value := range settings {
				env = append(env, key+"="+value)
			}
			env = append(env, "PORT="+restPort, "AXIOM_AUTH_MODE=", "AXIOM_AUTH_DISABLED=true")
			env = slices.DeleteFunc(env, func(entry string) bool {
				return strings.HasPrefix(entry, "AXIOM_AUTH_ISSUER=") || strings.HasPrefix(entry, "AXIOM_AUTH_AUDIENCE=") || strings.HasPrefix(entry, "AXIOM_AUTH_JWKS_URL=") ||
					strings.HasPrefix(entry, "AXIOM_TENANT_") || strings.HasPrefix(entry, "REDIS_URL=")
			})
			var stdin io.Reader
			pipeReader, pipeWriter := io.Pipe()
			if mode == "stdio" {
				stdin = pipeReader
			}
			cmd, records := startChild(t, mode, env, stdin)
			if mode == "default" {
				waitForRecord(t, records, "server.listening")
				waitForHealth(t, restPort)
			} else {
				time.Sleep(500 * time.Millisecond)
			}
			if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", "127.0.0.1:"+mcpPort); err == nil {
				_ = conn.Close()
				t.Fatalf("the %s command opened the remote MCP port", mode)
			}
			if mode == "stdio" {
				_ = pipeWriter.Close()
			} else if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			go func() {
				for range records {
				}
			}()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("exit: %v", err)
			}
		})
	}
}

func waitForHealth(t *testing.T, port string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+port+"/livez", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the HTTP API did not start")
}
