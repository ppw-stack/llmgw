// Package observ defines the Prometheus metrics exposed by the gateway.
package observ

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_requests_total",
		Help: "Total requests handled, by protocol and status.",
	}, []string{"protocol", "status"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gw_request_duration_seconds",
		Help:    "End-to-end request duration.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 120},
	}, []string{"protocol"})

	TTFT = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gw_ttft_seconds",
		Help:    "Time to first token.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30},
	}, []string{"protocol"})

	TokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_tokens_total",
		Help: "Tokens consumed, by protocol and kind.",
	}, []string{"protocol", "kind"})

	// UpstreamErrors counts upstream failures, by access-point instance and
	// failure class.
	UpstreamErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_upstream_errors_total",
		Help: "Upstream failures, by instance and class.",
	}, []string{"instance", "class"})

	LogDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gw_log_dropped_total",
		Help: "Request log entries dropped because the queue was full.",
	})

	Inflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gw_inflight_requests",
		Help: "Requests currently being forwarded.",
	})
)
