package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/pw/llmgw/internal/auth"
)

// metricsHandler exposes Prometheus metrics.
//
// The endpoint is switched off unless server.metrics_token is configured:
// an empty token means there is no credential to check, and serving the
// traffic volumes to whoever asks is not an option. With a token set,
// callers must present it as a Bearer token or a ?token= query parameter.
func (s *Server) metricsHandler() http.Handler {
	next := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.metricsTokenValue()
		if token == "" {
			writeAdminError(w, http.StatusNotFound,
				"the metrics endpoint is disabled; set server.metrics_token to enable it")
			return
		}
		presented := bearerToken(r)
		if presented == "" {
			presented = r.URL.Query().Get("token")
		}
		if !auth.EqualToken(presented, token) {
			writeAdminError(w, http.StatusUnauthorized, "invalid metrics token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
