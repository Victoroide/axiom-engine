package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

const wireDisconnectBudget = 500 * time.Millisecond

func netHTTPWireConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		switch key {
		case "HTTP_LISTENER":
			return "net-http"
		case "AXIOM_AUTH_DISABLED":
			return "true"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWireNetHTTPDisconnectReleasesReservation(t *testing.T) {
	for _, capacity := range []int{1, 2} {
		t.Run(fmt.Sprintf("capacity=%d", capacity), func(t *testing.T) {
			if runWireSubprocess(t) {
				wireNetHTTPDisconnect(t, capacity)
			}
		})
	}
}

func wireNetHTTPDisconnect(t *testing.T, capacity int) {
	t.Helper()
	cfg := netHTTPWireConfig(t)
	redis := miniredis.RunT(t)
	cfg.Redis.URL = "redis://" + redis.Addr()
	cfg.LLMMaxConcurrency = capacity
	cfg.LLMQueueTimeout = 100 * time.Millisecond
	blocked := newWireCall(t)
	logs := streamLogWriter{records: make(chan []byte, 20)}
	addr, _, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-close": blocked}), logs)
	client := addr.dial(t)
	headers := wireHeaders("wire-close")
	headers.Set("Idempotency-Key", "wire-disconnected")
	client.send(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	receiveWire(t, blocked.entered, "planner entry")
	closed := time.Now()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	var canceled wireCancellation
	select {
	case canceled = <-blocked.canceled:
	case <-time.After(wireDisconnectBudget):
		t.Fatal("planner did not observe the HTTP/1 close within 500ms")
	}
	assertWireCancelLatency(t, closed, canceled)
	assertWireAccess(t, logs, "wire-close", http.StatusRequestTimeout, true)

	other := addr.dial(t)
	if capacity == 1 {
		assertWirePermitHeld(t, other, "wire-permit-held")
		return
	}
	// The canceled planner stays blocked through the retry and replay.
	// Redis recovery can use the second permit without waiting for it.
	headers.Set("X-Request-ID", wireRecoveryID)
	resp, body := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("fresh retry status=%d headers=%v body=%s", resp.StatusCode, resp.Header, body)
	}
	assertWirePlan(t, body)
	headers.Set("X-Request-ID", "wire-replayed")
	resp, replay := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "true" || replay != body {
		t.Fatalf("retry replay status=%d headers=%v body=%s", resp.StatusCode, resp.Header, replay)
	}
}

func assertWireCancelLatency(t *testing.T, closed time.Time, canceled wireCancellation) {
	t.Helper()
	elapsed := canceled.at.Sub(closed)
	if !errors.Is(canceled.err, context.Canceled) || elapsed < 0 || elapsed > wireDisconnectBudget {
		t.Fatalf("cancel=%v after %s, want context canceled within %s", canceled.err, elapsed, wireDisconnectBudget)
	}
	t.Logf("disconnect to planner cancellation=%s", elapsed)
}

func TestWireNetHTTPPipelinedConnection(t *testing.T) {
	if !tcpCloseSupported {
		t.Skip("TCP peer shutdown observation is available on Linux and macOS")
	}
	for _, mode := range []string{"close", "half-close", "keep-alive"} {
		t.Run(mode, func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			cfg := netHTTPWireConfig(t)
			redis := miniredis.RunT(t)
			cfg.Redis.URL = "redis://" + redis.Addr()
			blocked := newWireCall(t)
			logs := streamLogWriter{records: make(chan []byte, 20)}
			addr, _, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-pipelined": blocked}), logs)
			conn := addr.dial(t)
			headers := wireHeaders("wire-pipelined")
			headers.Set("Idempotency-Key", "wire-pipelined")
			plan := conn.send(t, http.MethodPost, wirePlan, wireTopicBody, headers)
			receiveWire(t, blocked.entered, "planner entry")
			probe := conn.send(t, http.MethodGet, "/livez", "", wireHeaders("wire-pipelined-probe"))

			closed := time.Now()
			switch mode {
			case "close":
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			case "half-close":
				if err := conn.CloseWrite(); err != nil {
					t.Fatal(err)
				}
			case "keep-alive":
				// Leave the pipelined bytes unread while the connection is live.
				time.Sleep(100 * time.Millisecond)
				blocked.unblock()
			}
			if mode != "keep-alive" {
				select {
				case canceled := <-blocked.canceled:
					assertWireCancelLatency(t, closed, canceled)
				case <-time.After(wireDisconnectBudget):
					t.Fatal("planner did not observe the pipelined HTTP/1 close within 500ms")
				}
				assertWireAccess(t, logs, "wire-pipelined", http.StatusRequestTimeout, true)
			}

			if mode != "close" {
				first := conn.response(t, plan)
				body := readWireBody(t, first.Body)
				if mode == "half-close" {
					assertWireError(t, first, body, http.StatusRequestTimeout, "request_timeout", "request canceled", "wire-pipelined")
				} else {
					if first.StatusCode != http.StatusOK {
						t.Fatalf("pipelined plan status=%d body=%s", first.StatusCode, body)
					}
					assertWirePlan(t, body)
					assertWireAccess(t, logs, "wire-pipelined", http.StatusOK, false)
				}
				second := conn.response(t, probe)
				if body := readWireBody(t, second.Body); second.StatusCode != http.StatusOK || body != `{"status":"ok"}` {
					t.Fatalf("pipelined probe status=%d body=%s", second.StatusCode, body)
				}
				assertWireID(t, second, "wire-pipelined-probe")
			}
			if mode != "keep-alive" {
				headers.Set("X-Request-ID", "wire-pipelined-retry")
				other := addr.dial(t)
				resp, body := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
				if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
					t.Fatalf("fresh retry status=%d headers=%v body=%s", resp.StatusCode, resp.Header, body)
				}
				assertWirePlan(t, body)
			}
		})
	}
}

func TestWireNetHTTPConformance(t *testing.T) {
	for _, scenario := range []struct {
		name string
		run  func(*testing.T, config.Config)
	}{
		{"replay and keep-alive", wirePlanReplayAndKeepAlive},
		{"rejected requests", wireRejectedRequests},
		{"shared quota", wireRateLimitSharedByPlanRoutes},
		{"idempotency conflicts", wireIdempotencyInFlightConflicts},
		{"stream errors and heartbeats", wireStreamFailureCases},
		{"stream across multiple heartbeats", wireStreamAcrossHeartbeats},
		{"stream flush through a proxy", wireStreamFlushesThroughProxyWithSlowReader},
		{"graceful drain", wireGracefulDrain},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if runWireSubprocess(t) {
				scenario.run(t, netHTTPWireConfig(t))
			}
		})
	}
}

func wireStreamAcrossHeartbeats(t *testing.T, cfg config.Config) {
	t.Helper()
	pending := newWireCall(t)
	addr, _, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-long-stream": pending}), nil)
	conn := addr.dial(t)
	req := conn.send(t, http.MethodPost, wireStream, wireTopicBody, wireHeaders("wire-long-stream"))
	receiveWire(t, pending.entered, "long stream planner entry")
	resp := conn.response(t, req)
	assertWireStreamHeaders(t, resp, "wire-long-stream")
	for range 3 {
		assertWireBytes(t, resp.Body, wireKeepalive)
	}
	pending.unblock()
	reader := bufio.NewReader(resp.Body)
	assertWireEvent(t, reader, "topic", "{\"topic\":\"Recursion\",\"index\":0,\"items_generated\":1}")
	plan, ok := readWireEvent(t, reader)
	if !ok || plan.name != "plan" {
		t.Fatalf("long stream terminal event=%q, data=%s", plan.name, plan.data)
	}
	assertWirePlan(t, string(plan.data))
	assertWireEOF(t, reader)
}

func TestWireListenerParserErrors(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			cfg := defaultConfig(t)
			cfg.HTTPListener = listener
			reject := func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
				t.Error("parser error reached the planner")
				return nil, nil
			}
			addr, _, _ := startWireApp(t, cfg, reject, nil)
			for _, tc := range []struct {
				name, raw, code string
				status          int
			}{
				{"malformed headers", "Broken-Header\r\n\r\n", "bad_request", http.StatusBadRequest},
				{"oversized headers", "X-Large: " + strings.Repeat("a", 16*1024) + "\r\n\r\n", "request_header_fields_too_large", http.StatusRequestHeaderFieldsTooLarge},
				{"declared content length", "Transfer-Encoding: chunked\r\nTrailer: Content-Length\r\n\r\n", "bad_request", http.StatusBadRequest},
				{"declared transfer encoding", "Transfer-Encoding: chunked\r\nTrailer: Transfer-Encoding\r\n\r\n", "bad_request", http.StatusBadRequest},
				{"declared trailer", "Transfer-Encoding: chunked\r\nTrailer: Trailer\r\n\r\n", "bad_request", http.StatusBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					conn := addr.dial(t)
					conn.sendRaw(t, wirePlan, tc.raw)
					resp := conn.response(t, nil)
					body := readWireBody(t, resp.Body)
					if listener == config.HTTPListenerFiber {
						assertWireError(t, resp, body, tc.status, tc.code, http.StatusText(tc.status), "")
					} else if want := fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)); resp.StatusCode != tc.status || body != want || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" || resp.Header.Get("X-Request-ID") != "" {
						t.Fatalf("native rejection status=%d headers=%v body=%q, want %q", resp.StatusCode, resp.Header, body, want)
					}
				})
			}
			t.Run("incomplete headers", func(t *testing.T) {
				cfg.ReadTimeout = wireHeaderReadTimeout
				conn, elapsed := sendPartialWireRequest(t, cfg, reject, wirePlan, "X-Incomplete: ")
				if listener == config.HTTPListenerFiber {
					resp := conn.response(t, nil)
					assertWireError(t, resp, readWireBody(t, resp.Body), http.StatusRequestTimeout, "request_timeout", "Request Timeout", "")
				} else if data, err := io.ReadAll(conn.reader); err != nil || len(data) != 0 {
					t.Fatalf("timed-out headers received %q with %v, want EOF without a response", data, err)
				}
				if elapsed < cfg.ReadTimeout {
					t.Fatalf("header response or EOF arrived after %s, before the %s read deadline", elapsed, cfg.ReadTimeout)
				}
			})
			t.Run("unsupported expectation", func(t *testing.T) {
				cfg := defaultConfig(t)
				cfg.HTTPListener = listener
				var calls atomic.Int32
				addr, _, _ := startWireApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
					calls.Add(1)
					return wireItems(req), nil
				}, nil)
				headers := wireHeaders("wire-expect")
				headers.Set("Expect", "unsupported")
				resp, body := addr.dial(t).exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
				if listener == config.HTTPListenerFiber {
					if resp.StatusCode != http.StatusOK || calls.Load() != 1 {
						t.Fatalf("Fiber expectation response=%d body=%s calls=%d", resp.StatusCode, body, calls.Load())
					}
					assertWireID(t, resp, "wire-expect")
					assertWirePlan(t, body)
				} else if resp.StatusCode != http.StatusExpectationFailed || body != "" || resp.Header.Get("X-Request-ID") != "" || calls.Load() != 0 {
					t.Fatalf("native expectation response=%d headers=%v body=%q calls=%d", resp.StatusCode, resp.Header, body, calls.Load())
				}
			})
		})
	}
}

func TestWireBodyReadTimeout(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			cfg := defaultConfig(t)
			cfg.HTTPListener = listener
			cfg.ReadTimeout = wireHeaderReadTimeout
			reject := func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
				t.Error("timed-out body reached the planner")
				return nil, nil
			}
			for _, path := range []string{wirePlan, wireStream} {
				for _, tc := range []struct{ name, body, id string }{
					{"fixed length", "Content-Length: 7\r\n\r\n{", ""},
					{"chunked", "Transfer-Encoding: chunked\r\n\r\n7\r\n{", ""},
					{"trailer", "Transfer-Encoding: chunked\r\nTrailer: X-Probe\r\n\r\n2\r\n{}\r\n0\r\nX-Probe: partial", "wire-body-timeout"},
				} {
					t.Run(tc.name+strings.TrimPrefix(path, wirePlan), func(t *testing.T) {
						conn, elapsed := sendPartialWireRequest(t, cfg, reject, path, "X-Request-ID: wire-body-timeout\r\nContent-Type: application/json\r\n"+tc.body)
						resp := conn.response(t, nil)
						assertWireError(t, resp, readWireBody(t, resp.Body), http.StatusRequestTimeout, "request_timeout", "Request Timeout", tc.id)
						if elapsed < cfg.ReadTimeout {
							t.Fatalf("body response arrived after %s, before the %s read deadline", elapsed, cfg.ReadTimeout)
						}
					})
				}
			}
		})
	}
}

func TestWireBodyRejectionPolicies(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			for _, backend := range []string{"memory", "redis"} {
				for _, tc := range []struct {
					name, framing, id, code string
					status                  int
					bothRoutes              bool
				}{
					{"payload timeout", "Content-Length: 7\r\n\r\n{", "", "request_timeout", http.StatusRequestTimeout, false},
					{"malformed chunk", "Transfer-Encoding: chunked\r\n\r\nZ\r\n", "", "bad_request", http.StatusBadRequest, false},
					{"trailer timeout", "Transfer-Encoding: chunked\r\nTrailer: X-Probe\r\n\r\n2\r\n{}\r\n0\r\nX-Probe: partial", "wire-bad-body", "request_timeout", http.StatusRequestTimeout, false},
					{"malformed trailer", "Transfer-Encoding: chunked\r\nTrailer: X-Probe\r\n\r\n2\r\n{}\r\n0\r\nBroken-Trailer\r\n\r\n", "wire-bad-body", "bad_request", http.StatusBadRequest, true},
					{"oversized trailer", "Transfer-Encoding: chunked\r\nTrailer: X-Probe\r\n\r\n2\r\n{}\r\n0\r\nX-Probe: " + strings.Repeat("a", 9000) + "\r\n\r\n", "wire-bad-body", "request_header_fields_too_large", http.StatusRequestHeaderFieldsTooLarge, true},
					{"forbidden trailer", wireChunkedPlanTrailers("X-Probe", "X-Probe: finished\r\nRange: bytes=0-1\r\n"), "wire-bad-body", "bad_request", http.StatusBadRequest, true},
					{"forbidden declaration with field omitted", wireChunkedPlanTrailers("X-Probe, Range", "X-Probe: finished\r\n"), "", "bad_request", http.StatusBadRequest, true},
					{"forbidden declaration with field received", wireChunkedPlanTrailers("X-Probe, Range", "X-Probe: finished\r\nRange: bytes=0-1\r\n"), "", "bad_request", http.StatusBadRequest, true},
					{"forbidden declaration before continue", "Transfer-Encoding: chunked\r\nTrailer: Range\r\nExpect: 100-continue\r\n\r\n", "", "bad_request", http.StatusBadRequest, true},
					{"forbidden declaration with fixed length", fmt.Sprintf("Content-Length: %d\r\nTrailer: X-Probe\r\nTrailer: rAnGe\r\n\r\n%s", len(wireTopicBody), wireTopicBody), "", "bad_request", http.StatusBadRequest, true},
				} {
					paths := []string{wirePlan}
					if tc.bothRoutes {
						paths = append(paths, wireStream)
					}
					for _, path := range paths {
						t.Run(backend+"/"+tc.name+strings.TrimPrefix(path, wirePlan), func(t *testing.T) {
							cfg := defaultConfig(t)
							cfg.HTTPListener, cfg.RateLimitMax = listener, 1
							if tc.status == http.StatusRequestTimeout {
								cfg.ReadTimeout = wireHeaderReadTimeout
							}
							if backend == "redis" {
								cfg.Redis.URL = "redis://" + miniredis.RunT(t).Addr()
							}
							logs := streamLogWriter{records: make(chan []byte, 20)}
							var calls atomic.Int32
							accept := make(chan struct{})
							addr, _, _ := startWireAppAfter(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
								calls.Add(1)
								return wireItems(req), nil
							}, logs, accept)
							conn := addr.dial(t)
							conn.sendRaw(t, path, "Content-Type: application/json\r\nX-Request-ID: wire-bad-body\r\n"+tc.framing)
							close(accept)
							resp := conn.response(t, nil)
							if n := calls.Load(); n != 0 {
								t.Errorf("rejected request reached the planner %d times", n)
							}
							assertWireError(t, resp, readWireBody(t, resp.Body), tc.status, tc.code, http.StatusText(tc.status), tc.id)
							rejectionPath := "/"
							if tc.id != "" {
								rejectionPath = path
								if resp.Header.Get("X-RateLimit-Remaining") != "0" {
									t.Errorf("trailer rejection did not consume the one-request quota: %v", resp.Header)
								}
							} else {
								for _, header := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
									if resp.Header.Get(header) != "" {
										t.Errorf("rejection carried %s=%q", header, resp.Header.Get(header))
									}
								}
							}
							if resp.Header.Get("Retry-After") != "" {
								t.Errorf("body rejection carried Retry-After=%q", resp.Header.Get("Retry-After"))
							}
							assertWireBodyRejectionLog(t, logs, resp.Header.Get("X-Request-ID"), rejectionPath, tc.status)
							other := addr.dial(t)
							resp, body := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders("wire-body-retry"))
							if tc.id != "" {
								assertWireError(t, resp, body, http.StatusTooManyRequests, "rate_limit_exceeded", "too many requests", "wire-body-retry")
								if resp.Header.Get("Retry-After") == "" || calls.Load() != 0 {
									t.Fatalf("trailer retry headers=%v planner calls=%d", resp.Header, calls.Load())
								}
								return
							}
							if resp.StatusCode != http.StatusOK || resp.Header.Get("X-RateLimit-Remaining") != "0" || calls.Load() != 1 {
								t.Fatalf("valid retry status=%d headers=%v body=%s planner calls=%d", resp.StatusCode, resp.Header, body, calls.Load())
							}
							assertWirePlan(t, body)
						})
					}
				}
			}
		})
	}
}

func wireChunkedPlanTrailers(declaration, fields string) string {
	return fmt.Sprintf("Transfer-Encoding: chunked\r\nTrailer: %s\r\n\r\n%x\r\n%s\r\n0\r\n%s\r\n", declaration, len(wireTopicBody), wireTopicBody, fields)
}

func TestWireTrailerFieldNames(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			cfg := defaultConfig(t)
			cfg.HTTPListener = listener
			var calls atomic.Int32
			addr, _, _ := startWireApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				return wireItems(req), nil
			}, nil)
			for _, field := range []string{
				"Authorization", "Connection", "Content-Encoding", "Content-Length", "Content-Range", "Content-Type",
				"Cookie", "Expect", "Host", "Keep-Alive", "Location", "Max-Forwards", "Proxy-Authenticate",
				"Proxy-Authorization", "Proxy-Connection", "rAnGe", "Set-Cookie", "TE", "Trailer", "Transfer-Encoding",
				"WWW-Authenticate", "X-Forwarded", "X-Forwarded-For", "X-Real-IP", "X-Real-IP-Extra",
			} {
				t.Run(field, func(t *testing.T) {
					before := calls.Load()
					conn := addr.dial(t)
					conn.sendRaw(t, wirePlan, "Content-Type: application/json\r\nX-Request-ID: wire-trailer-field\r\n"+
						wireChunkedPlanTrailers("X-Probe", "X-Probe: finished\r\n"+field+":\r\n"))
					resp := conn.response(t, nil)
					body := readWireBody(t, resp.Body)
					if n := calls.Load() - before; n != 0 {
						t.Errorf("forbidden trailer reached the planner %d times", n)
					}
					assertWireError(t, resp, body, http.StatusBadRequest, "bad_request", "Bad Request", "wire-trailer-field")
				})
			}
			t.Run("ordinary trailer", func(t *testing.T) {
				before := calls.Load()
				conn := addr.dial(t)
				conn.sendRaw(t, wirePlan, "Content-Type: application/json\r\nX-Request-ID: wire-trailer-field\r\n"+
					wireChunkedPlanTrailers("X-Probe, X-Optional", "X-Probe: finished\r\nX-Other: also allowed\r\n"))
				resp := conn.response(t, nil)
				body := readWireBody(t, resp.Body)
				if n := calls.Load() - before; resp.StatusCode != http.StatusOK || n != 1 {
					t.Fatalf("allowed trailer status=%d body=%s planner calls=%d", resp.StatusCode, body, n)
				}
				assertWireID(t, resp, "wire-trailer-field")
				assertWirePlan(t, body)
			})
		})
	}
}

func assertWireBodyRejectionLog(t *testing.T, logs streamLogWriter, id, path string, status int) {
	t.Helper()
	for {
		record := receiveWire(t, logs.records, "body rejection log")
		var entry struct {
			Message      string `json:"msg"`
			RequestID    string `json:"request_id"`
			Status       int    `json:"status"`
			Path         string `json:"path"`
			Disconnected bool   `json:"client_disconnected"`
		}
		if err := json.Unmarshal(record, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Message == "storage.failed" {
			t.Errorf("body rejection reported a storage failure: %s", record)
		}
		if entry.Message == "http.access" && entry.RequestID == id {
			if entry.Status != status || entry.Path != path || entry.Disconnected {
				t.Errorf("unexpected body rejection access record: %s", record)
			}
			return
		}
	}
}

func TestWireBodyAtLimit(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			cfg := defaultConfig(t)
			cfg.HTTPListener = listener
			addr, _, _ := startWireApp(t, cfg, planWireCalls(nil), nil)
			conn := addr.dial(t)
			maxBody := wireTopicBody + strings.Repeat(" ", 64*1024-len(wireTopicBody))
			resp, body := conn.exchange(t, http.MethodPost, wirePlan, maxBody, wireHeaders("wire-max-body"))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("body at limit returned %d: %s", resp.StatusCode, body)
			}
			assertWireID(t, resp, "wire-max-body")
			assertWirePlan(t, body)
		})
	}
}

func TestWireTrustedPeerQuota(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			for _, trusted := range []bool{false, true} {
				t.Run(fmt.Sprintf("trusted=%t", trusted), func(t *testing.T) {
					cfg := defaultConfig(t)
					cfg.HTTPListener, cfg.RateLimitMax = listener, 1
					if trusted {
						cfg.TrustedProxies = []string{"127.0.0.1", "192.0.2.51"}
					}
					addr, _, _ := startWireApp(t, cfg, planWireCalls(nil), nil)
					conn := addr.dial(t)
					headers := wireHeaders("wire-peer-one")
					headers["X-Forwarded-For"] = []string{"198.51.100.1", "203.0.113.1, 192.0.2.51"}
					first, body := conn.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
					if first.StatusCode != http.StatusOK {
						t.Fatalf("first request returned %d: %s", first.StatusCode, body)
					}
					headers = wireHeaders("wire-peer-two")
					headers.Set("X-Forwarded-For", "203.0.113.2")
					second, body := conn.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
					if trusted {
						if second.StatusCode != http.StatusOK {
							t.Fatalf("separate trusted quota returned %d: %s", second.StatusCode, body)
						}
					} else {
						assertWireError(t, second, body, http.StatusTooManyRequests, "rate_limit_exceeded", "too many requests", "wire-peer-two")
					}
					headers = wireHeaders("wire-peer-again")
					headers.Set("X-Forwarded-For", "203.0.113.1")
					same, body := conn.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
					assertWireError(t, same, body, http.StatusTooManyRequests, "rate_limit_exceeded", "too many requests", "wire-peer-again")
				})
			}
		})
	}
}

func TestWireReasoningCancellationEnvelopes(t *testing.T) {
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			for _, drain := range []bool{false, true} {
				t.Run(fmt.Sprintf("drain=%t", drain), func(t *testing.T) {
					cfg := defaultConfig(t)
					cfg.HTTPListener = listener
					cfg.ShutdownTimeout = 1500 * time.Millisecond
					if !drain {
						cfg.RequestTimeout, cfg.LLMTimeout = 250*time.Millisecond, 250*time.Millisecond
					}
					call := newWireCall(t)
					addr, stop, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-bounded": call}), nil)
					conn := addr.dial(t)
					req := conn.send(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders("wire-bounded"))
					receiveWire(t, call.entered, "planner entry")
					if drain {
						stop()
					}
					resp := conn.response(t, req)
					body := readWireBody(t, resp.Body)
					canceled := receiveWire(t, call.canceled, "planner cancellation")
					call.unblock()
					if drain {
						assertWireError(t, resp, body, http.StatusServiceUnavailable, "service_unavailable", "server shutting down", "wire-bounded")
						if resp.Header.Get("Retry-After") != "1" || !errors.Is(canceled.err, context.Canceled) {
							t.Fatalf("headers=%v cancellation=%v", resp.Header, canceled.err)
						}
					} else {
						assertWireError(t, resp, body, http.StatusGatewayTimeout, "gateway_timeout", "reasoning exceeded deadline", "wire-bounded")
						if resp.Header.Get("Retry-After") != "" || !errors.Is(canceled.err, context.DeadlineExceeded) {
							t.Fatalf("headers=%v cancellation=%v", resp.Header, canceled.err)
						}
					}
				})
			}
		})
	}
}

func TestWireNetHTTPDisconnectWhileQueued(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	cfg := netHTTPWireConfig(t)
	cfg.RateLimitMax = 10
	cfg.LLMMaxConcurrency = 1
	cfg.LLMQueueTimeout = wireQueueTimeout
	blocked := newWireCall(t)
	queueConn := make(chan wireConn, 1)
	type closeResult struct {
		at  time.Time
		err error
	}
	closed := make(chan closeResult, 1)
	observations := observeWirePlan(t, func() {
		conn := <-queueConn
		at := time.Now()
		closed <- closeResult{at: at, err: conn.Close()}
	})
	logs := streamLogWriter{records: make(chan []byte, 20)}
	var queuedCalls atomic.Int32
	addr, _, _ := startWireApp(t, cfg, func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		switch telemetry.RequestID(ctx) {
		case "wire-close":
			return blocked.plan(ctx, req)
		case wireRecoveryID:
			queuedCalls.Add(1)
		}
		return wireItems(req), nil
	}, logs)
	first := addr.dial(t)
	firstReq := first.send(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders("wire-close"))
	receiveWire(t, blocked.entered, "occupied planner permit")
	queued := addr.dial(t)
	queueConn <- queued
	queued.send(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders(wireRecoveryID))
	receiveWire(t, observations.recoveryWait, "queued request")
	time.Sleep(cfg.LLMQueueTimeout + 50*time.Millisecond)
	peerClose := receiveWire(t, closed, "queued peer close")
	if peerClose.err != nil {
		t.Fatal(peerClose.err)
	}
	loggedAt := assertWireAccess(t, logs, wireRecoveryID, http.StatusRequestTimeout, true)
	if elapsed := loggedAt.Sub(peerClose.at); elapsed < 0 || elapsed > wireDisconnectBudget {
		t.Fatalf("queued cancellation was logged after %s", elapsed)
	}
	blocked.unblock()
	resp := first.response(t, firstReq)
	body := readWireBody(t, resp.Body)
	assertWireSuccessHeaders(t, resp, "wire-close", "8", "", body)
	assertWirePlan(t, body)
	if calls := queuedCalls.Load(); calls != 0 {
		t.Fatalf("disconnected queued request dispatched %d planner calls", calls)
	}
}
