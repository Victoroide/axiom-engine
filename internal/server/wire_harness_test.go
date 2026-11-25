package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

const (
	wirePlan       = "/api/v1/adaptive-plan"
	wireStream     = wirePlan + "/stream"
	wireTopicBody  = `{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion"],"vark_profile":"visual"}`
	wireWait       = 6 * time.Second
	wireSocketWait = 8 * time.Second
)

// wireAddress needs only a listening TCP address. The caller owns its lifecycle.
type wireAddress string

// The default limiter owns a process-lifetime worker. Join the subprocess so
// the worker cannot stay alive between socket scenarios.
func runWireSubprocess(t *testing.T) bool {
	t.Helper()
	const child = "AXIOM_WIRE_SUBPROCESS"
	if os.Getenv(child) == "1" {
		return true
	}
	// Schedule the launcher alongside other isolated scenarios.
	t.Parallel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(t.Name(), "/")
	for i := range parts {
		parts[i] = "^" + regexp.QuoteMeta(parts[i]) + "$"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.v", "-test.run="+strings.Join(parts, "/")) //nolint:gosec // Reexecute this test binary for the selected socket scenario.
	cmd.Env = append(os.Environ(), child+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("socket scenario subprocess: %v\n%s", err, output)
	}
	t.Logf("\n%s", output)
	return false
}

func startWireApp(t *testing.T, cfg config.Config, planner fakePlanner, logs io.Writer) (wireAddress, context.CancelFunc, <-chan error) {
	t.Helper()
	return startWireAppAfter(t, cfg, planner, logs, nil)
}

func startWireAppAfter(t *testing.T, cfg config.Config, planner fakePlanner, logs io.Writer, allowAccept <-chan struct{}) (wireAddress, context.CancelFunc, <-chan error) {
	t.Helper()
	app := newTestApp(t, cfg, planner, logs)
	ready := make(chan wireAddress, 1)
	app.Hooks().OnListen(func(data fiber.ListenData) error {
		ready <- wireAddress(net.JoinHostPort(data.Host, data.Port))
		return nil
	})
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if cfg.HTTPListener == config.HTTPListenerNetHTTP {
		ready <- wireAddress(ln.Addr().String())
	}
	if allowAccept != nil {
		ln = wireAcceptGate{Listener: ln, ready: allowAccept, stopped: ctx.Done()}
	}
	go func() {
		defer close(done)
		done <- serveListener(ctx, app, ln, cfg.ShutdownTimeout)
	}()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(cfg.ShutdownDrainDelay + cfg.ShutdownTimeout + time.Second):
			t.Error("server did not shut down")
		}
	})
	select {
	case addr := <-ready:
		return addr, stop, done
	case err := <-done:
		t.Fatalf("server exited before listening: %v", err)
	case <-ctx.Done():
		t.Fatalf("server startup canceled before listening: %v", ctx.Err())
	}
	return "", nil, nil
}

// wireAcceptGate holds the socket before the HTTP server can arm its deadline.
type wireAcceptGate struct {
	net.Listener
	ready, stopped <-chan struct{}
}

func (l wireAcceptGate) Accept() (net.Conn, error) {
	select {
	case <-l.stopped:
		return nil, net.ErrClosed
	case <-l.ready:
		return l.Listener.Accept()
	}
}

type wireConn struct {
	*net.TCPConn
	reader  *bufio.Reader
	address wireAddress
}

type wireResponse struct {
	response
	Body             io.Reader
	Proto            string
	Close            bool
	ContentLength    int64
	TransferEncoding []string
}

func (addr wireAddress) dial(t *testing.T) wireConn {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", string(addr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(wireSocketWait)); err != nil {
		t.Fatal(err)
	}
	return wireConn{TCPConn: conn.(*net.TCPConn), reader: bufio.NewReader(conn), address: addr}
}

func (c wireConn) send(t *testing.T, method, path, body string, headers http.Header) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+string(c.address)+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := req.Write(c); err != nil {
		t.Fatal(err)
	}
	return req
}

func (c wireConn) sendRaw(t *testing.T, path, headersAndBody string) {
	t.Helper()
	request := "POST " + path + " HTTP/1.1\r\nHost: " + string(c.address) + "\r\n" + headersAndBody
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
}

func sendPartialWireRequest(t *testing.T, cfg config.Config, planner fakePlanner, path, partial string) (wireConn, time.Duration) {
	t.Helper()
	accept := make(chan struct{})
	addr, _, _ := startWireAppAfter(t, cfg, planner, nil, accept)
	conn := addr.dial(t)
	// Pause the client while the server is unable to start reading.
	time.Sleep(cfg.ReadTimeout / 2)
	conn.sendRaw(t, path, partial)
	type arrival struct {
		at  time.Time
		err error
	}
	ready := make(chan struct{})
	arrived := make(chan arrival, 1)
	go func() {
		close(ready)
		_, err := conn.reader.Peek(1)
		arrived <- arrival{time.Now(), err}
	}()
	<-ready
	start := time.Now()
	close(accept)
	// Pause response parsing past the expected deadline. Peek timestamps the
	// first response byte or EOF independently and leaves the byte buffered.
	time.Sleep(250 * time.Millisecond)
	first := receiveWire(t, arrived, "first response byte or EOF")
	if first.err != nil && !errors.Is(first.err, io.EOF) {
		t.Fatal(first.err)
	}
	return conn, first.at.Sub(start)
}

func (c wireConn) response(t *testing.T, req *http.Request) wireResponse {
	t.Helper()
	resp, err := http.ReadResponse(c.reader, req)
	if err != nil {
		t.Fatal(err)
	}
	// Close the socket before an unread body so cleanup never blocks draining it.
	t.Cleanup(func() { _ = c.Close(); _ = resp.Body.Close() })
	return wireResponse{
		response:         response{StatusCode: resp.StatusCode, Header: resp.Header},
		Body:             resp.Body,
		Proto:            resp.Proto,
		Close:            resp.Close,
		ContentLength:    resp.ContentLength,
		TransferEncoding: resp.TransferEncoding,
	}
}

func (c wireConn) exchange(t *testing.T, method, path, body string, headers http.Header) (wireResponse, string) {
	t.Helper()
	resp := c.response(t, c.send(t, method, path, body, headers))
	return resp, readWireBody(t, resp.Body)
}

func readWireBody(t *testing.T, reader io.Reader) string {
	t.Helper()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func receiveWire[T any](t *testing.T, ch <-chan T, event string) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatalf("%s channel closed", event)
		}
		return v
	case <-time.After(wireWait):
		t.Fatalf("timed out waiting for %s", event)
	}
	var zero T
	return zero
}

type wireCancellation struct {
	at  time.Time
	err error
}

type wireEntry struct {
	at       time.Time
	deadline time.Time
}

// A controlled topic can observe cancellation while its planner still holds the permit.
type wireCall struct {
	entered  chan wireEntry
	canceled chan wireCancellation
	returned chan time.Time
	release  chan struct{}
	testDone <-chan struct{}
	failure  error
	empty    bool
	once     sync.Once
}

func newWireCall(t *testing.T) *wireCall {
	t.Helper()
	c := &wireCall{entered: make(chan wireEntry, 1), canceled: make(chan wireCancellation, 1), returned: make(chan time.Time, 1), release: make(chan struct{}), testDone: t.Context().Done()}
	t.Cleanup(c.unblock)
	return c
}

func (c *wireCall) unblock() { c.once.Do(func() { close(c.release) }) }

func (c *wireCall) plan(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	defer func() {
		select {
		case c.returned <- time.Now():
		case <-c.testDone:
		}
	}()
	deadline, _ := ctx.Deadline()
	select {
	case c.entered <- wireEntry{time.Now(), deadline}:
	case <-c.testDone:
		return nil, context.Canceled
	}
	select {
	case <-ctx.Done():
		select {
		case c.canceled <- wireCancellation{time.Now(), ctx.Err()}:
		case <-c.testDone:
		}
		select {
		case <-c.release:
		case <-c.testDone:
		}
		return nil, ctx.Err()
	case <-c.release:
		if c.failure != nil || c.empty {
			return nil, c.failure
		}
		return wireItems(r), nil
	case <-c.testDone:
		return nil, context.Canceled
	}
}

func wireItems(r service.TopicRequest) []domain.PlanItem {
	return []domain.PlanItem{{Topic: r.Topic, Explanation: "Study " + r.Topic}}
}

func planWireCalls(calls map[string]*wireCall) fakePlanner {
	return func(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		if call := calls[telemetry.RequestID(ctx)]; call != nil {
			return call.plan(ctx, r)
		}
		return wireItems(r), nil
	}
}
