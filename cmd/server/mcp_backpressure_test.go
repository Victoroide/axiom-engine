//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMCPShutdownWithUnreadStdout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "axiom-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".") //nolint:gosec // Build the server in a fresh test directory.
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp") //nolint:gosec // Execute the server built by this test.
	const budget = 100 * time.Millisecond
	cmd.Env = append(os.Environ(), "BAML_LOG=off", "AXIOM_INTERNAL_TOKEN=", "AXIOM_INTERNAL_TOKEN_PREVIOUS=", "AXIOM_AUTH_DISABLED=invalid", "GRAPH_FILE=", "SHUTDOWN_TIMEOUT="+budget.String(),
		"AWS_ACCESS_KEY_ID=dummy", "AWS_SECRET_ACCESS_KEY=dummy", "AWS_SESSION_TOKEN=", "AWS_PROFILE=", "AWS_REGION=us-east-1", "AWS_DEFAULT_REGION=us-east-1", "AWS_EC2_METADATA_DISABLED=true", "AWS_RETRY_MODE=standard", "AWS_MAX_ATTEMPTS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	file, ok := stdout.(*os.File)
	if !ok {
		t.Fatal("stdout pipe is not a file")
	}
	stdoutFD := int(file.Fd())
	stderr := &processRecords{shutdown: make(chan struct{})}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); <-exited; _ = stdout.Close() }()
	send := func(message string) {
		t.Helper()
		if _, err := io.WriteString(stdin, message+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`)
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var initialized map[string]any
	if err := json.Unmarshal(line, &initialized); err != nil || initialized["id"] != float64(1) {
		t.Fatalf("initialize=%s error=%v", line, err)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	// Do not read stdout again; the responses must fill the client's pipe.
	for id := 2; id < 102; id++ {
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"list_topics","arguments":{}}}`, id))
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	last, queued := 0, 0
	stable := time.Now()
	filled := false
	for !filled {
		select {
		case <-ticker.C:
			queued, err = unix.IoctlGetInt(stdoutFD, pipeBytesRequest)
			if err != nil {
				t.Fatal(err)
			}
			if queued == 0 || queued != last {
				stable = time.Now()
				last = queued
			} else if time.Since(stable) >= 100*time.Millisecond {
				filled = true
			}
		case <-deadline.C:
			t.Fatalf("stdout pipe did not fill: queued=%d", queued)
		case <-exited:
			t.Fatalf("process exited before shutdown: %v; %s", waitErr, stderr.text())
		}
	}
	t.Logf("unread stdout bytes before signal: %d", queued)
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(budget + time.Second):
		t.Fatalf("still alive after %s with SHUTDOWN_TIMEOUT=%s; %s", time.Since(start), budget, stderr.text())
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("timeout exit=%v; %s", waitErr, stderr.text())
	}
	logs := stderr.text()
	if !strings.Contains(logs, `"msg":"server.failed"`) || !strings.Contains(logs, `"error":"shutdown: context deadline exceeded"`) || strings.Contains(logs, `"msg":"server.shutdown_completed"`) {
		t.Fatalf("timeout logs=%s", logs)
	}
	if !bytes.Contains([]byte(logs), []byte(`"msg":"server.shutdown_started"`)) {
		t.Fatalf("missing shutdown log: %s", stderr.text())
	}
	t.Logf("signal-to-exit=%s exit=%v logs=%s", time.Since(start), waitErr, stderr.text())
}
