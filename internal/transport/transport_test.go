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
		if got := upstreamURL(tc.target); got != tc.want {
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
