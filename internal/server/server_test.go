package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/transport"
	"github.com/pw/llmgw/internal/webui"
)

type reqInfo struct {
	Path          string
	Authorization string
	APIKey        string
	Version       string
	Body          string
}

type capture struct {
	mu   sync.Mutex
	reqs []reqInfo
}

func (c *capture) add(r reqInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, r)
}

func (c *capture) all() []reqInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]reqInfo(nil), c.reqs...)
}

func record(c *capture, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.add(reqInfo{
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		APIKey:        r.Header.Get("x-api-key"),
		Version:       r.Header.Get("anthropic-version"),
		Body:          string(body),
	})
}

func setup(t *testing.T, cap *capture, upstreamHandler http.HandlerFunc, keys []string, opts ...func(*Options)) (*httptest.Server, *store.Store, string) {
	return setupConfig(t, cap, upstreamHandler, keys, nil, opts...)
}

func setupConfig(t *testing.T, cap *capture, upstreamHandler http.HandlerFunc, keys []string,
	mutate func(*config.Config), opts ...func(*Options)) (*httptest.Server, *store.Store, string) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cap != nil {
			record(cap, r)
		} else {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		upstreamHandler(w, r)
	}))
	t.Cleanup(upstream.Close)

	upstreamKey := ""
	if len(keys) > 0 {
		upstreamKey = keys[0]
	}
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name:             "test",
			APIKey:           upstreamKey,
			Models:           config.Models{"gpt-test": "gpt-upstream", "claude-test": "claude-upstream"},
			OpenAIBaseURL:    upstream.URL,
			AnthropicBaseURL: upstream.URL,
		}},
		Pricing: map[string]config.Price{
			"gpt-test":    {InputPerMTok: 1, OutputPerMTok: 2},
			"claude-test": {InputPerMTok: 3, OutputPerMTok: 15},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatalf("reload router: %v", err)
	}
	fw := transport.New(rt, st, logger)

	plain, hash, prefix, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateKey("test", hash, prefix); err != nil {
		t.Fatal(err)
	}

	srvOpts := Options{
		Router:     rt,
		Forwarder:  fw,
		Store:      st,
		AdminToken: "admin-test",
		MaxBody:    1 << 20,
		Log:        logger,
		UI:         webui.Handler(),
	}
	for _, apply := range opts {
		apply(&srvOpts)
	}
	srv, err := New(srvOpts)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	gw := httptest.NewServer(srv.Handler())
	t.Cleanup(gw.Close)

	return gw, st, plain
}

func post(t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitLog(t *testing.T, st *store.Store) store.LogRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(store.LogFilter{Limit: 10})
		if err == nil && len(rows) > 0 {
			return rows[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no request log written")
	return store.LogRow{}
}

func TestOpenAINonStream(t *testing.T) {
	cap := &capture{}
	gw, st, key := setup(t, cap, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}, []string{"up-1"})

	resp := post(t, gw.URL+"/openai/v1/chat/completions", key,
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	body := readAll(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"total_tokens":15`) {
		t.Fatalf("response not passed through: %s", body)
	}

	reqs := cap.all()
	if len(reqs) != 1 {
		t.Fatalf("upstream calls = %d", len(reqs))
	}
	if reqs[0].Authorization != "Bearer up-1" {
		t.Fatalf("upstream auth = %q", reqs[0].Authorization)
	}
	if !strings.Contains(reqs[0].Body, `"model":"gpt-upstream"`) {
		t.Fatalf("model not rewritten: %s", reqs[0].Body)
	}
	if !strings.Contains(reqs[0].Body, `"messages"`) {
		t.Fatalf("body lost fields: %s", reqs[0].Body)
	}

	row := waitLog(t, st)
	if row.Model != "gpt-test" || row.UpstreamModel != "gpt-upstream" {
		t.Fatalf("log row: %+v", row)
	}
	if row.PromptTokens != 10 || row.CompletionTokens != 5 {
		t.Fatalf("usage: %+v", row)
	}
	if row.CostUSD <= 0 {
		t.Fatalf("expected cost, got %v", row.CostUSD)
	}
}

func TestOpenAIStreamInjectsUsageOption(t *testing.T) {
	cap := &capture{}
	gw, st, key := setup(t, cap, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}, []string{"up-1"})

	resp := post(t, gw.URL+"/openai/v1/chat/completions", key,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := readAll(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"content":"he"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream not passed through: %s", body)
	}

	reqs := cap.all()
	if !strings.Contains(reqs[0].Body, `"include_usage":true`) {
		t.Fatalf("stream_options not injected: %s", reqs[0].Body)
	}

	row := waitLog(t, st)
	if row.PromptTokens != 7 || row.CompletionTokens != 3 {
		t.Fatalf("usage from stream: %+v", row)
	}
	if row.UsageEstimated {
		t.Fatal("usage should not be estimated")
	}
}

func TestAnthropicStream(t *testing.T) {
	cap := &capture{}
	gw, st, key := setup(t, cap, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}, []string{"up-1"})

	resp := post(t, gw.URL+"/anthropic/v1/messages", key,
		`{"model":"claude-test","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	body := readAll(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("stream not passed through: %s", body)
	}

	reqs := cap.all()
	if len(reqs) != 1 || reqs[0].Path != "/v1/messages" {
		t.Fatalf("upstream path: %+v", reqs)
	}
	if reqs[0].APIKey != "up-1" {
		t.Fatalf("x-api-key = %q", reqs[0].APIKey)
	}
	if reqs[0].Version != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", reqs[0].Version)
	}
	if reqs[0].Authorization != "" {
		t.Fatalf("client authorization leaked upstream: %q", reqs[0].Authorization)
	}
	if !strings.Contains(reqs[0].Body, `"model":"claude-upstream"`) {
		t.Fatalf("model not rewritten: %s", reqs[0].Body)
	}

	row := waitLog(t, st)
	if row.PromptTokens != 11 || row.CompletionTokens != 9 {
		t.Fatalf("anthropic usage: %+v", row)
	}
}

func TestCrossProtocolRejected(t *testing.T) {
	cap := &capture{}
	gw, _, key := setupConfig(t, cap, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called")
	}, []string{"up-1"}, func(cfg *config.Config) {
		// Only the OpenAI entry exists, so the same models are unreachable
		// through the Anthropic entry and the error must point at /openai/v1.
		cfg.Providers[0].AnthropicBaseURL = ""
	})

	resp := post(t, gw.URL+"/anthropic/v1/messages", key,
		`{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "available via") {
		t.Fatalf("expected hint: %s", body)
	}
	if n := len(cap.all()); n != 0 {
		t.Fatalf("upstream called %d times", n)
	}
}

func TestUnknownModel(t *testing.T) {
	cap := &capture{}
	gw, _, key := setup(t, cap, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})
	resp := post(t, gw.URL+"/openai/v1/chat/completions", key,
		`{"model":"nope","messages":[]}`)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "not_found_error") {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestAuthFailures(t *testing.T) {
	cap := &capture{}
	gw, st, key := setup(t, cap, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})

	resp := post(t, gw.URL+"/openai/v1/chat/completions", "sk-gw-wrong",
		`{"model":"gpt-test","messages":[]}`)
	body := readAll(t, resp)
	if resp.StatusCode != 401 || !strings.Contains(body, "invalid gateway key") {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}

	keys, err := st.ListKeys()
	if err != nil || len(keys) == 0 {
		t.Fatalf("list keys: %v", err)
	}
	// Disable through the admin API so the in-memory auth cache is updated.
	req, _ := http.NewRequest(http.MethodPatch,
		gw.URL+"/admin/api/keys/"+strconv.FormatInt(keys[0].ID, 10),
		strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Authorization", "Bearer admin-test")
	req.Header.Set("Content-Type", "application/json")
	disableResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	disableResp.Body.Close()
	if disableResp.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d", disableResp.StatusCode)
	}
	resp = post(t, gw.URL+"/openai/v1/chat/completions", key,
		`{"model":"gpt-test","messages":[]}`)
	body = readAll(t, resp)
	if resp.StatusCode != 401 || !strings.Contains(body, "disabled") {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
}

func TestPathNormalizationAndModels(t *testing.T) {
	cap := &capture{}
	gw, _, key := setup(t, cap, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}, []string{"up-1"})

	// Missing /v1 on the Anthropic path must still work.
	resp := post(t, gw.URL+"/anthropic/messages", key,
		`{"model":"claude-test","max_tokens":8,"messages":[]}`)
	if body := readAll(t, resp); resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	mresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, mresp)
	if !strings.Contains(body, "gpt-test") || !strings.Contains(body, "claude-test") {
		t.Fatalf("models endpoint should list both registered models: %s", body)
	}
}

func TestSPAAndUnknownPaths(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"})

	resp, err := http.Get(gw.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, `id="app"`) {
		t.Fatalf("root should serve the SPA shell: %d %s", resp.StatusCode, body)
	}

	// Client-side route falls back to the shell.
	resp, err = http.Get(gw.URL + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, `id="app"`) {
		t.Fatalf("client route fallback failed: %d %s", resp.StatusCode, body)
	}

	// Missing asset is a real 404, not the shell.
	resp, err = http.Get(gw.URL + "/assets/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = readAll(t, resp)
	if resp.StatusCode != 404 {
		t.Fatalf("missing asset status = %d", resp.StatusCode)
	}

	// Unknown API path stays JSON with the base_url hint.
	resp, err = http.Get(gw.URL + "/openai/v2/nope")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, resp)
	if resp.StatusCode != 404 || !strings.Contains(body, "invalid_request_error") {
		t.Fatalf("api 404 body = %s", body)
	}
	if !strings.Contains(body, "/openai/v1") {
		t.Fatalf("api 404 should hint the correct base_url: %s", body)
	}
}
