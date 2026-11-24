package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

type blockedLinkAddressPolicy struct {
	publicResourceLinkPolicy
	entered chan struct{}
	release <-chan struct{}
	active  atomic.Int32
}

func (p *blockedLinkAddressPolicy) ValidateAddress(netip.Addr) error {
	p.active.Add(1)
	defer p.active.Add(-1)
	p.entered <- struct{}{}
	<-p.release
	return errResourceLinkRefused
}

func TestResourceLinkCanceledPlanRetainsPermitsUntilDialsExit(t *testing.T) {
	for _, attempt := range []string{"head", "redirect", "fallback"} {
		t.Run(attempt, func(t *testing.T) { testResourceLinkCanceledPlanDials(t, attempt) })
	}
}

func testResourceLinkCanceledPlanDials(t *testing.T, attempt string) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		policy := &blockedLinkAddressPolicy{entered: make(chan struct{}, 9), release: release}
		checker := NewResourceLinkChecker(time.Minute, policy, nil)
		transport := checker.client.Transport
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case attempt == "redirect" && strings.HasPrefix(req.URL.Path, "/link/"):
				response := linkResponse(req, http.StatusFound)
				response.Header = http.Header{"Location": {"/redirected"}}
				return response, nil
			case attempt == "fallback" && req.Method == http.MethodHead:
				return linkResponse(req, http.StatusMethodNotAllowed), nil
			default:
				return transport.RoundTrip(req)
			}
		})
		items := make([]domain.PlanItem, 8)
		for i := range items {
			items[i].Resources = []domain.Resource{{URL: fmt.Sprintf("http://127.0.0.1/link/%d", i)}}
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first := make(chan int, 1)
		go func() { _, dropped := checker.filterResources(ctx, items); first <- dropped }()
		for range items {
			<-policy.entered
		}
		cancel()
		synctest.Wait()
		select {
		case dropped := <-first:
			if dropped != 8 {
				t.Errorf("drops on cancellation=%d, want 8", dropped)
			}
		default:
			t.Error("canceled plan did not return while its dials were still blocked")
		}

		second := make(chan int, 1)
		go func() { _, dropped := checker.filterResources(t.Context(), items[:1]); second <- dropped }()
		synctest.Wait()
		if got := policy.active.Load(); got != 8 {
			t.Errorf("live address validations after cancellation=%d, want 8", got)
		}
		select {
		case <-policy.entered:
			t.Error("second plan began a dial before the first plan's dials finished")
		default:
		}
		unblock()
		if dropped := <-second; dropped != 1 {
			t.Errorf("drops after permits became available=%d, want 1", dropped)
		}
		synctest.Wait()
		if !checker.permits.TryAcquire(8) {
			t.Fatal("canceled plan leaked permits after its dials exited")
		}
		checker.permits.Release(8)
	})
}

type blockedLinkSocket struct {
	net.Conn
	closing chan struct{}
	release <-chan struct{}
}

func (s blockedLinkSocket) Close() error {
	close(s.closing)
	<-s.release
	return s.Conn.Close()
}

func TestResourceLinkSocketCleanupBlocksNextDialAndPermitRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finished := make(chan struct{})
		work := &resourceLinkWork{release: func() { close(finished) }}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if err := work.startDial(ctx); err != nil {
			t.Fatal(err)
		}
		client, peer := net.Pipe()
		defer func() { _ = peer.Close() }()
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		socket := blockedLinkSocket{Conn: client, closing: make(chan struct{}), release: release}
		tracked := work.trackSocket(ctx, socket)
		go func() { _ = tracked.Close() }()
		<-socket.closing

		next := make(chan error, 1)
		go func() { next <- work.startDial(ctx) }()
		synctest.Wait()
		select {
		case err := <-next:
			t.Fatalf("dial overlapped socket cleanup: %v", err)
		default:
		}
		cancel()
		work.endCheck()
		if err := <-next; !errors.Is(err, context.Canceled) {
			t.Errorf("queued dial error=%v, want cancellation", err)
		}
		if err := work.startDial(t.Context()); !errors.Is(err, context.Canceled) {
			t.Errorf("late dial error=%v, want cancellation", err)
		}
		select {
		case <-finished:
			t.Fatal("check released its permit before the socket closed")
		default:
		}
		unblock()
		synctest.Wait()
		select {
		case <-finished:
		default:
			t.Fatal("socket cleanup did not release the permit")
		}
	})
}

type observedLinkResolverConn struct {
	net.Conn
	reads *atomic.Int32
}

func (c observedLinkResolverConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	defer c.reads.Add(-1)
	return c.Conn.Read(p)
}

func TestResourceLinkCanceledHostnameRetainsPermitUntilResolutionReturns(t *testing.T) {
	// Initialize the process-wide DNS configuration outside synthetic time.
	noReply := errors.New("no DNS fixture reply")
	warm := &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, noReply
	}}
	_, _ = warm.LookupIPAddr(t.Context(), "warmup.example.")
	synctest.Test(t, func(t *testing.T) {
		first, second := make(chan struct{}), make(chan struct{})
		releaseFirst := sync.OnceFunc(func() { close(first) })
		releaseSecond := sync.OnceFunc(func() { close(second) })
		var reads, queries atomic.Int32
		original := net.DefaultResolver
		defer func() {
			releaseFirst()
			releaseSecond()
			synctest.Wait()
			net.DefaultResolver = original
		}()
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			release := first
			switch queries.Add(1) {
			case 1:
			case 2:
				release = second
			default:
				return nil, noReply
			}
			client, peer := net.Pipe()
			go func() {
				defer func() { _ = peer.Close() }()
				var query [4096]byte
				if _, err := peer.Read(query[:]); err == nil {
					<-release
				}
			}()
			return observedLinkResolverConn{Conn: client, reads: &reads}, nil
		}}
		checker := NewResourceLinkChecker(time.Minute, nil, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan string, 1)
		go func() { result <- checker.Check(ctx, "http://accounting.example./guide") }()
		synctest.Wait()
		pending := reads.Load()
		if pending == 0 {
			t.Fatal("fixture did not reach a DNS reply read")
		}
		t.Logf("DNS reply reads before cancellation: %d", pending)
		cancel()
		synctest.Wait()
		select {
		case got := <-result:
			if got != "timeout" {
				t.Errorf("canceled hostname check=%q, want timeout", got)
			}
		default:
			t.Error("caller waited for DNS cleanup after cancellation")
		}
		assertOccupied := func() {
			t.Helper()
			pending := reads.Load()
			if pending == 0 {
				t.Fatal("fixture has no outstanding DNS reply read")
			}
			if checker.permits.TryAcquire(8) {
				checker.permits.Release(8)
				t.Errorf("all eight permits available with %d DNS reply reads still running", pending)
			}
			if !checker.permits.TryAcquire(7) {
				t.Error("hostname check should occupy one permit")
			} else {
				checker.permits.Release(7)
			}
		}
		assertOccupied()
		releaseFirst()
		synctest.Wait()
		// With serial queries, the second read can only start after the first ends.
		if reads.Load() > 0 {
			assertOccupied()
		}
		releaseSecond()
		synctest.Wait()
		if got := reads.Load(); got != 0 {
			t.Errorf("resolver has %d reply reads left after cleanup", got)
		}
		if !checker.permits.TryAcquire(8) {
			t.Fatal("hostname check leaked its permit after DNS returned")
		}
		checker.permits.Release(8)
	})
}

func TestResourceLinkFallbackSuccessAfterDeadlineIsDropped(t *testing.T) {
	recorder := metrics.New()
	synctest.Test(t, func(t *testing.T) {
		checker := NewResourceLinkChecker(time.Second, nil, recorder)
		var methods []string
		checker.client.Transport = resourceLinkRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			methods = append(methods, req.Method)
			if req.Method == http.MethodHead {
				return linkResponse(req, http.StatusMethodNotAllowed), nil
			}
			<-req.Context().Done()
			if err := req.Context().Err(); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("fallback context error=%v, want deadline exceeded", err)
			}
			return linkResponse(req, http.StatusPartialContent), nil
		})
		plan, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		const link = "https://links.example/guide"
		items := []domain.PlanItem{{Resources: []domain.Resource{{URL: link}, {URL: link}}}}
		start := time.Now()
		got, dropped := checker.filterResources(plan, items)
		if len(got) != 1 || len(got[0].Resources) != 0 || dropped != 2 {
			t.Errorf("fallback kept expired resources, got %v with %d drops", got, dropped)
		}
		if elapsed := time.Since(start); elapsed != time.Second || plan.Err() != nil || !slices.Equal(methods, []string{http.MethodHead, http.MethodGet}) {
			t.Errorf("fallback elapsed=%v, plan error=%v, methods=%v", elapsed, plan.Err(), methods)
		}
		if !checker.permits.TryAcquire(8) {
			t.Fatal("expired fallback retained a permit")
		}
		checker.permits.Release(8)
	})
	scrape := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(scrape, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	lines := strings.Split(scrape.Body.String(), "\n")
	if !slices.Contains(lines, `axiom_resource_links_checked_total{result="timeout"} 1`) || !slices.Contains(lines, `axiom_resource_links_checked_total{result="ok"} 0`) {
		t.Error("expected one timeout and zero successes for the expired fallback")
	}
}

func TestResourceLinkHTTPStatusAfterElapsedDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		checker := NewResourceLinkChecker(5*time.Second, nil, nil)
		transport := checker.client.Transport.(*http.Transport)
		transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
			client, peer := net.Pipe()
			go func() {
				defer func() { _ = peer.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(peer))
				if err != nil {
					t.Errorf("reading link request: %v", err)
					return
				}
				_ = req.Body.Close()
				time.Sleep(time.Second + time.Millisecond)
				if _, err := io.WriteString(peer, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err != nil {
					t.Errorf("writing link response: %v", err)
				}
			}()
			return client, nil
		}
		ctx := linkDeadlineContext{Context: t.Context(), deadline: time.Now().Add(time.Second)}
		if got := checker.check(ctx, "http://links.example/guide"); got != "timeout" {
			t.Errorf("HTTP success after the link deadline gave %q, want timeout", got)
		}
	})
}
