package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// clientIP resolves the caller's address.
//
// X-Forwarded-For is honoured only when the immediate peer is a configured
// trusted proxy. auth2api reads req.ip without ever setting Express's
// "trust proxy", so behind any ingress every client collapses into one
// rate-limit bucket; requiring an explicit trust list avoids both that and
// the opposite failure, where a client spoofs the header to dodge limits.
func clientIP(r *http.Request, trusted []*net.IPNet) string {
	peer := peerIP(r)
	if len(trusted) == 0 || peer == nil || !ipInAny(peer, trusted) {
		if peer == nil {
			return "unknown"
		}
		return peer.String()
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peer.String()
	}
	// Right-most entry that is not itself a trusted proxy is the real client.
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			continue
		}
		if !ipInAny(ip, trusted) {
			return ip.String()
		}
	}
	return peer.String()
}

func peerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func ipInAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxies accepts CIDRs and bare IPs.
func parseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(e)
		if ip == nil {
			return nil, &net.ParseError{Type: "trusted proxy CIDR or IP", Text: e}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
}
