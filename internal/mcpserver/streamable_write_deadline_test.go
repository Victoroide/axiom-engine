package mcpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

var (
	errWriteExpiredEarly = errors.New("write expired before the configured deadline")
	errWriteStillBlocked = errors.New("write still blocked at the configured deadline")
)

func TestStreamableHTTPBoundsBlockedWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adjust  func(time.Time) time.Time
		wantErr error
	}{
		{name: "configured deadline"},
		{name: "missing deadline", adjust: func(time.Time) time.Time { return time.Time{} }, wantErr: errWriteStillBlocked},
		{name: "premature deadline", adjust: func(time.Time) time.Time { return time.Now() }, wantErr: errWriteExpiredEarly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if err := checkStreamableBlockedWrite(t, tc.adjust); !errors.Is(err, tc.wantErr) {
					t.Fatalf("blocked write check = %v, want %v", err, tc.wantErr)
				}
			})
		})
	}
}

func checkStreamableBlockedWrite(t *testing.T, adjustDeadline func(time.Time) time.Time) error {
	t.Helper()
	const deadline = time.Second
	cfg := streamableConfig(t)
	cfg.WriteTimeout = deadline
	handler := NewStreamableHTTPHandler(cfg, graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", true)
	client, server := net.Pipe()
	conn := &streamablePipeWriter{
		Conn: server, adjustDeadline: adjustDeadline, started: make(chan string, 1), written: make(chan error, 1),
	}
	listener := &streamablePipeListener{incoming: make(chan net.Conn, 1), closed: make(chan struct{}), addr: server.LocalAddr()}
	listener.incoming <- conn
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	defer func() { _ = srv.Close() }()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	const method = "tools/list"
	req := streamableRequest(t, "http://127.0.0.1/mcp", method, "", streamableMessage(method, julyProtocol, `{}`))
	sent := make(chan error, 1)
	start := time.Now()
	go func() { sent <- req.Write(client) }()
	// The client leaves the pipe unread after sending this request.
	synctest.Wait()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("sending request: %v", err)
		}
	default:
		t.Fatal("request is still pending")
	}
	select {
	case status := <-conn.started:
		if status != "HTTP/1.1 200 OK" {
			t.Fatalf("first connection write began with %q", status)
		}
	default:
		t.Fatal("response did not reach the connection")
	}
	if elapsed := time.Since(start); elapsed != 0 {
		t.Fatalf("virtual clock advanced by %v during request setup", elapsed)
	}

	shutdown := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { shutdown <- srv.Shutdown(ctx) }()
	synctest.Sleep(deadline - time.Nanosecond)
	select {
	case err := <-conn.written:
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return errWriteExpiredEarly
		}
		t.Fatalf("first write returned %v before deadline", err)
	default:
	}
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown returned %v before deadline", err)
	default:
	}

	synctest.Sleep(time.Nanosecond)
	select {
	case err := <-conn.written:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("first write returned %v at deadline", err)
		}
	default:
		return errWriteStillBlocked
	}
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatalf("shutdown after write deadline: %v", err)
		}
	case <-time.After(2 * deadline):
		t.Fatal("shutdown did not drain after the write deadline")
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v after shutdown", err)
	}
	return nil
}

type streamablePipeListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	addr     net.Addr
	once     sync.Once
}

func (l *streamablePipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.incoming:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *streamablePipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *streamablePipeListener) Addr() net.Addr { return l.addr }

type streamablePipeWriter struct {
	net.Conn
	adjustDeadline func(time.Time) time.Time
	started        chan string
	written        chan error
	once           sync.Once
}

func (c *streamablePipeWriter) SetWriteDeadline(deadline time.Time) error {
	if c.adjustDeadline != nil && !deadline.IsZero() {
		deadline = c.adjustDeadline(deadline)
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *streamablePipeWriter) Write(p []byte) (int, error) {
	first := false
	c.once.Do(func() {
		first = true
		status, _, _ := strings.Cut(string(p), "\r\n")
		c.started <- status
	})
	n, err := c.Conn.Write(p)
	if first {
		c.written <- err
	}
	return n, err
}
