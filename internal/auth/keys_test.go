package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestRefreshRotationOutageAndCacheAge(t *testing.T) {
	first := newSigningKey(t, "first", jose.ES256)
	rotated := newSigningKey(t, "rotated", jose.ES256)
	unknown := newSigningKey(t, "unknown", jose.ES256)
	otherUnknown := newSigningKey(t, "other-unknown", jose.ES256)
	var published atomic.Pointer[[]byte]
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if data := published.Load(); data != nil {
			_, _ = w.Write(*data)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	config := fixtureConfig(server)
	config.RefreshInterval = 10 * time.Second
	config.MaxKeyAge = 30 * time.Second
	verifier, setTime := fixtureVerifier(t, config)
	initial, replacement := jwks(t, first.jwk), jwks(t, rotated.jwk)
	published.Store(&initial)
	claims := claimsFor(config.Issuer)
	claims["exp"] = testTime.Add(time.Hour).Unix()
	firstToken, rotatedToken, unknownToken := sign(t, first, claims), sign(t, rotated, claims), sign(t, unknown, claims)

	if _, err := verifier.Verify(t.Context(), firstToken); err != nil {
		t.Fatal(err)
	}
	published.Store(&replacement)
	setTime(testTime.Add(9 * time.Second))
	assertRejected(t, verifier, rotatedToken, ErrInvalidToken)
	assertRequests(t, &requests, 1)

	setTime(testTime.Add(10 * time.Second))
	if _, err := verifier.Verify(t.Context(), rotatedToken); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, verifier, firstToken, ErrInvalidToken)
	assertRequests(t, &requests, 2)

	published.Store(nil)
	setTime(testTime.Add(20 * time.Second))
	assertRejected(t, verifier, unknownToken, ErrInvalidToken)
	if _, err := verifier.Verify(t.Context(), rotatedToken); err != nil {
		t.Fatalf("fresh cached key during outage: %v", err)
	}
	assertRequests(t, &requests, 3)

	otherToken := sign(t, otherUnknown, claims)
	setTime(testTime.Add(29 * time.Second))
	assertRejected(t, verifier, otherToken, ErrInvalidToken)
	assertRequests(t, &requests, 3)

	setTime(testTime.Add(30 * time.Second))
	assertRejected(t, verifier, otherToken, ErrInvalidToken)
	assertRequests(t, &requests, 4)

	setTime(testTime.Add(40 * time.Second))
	assertRejected(t, verifier, rotatedToken, ErrInvalidToken)
	assertRequests(t, &requests, 5)
	published.Store(&replacement)
	setTime(testTime.Add(50 * time.Second))
	if _, err := verifier.Verify(t.Context(), rotatedToken); err != nil {
		t.Fatalf("issuer recovery: %v", err)
	}
	assertRequests(t, &requests, 6)
}

func TestConcurrentUnknownKeyRefresh(t *testing.T) {
	key := newSigningKey(t, "known", jose.ES256)
	unknown := newSigningKey(t, "unknown", jose.ES256)
	material := jwks(t, key.jwk)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(material)
	}))
	t.Cleanup(server.Close)
	config := fixtureConfig(server)
	config.RefreshInterval = 10 * time.Second
	verifier, setTime := fixtureVerifier(t, config)
	knownToken, unknownToken := sign(t, key, claimsFor(config.Issuer)), sign(t, unknown, claimsFor(config.Issuer))
	if _, err := verifier.Verify(t.Context(), knownToken); err != nil {
		t.Fatal(err)
	}
	setTime(testTime.Add(config.RefreshInterval))

	start := make(chan struct{})
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			<-start
			assertRejected(t, verifier, unknownToken, ErrInvalidToken)
		})
	}
	close(start)
	group.Wait()
	assertRequests(t, &requests, 2)
}

func TestRefreshRejectsInvalidKeySetsAndRedirects(t *testing.T) {
	key := newSigningKey(t, "known", jose.ES256)
	material := jwks(t, key.jwk)
	invalidUse := key.jwk
	invalidUse.Use = "enc"
	invalidAlgorithm := key.jwk
	invalidAlgorithm.Algorithm = "RS256"

	for _, tc := range []struct {
		name     string
		response []byte
		redirect bool
	}{
		{name: "duplicate key id", response: jwks(t, key.jwk, key.jwk)},
		{name: "private key", response: jwks(t, key.private)},
		{name: "key marked for encryption", response: jwks(t, invalidUse)},
		{name: "key algorithm mismatch", response: jwks(t, invalidAlgorithm)},
		{name: "oversized set", response: append([]byte(strings.Repeat(" ", 64*1024)), material...)},
		{name: "too many keys", response: []byte(strings.TrimSuffix(string(material), "]}") + strings.Repeat(",null", 64) + "]}")},
		{name: "redirect", redirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/keys" {
					t.Error("key request followed a redirect")
				}
				if tc.redirect {
					http.Redirect(w, r, "/unconfigured", http.StatusFound)
					return
				}
				_, _ = w.Write(tc.response)
			}))
			t.Cleanup(server.Close)
			config := fixtureConfig(server)
			verifier, _ := fixtureVerifier(t, config)
			assertRejected(t, verifier, sign(t, key, claimsFor(config.Issuer)), ErrInvalidToken)
			assertRequests(t, &requests, 1)
		})
	}
}

func TestVerifyReturnsAtRefreshDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deadlineReached := make(chan struct{})
		var requests atomic.Int32
		const cleanupTime = 50 * time.Millisecond
		verifier, token, setCacheTime := refreshTimingFixture(t, func(material string) refreshTransport {
			return func(req *http.Request) (*http.Response, error) {
				if requests.Add(1) != 1 {
					return keyResponse(http.StatusServiceUnavailable, ""), nil
				}
				<-req.Context().Done()
				close(deadlineReached)
				<-time.After(cleanupTime)
				return keyResponse(http.StatusOK, material), nil
			}
		})

		start := time.Now()
		first := verifyAsync(t.Context(), verifier, token)
		<-deadlineReached
		synctest.Wait()
		finished := assertRefreshResult(t, first, ErrInvalidToken)
		if !finished.IsZero() && finished.Sub(start) != 20*time.Millisecond {
			t.Errorf("verification returned after %s, want 20ms", finished.Sub(start))
		}

		// Let the cache interval elapse while the canceled transport is still cleaning up.
		setCacheTime(testTime.Add(30 * time.Second))
		next := verifyAsync(t.Context(), verifier, token)
		synctest.Wait()
		assertRefreshResult(t, next, ErrInvalidToken)
		assertRequests(t, &requests, 1)

		<-time.After(cleanupTime)
		synctest.Wait()
		select {
		case late := <-first:
			t.Errorf("verification returned after %s, want 20ms", late.finished.Sub(start))
		default:
		}

		// A response returned after cancellation cannot populate the cache.
		assertRejected(t, verifier, token, ErrInvalidToken)
		assertRequests(t, &requests, 2)
	})
}

func TestRefreshCancelsWhenLastWaiterLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, stopped := make(chan struct{}), make(chan struct{})
		verifier, token, _ := refreshTimingFixture(t, func(_ string) refreshTransport {
			return func(req *http.Request) (*http.Response, error) {
				close(started)
				<-req.Context().Done()
				close(stopped)
				return nil, req.Context().Err()
			}
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := verifyAsync(ctx, verifier, token)
		<-started
		cancel()
		synctest.Wait()
		assertRefreshResult(t, result, context.Canceled)
		select {
		case <-stopped:
		default:
			t.Error("refresh still running after its last waiter left")
		}
	})
}

func TestRefreshContinuesForOtherWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		var requests atomic.Int32
		verifier, token, _ := refreshTimingFixture(t, func(material string) refreshTransport {
			return func(req *http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					close(started)
				}
				select {
				case <-release:
					return keyResponse(http.StatusOK, material), nil
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			}
		})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first := verifyAsync(ctx, verifier, token)
		<-started
		second := verifyAsync(t.Context(), verifier, token)
		synctest.Wait()

		cancel()
		synctest.Wait()
		assertRefreshResult(t, first, context.Canceled)
		close(release)
		synctest.Wait()
		assertRefreshResult(t, second, nil)
		assertRequests(t, &requests, 1)
	})
}

type refreshTransport func(*http.Request) (*http.Response, error)

func (transport refreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func keyResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func refreshTimingFixture(t *testing.T, newTransport func(string) refreshTransport) (*Verifier, string, func(time.Time)) {
	t.Helper()
	key := newSigningKey(t, "key", jose.ES256)
	config := Config{
		Issuer: "https://issuer.example", Audience: testAudience, JWKSURL: "https://issuer.example/keys",
		Algorithms: []string{"ES256"}, Claims: ClaimMapping{Tenant: "org_id"},
		RefreshTimeout: 20 * time.Millisecond, Transport: newTransport(string(jwks(t, key.jwk))),
	}
	verifier, setTime := fixtureVerifier(t, config)
	return verifier, sign(t, key, claimsFor(config.Issuer)), setTime
}

type refreshResult struct {
	principal Principal
	err       error
	finished  time.Time
}

func verifyAsync(ctx context.Context, verifier *Verifier, token string) <-chan refreshResult {
	result := make(chan refreshResult, 1)
	go func() {
		principal, err := verifier.Verify(ctx, token)
		result <- refreshResult{principal, err, time.Now()}
	}()
	return result
}

func assertRefreshResult(t *testing.T, results <-chan refreshResult, want error) time.Time {
	t.Helper()
	select {
	case result := <-results:
		if !errors.Is(result.err, want) {
			t.Errorf("verification returned %v, want %v", result.err, want)
		}
		if (result.principal == Principal{}) != (want != nil) {
			t.Error("verification returned an unexpected principal")
		}
		return result.finished
	default:
		t.Error("verification remained blocked")
		return time.Time{}
	}
}
