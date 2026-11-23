package transport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/health"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/observ"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/usage"
	"github.com/tidwall/sjson"
)

// upstreamTimeout is the fallback bound for non-streaming upstream calls and
// for the idle gap of a stream. An instance may lower it.
const upstreamTimeout = config.DefaultTimeout

const maxUpstreamResponseDefault = 64 << 20

// variable so tests can lower the limit without allocating 64 MiB.
var maxUpstreamResponse int64 = maxUpstreamResponseDefault

// Forwarder turns a resolved target into an upstream call and streams the
// response back. It does not translate between protocols. When a model has
// several candidates it tries them in priority order and moves on to the next
// one when the current upstream fails in a retryable way.
type Forwarder struct {
	router  *router.Router
	store   *store.Store
	log     *slog.Logger
	client  *http.Client
	timeout time.Duration
	breaker *health.Breaker
}

// New builds a Forwarder with the default upstream timeout.
func New(r *router.Router, st *store.Store, log *slog.Logger) *Forwarder {
	return NewWithTimeout(r, st, log, upstreamTimeout)
}

// NewWithTimeout builds a Forwarder with an explicit upstream timeout. Tests
// use it to shorten the timeout without touching package-level state.
func NewWithTimeout(r *router.Router, st *store.Store, log *slog.Logger, timeout time.Duration) *Forwarder {
	if timeout <= 0 {
		timeout = upstreamTimeout
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: timeout,
	}
	return &Forwarder{
		router:  r,
		store:   st,
		log:     log,
		client:  &http.Client{Transport: tr},
		timeout: timeout,
		breaker: health.NewBreaker(),
	}
}

// Request is one inbound request together with the candidates that may serve
// it. The first candidate is the primary upstream, the rest are fallbacks.
type Request struct {
	Protocol  string
	Meta      ingress.Meta
	Targets   []router.Target
	APIKeyID  int64
	RequestID string
}

// attemptOutcome describes what one upstream attempt did.
type attemptOutcome struct {
	// handled is true when the attempt already wrote the response.
	handled bool
	// retryable is true when the next candidate may be tried.
	retryable bool
	status    int
	code      string
	body      []byte
	header    http.Header
}

// shouldFailoverStatus reports whether an upstream status means the request
// may be retried on the next candidate. Client errors are never retried: they
// describe the request, not the upstream.
func shouldFailoverStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// Forward sends the request to the first candidate and walks down the
// candidate list while the upstream fails in a retryable way.
func (f *Forwarder) Forward(w http.ResponseWriter, r *http.Request, in Request) {
	start := time.Now()
	observ.Inflight.Inc()
	defer observ.Inflight.Dec()

	if len(in.Targets) == 0 {
		ingress.WriteError(w, in.Protocol, http.StatusNotFound, "not_found_error",
			"no upstream instance serves this model")
		f.record(in, router.Target{}, http.StatusNotFound, start, 0, usageAcc{estimated: true}, "no_upstream")
		return
	}

	var last attemptOutcome
	for i := range in.Targets {
		target := in.Targets[i]
		if !f.breaker.Allow(target.Instance) {
			f.log.Warn("skip upstream instance in cooldown",
				"request_id", in.RequestID, "instance", target.Instance)
			continue
		}
		outcome := f.attempt(w, r, in, target, start, i < len(in.Targets)-1)
		if outcome.handled {
			return
		}
		last = outcome
		if !outcome.retryable {
			return
		}
		f.log.Warn("upstream attempt failed, trying the next candidate",
			"request_id", in.RequestID, "instance", target.Instance,
			"status", outcome.status, "class", outcome.code)
	}

	if last.body != nil {
		writeRawParts(w, last.header, last.status, last.body, in.RequestID)
		return
	}
	if last.status == 0 {
		// Every candidate was skipped by the breaker.
		ingress.WriteError(w, in.Protocol, http.StatusServiceUnavailable, "overloaded_error",
			"no upstream instance is currently available")
		f.record(in, in.Targets[0], http.StatusServiceUnavailable, start, 0,
			usageAcc{estimated: true}, "upstream_unavailable")
		return
	}
	ingress.WriteError(w, in.Protocol, last.status, errorType(last.status), upstreamMessage(last.status, last.code))
}

// attempt performs one upstream call. It writes to w only when the outcome is
// final: either a success, or a failure that must not be retried.
func (f *Forwarder) attempt(w http.ResponseWriter, r *http.Request, in Request, target router.Target,
	start time.Time, hasFallback bool) attemptOutcome {

	timeout := target.Timeout
	if timeout <= 0 {
		timeout = f.timeout
	}
	ctx := r.Context()
	if !in.Meta.Stream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	body, err := sjson.SetBytes(in.Meta.RawBody, "model", target.UpstreamModel)
	if err != nil {
		ingress.WriteError(w, in.Protocol, http.StatusBadRequest, "invalid_request_error", "cannot rewrite model field")
		return attemptOutcome{handled: true}
	}

	// OpenAI streaming does not report usage unless asked to. Inject the flag,
	// and remember so a rejecting upstream can be retried without it.
	injected := false
	if in.Protocol == config.ProtocolOpenAI && in.Meta.Stream {
		if b, err := sjson.SetBytes(body, "stream_options.include_usage", true); err == nil {
			body = b
			injected = true
		}
	}

	upstreamURL := upstreamURL(target)

	send := func(payload []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		copyRequestHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Del("Authorization")
		req.Header.Del("X-Api-Key")
		req.Header.Del("Anthropic-Version")
		if target.Protocol == config.ProtocolAnthropic {
			req.Header.Set("anthropic-version", target.Version)
			if target.Key != "" {
				req.Header.Set("x-api-key", target.Key)
			}
		} else if target.Key != "" {
			req.Header.Set("Authorization", "Bearer "+target.Key)
		}
		return f.client.Do(req)
	}

	resp, lastErr := send(body)
	if lastErr == nil && resp.StatusCode == http.StatusBadRequest && injected {
		// Some OpenAI-compatible upstreams reject stream_options. Retry once
		// without the injected flag and mark usage as estimated.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		resp.Body.Close()
		if bytes.Contains(raw, []byte("stream_options")) {
			body, _ = sjson.SetBytes(in.Meta.RawBody, "model", target.UpstreamModel)
			injected = false
			resp, lastErr = send(body)
		} else {
			resp = &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(raw)),
			}
		}
	}

	if lastErr != nil {
		status, code := classifyError(lastErr)
		if code == "client_canceled" {
			// The client is gone; another upstream would not help.
			f.record(in, target, status, start, 0, usageAcc{estimated: true}, code)
			return attemptOutcome{handled: true}
		}
		f.log.Warn("upstream request failed",
			"request_id", in.RequestID, "instance", target.Instance,
			"endpoint", target.Endpoint, "err", lastErr.Error(), "class", code)
		observ.UpstreamErrors.WithLabelValues(target.Instance, code).Inc()
		f.record(in, target, status, start, 0, usageAcc{estimated: true}, code)
		f.breaker.RecordFailure(target.Instance, target.MaxFails, target.Cooldown)
		return attemptOutcome{retryable: true, status: status, code: code}
	}
	defer resp.Body.Close()

	// A retryable status while a fallback is still available: keep the
	// upstream answer aside and let the caller try the next candidate.
	if hasFallback && shouldFailoverStatus(resp.StatusCode) {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponse+1))
		code := responseCode(resp.StatusCode)
		f.record(in, target, resp.StatusCode, start, 0, usageAcc{estimated: true}, code)
		f.breaker.RecordFailure(target.Instance, target.MaxFails, target.Cooldown)
		return attemptOutcome{
			retryable: true,
			status:    resp.StatusCode,
			code:      code,
			body:      raw,
			header:    resp.Header,
		}
	}

	acc := usageAcc{}
	streaming := in.Meta.Stream && resp.StatusCode >= 200 && resp.StatusCode < 300

	if !streaming {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponse+1))
		if err != nil {
			status, code := classifyError(err)
			f.log.Warn("read upstream body", "request_id", in.RequestID, "err", err.Error())
			ingress.WriteError(w, in.Protocol, status, errorType(status), "failed to read upstream response")
			f.record(in, target, status, start, 0, acc, code)
			return attemptOutcome{handled: true}
		}
		if len(raw) > int(maxUpstreamResponse) {
			ingress.WriteError(w, in.Protocol, http.StatusBadGateway, "api_error",
				"upstream response exceeded 64 MiB")
			f.record(in, target, http.StatusBadGateway, start, 0, usageAcc{estimated: true}, "upstream_response_too_large")
			return attemptOutcome{handled: true}
		}
		acc.observeBody(in.Protocol, raw)
		if !acc.seen && resp.StatusCode < 300 {
			acc.prompt = estimateTokens(len(body))
			acc.completion = estimateTokens(len(raw))
			acc.estimated = true
		}
		writeRaw(w, resp, raw, in.RequestID)
		f.record(in, target, resp.StatusCode, start, 0, acc, responseCode(resp.StatusCode))
		if resp.StatusCode < 300 {
			f.breaker.RecordSuccess(target.Instance)
		} else if shouldFailoverStatus(resp.StatusCode) {
			// The last candidate failed as well: count it so the instance
			// is skipped for a while instead of failing every request.
			f.breaker.RecordFailure(target.Instance, target.MaxFails, target.Cooldown)
		}
		return attemptOutcome{handled: true}
	}

	// From here on the response is committed to the client: switching to
	// another upstream would deliver two streams to the same client.
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Request-Id", in.RequestID)
	w.WriteHeader(resp.StatusCode)

	ttft, written, writeErr := streamSSE(w, resp.Body, in.Protocol, &acc, start, timeout)
	if writeErr != nil {
		f.log.Debug("stream interrupted", "request_id", in.RequestID, "err", writeErr.Error())
	}
	if !acc.seen {
		acc.prompt = estimateTokens(len(body))
		acc.completion = estimateTokens(int(written))
		acc.estimated = true
	}
	f.record(in, target, resp.StatusCode, start, ttft, acc, responseCode(resp.StatusCode))
	f.breaker.RecordSuccess(target.Instance)
	return attemptOutcome{handled: true}
}

// streamSSE forwards upstream bytes untouched while observing usage events.
func streamSSE(w http.ResponseWriter, body io.ReadCloser, protocol string, acc *usageAcc, start time.Time, idle time.Duration) (time.Duration, int64, error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32<<10)
	var ttft time.Duration
	var written int64
	wrote := false

	// If the upstream stops sending bytes, close the body so the read returns
	// instead of holding the goroutine forever.
	var idleTimer *time.Timer
	if idle > 0 {
		idleTimer = time.AfterFunc(idle, func() { _ = body.Close() })
		defer idleTimer.Stop()
	}

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if idleTimer != nil {
				idleTimer.Reset(idle)
			}
			acc.observeLine(protocol, line)
			n, werr := w.Write(line)
			written += int64(n)
			if werr != nil {
				return ttft, written, werr
			}
			if !wrote && isDataLine(line) {
				wrote = true
				ttft = time.Since(start)
			}
			if flusher != nil && len(bytes.TrimSpace(line)) == 0 {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if flusher != nil {
					flusher.Flush()
				}
				return ttft, written, nil
			}
			return ttft, written, err
		}
	}
}

// estimateTokens is a deliberately rough fallback when the upstream does not
// report usage. Four bytes per token is the usual English approximation.
func estimateTokens(bytes int) int64 {
	if bytes <= 0 {
		return 0
	}
	return int64((bytes + 3) / 4)
}

func isDataLine(line []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:"))
}

func (f *Forwarder) record(in Request, target router.Target, status int, start time.Time, ttft time.Duration, acc usageAcc, errCode string) {
	cost := 0.0
	if snap := f.router.Snapshot(); snap != nil {
		cost = usage.Cost(snap.Config.Pricing, in.Meta.Model, usage.Usage{
			PromptTokens:     acc.prompt,
			CompletionTokens: acc.completion,
			CacheReadTokens:  acc.cacheRead,
			CacheWriteTokens: acc.cacheWrite,
			Estimated:        acc.estimated,
		})
	}
	latency := time.Since(start)
	f.store.Enqueue(store.LogEntry{
		TS:               start.UTC(),
		RequestID:        in.RequestID,
		APIKeyID:         in.APIKeyID,
		IngressProtocol:  in.Protocol,
		Model:            in.Meta.Model,
		Provider:         target.Instance,
		Endpoint:         target.Endpoint,
		UpstreamModel:    target.UpstreamModel,
		Status:           status,
		LatencyMS:        latency.Milliseconds(),
		TTFTMS:           ttft.Milliseconds(),
		PromptTokens:     acc.prompt,
		CompletionTokens: acc.completion,
		CacheReadTokens:  acc.cacheRead,
		CacheWriteTokens: acc.cacheWrite,
		CostUSD:          cost,
		UsageEstimated:   acc.estimated,
		ErrorCode:        errCode,
	})
	observ.RequestsTotal.WithLabelValues(in.Protocol, strconv.Itoa(status)).Inc()
	observ.RequestDuration.WithLabelValues(in.Protocol).Observe(latency.Seconds())
	if ttft > 0 {
		observ.TTFT.WithLabelValues(in.Protocol).Observe(ttft.Seconds())
	}
	if acc.prompt > 0 {
		observ.TokensTotal.WithLabelValues(in.Protocol, "prompt").Add(float64(acc.prompt))
	}
	if acc.completion > 0 {
		observ.TokensTotal.WithLabelValues(in.Protocol, "completion").Add(float64(acc.completion))
	}
}

func upstreamURL(t router.Target) string {
	switch t.Protocol {
	case config.ProtocolAnthropic:
		return t.BaseURL + "/v1/messages"
	default:
		return t.BaseURL + "/chat/completions"
	}
}

func writeRaw(w http.ResponseWriter, resp *http.Response, body []byte, requestID string) {
	writeRawParts(w, resp.Header, resp.StatusCode, body, requestID)
}

// writeRawParts forwards an upstream answer to the client untouched.
func writeRawParts(w http.ResponseWriter, header http.Header, status int, body []byte, requestID string) {
	copyResponseHeaders(w.Header(), header)
	w.Header().Set("X-Request-Id", requestID)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

var hopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		// Accept-Encoding is dropped so the transport negotiates gzip itself
		// and hands us a decoded body we can inspect for usage.
		if hopHeaders[k] || k == "Content-Length" || k == "Host" || k == "Accept-Encoding" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopHeaders[k] || k == "Content-Length" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func classifyError(err error) (int, string) {
	if errors.Is(err, context.Canceled) {
		return 499, "client_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "upstream_timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return http.StatusGatewayTimeout, "upstream_timeout"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return http.StatusGatewayTimeout, "upstream_timeout"
	}
	return http.StatusBadGateway, "upstream_unreachable"
}

func errorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusServiceUnavailable:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func upstreamMessage(status int, code string) string {
	switch code {
	case "upstream_timeout":
		return "upstream request timed out"
	case "client_canceled":
		return "client canceled the request"
	default:
		if status == http.StatusBadGateway {
			return "upstream is unreachable"
		}
		return "upstream request failed"
	}
}

func responseCode(status int) string {
	if status >= 200 && status < 300 {
		return ""
	}
	return "upstream_" + strconv.Itoa(status)
}
