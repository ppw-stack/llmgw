package server

import (
	"net/http"
	"net/netip"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/clientip"
)

// metricsHandler exposes Prometheus metrics.
//
// With server.metrics_token set, callers must present it as a Bearer token or
// a ?token= query parameter. Without a token the endpoint is readable only
// from the gateway host itself or from a trusted proxy, so exposing the
// process by accident does not publish traffic volumes to the internet.
func (s *Server) metricsHandler() http.Handler {
	next := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.metricsToken != "" {
			token := bearerToken(r)
			if token == "" {
				token = r.URL.Query().Get("token")
			}
			if !auth.EqualToken(token, s.metricsToken) {
				writeAdminError(w, http.StatusUnauthorized, "invalid metrics token")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !s.localOrTrusted(r) {
			writeAdminError(w, http.StatusForbidden,
				"metrics are not public; set server.metrics_token or scrape from localhost")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// localOrTrusted reports whether the request comes from the host itself or
// from a configured proxy.
func (s *Server) localOrTrusted(r *http.Request) bool {
	if peer, ok := clientip.PeerAddr(r); ok && (peer.IsLoopback() || prefixContains(s.trustedProxies, peer)) {
		return true
	}
	if addr, err := netip.ParseAddr(s.clientIP(r)); err == nil {
		return addr.IsLoopback() || prefixContains(s.trustedProxies, addr)
	}
	return false
}
