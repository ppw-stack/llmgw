package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/pw/llmgw/internal/auth"
)

// metricsHandler exposes Prometheus metrics. When server.metrics_token is set,
// callers must present it as a Bearer token or ?token= query parameter.
func (s *Server) metricsHandler() http.Handler {
	next := promhttp.Handler()
	if s.metricsToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if !auth.EqualToken(token, s.metricsToken) {
			writeAdminError(w, http.StatusUnauthorized, "invalid metrics token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
