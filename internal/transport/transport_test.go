package transport

import (
	"bytes"
	"context"
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

func newForwarder(t *testing.T, upstream string, timeout time.Duration) (*Forwarder, *store.Store, router.Target) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Providers: []config.Provider{{
		Name:          "p",
		APIKey:        upstreamTestKey(),
		Models:        config.Models{"m": "upstream-m"},
		OpenAIBaseURL: upstream,
	}}}
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	target, err := rt.Resolve(config.ProtocolOpenAI, "m")
	if err != nil {
		t.Fatal(err)
	}
	return NewWithTimeout(rt, st, logger, timeout), st, target
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

	fw, _, target := newForwarder(t, upstream.URL, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Target:    target,
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

	fw, _, target := newForwarder(t, upstream.URL, 200*time.Millisecond)
	body := []byte(`{"model":"m","stream":true,"messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	start := time.Now()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", Stream: true, RawBody: body},
		Target:    target,
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

	fw, _, target := newForwarder(t, upstream.URL, 5*time.Second)
	body := []byte(`{"model":"m","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Target:    target,
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

	fw, st, target := newForwarder(t, upstream.URL, 5*time.Second)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	fw.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Meta:      ingress.Meta{Model: "m", RawBody: body},
		Target:    target,
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
