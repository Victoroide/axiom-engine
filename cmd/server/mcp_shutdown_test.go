package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/test/fakebedrock"
)

type processRecords struct {
	mu       sync.Mutex
	data     bytes.Buffer
	pending  string
	protocol bool
	invalid  error
	messages chan map[string]any
	shutdown chan struct{}
	once     sync.Once
}

func (r *processRecords) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.data.Write(p)
	r.pending += string(p)
	for {
		line, rest, ok := strings.Cut(r.pending, "\n")
		if !ok {
			break
		}
		r.pending = rest
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			r.invalid = err
			continue
		}
		if r.protocol && message["jsonrpc"] != "2.0" {
			r.invalid = fmt.Errorf("non JSON-RPC stdout: %s", line)
		}
		if r.messages != nil {
			r.messages <- message
		}
		if message["msg"] == "server.shutdown_started" {
			r.once.Do(func() { close(r.shutdown) })
		}
	}
	return len(p), nil
}

func (r *processRecords) text() string { r.mu.Lock(); defer r.mu.Unlock(); return r.data.String() }

func TestMCPShutdownProcess(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "axiom-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".") //nolint:gosec // Build the server in a fresh test directory.
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	for _, tc := range []struct {
		name                    string
		signal                  syscall.Signal
		active, release, second bool
		budget                  time.Duration
	}{
		{name: "idle SIGTERM", signal: syscall.SIGTERM, budget: time.Second},
		{name: "idle SIGINT", signal: syscall.SIGINT, budget: time.Second},
		{name: "deadline cancels active call", signal: syscall.SIGTERM, active: true, budget: 150 * time.Millisecond},
		{name: "drain rejects new calls and finishes accepted call", signal: syscall.SIGTERM, active: true, release: true, budget: 2 * time.Second},
		{name: "second signal terminates drain", signal: syscall.SIGTERM, active: true, second: true, budget: 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arrived, released := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(released) }) }
			handler := fakebedrock.NewHandler("ok")
			var arriveOnce sync.Once
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				arriveOnce.Do(func() { close(arrived) })
				select {
				case <-released:
					handler.ServeHTTP(w, r)
				case <-r.Context().Done():
				}
			}))
			defer backend.Close()
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "mcp") //nolint:gosec // Execute the server built by this test.
			cmd.Env = append(os.Environ(), "BAML_LOG=off", "AXIOM_INTERNAL_TOKEN=", "AXIOM_INTERNAL_TOKEN_PREVIOUS=", "AXIOM_AUTH_DISABLED=invalid", "GRAPH_FILE=",
				"AWS_ACCESS_KEY_ID=dummy", "AWS_SECRET_ACCESS_KEY=dummy", "AWS_SESSION_TOKEN=", "AWS_PROFILE=", "AWS_REGION=us-east-1", "AWS_DEFAULT_REGION=us-east-1", "AWS_EC2_METADATA_DISABLED=true", "AWS_RETRY_MODE=standard", "AWS_MAX_ATTEMPTS=1", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=false",
				"AWS_ENDPOINT_URL="+backend.URL, "AWS_ENDPOINT_URL_BEDROCK_RUNTIME="+backend.URL, "SHUTDOWN_TIMEOUT="+tc.budget.String(), "REQUEST_TIMEOUT=5s", "LLM_TIMEOUT=3s", "LLM_QUEUE_TIMEOUT=50ms")
			stdout := &processRecords{protocol: true, messages: make(chan map[string]any, 32)}
			stderr := &processRecords{shutdown: make(chan struct{})}
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			var waitErr error
			go func() { waitErr = cmd.Wait(); close(exited) }()
			defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); <-exited }()
			send := func(message string) {
				t.Helper()
				if _, err := io.WriteString(stdin, message+"\n"); err != nil {
					t.Fatal(err)
				}
			}
			receive := func(id float64) map[string]any {
				t.Helper()
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				for {
					select {
					case message := <-stdout.messages:
						if message["id"] == id {
							return message
						}
					case <-exited:
						for {
							select {
							case message := <-stdout.messages:
								if message["id"] == id {
									return message
								}
							default:
								t.Fatalf("process ended before response %v: %v; %s", id, waitErr, stderr.text())
								return nil
							}
						}
					case <-timer.C:
						t.Fatalf("response %v timed out; %s", id, stderr.text())
						return nil
					}
				}
			}
			send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`)
			if result := receive(1); result["error"] != nil {
				t.Fatal(result)
			}
			send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
			if tc.active {
				send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}}`)
				select {
				case <-arrived:
				case <-time.After(2 * time.Second):
					t.Fatal("provider did not receive request")
				}
			}
			start := time.Now()
			if err := cmd.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			if tc.second || tc.release {
				select {
				case <-stderr.shutdown:
				case <-exited:
					t.Fatalf("process ended before draining: %v; %s", waitErr, stderr.text())
				case <-time.After(time.Second):
					t.Fatal("missing shutdown record")
				}
			}
			if tc.release {
				send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_topics","arguments":{}}}`)
				response := receive(3)
				result, ok := response["result"].(map[string]any)
				if !ok || result["isError"] != true {
					t.Fatalf("new call accepted during drain: %v", response)
				}
				failure, ok := result["structuredContent"].(map[string]any)
				if !ok || failure["error"] != "service_unavailable" {
					t.Fatalf("rejection=%v", response)
				}
				release()
				response = receive(2)
				result, ok = response["result"].(map[string]any)
				if !ok || result["isError"] == true {
					t.Fatalf("accepted call did not drain: %v", response)
				}
			}
			limit := time.Second
			if tc.active && !tc.release && !tc.second {
				limit = tc.budget + 600*time.Millisecond
			}
			if tc.second {
				start = time.Now()
				if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
					t.Fatal(err)
				}
				limit = 600 * time.Millisecond
			}
			select {
			case <-exited:
			case <-time.After(limit):
				t.Fatalf("process exceeded shutdown limit %s; %s", limit, stderr.text())
			}
			elapsed := time.Since(start)
			if tc.second {
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if waitErr == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
					t.Fatalf("second signal exit=%v status=%v", waitErr, status)
				}
			} else {
				exhausted := tc.active && !tc.release
				if exhausted {
					var exitErr *exec.ExitError
					if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
						t.Fatalf("timeout exit=%v; %s", waitErr, stderr.text())
					}
					logs := stderr.text()
					if !strings.Contains(logs, `"msg":"server.failed"`) || !strings.Contains(logs, `"error":"shutdown: context deadline exceeded"`) || strings.Contains(logs, `"msg":"server.shutdown_completed"`) {
						t.Fatalf("timeout logs=%s", logs)
					}
					if elapsed < tc.budget-50*time.Millisecond {
						t.Fatalf("active work canceled before drain budget: %s", elapsed)
					}
				} else {
					if waitErr != nil {
						t.Fatalf("shutdown exit=%v; %s", waitErr, stderr.text())
					}
					if !strings.Contains(stderr.text(), `"msg":"server.shutdown_completed"`) || strings.Contains(stderr.text(), `"msg":"server.failed"`) {
						t.Fatalf("shutdown logs=%s", stderr.text())
					}
				}
				if !strings.Contains(stderr.text(), `"msg":"server.shutdown_started"`) {
					t.Fatalf("missing shutdown record: %s", stderr.text())
				}
			}
			if stdout.invalid != nil || stderr.invalid != nil {
				t.Fatalf("invalid process output: stdout=%v stderr=%v", stdout.invalid, stderr.invalid)
			}
			t.Logf("signal-to-exit=%s exit=%v", elapsed, waitErr)
		})
	}
}
