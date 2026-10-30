package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunRejectsInvalidRetrySettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AWS_MAX_ATTEMPTS", ""}, {"AWS_MAX_ATTEMPTS", "abc"}, {"AWS_MAX_ATTEMPTS", "0"},
		{"AWS_MAX_ATTEMPTS", "-1"}, {"AWS_MAX_ATTEMPTS", "1.5"}, {"AWS_MAX_ATTEMPTS", " 3"},
		{"AWS_MAX_ATTEMPTS", "4294967296"},
		{"AWS_RETRY_MODE", ""}, {"AWS_RETRY_MODE", "legacy"}, {"AWS_RETRY_MODE", "bogus"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			t.Setenv("AWS_MAX_ATTEMPTS", "3")
			t.Setenv("AWS_RETRY_MODE", "standard")
			t.Setenv(tc.key, tc.value)
			if err := run([]string{"unexpected"}); err == nil || !strings.Contains(err.Error(), tc.key+" must be") {
				t.Fatalf("expected retry configuration error before startup, got %v", err)
			}
		})
	}
}

func TestRunAcceptsRetrySettings(t *testing.T) {
	for _, tc := range []struct{ attempts, mode string }{
		{"unset", "unset"}, {"1", "standard"}, {"5", "adaptive"}, {"4294967295", "standard"},
	} {
		t.Run(tc.attempts+"/"+tc.mode, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			for key, value := range map[string]string{"AWS_MAX_ATTEMPTS": tc.attempts, "AWS_RETRY_MODE": tc.mode} {
				t.Setenv(key, value)
				if value == "unset" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := run([]string{"unexpected"}); err == nil || err.Error() != "usage: axiom-server [healthcheck]" {
				t.Fatalf("expected argument validation after configuration, got %v", err)
			}
		})
	}
}

func TestSecondSignalStopsServer(t *testing.T) {
	if os.Getenv("AXIOM_SIGNAL_TEST") == "1" {
		if err := run(nil); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSecondSignalStopsServer$") //nolint:gosec // Reexecute this test binary to exercise process signals.
	cmd.Env = append(os.Environ(), "AXIOM_SIGNAL_TEST=1", "AXIOM_AUTH_DISABLED=true", "AXIOM_INTERNAL_TOKEN=", "PORT="+port, "SHUTDOWN_TIMEOUT=25s", "SHUTDOWN_DRAIN_DELAY=0s", "GRAPH_FILE=", "AWS_MAX_ATTEMPTS=3", "AWS_RETRY_MODE=standard")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		_ = cmd.Process.Kill()
		if !waited {
			if err := <-done; err == nil {
				t.Error("child exited without a signal")
			}
		}
	}()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	defer client.CloseIdleConnections()
	probe := func() error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for probe() != nil {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "POST /api/v1/adaptive-plan HTTP/1.1\r\nHost: localhost\r\nContent-Length: 1\r\nExpect: 100-continue\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "HTTP/1.1 100 Continue\r\n" {
		t.Fatalf("continue response=%q error=%v", line, err)
	}
	// Keep an incomplete body open while the listener closes.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for probe() == nil {
		if time.Now().After(deadline) {
			t.Fatal("listener stayed open after termination signal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		waited = true
		if err == nil {
			t.Fatal("second signal did not interrupt shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second signal did not stop the process")
	}
}
