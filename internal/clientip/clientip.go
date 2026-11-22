// Package clientip resolves the real client address behind a trusted proxy.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolve returns the client IP. X-Forwarded-For is only consulted when the
// immediate peer is a trusted proxy; otherwise the header could be spoofed.
func Resolve(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return host
	}
	if !isTrusted(addr, trusted) {
		return addr.String()
	}

	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		raw := strings.TrimSpace(parts[i])
		if raw == "" {
			continue
		}
		if a, err := netip.ParseAddr(raw); err == nil {
			if isTrusted(a, trusted) {
				continue
			}
			return a.String()
		}
		return raw
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return addr.String()
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
