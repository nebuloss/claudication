package httpapi

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustTrust(t *testing.T, entries ...string) []*net.IPNet {
	t.Helper()
	nets, err := parseTrustedProxies(entries)
	if err != nil {
		t.Fatalf("parseTrustedProxies(%v): %v", entries, err)
	}
	return nets
}

// The two failures this exists to sit between: trusting a header nobody
// verified, so a client spoofs its way out of a rate limit, and trusting
// nothing, so every client behind an ingress shares one bucket.
func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peer    string
		xff     string
		trusted []string
		want    string
	}{
		{
			name: "no proxy configured, header ignored",
			peer: "203.0.113.9:5000", xff: "1.2.3.4",
			want: "203.0.113.9",
		},
		{
			// The attack the trust list exists to stop: an untrusted caller
			// naming whichever address it likes to dodge a per-IP limit.
			name: "untrusted peer cannot spoof",
			peer: "203.0.113.9:5000", xff: "9.9.9.9",
			trusted: []string{"10.0.50.1"},
			want:    "203.0.113.9",
		},
		{
			name: "trusted proxy, real client honoured",
			peer: "10.0.50.1:44100", xff: "198.51.100.7",
			trusted: []string{"10.0.50.1"},
			want:    "198.51.100.7",
		},
		{
			name: "trusted proxy by CIDR",
			peer: "10.0.50.1:44100", xff: "198.51.100.7",
			trusted: []string{"10.0.50.0/24"},
			want:    "198.51.100.7",
		},
		{
			// Two proxies in the path: the right-most entry that is not itself
			// a proxy is the client. Taking the left-most would let the client
			// prepend anything it liked.
			name: "chain of proxies",
			peer: "10.0.50.1:44100", xff: "198.51.100.7, 10.0.50.2",
			trusted: []string{"10.0.50.0/24"},
			want:    "198.51.100.7",
		},
		{
			// And a client that prepends a lie in front of its real address
			// still gets attributed to its real address.
			name: "client prepends a forged hop",
			peer: "10.0.50.1:44100", xff: "9.9.9.9, 198.51.100.7",
			trusted: []string{"10.0.50.0/24"},
			want:    "198.51.100.7",
		},
		{
			name: "trusted proxy sending no header",
			peer: "10.0.50.1:44100", xff: "",
			trusted: []string{"10.0.50.1"},
			want:    "10.0.50.1",
		},
		{
			name: "rubbish in the header falls back to the peer",
			peer: "10.0.50.1:44100", xff: "not-an-ip",
			trusted: []string{"10.0.50.1"},
			want:    "10.0.50.1",
		},
		{
			name: "IPv6 proxy and client",
			peer: "[2001:db8::1]:44100", xff: "2606:4700::1111",
			trusted: []string{"2001:db8::/32"},
			want:    "2606:4700::1111",
		},
		{
			// An address inside the trusted range is another hop, not the
			// client — which is the whole point of walking right to left.
			name: "IPv6 client inside the trusted range is treated as a proxy",
			peer: "[2001:db8::1]:44100", xff: "2001:db8:ffff::9",
			trusted: []string{"2001:db8::/32"},
			want:    "2001:db8::1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.peer
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(r, mustTrust(t, tc.trusted...)); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	if _, err := parseTrustedProxies([]string{"10.0.50.1", "10.0.0.0/8", "2001:db8::/32", "  "}); err != nil {
		t.Errorf("valid entries rejected: %v", err)
	}
	if _, err := parseTrustedProxies([]string{"not-an-address"}); err == nil {
		t.Error("an unparseable entry was accepted; a typo here silently disables the trust list")
	}
}

// Behind a proxy every request arrives from the same peer, so without a trust
// list the whole world shares one anonymous bucket: one scanner exhausting it
// locks the operator out of sign-in. With the proxy trusted, the budget is
// per real client again.
func TestTheAnonymousBudgetIsPerClientBehindAProxy(t *testing.T) {
	proxy := "10.0.50.1:44100"
	clients := []string{"198.51.100.7", "198.51.100.8"}

	t.Run("without a trust list they share one bucket", func(t *testing.T) {
		seen := map[string]bool{}
		for _, c := range clients {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = proxy
			r.Header.Set("X-Forwarded-For", c)
			seen[clientIP(r, nil)] = true
		}
		if len(seen) != 1 {
			t.Errorf("resolved %d distinct addresses, want 1", len(seen))
		}
	})

	t.Run("with the proxy trusted they get their own", func(t *testing.T) {
		trusted := mustTrust(t, "10.0.50.1")
		seen := map[string]bool{}
		for _, c := range clients {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = proxy
			r.Header.Set("X-Forwarded-For", c)
			seen[clientIP(r, trusted)] = true
		}
		if len(seen) != len(clients) {
			t.Errorf("resolved %d distinct addresses, want %d", len(seen), len(clients))
		}

		// And the budget actually follows the distinction.
		l := newLimiter()
		for range 3 {
			l.allowPerMinute("198.51.100.7", 3)
		}
		if l.allowPerMinute("198.51.100.7", 3) {
			t.Error("the noisy client was not throttled")
		}
		if !l.allowPerMinute("198.51.100.8", 3) {
			t.Error("a quiet client was throttled by its neighbour's traffic")
		}
	})
}

// The session cookie must be Secure exactly when the browser's connection was
// encrypted. Fixed false leaks it over a TLS-terminating proxy; fixed true
// means it is never sent on a LAN or through an SSH tunnel, and sign-in fails
// with nothing on screen to explain why.
func TestOverTLS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peer    string
		proto   string
		trusted []string
		want    bool
	}{
		{name: "plain HTTP, no proxy", peer: "10.0.50.20:5000", want: false},
		{
			name: "proxy says https and is trusted",
			peer: "10.0.50.1:44100", proto: "https",
			trusted: []string{"10.0.50.1"}, want: true,
		},
		{
			name: "proxy says http",
			peer: "10.0.50.1:44100", proto: "http",
			trusted: []string{"10.0.50.1"}, want: false,
		},
		{
			// Otherwise any client could pin a Secure cookie onto a plain
			// session and lock itself out.
			name: "untrusted client claiming https is ignored",
			peer: "203.0.113.9:5000", proto: "https",
			trusted: []string{"10.0.50.1"}, want: false,
		},
		{
			name: "no trust list, header ignored",
			peer: "10.0.50.1:44100", proto: "https",
			want: false,
		},
		{
			name: "chain, client-facing hop wins",
			peer: "10.0.50.1:44100", proto: "https, http",
			trusted: []string{"10.0.50.1"}, want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.peer
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			if got := overTLS(r, mustTrust(t, tc.trusted...)); got != tc.want {
				t.Errorf("overTLS = %v, want %v", got, tc.want)
			}
		})
	}
}
