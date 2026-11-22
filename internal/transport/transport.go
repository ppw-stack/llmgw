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
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/observ"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/usage"
	"github.com/tidwall/sjson"
)

// Timeout bounds non-streaming upstream calls and the idle gap of a stream.
// It is fixed on purpose: a provider is just an API key, two base URLs and a
// model list.
const upstreamTimeout = config.DefaultTimeout

const maxUpstreamResponseDefault = 64 << 20

// variable so tests can lower the limit without allocating 64 MiB.
var maxUpstreamResponse int64 = maxUpstreamResponseDefault

// Forwarder turns a resolved target into an upstream call and streams the
// response back. It intentionally does not retry on 5xx, fall back to another
// provider, or translate between protocols.
type Forwarder struct {
	router  *router.Router
	store   *store.Store
	log     *slog.Logger
	client  *http.Client
	timeout time.Duration
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
	}
}

type Request struct {
	Protocol  string
	Meta      ingress.Meta
	Target    router.Target
	APIKeyID  int64
	RequestID string
}

func (f *Forwarder) Forward(w http.ResponseWriter, r *http.Request, in Request) {
	start := time.Now()
	observ.Inflight.Inc()
	defer observ.Inflight.Dec()

	body, err := sjson.SetBytes(in.Meta.RawBody, "model", in.Target.UpstreamModel)
	if err != nil {
		ingress.WriteError(w, in.Protocol, http.StatusBadRequest, "invalid_request_error", "cannot rewrite model field")
		return
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

	upstreamURL := upstreamURL(in.Target)

	// Non-streaming calls are bounded by the fixed timeout. Streaming calls use
	// ResponseHeaderTimeout plus an idle watchdog in streamSSE.
	ctx := r.Context()
	if !in.Meta.Stream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}

	var resp *http.Response
	var lastErr error

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
		if in.Target.Protocol == config.ProtocolAnthropic {
			req.Header.Set("anthropic-version", in.Target.Version)
			if in.Target.Key != "" {
				req.Header.Set("x-api-key", in.Target.Key)
			}
		} else if in.Target.Key != "" {
			req.Header.Set("Authorization", "Bearer "+in.Target.Key)
		}
		return f.client.Do(req)
	}

	resp, lastErr = send(body)
	if lastErr == nil && resp.StatusCode == http.StatusBadRequest && injected {
		// Some OpenAI-compatible upstreams reject stream_options. Retry once
		// without the injected flag and mark usage as estimated.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		resp.Body.Close()
		if bytes.Contains(raw, []byte("stream_options")) {
			body, _ = sjson.SetBytes(in.Meta.RawBody, "model", in.Target.UpstreamModel)
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
		if code != "client_canceled" {
			f.log.Warn("upstream request failed",
				"request_id", in.RequestID, "provider", in.Target.Provider,
				"endpoint", in.Target.Endpoint, "err", lastErr.Error(), "class", code)
			observ.UpstreamErrors.WithLabelValues(in.Target.Provider, code).Inc()
			ingress.WriteError(w, in.Protocol, status, errorType(status), upstreamMessage(status, code))
		}
		f.record(in, status, start, 0, usageAcc{estimated: true}, code)
		return
	}
	defer resp.Body.Close()

	acc := usageAcc{}
	streaming := in.Meta.Stream && resp.StatusCode >= 200 && resp.StatusCode < 300

	if !streaming {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponse+1))
		if err != nil {
			status, code := classifyError(err)
			f.log.Warn("read upstream body", "request_id", in.RequestID, "err", err.Error())
			ingress.WriteError(w, in.Protocol, status, errorType(status), "failed to read upstream response")
			f.record(in, status, start, 0, acc, code)
			return
		}
		if len(raw) > int(maxUpstreamResponse) {
			ingress.WriteError(w, in.Protocol, http.StatusBadGateway, "api_error",
				"upstream response exceeded 64 MiB")
			f.record(in, http.StatusBadGateway, start, 0, usageAcc{estimated: true}, "upstream_response_too_large")
			return
		}
		acc.observeBody(in.Protocol, raw)
		if !acc.seen && resp.StatusCode < 300 {
			acc.prompt = estimateTokens(len(body))
			acc.completion = estimateTokens(len(raw))
			acc.estimated = true
		}
		writeRaw(w, resp, raw, in.RequestID)
		f.record(in, resp.StatusCode, start, 0, acc, responseCode(resp.StatusCode))
		return
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Request-Id", in.RequestID)
	w.WriteHeader(resp.StatusCode)

	ttft, written, writeErr := streamSSE(w, resp.Body, in.Protocol, &acc, start, f.timeout)
	if writeErr != nil {
		f.log.Debug("stream interrupted", "request_id", in.RequestID, "err", writeErr.Error())
	}
	if !acc.seen {
		acc.prompt = estimateTokens(len(body))
		acc.completion = estimateTokens(int(written))
		acc.estimated = true
	}
	f.record(in, resp.StatusCode, start, ttft, acc, responseCode(resp.StatusCode))
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

func (f *Forwarder) record(in Request, status int, start time.Time, ttft time.Duration, acc usageAcc, errCode string) {
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
		Provider:         in.Target.Provider,
		Endpoint:         in.Target.Endpoint,
		UpstreamModel:    in.Target.UpstreamModel,
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
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Request-Id", requestID)
	w.WriteHeader(resp.StatusCode)
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
