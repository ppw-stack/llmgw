package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

func waitLogs(t *testing.T, st *store.Store, want int) []store.LogRow {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(store.LogFilter{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) >= want {
			return rows
		}
		time.Sleep(15 * time.Millisecond)
	}
	rows, err := st.Logs(store.LogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("timed out waiting for %d logs, got %d", want, len(rows))
	return nil
}

func TestCopyRequestHeadersPreservesAnthropicBeta(t *testing.T) {
	t.Parallel()
	src := http.Header{
		"Anthropic-Beta":    {"prompt-caching-2024-07-31,interleaved-thinking-2025-05-14"},
		"Anthropic-Version": {"2023-06-01"},
		"X-Api-Key":         {"client-key"},
	}
	dst := http.Header{}
	copyRequestHeaders(dst, src)
	if got := dst.Get("Anthropic-Beta"); got != "prompt-caching-2024-07-31,interleaved-thinking-2025-05-14" {
		t.Fatalf("Anthropic-Beta = %q, want preserved", got)
	}
	if dst.Get("Anthropic-Version") != "" || dst.Get("X-Api-Key") != "" {
		t.Fatal("auth and version headers must be stripped before upstream injection")
	}
}

func TestResponsesNonStreamAndUsage(t *testing.T) {
	t.Parallel()
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","model":"upstream-m","output":[],"usage":{"input_tokens":120,"output_tokens":45,"input_tokens_details":{"cached_tokens":80}}}`)
	}))
	t.Cleanup(up.Close)

	fwd, st, targets := newForwarder(t, up.URL, 5*time.Second)
	raw := []byte(`{"model":"m","input":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Endpoint:  EndpointOpenAIResponses,
		Meta:      ingress.Meta{Model: "m", Stream: false, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-resp-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("upstream path = %q, want /responses", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"upstream-m"`) {
		t.Fatalf("upstream body did not rewrite model: %s", gotBody)
	}
	if strings.Contains(gotBody, "stream_options") {
		t.Fatalf("responses request must not inject stream_options: %s", gotBody)
	}

	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens != 120 || got.CompletionTokens != 45 || got.CacheReadTokens != 80 || got.UsageEstimated {
		t.Fatalf("unexpected logged usage: %+v", got)
	}
}

func TestResponsesStreamDoesNotInjectStreamOptionsAndParsesUsage(t *testing.T) {
	t.Parallel()
	var gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_s\"}}\n\n",
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"usage\":{\"input_tokens\":200,\"output_tokens\":30,\"input_tokens_details\":{\"cached_tokens\":150}}}}\n\n",
		}
		for _, ev := range events {
			_, _ = io.WriteString(w, ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(up.Close)

	fwd, st, targets := newForwarder(t, up.URL, 5*time.Second)
	raw := []byte(`{"model":"m","stream":true,"input":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Endpoint:  EndpointOpenAIResponses,
		Meta:      ingress.Meta{Model: "m", Stream: true, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-resp-stream",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(gotBody, "stream_options") {
		t.Fatalf("streaming responses request must not inject stream_options: %s", gotBody)
	}

	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens != 200 || got.CompletionTokens != 30 || got.CacheReadTokens != 150 || got.UsageEstimated {
		t.Fatalf("unexpected streaming responses usage: %+v", got)
	}
}

func TestConversationPrefixResponsesInput(t *testing.T) {
	t.Parallel()
	turn1 := []byte(`{"model":"m","instructions":"you are helpful","input":[{"role":"user","content":"hello"}]}`)
	turn2 := []byte(`{"model":"m","instructions":"you are helpful","input":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`)
	other := []byte(`{"model":"m","instructions":"you are helpful","input":[{"role":"user","content":"different"}]}`)

	p1, err := conversationPrefix(turn1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := conversationPrefix(turn2)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := conversationPrefix(other)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == "" || p1 != p2 {
		t.Fatalf("same conversation across turns should keep same prefix: %q vs %q", p1, p2)
	}
	if p1 == p3 {
		t.Fatalf("different conversations should diverge: %q", p1)
	}
}

func TestAnthropicCountTokensSkipsTokenEstimation(t *testing.T) {
	t.Parallel()
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	}))
	t.Cleanup(up.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Instances: []config.Instance{{
			Name:      "ant",
			APIKey:    upstreamTestKey(),
			Endpoints: []config.Endpoint{{Protocol: config.ProtocolAnthropic, BaseURL: up.URL}},
			Models:    []string{"claude-up"},
		}},
		Models: []config.Model{{
			Name:      "claude-m",
			Instances: []config.ModelInstance{{Name: "ant", Upstream: "claude-up"}},
		}},
	}
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
	targets, err := rt.Resolve(config.ProtocolAnthropic, "claude-m")
	if err != nil {
		t.Fatal(err)
	}
	fwd := NewWithTimeout(rt, st, logger, 5*time.Second)

	raw := []byte(`{"model":"claude-m","messages":[{"role":"user","content":"count me"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages/count_tokens", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolAnthropic,
		Endpoint:  EndpointAnthropicCountTokens,
		Meta:      ingress.Meta{Model: "claude-m", Stream: false, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-ct-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages/count_tokens" {
		t.Fatalf("upstream path = %q, want /v1/messages/count_tokens", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"claude-up"`) {
		t.Fatalf("model not rewritten: %s", gotBody)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"input_tokens":42}` {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}

	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens != 0 || got.CompletionTokens != 0 || got.UsageEstimated {
		t.Fatalf("count_tokens should log zero tokens without estimation: %+v", got)
	}
}

func TestImagesGenerationsForwardsAndSkipsEstimationWhenUsageAbsent(t *testing.T) {
	t.Parallel()
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1700000000,"data":[{"b64_json":"`+strings.Repeat("A", 1024)+`"}]}`)
	}))
	t.Cleanup(up.Close)

	fwd, st, targets := newForwarder(t, up.URL, 5*time.Second)
	raw := []byte(`{"model":"m","prompt":"a red cat","size":"1024x1024"}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/generations", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Endpoint:  EndpointOpenAIImagesGenerations,
		Meta:      ingress.Meta{Model: "m", Stream: false, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-img-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/images/generations" {
		t.Fatalf("upstream path = %q, want /images/generations", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"upstream-m"`) {
		t.Fatalf("upstream body did not rewrite model: %s", gotBody)
	}

	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens != 0 || got.CompletionTokens != 0 || got.UsageEstimated {
		t.Fatalf("images/generations without usage should skip token estimation: %+v", got)
	}
}

func TestImagesGenerationsRecordsUsageWhenProvided(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1700000000,"data":[{"b64_json":"AAAA"}],"usage":{"input_tokens":50,"output_tokens":1024,"total_tokens":1074}}`)
	}))
	t.Cleanup(up.Close)

	fwd, st, targets := newForwarder(t, up.URL, 5*time.Second)
	raw := []byte(`{"model":"m","prompt":"a red cat"}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/generations", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Endpoint:  EndpointOpenAIImagesGenerations,
		Meta:      ingress.Meta{Model: "m", Stream: false, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-img-usage",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens != 50 || got.CompletionTokens != 1024 || got.UsageEstimated {
		t.Fatalf("unexpected images/generations usage: %+v", got)
	}
}

func TestEmbeddingsForwardsAndParsesUsage(t *testing.T) {
	t.Parallel()
	var gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"upstream-m","usage":{"prompt_tokens":12,"total_tokens":12}}`)
	}))
	t.Cleanup(up.Close)

	fwd, st, targets := newForwarder(t, up.URL, 5*time.Second)
	raw := []byte(`{"model":"m","input":"hello world"}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/embeddings", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Endpoint:  EndpointOpenAIEmbeddings,
		Meta:      ingress.Meta{Model: "m", Stream: false, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-emb-1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/embeddings" {
		t.Fatalf("upstream path = %q, want /embeddings", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"upstream-m"`) {
		t.Fatalf("upstream body did not rewrite model: %s", gotBody)
	}

	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens != 12 || got.CompletionTokens != 0 || got.UsageEstimated {
		t.Fatalf("unexpected embeddings usage: %+v", got)
	}
}

func TestEmbeddingsFallbackEstimatesPromptOnly(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3,0.4,0.5]}]}`)
	}))
	t.Cleanup(up.Close)

	fwd, st, targets := newForwarder(t, up.URL, 5*time.Second)
	raw := []byte(`{"model":"m","input":"hello world"}`)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/embeddings", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	fwd.Forward(rec, req, Request{
		Protocol:  config.ProtocolOpenAI,
		Endpoint:  EndpointOpenAIEmbeddings,
		Meta:      ingress.Meta{Model: "m", Stream: false, RawBody: raw},
		Targets:   targets,
		APIKeyID:  1,
		RequestID: "req-emb-est",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	logs := waitLogs(t, st, 1)
	got := logs[0]
	if got.PromptTokens <= 0 || got.CompletionTokens != 0 || !got.UsageEstimated {
		t.Fatalf("embeddings fallback should estimate prompt only: %+v", got)
	}
}
