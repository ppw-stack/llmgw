package router

import (
	"strings"
	"testing"

	"github.com/pw/llmgw/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{
				Name:             "glm",
				APIKey:           "k1",
				Models:           config.Models{"glm-4.6": "glm-4.6", "claude-only": "claude-3"},
				OpenAIBaseURL:    "https://a/v4",
				AnthropicBaseURL: "https://a/anthropic",
			},
			{
				Name:          "deepseek",
				APIKey:        "k2",
				Models:        config.Models{"deepseek-chat": "deepseek-chat"},
				OpenAIBaseURL: "https://b/v1",
			},
		},
	}
}

func TestResolve(t *testing.T) {
	r := New()
	if err := r.Reload(testConfig()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	tgt, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6")
	if err != nil {
		t.Fatalf("resolve openai: %v", err)
	}
	if tgt.Provider != "glm" || tgt.Protocol != config.ProtocolOpenAI {
		t.Fatalf("unexpected target: %+v", tgt)
	}
	if tgt.Key != "k1" || tgt.UpstreamModel != "glm-4.6" {
		t.Fatalf("unexpected target: %+v", tgt)
	}

	tgt, err = r.Resolve(config.ProtocolAnthropic, "glm-4.6")
	if err != nil {
		t.Fatalf("resolve anthropic: %v", err)
	}
	if tgt.Endpoint != "anthropic" || tgt.BaseURL != "https://a/anthropic" {
		t.Fatalf("expected anthropic endpoint, got %+v", tgt)
	}
	if tgt.Version != config.DefaultAnthropicVersion {
		t.Fatalf("anthropic version = %q", tgt.Version)
	}

	if _, err := r.Resolve(config.ProtocolOpenAI, "nope"); err == nil {
		t.Fatal("expected model not found")
	}
}

func TestCrossProtocolHint(t *testing.T) {
	r := New()
	if err := r.Reload(testConfig()); err != nil {
		t.Fatal(err)
	}
	// deepseek only has an OpenAI base URL, so the Anthropic entry gets a hint.
	_, err := r.Resolve(config.ProtocolAnthropic, "deepseek-chat")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "available via") {
		t.Fatalf("expected cross-protocol hint, got: %v", err)
	}
}

func TestDuplicateModelRejected(t *testing.T) {
	cfg := testConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name:          "conflict",
		Models:        config.Models{"glm-4.6": "something"},
		OpenAIBaseURL: "https://c/v1",
	})
	r := New()
	err := r.Reload(cfg)
	if err == nil {
		t.Fatal("expected duplicate model error")
	}
	if !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReloadKeepsOldSnapshotOnError(t *testing.T) {
	r := New()
	if err := r.Reload(testConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6"); err != nil {
		t.Fatal(err)
	}
	bad := testConfig()
	bad.Providers[0].Preset = "does-not-exist"
	// A preset wins over custom URLs, so this must fail during Build.
	bad.Providers[0].OpenAIBaseURL = ""
	bad.Providers[0].AnthropicBaseURL = ""
	if err := r.Reload(bad); err == nil {
		t.Fatal("expected error")
	}
	if _, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6"); err != nil {
		t.Fatalf("old snapshot should still work: %v", err)
	}
}

func TestModelListByProtocol(t *testing.T) {
	r := New()
	if err := r.Reload(testConfig()); err != nil {
		t.Fatal(err)
	}
	openai := r.ModelList(config.ProtocolOpenAI)
	if len(openai) != 3 {
		t.Fatalf("openai models = %v", openai)
	}
	anthropic := r.ModelList(config.ProtocolAnthropic)
	if len(anthropic) != 2 {
		t.Fatalf("anthropic models = %v", anthropic)
	}
}

func TestPresetProviderRouting(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{
		Name:   "minimax",
		Preset: "minimax-mplan-global",
		APIKey: "sk-cp",
	}}}
	r := New()
	if err := r.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{config.ProtocolOpenAI, config.ProtocolAnthropic} {
		tgt, err := r.Resolve(protocol, "MiniMax-M3.1-Flash")
		if err != nil {
			t.Fatalf("resolve %s: %v", protocol, err)
		}
		if tgt.BaseURL == "" || tgt.Key != "sk-cp" {
			t.Fatalf("unexpected target: %+v", tgt)
		}
	}
	tgt, _ := r.Resolve(config.ProtocolOpenAI, "MiniMax-M3.1-Flash")
	if tgt.BaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("openai base = %q", tgt.BaseURL)
	}
}
