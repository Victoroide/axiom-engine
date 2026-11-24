package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/Victoroide/axiom-engine/internal/api"
	"github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestVerifiedCancellationDuringKeyLookup(t *testing.T) {
	for _, tc := range []struct {
		name, code, retry string
		cause             error
		status            int
	}{
		{"peer", "request_timeout", "", context.Canceled, http.StatusRequestTimeout},
		{"shutdown", "service_unavailable", "1", api.ErrServerDraining, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, signer := verifiedSigningKey(t, "cancel-key")
			jwks := verifiedJWKS(t, key)
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/blocked" {
					started <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				_, _ = w.Write(jwks)
			}))
			t.Cleanup(endpoint.Close)
			defer close(release)
			fixture := verifiedFixture{issuer: endpoint.URL}
			token := fixture.token(t, signer, "tenant", "caller", "plan:generate")
			ctx, cancel := context.WithCancelCause(api.WithPeerCancellation(t.Context()))
			defer cancel(nil)
			for _, keyPath := range []string{"/ready", "/blocked"} {
				verifier, err := auth.New(auth.Config{
					Issuer: fixture.issuer, Audience: verifiedAudience, JWKSURL: endpoint.URL + keyPath,
					Algorithms: []string{"RS256"}, Claims: auth.ClaimMapping{Tenant: "tenant_id"},
					Transport: endpoint.Client().Transport,
				})
				if err != nil {
					t.Fatal(err)
				}
				cfg := defaultConfig(t)
				cfg.AuthDisabled, cfg.VerifiedAuth = false, verifier
				var calls atomic.Int32
				app := newTestApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
					calls.Add(1)
					return wireItems(req), nil
				}, nil)
				app.State().Set(requestContextKey, ctx)
				headers := replayHeaders(token, "cancel-key")
				if keyPath == "/ready" {
					resp, _ := call(t, app, http.MethodPost, wirePlan, wireTopicBody, headers)
					if resp.StatusCode != http.StatusOK || calls.Load() != 1 {
						t.Fatalf("control status=%d calls=%d", resp.StatusCode, calls.Load())
					}
					continue
				}
				type outcome struct {
					resp response
					body string
					err  error
				}
				done := make(chan outcome, 1)
				go func() {
					resp, body, err := callWithError(app, http.MethodPost, wirePlan, wireTopicBody, headers)
					done <- outcome{resp, body, err}
				}()
				receiveWire(t, started, "key lookup")
				cancel(tc.cause)
				got := receiveWire(t, done, "canceled verification")
				if got.err != nil || got.resp.StatusCode != tc.status || got.resp.Header.Get("Retry-After") != tc.retry ||
					got.resp.Header.Get("WWW-Authenticate") != "" || got.resp.Header.Get("X-RateLimit-Limit") != "" ||
					!strings.Contains(got.body, `"error":"`+tc.code+`"`) || calls.Load() != 0 {
					t.Fatalf("verification cancellation: status=%d headers=%v body=%s calls=%d err=%v", got.resp.StatusCode, got.resp.Header, got.body, calls.Load(), got.err)
				}
			}
		})
	}
}

func TestWireVerifiedCredentialTrailers(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			cfg, fixture := newVerifiedFixture(t, false)
			cfg.HTTPListener, cfg.RateLimitMax = listener, 1
			var calls atomic.Int32
			addr, _, _ := startWireApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				return wireItems(req), nil
			}, nil)
			bearer := "Bearer " + fixture.token(t, fixture.signer, "tenant", "caller", "plan:generate")
			initial := "Authorization: " + bearer + "\r\n"
			conn := addr.dial(t)
			for _, tc := range []struct{ name, headers, declaration, trailers string }{
				{"legacy declared", initial, "X-Internal-Token", "X-Internal-Token: legacy\r\n"},
				{"legacy undeclared", initial, "X-Probe", "x-internal-token:\r\n"},
			} {
				conn.sendRaw(t, wirePlan, "Content-Type: application/json\r\n"+tc.headers+wireChunkedPlanTrailers(tc.declaration, tc.trailers))
				resp := conn.response(t, nil)
				body := readWireBody(t, resp.Body)
				if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != "Bearer" ||
					resp.Header.Get("X-RateLimit-Limit") != "" || !strings.Contains(body, `"error":"unauthorized"`) {
					t.Errorf("%s: status=%d headers=%v body=%s", tc.name, resp.StatusCode, resp.Header, body)
				}
			}
			conn.sendRaw(t, wirePlan, "Content-Type: application/json\r\n"+initial+wireChunkedPlanTrailers("X-Internal-Token", ""))
			resp := conn.response(t, nil)
			body := readWireBody(t, resp.Body)
			if resp.StatusCode != http.StatusOK || resp.Header.Get("X-RateLimit-Remaining") != "0" || calls.Load() != 1 {
				t.Fatalf("omitted trailer control status=%d headers=%v body=%s calls=%d", resp.StatusCode, resp.Header, body, calls.Load())
			}
		})
	}
}

func TestWireVerifiedParserQuota(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(listener), func(t *testing.T) {
			base, fixture := newVerifiedFixture(t, false)
			plan := "Bearer " + fixture.token(t, fixture.signer, "tenant", "caller", "plan:generate")
			graph := "Bearer " + fixture.token(t, fixture.signer, "tenant", "caller", "graph:read")
			for _, tc := range []struct {
				name, path, credential, framing, requestID string
				status                                     int
				charged                                    bool
			}{
				{"declared length", wirePlan, plan, "Content-Length: 65537\r\n\r\n", "parser-test", http.StatusRequestEntityTooLarge, true},
				{"received invalid trailer", wireStream, plan, wireChunkedPlanTrailers("X-Probe", "Range: bytes=0-1\r\n"), "parser-test", http.StatusBadRequest, true},
				{"insufficient scope", wirePlan, graph, "Content-Length: 65537\r\n\r\n", "parser-test", http.StatusRequestEntityTooLarge, false},
				{"wrong method", "/api/v1/topics", plan, "Content-Length: 65537\r\n\r\n", "parser-test", http.StatusRequestEntityTooLarge, false},
				{"unknown operation", wirePlan + "/extra", plan, "Content-Length: 65537\r\n\r\n", "parser-test", http.StatusRequestEntityTooLarge, false},
				{"forbidden credential declaration", wirePlan, plan, "Transfer-Encoding: chunked\r\nTrailer: Authorization\r\n\r\n", "", http.StatusBadRequest, false},
				{"discarded metadata", wirePlan, plan, "Transfer-Encoding: chunked\r\n\r\nZ\r\n", "", http.StatusBadRequest, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := base
					cfg.HTTPListener, cfg.RateLimitMax = listener, 1
					if tc.name == "declared length" {
						cfg.Redis.URL = "redis://" + miniredis.RunT(t).Addr()
					}
					var calls atomic.Int32
					addr, _, _ := startWireApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
						calls.Add(1)
						return wireItems(req), nil
					}, nil)
					conn := addr.dial(t)
					conn.sendRaw(t, tc.path, "Content-Type: application/json\r\nX-Request-ID: parser-test\r\nAuthorization: "+tc.credential+"\r\n"+tc.framing)
					resp := conn.response(t, nil)
					body := readWireBody(t, resp.Body)
					assertWireID(t, resp, tc.requestID)
					if resp.StatusCode != tc.status || resp.Header.Get("WWW-Authenticate") != "" ||
						(resp.Header.Get("X-RateLimit-Remaining") == "0") != tc.charged || calls.Load() != 0 {
						t.Errorf("parser rejection status=%d headers=%v body=%s calls=%d", resp.StatusCode, resp.Header, body, calls.Load())
					}
					resp, body = addr.dial(t).exchange(t, http.MethodPost, wirePlan, wireTopicBody, http.Header{"Authorization": {plan}})
					if tc.charged {
						if resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 0 {
							t.Fatalf("expected exhausted quota: status=%d body=%s calls=%d", resp.StatusCode, body, calls.Load())
						}
					} else if resp.StatusCode != http.StatusOK || calls.Load() != 1 {
						t.Fatalf("expected available quota: status=%d body=%s calls=%d", resp.StatusCode, body, calls.Load())
					}
				})
			}
		})
	}
}
