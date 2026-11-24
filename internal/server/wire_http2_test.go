package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestWireHTTP2ResetPreservesSiblingStream(t *testing.T) {
	for _, name := range []string{"cleartext", "TLS"} {
		t.Run(name, func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			first, sibling := newWireCall(t), newWireCall(t)
			cfg := netHTTPWireConfig(t)
			planner := planWireCalls(map[string]*wireCall{"wire-reset": first, "wire-sibling": sibling})
			if name == "cleartext" {
				addr, _, _ := startWireApp(t, cfg, planner, nil)
				protocols := new(http.Protocols)
				protocols.SetUnencryptedHTTP2(true)
				transport := &http.Transport{Protocols: protocols}
				t.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: wireSocketWait}
				assertWireHTTP2Reset(t, client, "http://"+string(addr), first, sibling, assertWireHTTP2Plan)
				return
			}
			app := newTestApp(t, cfg, planner, nil)
			app.Get("/transport", func(c fiber.Ctx) error {
				state := c.RequestCtx().TLSConnectionState()
				if !c.Secure() || state == nil || state.NegotiatedProtocol != "h2" || c.IP() != "127.0.0.1" {
					t.Errorf("TLS=%v, secure=%v, peer=%q", state, c.Secure(), c.IP())
				}
				return c.SendStatus(http.StatusNoContent)
			})
			server := newHTTPServer(app, t.Context())
			listener := httptest.NewUnstartedServer(server.Handler)
			listener.Config = server
			listener.EnableHTTP2 = true
			listener.StartTLS()
			t.Cleanup(listener.Close)
			client := listener.Client()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, listener.URL+"/transport", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent || resp.ProtoMajor != 2 {
				t.Fatalf("transport status=%d protocol=%s", resp.StatusCode, resp.Proto)
			}
			assertWireHTTP2Reset(t, client, listener.URL, first, sibling, assertWireHTTP2Plan)
		})
	}
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
	select {
	case firstCanceled := <-first.canceled:
		assertWireCancelLatency(t, reset, firstCanceled)
	case <-time.After(wireDisconnectBudget):
		t.Fatal("planner did not observe the HTTP/2 stream reset within 500ms")
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
