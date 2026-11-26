package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/preset"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

// upstreamTestKey returns the upstream key used by the transport tests. Set
// TEST_UPSTREAM_API_KEY to inject a real one; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func upstreamTestKey() string {
	if v := strings.TrimSpace(os.Getenv("TEST_UPSTREAM_API_KEY")); v != "" {
		return v
	}
	return "sk-test-placeholder-upstream"
}

func newForwarder(t *testing.T, upstream string, timeout time.Duration) (*Forwarder, *store.Store, []router.Target) {
	t.Helper()
	return newMultiForwarder(t, []string{upstream}, timeout)
}

// newMultiForwarder wires one instance per upstream so the failover chain can
// be exercised. The instances are ordered, the first one is the primary.
func newMultiForwarder(t *testing.T, upstreams []string, timeout time.Duration) (*Forwarder, *store.Store, []router.Target) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Instances: []config.Instance{},
		Models:    []config.Model{},
	}
	candidates := make([]config.ModelInstance, 0, len(upstreams))
	for i, upstream := range upstreams {
		name := fmt.Sprintf("p%d", i)
		cfg.Instances = append(cfg.Instances, config.Instance{
			Name:      name,
			APIKey:    upstreamTestKey(),
			Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: upstream}},
			Models:    []string{"upstream-m"},
		})
		candidates = append(candidates, config.ModelInstance{Name: name, Upstream: "upstream-m", Priority: i})
	}
	cfg.Models = append(cfg.Models, config.Model{
		Name:      "m",
		Instances: candidates,
	})
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	targets, err := rt.Resolve(config.ProtocolOpenAI, "m")
	if err != nil {
		t.Fatal(err)
	}
	return NewWithTimeout(rt, st, logger, timeout), st, targets
}

func TestCopyRequestHeadersDropsAcceptEncoding(t *testing.T) {
	src := http.Header{
		"Accept-Encoding": {"gzip, deflate"},
		"Authorization":   {"Bearer client-key"},
		"X-Test":          {"1"},
	}
	dst := http.Header{}
	copyRequestHeaders(dst, src)
	if dst.Get("Accept-Encoding") != "" {
		t.Fatal("Accept-Encoding must be dropped")
	}
	if dst.Get("X-Test") != "1" {
		t.Fatal("ordinary headers must be preserved")
	}
}

// TestPresetUpstreamURLs locks the final upstream URL of the built-in
// presets. The gateway appends /chat/completions on the OpenAI protocol and
// /v1/messages on the Anthropic one, so the two base URLs of a preset differ
// by exactly that segment.
func TestPresetUpstreamURLs(t *testing.T) {
	t.Parallel()
	goPreset, ok := preset.Lookup("opencode-go")
	if !ok {
		t.Fatal("missing opencode-go preset")
	}
	arkPreset, ok := preset.Lookup("ark-coding-plan-cn")
	if !ok {
		t.Fatal("missing ark-coding-plan-cn preset")
	}
	cases := []struct {
		name   string
		target router.Target
		want   string
	}{
		{
			name:   "opencode go openai",
			target: router.Target{Protocol: config.ProtocolOpenAI, BaseURL: goPreset.OpenAIBaseURL},
			want:   "https://opencode.ai/inference/go/openai/v1/chat/completions",
		},
		{
			name:   "opencode go anthropic",
			target: router.Target{Protocol: config.ProtocolAnthropic, BaseURL: goPreset.AnthropicBaseURL},
			want:   "https://opencode.ai/inference/go/anthropic/v1/messages",
		},
		{
			name:   "ark coding openai",
			target: router.Target{Protocol: config.ProtocolOpenAI, BaseURL: arkPreset.OpenAIBaseURL},
			want:   "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions",
		},
		{
			name:   "ark coding anthropic",
			target: router.Target{Protocol: config.ProtocolAnthropic, BaseURL: arkPreset.AnthropicBaseURL},
			want:   "https://ark.cn-beijing.volces.com/api/coding/v1/messages",
		},
	}
	for _, tc := range cases {
		if got := upstreamURL(tc.target, EndpointDefault); got != tc.want {
			t.Fatalf("%s: upstream url = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	cases := map[int]int64{0: 0, 1: 1, 4: 1, 5: 2, 400: 100}
	for in, want := range cases {
		if got := estimateTokens(in); got != want {
			t.Errorf("estimateTokens(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestOpenAICacheReadTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		usage string
		want  int64
	}{
		{"nested cached_tokens", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6}}}`, 6},
		{"nested takes precedence", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6},"prompt_cache_hit_tokens":7}}`, 6},
		{"deepseek prompt_cache_hit_tokens", `{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":7}}`, 7},
		{"no cache counters", `{"usage":{"prompt_tokens":10,"completion_tokens":2}}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyAcc usageAcc
			bodyAcc.observeBody(config.ProtocolOpenAI, []byte(tc.usage))
			if bodyAcc.cacheRead != tc.want {
				t.Fatalf("body cache read = %d, want %d", bodyAcc.cacheRead, tc.want)
			}

			var streamAcc usageAcc
			streamAcc.observeLine(config.ProtocolOpenAI, []byte("data: "+tc.usage))
			if streamAcc.cacheRead != tc.want {
				t.Fatalf("stream cache read = %d, want %d", streamAcc.cacheRead, tc.want)
			}
		})
	}
}

func TestAnthropicStreamUsageFromMessageDelta(t *testing.T) {
	var acc usageAcc
	acc.observeLine(config.ProtocolAnthropic,
		[]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}`))
	acc.observeLine(config.ProtocolAnthropic,
		[]byte(`data: {"type":"message_delta","usage":{"input_tokens":1,"output_tokens":3,"cache_read_input_tokens":205}}`))
	if !acc.seen || acc.prompt != 1 || acc.completion != 3 || acc.cacheRead != 205 {
		t.Fatalf("usage = %+v", acc)
	}
}

func TestResponseTooLargeIsRejected(t *testing.T) {
	old := maxUpstreamResponse
	maxUpstreamResponse = 1024
	defer func() { maxUpstreamResponse = old }()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), 4096))
	}))
	defer upstream.Close()

	fw, _, targets := newForwarder(t, upstream.URL, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "64 MiB") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer func() {
		close(release)
		upstream.Close()
	}()

	fw, _, targets := newForwarder(t, upstream.URL, 200*time.Millisecond)
	body := []byte(`{"model":"m","stream":true,"messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	start := time.Now()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", Stream: true, RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("idle timeout did not fire, elapsed %v", elapsed)
	}
	if !strings.Contains(rec.Body.String(), "content") {
		t.Fatalf("first event was not forwarded: %s", rec.Body.String())
	}
}

func TestUpstreamAuthErrorIsPassedThrough(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad upstream key"}}`)
	}))
	defer upstream.Close()

	fw, _, targets := newForwarder(t, upstream.URL, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (no key rotation)", calls)
	}
	if !strings.Contains(rec.Body.String(), "bad upstream key") {
		t.Fatalf("body not passed through: %s", rec.Body.String())
	}
}

func TestUsageFallbackIsEstimated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer upstream.Close()

	fw, st, targets := newForwarder(t, upstream.URL, 5*time.Second)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(store.LogFilter{Limit: 5})
		if err == nil && len(rows) == 1 {
			row := rows[0]
			if !row.UsageEstimated {
				t.Fatal("usage should be marked estimated")
			}
			if row.PromptTokens == 0 || row.CompletionTokens == 0 {
				t.Fatalf("estimated tokens missing: %+v", row)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no log row")
}

// ---- failover ----

func TestFailoverToNextInstanceOnServerError(t *testing.T) {
	t.Parallel()
	var firstCalls, secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer second.Close()

	fw, st, targets := newMultiForwarder(t, []string{first.URL, second.URL}, 5*time.Second)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("calls = %d/%d, want 1/1", firstCalls, secondCalls)
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// Both attempts are logged so the switch is visible afterwards.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(store.LogFilter{Limit: 5})
		if err == nil && len(rows) == 2 {
			statuses := map[int]bool{}
			providers := map[string]bool{}
			for _, row := range rows {
				statuses[row.Status] = true
				providers[row.Provider] = true
			}
			if !statuses[http.StatusInternalServerError] || !statuses[http.StatusOK] {
				t.Fatalf("unexpected log rows: %+v", rows)
			}
			if len(providers) != 2 {
				t.Fatalf("both rows point at the same instance: %+v", rows)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("both attempts were not logged")
}

func TestNoFailoverOnClientError(t *testing.T) {
	t.Parallel()
	var firstCalls, secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad model"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer second.Close()

	fw, _, targets := newMultiForwarder(t, []string{first.URL, second.URL}, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("calls = %d/%d, want 1/0 (4xx must not fail over)", firstCalls, secondCalls)
	}
	if !strings.Contains(rec.Body.String(), "bad model") {
		t.Fatalf("body not passed through: %s", rec.Body.String())
	}
}

func TestFailoverOnRateLimit(t *testing.T) {
	t.Parallel()
	var firstCalls, secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer second.Close()

	fw, _, targets := newMultiForwarder(t, []string{first.URL, second.URL}, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("calls = %d/%d, want 1/1", firstCalls, secondCalls)
	}
}

func TestAllCandidatesFailReturnsTheLastUpstreamError(t *testing.T) {
	t.Parallel()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"first"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"second"}}`)
	}))
	defer second.Close()

	fw, _, targets := newMultiForwarder(t, []string{first.URL, second.URL}, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "second") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUnreachableUpstreamFailsOver(t *testing.T) {
	t.Parallel()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer second.Close()

	// A closed server refuses the connection, which is a transport error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	fw, _, targets := newMultiForwarder(t, []string{deadURL, second.URL}, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCircuitBreakerSkipsAFailingInstance(t *testing.T) {
	t.Parallel()
	var firstCalls, secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer second.Close()

	fw, _, targets := newMultiForwarder(t, []string{first.URL, second.URL}, 5*time.Second)
	// One failure is enough to open the circuit of the first instance.
	targets[0].MaxFails = 1
	targets[0].Cooldown = time.Minute

	body := []byte(`{"model":"m","messages":[]}`)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		fw.Forward(rec, req, Request{
			Protocol:  config.ProtocolOpenAI,
			Meta:      ingress.Meta{Model: "m", RawBody: body},
			Targets:   targets,
			APIKeyID:  1,
			RequestID: "req",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, body = %s", i, rec.Code, rec.Body.String())
		}
	}
	if firstCalls != 1 {
		t.Fatalf("failing instance calls = %d, want 1 (the breaker must skip it)", firstCalls)
	}
	if secondCalls != 2 {
		t.Fatalf("backup calls = %d, want 2", secondCalls)
	}
}

func TestAllInstancesInCooldownReturns503(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	fw, st, targets := newMultiForwarder(t, []string{upstream.URL}, 5*time.Second)
	targets[0].MaxFails = 1
	targets[0].Cooldown = time.Minute

	body := []byte(`{"model":"m","messages":[]}`)
	// The first request opens the circuit.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		fw.Forward(rec, req, Request{
			Protocol:  config.ProtocolOpenAI,
			Meta:      ingress.Meta{Model: "m", RawBody: body},
			Targets:   targets,
			APIKeyID:  1,
			RequestID: "req",
		})
		if i == 1 && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	// The skipped request is still logged.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(store.LogFilter{Limit: 5})
		if err == nil && len(rows) > 0 {
			found := false
			for _, row := range rows {
				if row.ErrorCode == "upstream_unavailable" {
					found = true
				}
			}
			if found {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the skipped request was not logged")
}

// TestShouldFailoverStatuses locks the retry policy: the defaults always
// apply, and instance-specific codes are additive rather than replacing them.
func TestShouldFailoverStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		code  int
		extra []int
		want  bool
	}{
		{"rate limit", http.StatusTooManyRequests, nil, true},
		{"server error", http.StatusInternalServerError, nil, true},
		{"not configured", http.StatusPaymentRequired, nil, false},
		{"miniMax quota", http.StatusPaymentRequired, []int{402}, true},
		{"opencode missing session", http.StatusBadRequest, []int{400}, true},
		{"bad request stays", http.StatusBadRequest, []int{402}, false},
		{"5xx survives a narrow list", http.StatusBadGateway, []int{402}, true},
	}
	for _, tc := range cases {
		if got := shouldFailover(tc.code, tc.extra); got != tc.want {
			t.Fatalf("%s: shouldFailover(%d, %v) = %v, want %v", tc.name, tc.code, tc.extra, got, tc.want)
		}
	}
}

// newConfiguredForwarder builds a forwarder from explicit instances so a test
// can exercise per-instance options such as failover_statuses and
// session_header. The first instance is the primary candidate.
func newConfiguredForwarder(t *testing.T, instances []config.Instance, upstreamModel string) (*Forwarder, []router.Target) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	candidates := make([]config.ModelInstance, 0, len(instances))
	for i := range instances {
		instances[i].Models = []string{upstreamModel}
		candidates = append(candidates, config.ModelInstance{
			Name: instances[i].Name, Upstream: upstreamModel, Priority: i,
		})
	}
	cfg := &config.Config{
		Instances: instances,
		Models:    []config.Model{{Name: "m", Instances: candidates}},
	}
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	targets, err := rt.Resolve(config.ProtocolOpenAI, "m")
	if err != nil {
		t.Fatal(err)
	}
	return NewWithTimeout(rt, st, logger, 5*time.Second), targets
}

// TestFailoverOnConfiguredStatus: a 402 from an instance that lists 402 in
// failover_statuses must move the request to the next candidate.
func TestFailoverOnConfiguredStatus(t *testing.T) {
	t.Parallel()
	var secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"plan exhausted"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer second.Close()

	fw, targets := newConfiguredForwarder(t, []config.Instance{
		{
			Name:             "quota",
			APIKey:           upstreamTestKey(),
			Endpoints:        []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: first.URL}},
			FailoverStatuses: []int{402},
		},
		{
			Name:      "backup",
			APIKey:    upstreamTestKey(),
			Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: second.URL}},
		},
	}, "upstream-m")

	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-402",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if secondCalls != 1 {
		t.Fatalf("backup calls = %d, want 1", secondCalls)
	}
}

// TestNoFailoverOnUnconfiguredStatus is the counterpart: without the opt-in a
// 4xx still describes the request and must be returned untouched.
func TestNoFailoverOnUnconfiguredStatus(t *testing.T) {
	t.Parallel()
	var secondCalls int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"plan exhausted"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer second.Close()

	fw, targets := newConfiguredForwarder(t, []config.Instance{
		{Name: "a", APIKey: upstreamTestKey(), Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: first.URL}}},
		{Name: "b", APIKey: upstreamTestKey(), Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: second.URL}}},
	}, "upstream-m")

	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-402-default",
	})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
	if secondCalls != 0 {
		t.Fatalf("backup calls = %d, want 0", secondCalls)
	}
}

// mustSessionHeaderValue fails the test when the derived session header cannot
// be computed.
func mustSessionHeaderValue(t *testing.T, r *http.Request, in Request, target router.Target) string {
	t.Helper()
	value, err := sessionHeaderValue(r, in, target)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestSessionHeaderValueIsStablePerConversation: every turn of one
// conversation maps to the same value while a different conversation starts
// from a different prefix. A per-request random value would satisfy the
// upstream check and destroy the prompt cache.
func TestSessionHeaderValueIsStablePerConversation(t *testing.T) {
	t.Parallel()
	target := router.Target{SessionHeader: "x-opencode-session"}
	conv := func(first string) Request {
		body := []byte(`{"messages":[{"role":"system","content":"you are a bot"},{"role":"user","content":"` + first + `"}]}`)
		return Request{Protocol: config.ProtocolOpenAI, Meta: ingress.Meta{RawBody: body}, APIKeyID: 7}
	}
	one := mustSessionHeaderValue(t, httptest.NewRequest(http.MethodPost, "/", nil), conv("hello"), target)
	two := mustSessionHeaderValue(t, httptest.NewRequest(http.MethodPost, "/", nil), conv("hello"), target)
	other := mustSessionHeaderValue(t, httptest.NewRequest(http.MethodPost, "/", nil), conv("different"), target)

	if one != two {
		t.Fatalf("same conversation derived %q and %q", one, two)
	}
	if one == other {
		t.Fatalf("different conversations share %q", one)
	}
	if !strings.HasPrefix(one, "ses_") || len(one) != 36 {
		t.Fatalf("derived value %q is not ses_ plus 32 hex characters", one)
	}
}

// TestSessionHeaderValuePrefersClientHeader: overwriting a client value would
// split one conversation into two upstream sessions.
func TestSessionHeaderValuePrefersClientHeader(t *testing.T) {
	t.Parallel()
	target := router.Target{SessionHeader: "x-opencode-session"}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("x-opencode-session", "ses_from_client")
	if got := mustSessionHeaderValue(t, req, Request{APIKeyID: 1}, target); got != "ses_from_client" {
		t.Fatalf("value = %q, want the client's own header", got)
	}
}

// TestSessionHeaderValueReusesAliases: a client that already identifies its
// conversation under another name keeps that identity.
func TestSessionHeaderValueReusesAliases(t *testing.T) {
	t.Parallel()
	target := router.Target{SessionHeader: "x-opencode-session"}
	alias := httptest.NewRequest(http.MethodPost, "/", nil)
	alias.Header.Set("x-session-id", "conv-42")
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := mustSessionHeaderValue(t, alias, Request{APIKeyID: 1, Meta: ingress.Meta{RawBody: body}}, target)

	same := httptest.NewRequest(http.MethodPost, "/", nil)
	same.Header.Set("x-session-id", "conv-42")
	again := mustSessionHeaderValue(t, same, Request{APIKeyID: 1, Meta: ingress.Meta{RawBody: body}}, target)
	if got != again {
		t.Fatalf("alias-derived value is not stable: %q vs %q", got, again)
	}

	different := httptest.NewRequest(http.MethodPost, "/", nil)
	different.Header.Set("x-session-id", "conv-43")
	if other := mustSessionHeaderValue(t, different, Request{APIKeyID: 1, Meta: ingress.Meta{RawBody: body}}, target); other == got {
		t.Fatalf("different alias values share %q", got)
	}
}

// TestSessionHeaderReachesUpstream covers the wiring: an instance that names a
// session header always sends a derived value upstream.
func TestSessionHeaderReachesUpstream(t *testing.T) {
	t.Parallel()
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	fw, targets := newConfiguredForwarder(t, []config.Instance{{
		Name:          "go",
		APIKey:        upstreamTestKey(),
		SessionHeader: "x-opencode-session",
		Endpoints:     []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: upstream.URL}},
	}}, "upstream-m")

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-session",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.HasPrefix(received, "ses_") {
		t.Fatalf("upstream received session header %q", received)
	}
}
