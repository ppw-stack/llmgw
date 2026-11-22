package router

import (
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{
				Name:    "glm",
				Keys:    []string{"k1"},
				Timeout: config.Duration(5 * time.Second),
				Endpoints: []config.Endpoint{
					{Name: "openai", Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v4",
						Models: map[string]string{"glm-4.6": "glm-4.6"}},
					{Name: "anthropic", Protocol: config.ProtocolAnthropic, BaseURL: "https://a/anthropic",
						Models: map[string]string{"glm-4.6": "glm-4.6", "claude-only": "claude-3"}},
				},
			},
			{
				Name: "deepseek",
				Keys: []string{"k2"},
				Endpoints: []config.Endpoint{
					{Name: "openai", Protocol: config.ProtocolOpenAI, BaseURL: "https://b/v1",
						Models: map[string]string{"deepseek-chat": "deepseek-chat"}},
				},
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
	if tgt.Provider != "glm" || tgt.Endpoint != "openai" || tgt.Protocol != config.ProtocolOpenAI {
		t.Fatalf("unexpected target: %+v", tgt)
	}

	tgt, err = r.Resolve(config.ProtocolAnthropic, "glm-4.6")
	if err != nil {
		t.Fatalf("resolve anthropic: %v", err)
	}
	if tgt.Endpoint != "anthropic" {
		t.Fatalf("expected anthropic endpoint, got %+v", tgt)
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
	_, err := r.Resolve(config.ProtocolOpenAI, "claude-only")
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
		Name: "conflict",
		Endpoints: []config.Endpoint{
			{Name: "openai", Protocol: config.ProtocolOpenAI, BaseURL: "https://c/v1",
				Models: map[string]string{"glm-4.6": "something"}},
		},
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
	bad.Providers[0].Endpoints[0].Protocol = "invalid"
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
	if len(openai) != 2 {
		t.Fatalf("openai models = %v", openai)
	}
	anthropic := r.ModelList(config.ProtocolAnthropic)
	if len(anthropic) != 2 {
		t.Fatalf("anthropic models = %v", anthropic)
	}
}
