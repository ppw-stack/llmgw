package ingress

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/anthropic/v1/messages":                 PathAnthropicMsgs,
		"/anthropic/messages":                    PathAnthropicMsgs,
		"/anthropic/v1/v1/messages":              PathAnthropicMsgs,
		"/anthropic/v1/messages/count_tokens":    PathAnthropicCountTokens,
		"/anthropic/messages/count_tokens":       PathAnthropicCountTokens,
		"/anthropic/v1/v1/messages/count_tokens": PathAnthropicCountTokens,
		"/anthropic/v1/models":                   PathAnthropicModels,
		"/anthropic/models":                      PathAnthropicModels,
		"/openai/v1/chat/completions":            PathOpenAIChat,
		"/openai/chat/completions":               PathOpenAIChat,
		"/openai/v1/v1/chat/completions":         PathOpenAIChat,
		"/openai/v1/responses":                   PathOpenAIResponses,
		"/openai/responses":                      PathOpenAIResponses,
		"/openai/v1/v1/responses":                PathOpenAIResponses,
		"/openai/v1/models":                      PathOpenAIModels,
		"/metrics":                               "/metrics",
		"/":                                      "/",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProtocolOf(t *testing.T) {
	t.Parallel()
	if p, ok := ProtocolOf(PathAnthropicMsgs); !ok || p != "anthropic" {
		t.Fatalf("anthropic path -> %q %v", p, ok)
	}
	if p, ok := ProtocolOf(PathAnthropicCountTokens); !ok || p != "anthropic" {
		t.Fatalf("anthropic count_tokens path -> %q %v", p, ok)
	}
	if p, ok := ProtocolOf(PathAnthropicModels); !ok || p != "anthropic" {
		t.Fatalf("anthropic models path -> %q %v", p, ok)
	}
	if p, ok := ProtocolOf(PathOpenAIChat); !ok || p != "openai" {
		t.Fatalf("openai path -> %q %v", p, ok)
	}
	if p, ok := ProtocolOf(PathOpenAIResponses); !ok || p != "openai" {
		t.Fatalf("openai responses path -> %q %v", p, ok)
	}
	if _, ok := ProtocolOf("/nope"); ok {
		t.Fatal("unexpected protocol")
	}
}

func TestParseOpenAI(t *testing.T) {
	m, err := ParseOpenAI([]byte(`{"model":"glm-4.6","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Model != "glm-4.6" || !m.Stream {
		t.Fatalf("unexpected meta: %+v", m)
	}
	if _, err := ParseOpenAI([]byte(`{"messages":[]}`)); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := ParseOpenAI([]byte(`not json`)); err == nil {
		t.Fatal("expected invalid json error")
	}
}

func TestParseAnthropic(t *testing.T) {
	m, err := ParseAnthropic([]byte(`{"model":"claude-x","stream":false,"max_tokens":8}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Model != "claude-x" || m.Stream {
		t.Fatalf("unexpected meta: %+v", m)
	}
}
