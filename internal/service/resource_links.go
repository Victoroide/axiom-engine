package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/metrics"
)

var (
	errResourceLinkInvalid   = errors.New("resource link is not an allowed HTTP URL")
	errResourceLinkRefused   = errors.New("resource link connection refused")
	errResourceLinkRedirects = errors.New("resource link exceeds three redirects")
)

// ResourceLinkPolicy validates destinations before requests and after name resolution.
type ResourceLinkPolicy interface {
	ValidateURL(*url.URL) error
	ValidateAddress(netip.Addr) error
}

type publicResourceLinkPolicy struct{}

func (publicResourceLinkPolicy) ValidateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return errResourceLinkInvalid
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return errResourceLinkInvalid
	}
	return nil
}

var nonPublicResourcePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func (publicResourceLinkPolicy) ValidateAddress(ip netip.Addr) error {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
		return errResourceLinkRefused
	}
	// Restrict IPv6 to global unicast; translation prefixes can embed private IPv4.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return errResourceLinkRefused
	}
	for _, prefix := range nonPublicResourcePrefixes {
		if prefix.Contains(ip) {
			return errResourceLinkRefused
		}
	}
	return nil
}

type resourceLinkRedirectKey struct{}

type resourceLinkRedirects struct{ count int }

// ResourceLinkChecker checks bounded HTTP responses without cookies or credentials.
type ResourceLinkChecker struct {
	client  *http.Client
	timeout time.Duration
	policy  ResourceLinkPolicy
	metrics *metrics.Metrics
}

// NewResourceLinkChecker uses the public-only destination policy when policy is nil.
func NewResourceLinkChecker(timeout time.Duration, policy ResourceLinkPolicy, recorder *metrics.Metrics) *ResourceLinkChecker {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if policy == nil {
		policy = publicResourceLinkPolicy{}
	}
	dialer := &net.Dialer{Timeout: timeout, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return errResourceLinkRefused
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return errResourceLinkRefused
		}
		// The address here is the resolved address actually used for this connection.
		return policy.ValidateAddress(ip)
	}}
	checker := &ResourceLinkChecker{timeout: timeout, policy: policy, metrics: recorder}
	checker.client = &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := policy.ValidateURL(req.URL); err != nil {
				return err
			}
			redirects, _ := req.Context().Value(resourceLinkRedirectKey{}).(*resourceLinkRedirects)
			if len(via) > 3 || redirects != nil && redirects.count >= 3 {
				return errResourceLinkRedirects
			}
			if redirects != nil {
				redirects.count++
			}
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			req.Header.Del("Proxy-Authorization")
			return nil
		},
	}
	return checker
}

// Check returns a bounded result label for one link and records its outcome.
func (c *ResourceLinkChecker) Check(ctx context.Context, rawURL string) (result string) {
	defer func() { c.metrics.ObserveResourceLink(result) }()
	u, err := url.Parse(rawURL)
	if err != nil || c.policy.ValidateURL(u) != nil {
		return "invalid"
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ctx = context.WithValue(ctx, resourceLinkRedirectKey{}, &resourceLinkRedirects{})
	status, err := c.request(ctx, http.MethodHead, u.String())
	if err == nil && (status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented) {
		status, err = c.request(ctx, http.MethodGet, u.String())
	}
	if err != nil {
		var networkError net.Error
		switch {
		case errors.Is(err, errResourceLinkInvalid):
			return "invalid"
		case errors.Is(err, errResourceLinkRedirects):
			return "status"
		case ctx.Err() != nil || errors.As(err, &networkError) && networkError.Timeout():
			return "timeout"
		default:
			return "refused"
		}
	}
	if status >= 200 && status < 300 {
		return "ok"
	}
	return "status"
}

func (c *ResourceLinkChecker) request(ctx context.Context, method, rawURL string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return 0, errResourceLinkInvalid
	}
	req.Header.Set("User-Agent", "axiom-engine")
	if method == http.MethodGet {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func (c *ResourceLinkChecker) filterResources(ctx context.Context, items []domain.PlanItem) ([]domain.PlanItem, int) {
	type resourceIndex struct{ item, resource int }
	var resources []resourceIndex
	for i, item := range items {
		for j := range item.Resources {
			resources = append(resources, resourceIndex{i, j})
		}
	}
	if len(resources) == 0 {
		return items, 0
	}
	passed := make([]bool, len(resources))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(8, len(resources)) {
		workers.Go(func() {
			for i := range jobs {
				index := resources[i]
				passed[i] = c.Check(ctx, items[index.item].Resources[index.resource].URL) == "ok"
			}
		})
	}
	for i := range resources {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	out := make([]domain.PlanItem, len(items))
	index, dropped := 0, 0
	for i, item := range items {
		kept := make([]domain.Resource, 0, len(item.Resources))
		for _, resource := range item.Resources {
			if passed[index] {
				kept = append(kept, resource)
			} else {
				dropped++
			}
			index++
		}
		item.Resources = kept
		out[i] = item
	}
	return out, dropped
}
