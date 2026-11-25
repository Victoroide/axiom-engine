package server

import (
	"net"
	"net/netip"
	"strings"
)

// proxyPolicy resolves the client IP like the HTTP API does. Without a trusted
// proxy list the direct peer is the client. A peer inside the list is a proxy,
// so the client is the nearest hop to its left that is not a trusted proxy.
type proxyPolicy struct{ trusted []*net.IPNet }

func newProxyPolicy(proxies []string) proxyPolicy {
	policy := proxyPolicy{trusted: make([]*net.IPNet, 0, len(proxies))}
	for _, proxy := range proxies {
		if _, network, err := net.ParseCIDR(proxy); err == nil {
			// Keep the mask in its original address family. IPNet.Contains
			// matches IPv4-mapped networks the same way as the HTTP API.
			policy.trusted = append(policy.trusted, network)
		} else if address, err := netip.ParseAddr(proxy); err == nil && address.Zone() == "" {
			address = address.Unmap()
			policy.trusted = append(policy.trusted, &net.IPNet{IP: address.AsSlice(), Mask: net.CIDRMask(address.BitLen(), address.BitLen())})
		}
	}
	return policy
}

func (p proxyPolicy) isTrusted(address netip.Addr) bool {
	ip := address.AsSlice()
	for _, network := range p.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the address to admit. remote is the connection's peer, and
// forwarded holds every X-Forwarded-For header value in wire order.
func (p proxyPolicy) clientIP(remote string, forwarded []string) string {
	peer, err := netip.ParseAddrPort(remote)
	if err != nil {
		return ""
	}
	direct := peer.Addr().Unmap().String()
	if len(p.trusted) == 0 || !p.isTrusted(peer.Addr()) {
		return direct
	}
	chain := strings.Join(forwarded, ",")
	leftmost := ""
	for end := len(chain); end > 0; {
		start := strings.LastIndexByte(chain[:end], ',')
		hop := strings.TrimSpace(chain[start+1 : end])
		end = max(start, 0)
		if hop == "" {
			continue
		}
		address, err := netip.ParseAddr(hop)
		if err != nil {
			// A hop such as "unknown" or "IP:port" names no trusted proxy, so
			// nothing to its left can be trusted either.
			break
		}
		leftmost = address.Unmap().String()
		if !p.isTrusted(address) {
			return leftmost
		}
	}
	if leftmost != "" {
		return leftmost
	}
	return direct
}
