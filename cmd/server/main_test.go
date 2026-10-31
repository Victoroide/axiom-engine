package main

import (
	"bufio"
	"encoding/json"
	"errors"
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
	testSecondSignalStopsServer(t, false)
}

func TestBackToBackSignalsStopServer(t *testing.T) {
	testSecondSignalStopsServer(t, true)
}

func testSecondSignalStopsServer(t *testing.T, backToBack bool) {
	t.Helper()
	if os.Getenv("AXIOM_SIGNAL_TEST") == "1" {
		if err := run(nil); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	const shutdownTimeout = 25 * time.Second
	// Finish before the incomplete body's read timeout or the shutdown budget.
	const waitTimeout = shutdownTimeout / 4
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
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // Reexecute this test binary to exercise process signals.
	cmd.Env = append(os.Environ(), "AXIOM_SIGNAL_TEST=1", "AXIOM_AUTH_DISABLED=true", "AXIOM_INTERNAL_TOKEN=", "PORT="+port, "SHUTDOWN_TIMEOUT="+shutdownTimeout.String(), "SHUTDOWN_DRAIN_DELAY=0s", "GRAPH_FILE=", "LOG_LEVEL=info", "AWS_MAX_ATTEMPTS=3", "AWS_RETRY_MODE=standard")
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	type shutdownRecord struct {
		Message string `json:"msg"`
		Level   string `json:"level"`
		Signal  string `json:"signal"`
		Timeout any    `json:"timeout"`
	}
	shutdownStarted := make(chan shutdownRecord, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var record shutdownRecord
			if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Message == "server.shutdown_started" {
				select {
				case shutdownStarted <- record:
				default:
				}
			}
		}
		close(shutdownStarted)
		done <- errors.Join(cmd.Wait(), scanner.Err())
	}()
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
	deadline := time.Now().Add(waitTimeout)
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
	if err := conn.SetDeadline(time.Now().Add(waitTimeout)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "HTTP/1.1 100 Continue\r\n" {
		t.Fatalf("continue response=%q error=%v", line, err)
	}
	// Keep an incomplete body open while the listener closes.
	deadline = time.Now().Add(waitTimeout)
	shutdownTimer := time.NewTimer(waitTimeout)
	defer shutdownTimer.Stop()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if backToBack {
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	} else {
		select {
		case record, ok := <-shutdownStarted:
			if !ok {
				t.Fatal("child output closed before shutdown started")
			}
			if record.Level != "INFO" || record.Signal != "terminated" || record.Timeout != "25s" {
				t.Fatalf("unexpected shutdown record: %+v", record)
			}
		case <-shutdownTimer.C:
			t.Fatal("server did not restore signal defaults before shutdown")
		}
		for probe() == nil {
			if time.Now().After(deadline) {
				t.Fatal("listener stayed open after termination signal")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		waited = true
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected signal exit, got %v", err)
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			t.Fatalf("expected signal status, got %v", err)
		}
		if backToBack {
			// Pending signals can be delivered in either order.
			if status.Signal() != syscall.SIGINT && status.Signal() != syscall.SIGTERM {
				t.Fatalf("expected interrupt or termination exit, got %v", err)
			}
		} else if status.Signal() != syscall.SIGINT {
			t.Fatalf("expected interrupt exit, got %v", err)
		}
	case <-shutdownTimer.C:
		t.Fatal("second signal did not stop the process")
	}
}
