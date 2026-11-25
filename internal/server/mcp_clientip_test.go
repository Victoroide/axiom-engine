package server

import "testing"

func TestProxyPolicyClientIP(t *testing.T) {
	trusted := newProxyPolicy([]string{"10.0.0.0/8", "192.0.2.7", "2001:db8::/32"})
	for _, tc := range []struct {
		name      string
		policy    proxyPolicy
		remote    string
		forwarded []string
		want      string
	}{
		{"no policy uses the peer", newProxyPolicy(nil), "198.51.100.4:5000", []string{"203.0.113.9"}, "198.51.100.4"},
		{"unparsable peer", trusted, "pipe", []string{"203.0.113.9"}, ""},
		{"untrusted peer ignores the header", trusted, "198.51.100.4:5000", []string{"203.0.113.9"}, "198.51.100.4"},
		{"trusted peer without a header", trusted, "10.1.2.3:5000", nil, "10.1.2.3"},
		{"trusted peer with a client", trusted, "10.1.2.3:5000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"nearest untrusted hop wins", trusted, "10.1.2.3:5000", []string{"198.51.100.1, 203.0.113.9, 10.9.9.9"}, "203.0.113.9"},
		{"spoofed leftmost entry is skipped", trusted, "10.1.2.3:5000", []string{"1.1.1.1", "203.0.113.9"}, "203.0.113.9"},
		{"all hops trusted", trusted, "10.1.2.3:5000", []string{"10.5.5.5, 10.6.6.6"}, "10.5.5.5"},
		{"exact address entry", trusted, "192.0.2.7:443", []string{"203.0.113.9"}, "203.0.113.9"},
		{"IPv6 proxy", trusted, "[2001:db8::5]:443", []string{"203.0.113.9"}, "203.0.113.9"},
		{"IPv4-mapped peer", trusted, "[::ffff:10.1.2.3]:5000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"mapped client is normalized", trusted, "10.1.2.3:5000", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"unparsable hop stops the walk", trusted, "10.1.2.3:5000", []string{"198.51.100.1, unknown, 10.9.9.9"}, "10.9.9.9"},
		{"hop with a port is not an address", trusted, "10.1.2.3:5000", []string{"203.0.113.9:80"}, "10.1.2.3"},
		{"empty elements are ignored", trusted, "10.1.2.3:5000", []string{"203.0.113.9, ,"}, "203.0.113.9"},
		{"empty header", trusted, "10.1.2.3:5000", []string{""}, "10.1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.clientIP(tc.remote, tc.forwarded); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
