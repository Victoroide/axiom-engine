package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
		case "/slow":
			<-r.Context().Done()
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
	checker := NewResourceLinkChecker(50*time.Millisecond, loopbackLinkPolicy{}, recorder)
	for _, tc := range []struct{ path, want string }{{"/ok", "ok"}, {"/missing", "status"}, {"/redirect", "ok"}, {"/fallback405", "ok"}, {"/fallback501", "ok"}, {"/slow", "timeout"}, {"/r3", "ok"}, {"/r4", "status"}} {
		t.Run(tc.path, func(t *testing.T) {
			if got := checker.Check(t.Context(), remote.URL+tc.path); got != tc.want {
				t.Fatalf("result=%s want=%s", got, tc.want)
			}
		})
	}
	if fallback.Load() != 2 {
		t.Fatalf("fallback requests=%d", fallback.Load())
	}
	if got := checker.Check(t.Context(), "ftp://example.com/guide"); got != "invalid" {
		t.Fatal(got)
	}
	if got := checker.Check(t.Context(), "http://user:pass@example.com/guide"); got != "invalid" {
		t.Fatal(got)
	}
	// A refused destination must also produce the bounded refused metric.
	checker = NewResourceLinkChecker(time.Second, nil, recorder)
	if got := checker.Check(t.Context(), "http://localhost/guide"); got != "refused" {
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
	checker := NewResourceLinkChecker(time.Second, nil, nil)
	transport := checker.client.Transport.(*http.Transport)
	for _, ip := range nonPublicLinkAddresses {
		t.Run(ip, func(t *testing.T) {
			conn, err := transport.DialContext(t.Context(), "tcp", net.JoinHostPort(ip, "80"))
			if conn != nil {
				_ = conn.Close()
				t.Fatal("connected to non-public address")
			}
			if !errors.Is(err, errResourceLinkRefused) {
				t.Fatalf("dial error=%v", err)
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
			if got := checker.Check(t.Context(), "http://public.example/start"); got != "refused" {
				t.Fatalf("redirect result=%s", got)
			}
		})
	}
	for _, target := range []string{"file:///etc/passwd", "ftp://public.example/file", "http://public.example:8080/file", "https://user:secret@public.example/file"} {
		checker := NewResourceLinkChecker(time.Second, nil, nil)
		checker.client.Transport = redirectResponseTransport{next: checker.client.Transport, target: target}
		if got := checker.Check(t.Context(), "http://public.example/start"); got != "invalid" {
			t.Errorf("target=%s result=%s", target, got)
		}
	}
}

func TestResourceLinkTransportHasNoProxy(t *testing.T) {
	checker := NewResourceLinkChecker(time.Second, nil, nil)
	if checker.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("resource link transport consults a proxy")
	}
}

func TestResourceLinkFallbackSharesDeadline(t *testing.T) {
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(140 * time.Millisecond)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	checker := NewResourceLinkChecker(200*time.Millisecond, loopbackLinkPolicy{}, nil)
	if got := checker.Check(t.Context(), remote.URL); got != "timeout" {
		t.Fatalf("result=%s, want timeout within one 200ms budget", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests=%d, want HEAD and fallback GET", got)
	}
}

func TestResourceLinkChecksRespectRequestDeadline(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer remote.Close()
	checker := NewResourceLinkChecker(time.Second, loopbackLinkPolicy{}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := checker.Check(ctx, remote.URL); got != "timeout" {
		t.Fatal(got)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed=%v", elapsed)
	}
}

func TestResourceLinkFilteringIsBoundedAndOrdered(t *testing.T) {
	var active, maximum atomic.Int32
	entered := make(chan struct{}, 30)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer remote.Close()
	items := make([]domain.PlanItem, 10)
	for i := range items {
		items[i] = domain.PlanItem{Topic: "Recursion", Resources: []domain.Resource{{Title: "first", URL: remote.URL + "/ok"}, {Title: "removed", URL: remote.URL + "/missing"}, {Title: "last", URL: remote.URL + "/ok"}}}
	}
	checker := NewResourceLinkChecker(time.Second, loopbackLinkPolicy{}, nil)
	type result struct {
		items   []domain.PlanItem
		dropped int
	}
	done := make(chan result, 1)
	go func() { got, n := checker.filterResources(t.Context(), items); done <- result{got, n} }()
	for range 8 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			unblock()
			t.Fatal("checks did not fill concurrency limit")
		}
	}
	unblock()
	got := <-done
	if maximum.Load() != 8 || got.dropped != 10 {
		t.Fatalf("concurrency=%d dropped=%d", maximum.Load(), got.dropped)
	}
	for _, item := range got.items {
		var titles []string
		for _, r := range item.Resources {
			titles = append(titles, r.Title)
		}
		if !reflect.DeepEqual(titles, []string{"first", "last"}) {
			t.Fatalf("titles=%v", titles)
		}
	}
}

func TestResourceLinksAreCheckedAfterSanitizing(t *testing.T) {
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(http.StatusNotFound) }))
	defer remote.Close()
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "bad type", URL: remote.URL, ResourceType: "podcast"}, {Title: "missing", URL: remote.URL, ResourceType: "video"}}}}, nil
	})
	svc.resourceLinks = NewResourceLinkChecker(time.Second, loopbackLinkPolicy{}, nil)
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
	if got := checker.Check(t.Context(), remote.URL+"/start"); got != "status" {
		t.Fatalf("result=%s requests=%d", got, hits.Load())
	}
	if hits.Load() != 5 {
		t.Fatalf("requests=%d, want HEAD plus at most three total redirects", hits.Load())
	}
}

func TestResourceLinkDeadlinePropagatesFromPlan(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer remote.Close()
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "Slow guide", URL: remote.URL, ResourceType: "video"}}}}, nil
	})
	svc.resourceLinks = NewResourceLinkChecker(time.Second, loopbackLinkPolicy{}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := svc.GeneratePlan(ctx, request("Recursion")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}
