// Package resourcelinkfixture provides a loopback link checker fixture for transport tests.
package resourcelinkfixture

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

var errOutsideFixture = errors.New("resource link destination is outside the fixture")
var planLatency = regexp.MustCompile(`("(?:llm|total)_latency_ms":)[0-9]+`)

// Fixture serves a missing resource and permits only its own test endpoint.
type Fixture struct {
	URL     string
	address netip.AddrPort
	hits    atomic.Int32
}

// New starts a local server and registers its cleanup with t.
func New(t testing.TB) *Fixture {
	t.Helper()
	f := &Fixture{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.Method != http.MethodHead || r.URL.Path != "/missing" {
			t.Errorf("fixture received %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(s.Close)
	f.URL = s.URL + "/missing"
	f.address = s.Listener.Addr().(*net.TCPAddr).AddrPort()
	return f
}

// Hits reports the number of requests received by the fixture.
func (f *Fixture) Hits() int32 { return f.hits.Load() }

// Resources returns two resources with the same URL and distinct presentation.
func (f *Fixture) Resources() []domain.Resource {
	return []domain.Resource{
		{Title: "First", URL: f.URL, ResourceType: "video"},
		{Title: "Second", URL: f.URL, ResourceType: "diagram"},
	}
}

// NormalizePlanLatency replaces values of the two plan latency fields with zero.
func NormalizePlanLatency(response string) string {
	return planLatency.ReplaceAllString(response, "${1}0")
}

// ValidateURL permits HTTP to the fixture's authority.
func (f *Fixture) ValidateURL(u *url.URL) error {
	if u.Scheme != "http" || u.Host != f.address.String() || u.User != nil {
		return errOutsideFixture
	}
	return nil
}

// ValidateAddress permits only the fixture's loopback address.
func (f *Fixture) ValidateAddress(ip netip.Addr) error {
	if ip != f.address.Addr() {
		return errOutsideFixture
	}
	return nil
}
