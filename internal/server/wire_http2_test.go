package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// Fiber serves HTTP/1.1. Exercise the reset probe with a real TLS HTTP/2 listener
// that propagates the individual request context to the planner.
func TestWireHTTP2ResetPreservesSiblingStream(t *testing.T) {
	first, sibling := newWireCall(t), newWireCall(t)
	planner := planWireCalls(map[string]*wireCall{"wire-reset": first, "wire-sibling": sibling})
	cfg := defaultConfig(t)
	reasoning := service.NewReasoningService(graph.NewInMemoryGraph(), planner, service.Options{
		LLMTimeout: cfg.LLMTimeout, MaxFailedTopics: cfg.MaxFailedTopics,
	})
	listener := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req domain.AdaptivePlanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		id := r.Header.Get("X-Request-ID")
		plan, err := reasoning.GeneratePlan(telemetry.WithRequestID(r.Context(), id), req)
		if err != nil {
			http.Error(w, "plan failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(plan); err != nil {
			t.Errorf("write HTTP/2 plan: %v", err)
		}
	}))
	listener.EnableHTTP2 = true
	listener.StartTLS()
	t.Cleanup(listener.Close)
	assertWireHTTP2Reset(t, listener.Client(), listener.URL, first, sibling, assertWireHTTP2Plan)
}

func assertWireHTTP2Reset(t *testing.T, client *http.Client, baseURL string, first, sibling *wireCall, validateSibling func(*testing.T, wireHTTP2Result)) {
	t.Helper()
	resetCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstConn, firstResult := sendWireHTTP2(t, resetCtx, client, baseURL, "wire-reset")
	receiveWire(t, first.entered, "HTTP/2 reset stream planner")
	siblingConn, siblingResult := sendWireHTTP2(t, t.Context(), client, baseURL, "wire-sibling")
	receiveWire(t, sibling.entered, "HTTP/2 sibling planner")
	if receiveWire(t, firstConn, "first client connection") != receiveWire(t, siblingConn, "sibling client connection") {
		t.Fatal("requests did not share a TCP connection")
	}

	reset := time.Now()
	cancel()
	firstCanceled := receiveWire(t, first.canceled, "HTTP/2 reset cancellation")
	if !errors.Is(firstCanceled.err, context.Canceled) || firstCanceled.at.Before(reset) {
		t.Fatalf("first stream cancellation=%v at %s", firstCanceled.err, firstCanceled.at)
	}
	first.unblock()
	if got := receiveWire(t, firstResult, "canceled client result"); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("first client result=%+v", got)
	}
	sibling.unblock()
	got := receiveWire(t, siblingResult, "sibling client result")
	if got.err != nil || got.proto != "HTTP/2.0" {
		t.Fatalf("sibling result=%+v", got)
	}
	validateSibling(t, got)
	t.Logf("stream reset to planner cancellation=%s", firstCanceled.at.Sub(reset))
}

func assertWireHTTP2Plan(t *testing.T, result wireHTTP2Result) {
	t.Helper()
	if result.StatusCode != http.StatusOK || result.Header.Get("Content-Type") != "application/json; charset=utf-8" || result.Header.Get("X-Request-ID") != "wire-sibling" {
		t.Fatalf("sibling plan status=%d headers=%v body=%s", result.StatusCode, result.Header, result.body)
	}
	assertWirePlan(t, result.body)
}

type wireHTTP2Result struct {
	response
	proto string
	body  string
	err   error
}

func sendWireHTTP2(t *testing.T, ctx context.Context, client *http.Client, baseURL, id string) (<-chan net.Conn, <-chan wireHTTP2Result) {
	t.Helper()
	connections := make(chan net.Conn, 1)
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		select {
		case connections <- info.Conn:
		default:
		}
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, baseURL+wirePlan, strings.NewReader(wireTopicBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = wireHeaders(id)
	req.Header.Set("Content-Type", "application/json")
	result := make(chan wireHTTP2Result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := client.Do(req)
		if err != nil {
			result <- wireHTTP2Result{err: err}
			return
		}
		body, readErr := io.ReadAll(resp.Body)
		result <- wireHTTP2Result{
			response: response{StatusCode: resp.StatusCode, Header: resp.Header},
			proto:    resp.Proto,
			body:     string(body),
			err:      errors.Join(readErr, resp.Body.Close()),
		}
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(wireWait):
			t.Error("HTTP/2 client did not exit")
		}
	})
	return connections, result
}
