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
	"sync"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/keypool"
	"github.com/pw/llmgw/internal/observ"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/usage"
	"github.com/tidwall/sjson"
)

// Forwarder turns a resolved target into an upstream call and streams the
// response back. It intentionally does not retry on 5xx, fall back to another
// provider, or translate between protocols.
type Forwarder struct {
	router *router.Router
	store  *store.Store
	keys   *keypool.Manager
	log    *slog.Logger

	mu      sync.Mutex
	clients map[string]*http.Client
}

func New(r *router.Router, st *store.Store, keys *keypool.Manager, log *slog.Logger) *Forwarder {
	return &Forwarder{
		router:  r,
		store:   st,
		keys:    keys,
		log:     log,
		clients: map[string]*http.Client{},
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

	pool := f.keys.Get(in.Target.Provider, in.Target.Keys)
	client := f.clientFor(in.Target)
	upstreamURL := upstreamURL(in.Target)

	var resp *http.Response
	var lastErr error

	lastKeyIndex := -1
	send := func(payload []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		copyRequestHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")

		keyIndex, key, ok := pool.Next()
		if !ok {
			return nil, errNoKey
		}
		lastKeyIndex = keyIndex
		req.Header.Del("Authorization")
		req.Header.Del("X-Api-Key")
		req.Header.Del("Anthropic-Version")
		if key != "" {
			switch in.Target.Protocol {
			case config.ProtocolAnthropic:
				req.Header.Set("x-api-key", key)
				req.Header.Set("anthropic-version", in.Target.Version)
			default:
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
		return client.Do(req)
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

	// A 401/403 from the upstream means the key is bad; burn it and retry once
	// with the next key in the pool.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		pool.Invalidate(lastKeyIndex)
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		resp.Body.Close()
		if pool.Available() > 0 {
			resp, lastErr = send(body)
			if lastErr != nil {
				status, code := classifyError(lastErr)
				ingress.WriteError(w, in.Protocol, status, errorType(status), upstreamMessage(status, code))
				f.record(in, status, start, 0, usageAcc{estimated: true}, code)
				return
			}
			defer resp.Body.Close()
		} else {
			status := resp.StatusCode
			writeRaw(w, resp, raw, in.RequestID)
			f.record(in, status, start, 0, usageAcc{}, "upstream_auth")
			return
		}
	}

	acc := usageAcc{}
	streaming := in.Meta.Stream && resp.StatusCode >= 200 && resp.StatusCode < 300

	if !streaming {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			status, code := classifyError(err)
			f.log.Warn("read upstream body", "request_id", in.RequestID, "err", err.Error())
			ingress.WriteError(w, in.Protocol, status, errorType(status), "failed to read upstream response")
			f.record(in, status, start, 0, acc, code)
			return
		}
		acc.observeBody(in.Protocol, raw)
		if !acc.seen && resp.StatusCode < 300 {
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

	ttft, writeErr := streamSSE(w, resp.Body, in.Protocol, &acc, start)
	if writeErr != nil {
		f.log.Debug("stream interrupted", "request_id", in.RequestID, "err", writeErr.Error())
	}
	if !acc.seen {
		acc.estimated = true
	}
	f.record(in, resp.StatusCode, start, ttft, acc, responseCode(resp.StatusCode))
}

// streamSSE forwards upstream bytes untouched while observing usage events.
func streamSSE(w http.ResponseWriter, body io.Reader, protocol string, acc *usageAcc, start time.Time) (time.Duration, error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32<<10)
	var ttft time.Duration
	wrote := false
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			acc.observeLine(protocol, line)
			if _, werr := w.Write(line); werr != nil {
				return ttft, werr
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
				return ttft, nil
			}
			return ttft, err
		}
	}
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

func (f *Forwarder) clientFor(t router.Target) *http.Client {
	key := t.Provider + "|" + t.Proxy
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[key]; ok {
		return c
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: t.Timeout,
	}
	if t.Proxy != "" {
		if u, err := url.Parse(t.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	c := &http.Client{Transport: tr}
	f.clients[key] = c
	return c
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
		if hopHeaders[k] || k == "Content-Length" || k == "Host" {
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

var errNoKey = errors.New("no upstream key available")

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
	if errors.Is(err, errNoKey) {
		return http.StatusServiceUnavailable, "no_upstream_key"
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
	case "no_upstream_key":
		return "all upstream keys for this provider are marked invalid"
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
