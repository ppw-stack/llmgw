package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pw/llmgw/internal/config"
)

func fetchBody(t *testing.T, payload string) ([]Candidate, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)
	return Fetch(context.Background(), srv.Client(), srv.URL+"/models", config.ProtocolOpenAI, "sk-test")
}

func ids(candidates []Candidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.ID)
	}
	return out
}

func TestFetchSendsOpenAIAuth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(srv.Close)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL, config.ProtocolOpenAI, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestFetchSendsAnthropicAuth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Error("missing anthropic-version")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL,
		config.ProtocolAnthropic, "sk-test"); err != nil {
		t.Fatal(err)
	}
}

func TestParseOpenCodeCapabilities(t *testing.T) {
	t.Parallel()
	got, err := fetchBody(t, `{"data":[
		{"id":"glm-5.3","api":{"chat-completions":{}}},
		{"id":"claude-haiku-5-5","api":{"messages":{}}},
		{"id":"gpt-6-luna","api":{"responses":{}}},
		{"id":"gemini-4-argon","api":{"generate-content":{},"interactions":{}}}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"glm-5.3", "claude-haiku-5-5"}
	if gotIDs := ids(got); len(gotIDs) != len(want) || gotIDs[0] != want[0] || gotIDs[1] != want[1] {
		t.Fatalf("candidates = %v, want %v", gotIDs, want)
	}
	if len(got[0].Protocols) != 1 || got[0].Protocols[0] != config.ProtocolOpenAI {
		t.Fatalf("protocols = %v", got[0].Protocols)
	}
	if len(got[1].Protocols) != 1 || got[1].Protocols[0] != config.ProtocolAnthropic {
		t.Fatalf("protocols = %v", got[1].Protocols)
	}
}

func TestParseArkCapabilities(t *testing.T) {
	t.Parallel()
	got, err := fetchBody(t, `{"data":[
		{"id":"doubao-seed-2-1-pro-260915","api_supported":{"chat/completions":true,"messages":true}},
		{"id":"qwen3-8b-20250429","api_supported":{"chat/completions":true}},
		{"id":"doubao-seedream-5-0","api_supported":{"images/generations":true}},
		{"id":"hitem3d-2-0"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"doubao-seed-2-1-pro-260915", "qwen3-8b-20250429"}
	gotIDs := ids(got)
	if len(gotIDs) != len(want) || gotIDs[0] != want[0] || gotIDs[1] != want[1] {
		t.Fatalf("candidates = %v, want %v", gotIDs, want)
	}
	if len(got[0].Protocols) != 2 {
		t.Fatalf("protocols = %v", got[0].Protocols)
	}
}

func TestParseKeepsModelsWithoutMetadata(t *testing.T) {
	t.Parallel()
	got, err := fetchBody(t, `{"object":"list","data":[
		{"id":"MiniMax-M3.1-Flash"},
		{"id":"MiniMax-M2.5"},
		{"id":""}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"MiniMax-M3.1-Flash", "MiniMax-M2.5"}
	gotIDs := ids(got)
	if len(gotIDs) != len(want) || gotIDs[0] != want[0] || gotIDs[1] != want[1] {
		t.Fatalf("candidates = %v, want %v", gotIDs, want)
	}
	for _, c := range got {
		if len(c.Protocols) != 0 {
			t.Fatalf("protocols = %v", c.Protocols)
		}
	}
}

func TestParseDeduplicates(t *testing.T) {
	t.Parallel()
	got, err := fetchBody(t, `{"data":[{"id":"m1"},{"id":"m1"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestFetchRejectsBadResponses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
		case "/bad-json":
			_, _ = io.WriteString(w, `{`)
		default:
			_, _ = io.WriteString(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(srv.Close)

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/unauthorized",
		config.ProtocolOpenAI, "k"); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/bad-json",
		config.ProtocolOpenAI, "k"); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
