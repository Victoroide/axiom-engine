package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func assertWireStreamHeaders(t *testing.T, resp wireResponse, id string) {
	t.Helper()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" || len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Fatalf("status=%d headers=%v transfer encoding=%v", resp.StatusCode, resp.Header, resp.TransferEncoding)
	}
	assertWireID(t, resp, id)
}

func assertWireBytes(t *testing.T, reader io.Reader, want string) {
	t.Helper()
	got := make([]byte, len(want))
	if n, err := io.ReadFull(reader, got); err != nil || string(got) != want {
		t.Fatalf("stream bytes=%q, want %q, err=%v", got[:n], want, err)
	}
}

const wireKeepalive = ": keepalive\n\n"

func readWireEvent(t *testing.T, reader *bufio.Reader) (streamEvent, bool) {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) && line == "" {
			return streamEvent{}, false
		}
		if err != nil {
			t.Fatalf("incomplete SSE frame %q: %v", line, err)
		}
		if line == ": keepalive\n" {
			assertWireBytes(t, reader, "\n")
			continue
		}
		name, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "event: ")
		if !ok || name == "" {
			t.Fatalf("invalid SSE event line %q", line)
		}
		data, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("incomplete SSE data %q: %v", data, err)
		}
		payload, ok := strings.CutPrefix(strings.TrimSuffix(data, "\n"), "data: ")
		if !ok || !json.Valid([]byte(payload)) {
			t.Fatalf("invalid SSE data line %q", data)
		}
		assertWireBytes(t, reader, "\n")
		return streamEvent{name: name, data: json.RawMessage(payload)}, true
	}
}

func assertWireEvent(t *testing.T, reader *bufio.Reader, name, data string) {
	t.Helper()
	event, ok := readWireEvent(t, reader)
	if !ok || event.name != name || string(event.data) != data {
		t.Fatalf("SSE event %q with data %s, want %q with data %s", event.name, event.data, name, data)
	}
}

func assertWireEOF(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	if b, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after terminal SSE frame read byte %q, err=%v, want EOF", b, err)
	}
}

func TestWireStreamFailureAfterFirstByte(t *testing.T) {
	wireStreamFailureCases(t, defaultConfig(t))
}

func wireStreamFailureCases(t *testing.T, cfg config.Config) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		failure error
		details string
	}{
		{"upstream rejection", service.ErrUpstreamRejected, "upstream reasoning failed"},
		{"missing required topic", nil, "model returned no usable item"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			failing := newWireCall(t)
			failing.failure, failing.empty = tc.failure, tc.failure == nil
			addr, _, _ := startWireApp(t, cfg, func(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
				if r.Topic == "Polymorphism" {
					return failing.plan(ctx, r)
				}
				return wireItems(r), nil
			}, nil)
			assertWireStreamFailure(t, addr, failing, tc.details)
		})
	}
}

func assertWireStreamFailure(t *testing.T, addr wireAddress, failing *wireCall, details string) {
	t.Helper()
	conn := addr.dial(t)
	req := conn.send(t, http.MethodPost, wireStream, validBody, wireHeaders("wire-error"))
	receiveWire(t, failing.entered, "failing topic entry")
	resp := conn.response(t, req)
	assertWireStreamHeaders(t, resp, "wire-error")
	reader := bufio.NewReader(resp.Body)
	assertWireBytes(t, reader, wireKeepalive)
	assertWireEvent(t, reader, "topic", "{\"topic\":\"Recursion\",\"index\":0,\"items_generated\":1}")
	if failing.empty {
		// Keep the topic blocked until the server emits an interleaved heartbeat.
		line, err := reader.Peek(len(wireKeepalive))
		if err != nil || string(line) != wireKeepalive {
			t.Fatalf("interleaved SSE heartbeat=%q, err=%v", line, err)
		}
	}
	failing.unblock()
	assertWireEvent(t, reader, "topic", "{\"topic\":\"Polymorphism\",\"index\":1,\"items_generated\":0}")
	assertWireEvent(t, reader, "error", "{\"error\":\"bad_gateway\",\"details\":"+strconv.Quote(details)+",\"status\":502}")
	assertWireEOF(t, reader)
}

func TestWireStreamFlushesThroughProxyWithSlowReader(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	wireStreamFlushesThroughProxyWithSlowReader(t, defaultConfig(t))
}

func wireStreamFlushesThroughProxyWithSlowReader(t *testing.T, cfg config.Config) {
	t.Helper()
	pending := newWireCall(t)
	logs := streamLogWriter{records: make(chan []byte, 40)}
	addr, _, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-proxied": pending}), logs)
	upstream := &url.URL{Scheme: "http", Host: string(addr)}
	transport := &http.Transport{ResponseHeaderTimeout: wireSocketWait}
	t.Cleanup(transport.CloseIdleConnections)
	proxy := &httputil.ReverseProxy{
		Rewrite:       func(r *httputil.ProxyRequest) { r.SetURL(upstream) },
		Transport:     transport,
		FlushInterval: -1,
	}
	writePending := make(chan struct{})
	resume := make(chan struct{})
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			t.Errorf("enable proxy full duplex: %v", err)
			http.Error(w, "proxy full duplex unavailable", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == wireStream {
			w = &pausedStreamWriter{ResponseWriter: w, ctx: r.Context(), pending: writePending, resume: resume}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(listener.Close)
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	assertWireSlowReader(t, wireAddress(listener.Listener.Addr().String()), pending, writePending, resume, logs)
}

type pausedStreamWriter struct {
	http.ResponseWriter
	ctx           context.Context
	pending       chan<- struct{}
	resume        <-chan struct{}
	commentOffset int
}

func (w *pausedStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *pausedStreamWriter) Write(p []byte) (int, error) {
	if w.pending != nil {
		for _, b := range p {
			if b == wireKeepalive[w.commentOffset] {
				w.commentOffset = (w.commentOffset + 1) % len(wireKeepalive)
				continue
			}
			// Earlier writes may contain any number of complete or split keepalives.
			// Stop the first data write before it reaches the downstream socket.
			close(w.pending)
			w.pending = nil
			select {
			case <-w.resume:
			case <-w.ctx.Done():
				return 0, w.ctx.Err()
			}
			break
		}
	}
	return w.ResponseWriter.Write(p)
}

func assertWireSlowReader(t *testing.T, addr wireAddress, pending *wireCall, writePending <-chan struct{}, resume chan<- struct{}, logs streamLogWriter) {
	t.Helper()
	conn := addr.dial(t)
	req := conn.send(t, http.MethodPost, wireStream, wireTopicBody, wireHeaders("wire-proxied"))
	receiveWire(t, pending.entered, "proxied planner entry")
	resp := conn.response(t, req)
	assertWireStreamHeaders(t, resp, "wire-proxied")
	reader := bufio.NewReader(resp.Body)
	assertWireBytes(t, reader, wireKeepalive)

	pending.unblock()
	select {
	case <-writePending:
	case <-time.After(wireWait):
		t.Fatal("proxy never reached a pending stream data write")
	}
	other := addr.dial(t)
	second, body := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders("wire-other"))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("parallel request status=%d body=%s", second.StatusCode, body)
	}
	assertWireID(t, second, "wire-other")
	assertWirePlan(t, body)

	close(resume)
	assertWireEvent(t, reader, "topic", "{\"topic\":\"Recursion\",\"index\":0,\"items_generated\":1}")
	plan, ok := readWireEvent(t, reader)
	if !ok || plan.name != "plan" {
		t.Fatalf("unexpected terminal SSE event: %q %s", plan.name, plan.data)
	}
	assertWirePlan(t, string(plan.data))
	assertWireEOF(t, reader)
	assertWireAccess(t, logs, "wire-proxied", http.StatusOK, false)
	probe, body := conn.exchange(t, http.MethodGet, "/livez", "", wireHeaders("wire-after-stream"))
	if probe.StatusCode != http.StatusOK || body != "{\"status\":\"ok\"}" {
		t.Fatalf("keep-alive probe status=%d body=%s", probe.StatusCode, body)
	}
	assertWireID(t, probe, "wire-after-stream")
}
