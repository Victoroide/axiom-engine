package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/metrics"
)

type loopbackLinkPolicy struct{}

func (loopbackLinkPolicy) ValidateURL(u *url.URL) error {
	if u.User == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.IsLoopback() {
			return nil
		}
	}
	return publicResourceLinkPolicy{}.ValidateURL(u)
}
func (loopbackLinkPolicy) ValidateAddress(ip netip.Addr) error {
	if ip.IsLoopback() {
		return nil
	}
	return publicResourceLinkPolicy{}.ValidateAddress(ip)
}

func TestResourceLinkHTTPOutcomes(t *testing.T) {
	var fallback atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" || r.UserAgent() != "axiom-engine" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusNoContent)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/redirect":
			w.Header().Add("Set-Cookie", "session=private; Path=/; HttpOnly; SameSite=Strict")
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/fallback405", "/fallback501":
			if r.Method == http.MethodHead {
				if r.URL.Path == "/fallback405" {
					w.WriteHeader(http.StatusMethodNotAllowed)
				} else {
					w.WriteHeader(http.StatusNotImplemented)
				}
				return
			}
			if r.Method != http.MethodGet || r.Header.Get("Range") != "bytes=0-0" {
				t.Errorf("fallback method=%s headers=%v", r.Method, r.Header)
			}
			fallback.Add(1)
			w.WriteHeader(http.StatusPartialContent)
		case "/r3":
			http.Redirect(w, r, "/r2", http.StatusFound)
		case "/r2":
			http.Redirect(w, r, "/redirect", http.StatusFound)
		case "/r4":
			http.Redirect(w, r, "/r3", http.StatusFound)
		}
	}))
	defer remote.Close()
	recorder := metrics.New()
	checker := NewResourceLinkChecker(5*time.Second, loopbackLinkPolicy{}, recorder)
	for _, tc := range []struct{ path, want string }{{"/ok", "ok"}, {"/missing", "status"}, {"/redirect", "ok"}, {"/fallback405", "ok"}, {"/fallback501", "ok"}, {"/r3", "ok"}, {"/r4", "status"}} {
		t.Run(tc.path, func(t *testing.T) {
			if got := checker.check(t.Context(), remote.URL+tc.path); got != tc.want {
				t.Fatalf("result=%s want=%s", got, tc.want)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			checker := NewResourceLinkChecker(time.Second, nil, recorder)
			checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			if got := checker.Check(t.Context(), "https://links.example/slow"); got != "timeout" {
				t.Fatalf("result=%s want=timeout", got)
			}
		})
	})
	if fallback.Load() != 2 {
		t.Fatalf("fallback requests=%d", fallback.Load())
	}
	if got := checker.check(t.Context(), "ftp://example.com/guide"); got != "invalid" {
		t.Fatal(got)
	}
	if got := checker.check(t.Context(), "http://user:pass@example.com/guide"); got != "invalid" {
		t.Fatal(got)
	}
	// A refused destination must also produce the bounded refused metric.
	checker = NewResourceLinkChecker(time.Second, nil, recorder)
	if got := checker.check(t.Context(), "http://127.0.0.1/guide"); got != "refused" {
		t.Fatal(got)
	}
	scrape := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(scrape, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for _, line := range []string{`axiom_resource_links_checked_total{result="ok"} 5`, `axiom_resource_links_checked_total{result="status"} 2`, `axiom_resource_links_checked_total{result="timeout"} 1`, `axiom_resource_links_checked_total{result="invalid"} 2`, `axiom_resource_links_checked_total{result="refused"} 1`} {
		if !strings.Contains(scrape.Body.String(), line) {
			t.Errorf("missing %s", line)
		}
	}
}

var nonPublicLinkAddresses = []string{
	"0.0.0.0", "0.1.2.3", "10.0.0.1", "10.255.255.255", "100.64.0.1", "100.127.255.255", "127.0.0.1", "127.255.255.254", "169.254.0.1", "172.16.0.1", "172.31.255.255", "192.0.0.1", "192.0.2.1", "192.88.99.2", "192.168.0.1", "198.18.0.1", "198.19.255.255", "198.51.100.1", "203.0.113.1", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255",
	"::", "::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:100.64.0.1", "::ffff:169.254.1.1", "::ffff:192.168.1.1", "::ffff:224.0.0.1", "::ffff:0.0.0.0", "fc00::1", "fdff::1", "fe80::1", "ff02::1", "100::1", "100:0:0:1::1", "64:ff9b::a00:1", "64:ff9b:1::1", "2001:db8::1", "2001:2::1", "2002:a00:1::1", "3fff::1", "5f00::1",
}

func TestResourceLinkDefaultDialRefusesNonPublicAddresses(t *testing.T) {
	for _, ip := range nonPublicLinkAddresses {
		t.Run(ip, func(t *testing.T) {
			checker := NewResourceLinkChecker(time.Second, nil, nil)
			transport := checker.client.Transport
			checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(req)
				if !errors.Is(err, errResourceLinkRefused) {
					t.Errorf("dial error=%v, want address policy refusal", err)
				}
				return response, err
			})
			if got := checker.check(t.Context(), "http://"+net.JoinHostPort(ip, "80")); got != "refused" {
				t.Errorf("check result=%q, want refused", got)
			}
		})
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "172.15.255.255", "172.32.0.1", "192.167.255.255", "192.169.0.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if err := (publicResourceLinkPolicy{}).ValidateAddress(netip.MustParseAddr(ip)); err != nil {
			t.Errorf("public IP %s refused: %v", ip, err)
		}
	}
}

type redirectResponseTransport struct {
	next   http.RoundTripper
	target string
}

func (r redirectResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "public.example" {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{r.target}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	return r.next.RoundTrip(req)
}

func TestResourceLinkDefaultPolicyRefusesRedirectDestinations(t *testing.T) {
	for _, ip := range nonPublicLinkAddresses {
		t.Run(ip, func(t *testing.T) {
			checker := NewResourceLinkChecker(time.Second, nil, nil)
			checker.client.Transport = redirectResponseTransport{next: checker.client.Transport, target: "http://" + net.JoinHostPort(ip, "80") + "/private"}
			if got := checker.check(t.Context(), "http://public.example/start"); got != "refused" {
				t.Fatalf("redirect result=%s", got)
			}
		})
	}
	for _, target := range []string{"file:///etc/passwd", "ftp://public.example/file", "http://public.example:8080/file", "https://user:secret@public.example/file"} {
		checker := NewResourceLinkChecker(time.Second, nil, nil)
		checker.client.Transport = redirectResponseTransport{next: checker.client.Transport, target: target}
		if got := checker.check(t.Context(), "http://public.example/start"); got != "invalid" {
			t.Errorf("target=%s result=%s", target, got)
		}
	}
}

type redirectLinkPolicy struct {
	loopbackLinkPolicy
	denied   netip.Addr
	observed []netip.Addr
}

func (p *redirectLinkPolicy) ValidateAddress(ip netip.Addr) error {
	p.observed = append(p.observed, ip)
	if ip == p.denied {
		return errResourceLinkRefused
	}
	return p.loopbackLinkPolicy.ValidateAddress(ip)
}

func TestResourceLinkRedirectRechecksDialAddress(t *testing.T) {
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/denied", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	start := remote.Listener.Addr().(*net.TCPAddr).AddrPort()
	denied := netip.MustParseAddr("127.0.0.2")
	policy := &redirectLinkPolicy{denied: denied}
	checker := NewResourceLinkChecker(time.Second, policy, nil)
	transport := checker.client.Transport.(*http.Transport)
	dial := transport.DialContext
	var lookups atomic.Int32
	transport.DialContext = func(ctx context.Context, network, authority string) (net.Conn, error) {
		if authority != "links.example:80" {
			t.Errorf("unexpected authority %s", authority)
			return nil, errResourceLinkRefused
		}
		// Resolve the same authority to a different IP on the redirect.
		address := start
		if lookups.Add(1) > 1 {
			address = netip.AddrPortFrom(denied, start.Port())
		}
		return dial(ctx, network, address.String())
	}
	if got := checker.check(t.Context(), "http://links.example/start"); got != "refused" {
		t.Fatalf("redirect result=%q, want refused", got)
	}
	if !slices.Equal(policy.observed, []netip.Addr{start.Addr(), denied}) || hits.Load() != 1 {
		t.Fatalf("dial addresses=%v, HTTP requests=%d", policy.observed, hits.Load())
	}
}

func TestResourceLinkTransportHasNoProxy(t *testing.T) {
	checker := NewResourceLinkChecker(time.Second, nil, nil)
	if checker.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("resource link transport consults a proxy")
	}
}

func TestResourceLinkFallbackSharesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 200 * time.Millisecond
		checker := NewResourceLinkChecker(timeout, nil, nil)
		var methods []string
		checker.client.Transport = resourceLinkRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			methods = append(methods, r.Method)
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-time.After(140 * time.Millisecond):
			}
			if r.Method == http.MethodHead {
				return linkResponse(r, http.StatusMethodNotAllowed), nil
			}
			return linkResponse(r, http.StatusPartialContent), nil
		})
		start := time.Now()
		got := checker.Check(t.Context(), "https://links.example/guide")
		if got != "timeout" || time.Since(start) != timeout || !slices.Equal(methods, []string{http.MethodHead, http.MethodGet}) {
			t.Fatalf("result=%s elapsed=%s methods=%v", got, time.Since(start), methods)
		}
	})
}

func TestResourceLinkChecksRespectRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		checker := NewResourceLinkChecker(time.Second, nil, nil)
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		if got := checker.Check(ctx, "https://links.example/guide"); got != "timeout" || time.Since(start) != 30*time.Millisecond {
			t.Fatalf("result=%s elapsed=%v, want timeout after 30ms", got, time.Since(start))
		}
	})
}

func TestResourceLinkCancellationClosesTLSConnection(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	checker := NewResourceLinkChecker(time.Second, loopbackLinkPolicy{}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- checker.check(ctx, "https://"+listener.Addr().String()) }()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var firstByte [1]byte
	if _, err := io.ReadFull(conn, firstByte[:]); err != nil {
		t.Fatalf("read TLS handshake: %v", err)
	}
	cancel()
	if got := <-done; got != "timeout" {
		t.Errorf("canceled result=%q, want timeout", got)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("cancellation did not close the TLS connection: %v", err)
	}
}

type resourceLinkRoundTripFunc func(*http.Request) (*http.Response, error)

func (f resourceLinkRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func linkResponse(req *http.Request, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("fixture-body-canary")), Request: req}
}

func TestResourceLinkFilteringAcrossRequests(t *testing.T) {
	const base = "https://links.example/"
	const guide = base + "guide?fixture-query-canary=1"
	const missing = base + "missing"
	const occurrences = 101
	const distinct = 11
	type requestKey struct{}
	recorder := metrics.New()
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var active, maximum int
		hits := [2]map[string]int{make(map[string]int), make(map[string]int)}
		release := make(chan struct{})
		checker := NewResourceLinkChecker(time.Second, nil, recorder)
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			i := req.Context().Value(requestKey{}).(int)
			mu.Lock()
			hits[i][req.URL.String()]++
			active++
			maximum = max(maximum, active)
			mu.Unlock()
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			if req.URL.Path == "/missing" {
				return linkResponse(req, http.StatusNotFound), nil
			}
			return linkResponse(req, http.StatusOK), nil
		})
		items := make([]domain.PlanItem, 0, occurrences+4)
		for i := range occurrences {
			items = append(items, domain.PlanItem{Topic: fmt.Sprintf("Topic %d", i), Resources: []domain.Resource{
				{Title: "first", URL: " " + guide, ResourceType: "video"},
				{Title: "removed", URL: missing, ResourceType: "video"},
				{Title: "last", URL: guide, ResourceType: "diagram"},
			}})
		}
		for i := range 3 {
			resources := make([]domain.Resource, 4)
			for j := range resources {
				resources[j] = domain.Resource{Title: fmt.Sprint(j), URL: fmt.Sprintf("%sguide?group=%d&index=%d", base, i, j), ResourceType: "video"}
			}
			items = append(items, domain.PlanItem{Topic: fmt.Sprintf("Distinct %d", i), Resources: resources})
		}
		items = append(items, domain.PlanItem{Topic: "Empty"})
		sanitized, removed := sanitizeOutput(items, "visual", false)
		if removed != 3 {
			t.Fatalf("output policy drops=%d, want 3", removed)
		}
		var requests sync.WaitGroup
		for i := range hits {
			requests.Go(func() {
				ctx := context.WithValue(t.Context(), requestKey{}, i)
				got, dropped := checker.filterResources(ctx, sanitized)
				if dropped != occurrences || len(got) != len(sanitized) {
					t.Errorf("request %d: items=%d drops=%d, want %d items and %d drops", i, len(got), dropped, len(sanitized), occurrences)
					return
				}
				for j, item := range got {
					want := sanitized[j].Resources
					if j < occurrences {
						want = []domain.Resource{want[0], want[2]}
					}
					if item.Topic != sanitized[j].Topic || !slices.Equal(item.Resources, want) || item.Resources == nil {
						t.Errorf("request %d item %d: topic=%q resources=%v, want %q %v", i, j, item.Topic, item.Resources, sanitized[j].Topic, want)
					}
				}
			})
		}
		synctest.Wait()
		if active != 8 {
			t.Errorf("aggregate concurrency while blocked=%d, want 8", active)
		}
		close(release)
		requests.Wait()
		if maximum != 8 {
			t.Errorf("maximum aggregate concurrency=%d, want 8", maximum)
		}
		for i, counts := range hits {
			if len(counts) != distinct {
				t.Errorf("request %d: distinct checks=%d, want %d", i, len(counts), distinct)
			}
			for url, n := range counts {
				if n != 1 || strings.Contains(url, "index=3") {
					t.Errorf("request %d: checks for %s=%d", i, url, n)
				}
			}
		}
	})
	scrape := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(scrape, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for _, line := range []string{`axiom_resource_links_checked_total{result="ok"} 20`, `axiom_resource_links_checked_total{result="status"} 2`} {
		if !slices.Contains(strings.Split(scrape.Body.String(), "\n"), line) {
			t.Errorf("missing %s", line)
		}
	}
	for _, marker := range []string{base, "fixture-query-canary", "fixture-body-canary"} {
		if strings.Contains(scrape.Body.String(), marker) {
			t.Errorf("resource link metrics contain %q", marker)
		}
	}
}

func TestResourceLinkCancellationAndTimeoutWhileQueued(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		wait       time.Duration
	}{
		{name: "caller cancels", want: "timeout"},
		{name: "check deadline expires", want: "timeout", wait: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				checker := NewResourceLinkChecker(time.Second, nil, nil)
				entered := make(chan struct{}, 8)
				release := make(chan struct{})
				var queuedHits atomic.Int32
				checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/hold" {
						entered <- struct{}{}
						<-release
					} else {
						queuedHits.Add(1)
					}
					return linkResponse(req, http.StatusOK), nil
				})
				var running sync.WaitGroup
				for range 8 {
					running.Go(func() { checker.Check(t.Context(), "https://links.example/hold") })
				}
				for range 8 {
					<-entered
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan string, 1)
				go func() { done <- checker.Check(ctx, "https://links.example/queued") }()
				synctest.Wait()
				if queuedHits.Load() != 0 {
					t.Error("queued check reached the transport while all permits were occupied")
				}
				if tc.wait == 0 {
					cancel()
				} else {
					time.Sleep(tc.wait)
				}
				synctest.Wait()
				select {
				case got := <-done:
					if got != tc.want {
						t.Errorf("queued result=%q, want %q", got, tc.want)
					}
				default:
					t.Error("queued check did not finish while all permits were occupied")
				}
				close(release)
				running.Wait()
				if queuedHits.Load() != 0 {
					t.Error("canceled check reached the transport")
				}
				if got := checker.Check(t.Context(), "https://links.example/recovered"); got != "ok" {
					t.Errorf("result after releasing permits=%q, want ok", got)
				}
			})
		})
	}
}

func TestResourceLinkPlanQueueUsesCheckDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		checker := NewResourceLinkChecker(time.Second, nil, nil)
		var hits atomic.Int32
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			hits.Add(1)
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		items := make([]domain.PlanItem, 9)
		for i := range items {
			items[i].Resources = []domain.Resource{{URL: fmt.Sprintf("https://links.example/%d", i)}}
		}
		start := time.Now()
		got, dropped := checker.filterResources(t.Context(), items)
		if len(got) != len(items) || dropped != 9 || hits.Load() != 8 || time.Since(start) != time.Second {
			t.Fatalf("items=%d dropped=%d attempts=%d duration=%s", len(got), dropped, hits.Load(), time.Since(start))
		}
	})
}

func TestResourceLinksAreCheckedAfterSanitizing(t *testing.T) {
	const link = "https://links.example/guide"
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "bad type", URL: link, ResourceType: "podcast"}, {Title: "missing", URL: link, ResourceType: "video"}}}}, nil
	})
	var hits atomic.Int32
	checker := NewResourceLinkChecker(time.Second, nil, nil)
	checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		hits.Add(1)
		return linkResponse(req, http.StatusNotFound), nil
	})
	svc.resourceLinks = checker
	got, err := svc.GeneratePlan(t.Context(), request("Recursion"))
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || len(got.Items[0].Resources) != 0 || got.Meta.ResourcesUnreachable != 1 {
		t.Fatalf("hits=%d plan=%+v", hits.Load(), got)
	}
}

func TestResourceLinkRedirectLimitIncludesFallback(t *testing.T) {
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, "/last", http.StatusFound)
		case "/last":
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}
	}))
	defer remote.Close()
	checker := NewResourceLinkChecker(time.Second, loopbackLinkPolicy{}, nil)
	if got := checker.check(t.Context(), remote.URL+"/start"); got != "status" {
		t.Fatalf("result=%s requests=%d", got, hits.Load())
	}
	if hits.Load() != 5 {
		t.Fatalf("requests=%d, want HEAD plus at most three total redirects", hits.Load())
	}
}

func TestResourceLinkDeadlinePropagatesFromPlan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "Slow guide", URL: "https://links.example/slow", ResourceType: "video"}}}}, nil
		})
		checker := NewResourceLinkChecker(time.Second, nil, nil)
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		svc.resourceLinks = checker
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		if _, err := svc.GeneratePlan(ctx, request("Recursion")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
	})
}

type linkDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c linkDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestResourceLinkPlanDeadlineBeforeCancellationSignal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const link = "https://links.example/guide"
		svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "Guide", URL: link, ResourceType: "video"}}}}, nil
		})
		checker := NewResourceLinkChecker(5*time.Second, nil, nil)
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			if !ok {
				t.Error("resource request lost its deadline")
				return linkResponse(req, http.StatusOK), nil
			}
			time.Sleep(time.Until(deadline))
			// An I/O timeout can complete before the parent timer signals Done.
			return nil, os.ErrDeadlineExceeded
		})
		svc.resourceLinks = checker
		ctx := linkDeadlineContext{Context: t.Context(), deadline: time.Now().Add(time.Second)}
		if _, err := svc.GeneratePlan(ctx, request("Recursion")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v, want deadline exceeded", err)
		}
	})
}
