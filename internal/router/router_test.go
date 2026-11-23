package router

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
)

// testAPIKey returns the upstream key used by the router tests. The
// environment variable injects a real key; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func testAPIKey(env, suffix string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return "sk-test-placeholder-" + suffix
}

func testConfig() *config.Config {
	return &config.Config{
		Instances: []config.Instance{
			{
				Name:   "glm",
				APIKey: "k1",
				Endpoints: []config.Endpoint{
					{Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v4"},
					{Protocol: config.ProtocolAnthropic, BaseURL: "https://a/anthropic"},
				},
				Models: []string{"glm-4.6", "claude-3"},
			},
			{
				Name:      "deepseek",
				APIKey:    "k2",
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://b/v1"}},
				Models:    []string{"deepseek-chat"},
			},
		},
		Models: []config.Model{
			{
				Name: "glm-4.6",
				Instances: []config.ModelInstance{
					{Name: "glm", Upstream: "glm-4.6"},
				},
			},
			{
				Name: "claude-only",
				Instances: []config.ModelInstance{
					{Name: "glm", Upstream: "claude-3"},
				},
			},
			{
				Name: "deepseek-chat",
				Instances: []config.ModelInstance{
					{Name: "deepseek", Upstream: "deepseek-chat"},
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

	targets, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6")
	if err != nil {
		t.Fatalf("resolve openai: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	tgt := targets[0]
	if tgt.Instance != "glm" || tgt.Protocol != config.ProtocolOpenAI {
		t.Fatalf("unexpected target: %+v", tgt)
	}
	if tgt.Key != "k1" || tgt.UpstreamModel != "glm-4.6" {
		t.Fatalf("unexpected target: %+v", tgt)
	}

	targets, err = r.Resolve(config.ProtocolAnthropic, "glm-4.6")
	if err != nil {
		t.Fatalf("resolve anthropic: %v", err)
	}
	tgt = targets[0]
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

func TestCandidatesUseTheirOwnUpstreamModel(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Instances = append(cfg.Instances, config.Instance{
		Name:      "glm-next",
		APIKey:    testAPIKey("LLMGW_TEST_GLM_NEXT_API_KEY", "glm-next"),
		Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://d/v1"}},
		Models:    []string{"glm-5.3-flash"},
	})
	cfg.Models[0].Instances = []config.ModelInstance{
		{Name: "glm", Upstream: "glm-4.6", Priority: 0},
		{Name: "glm-next", Upstream: "glm-5.3-flash", Priority: 1},
	}
	r := New()
	if err := r.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	targets, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].UpstreamModel != "glm-4.6" || targets[1].UpstreamModel != "glm-5.3-flash" {
		t.Fatalf("candidate upstreams = %+v", targets)
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

// TestSameModelOnSeveralInstances covers the failover chain: one model may be
// served by several instances, ordered by priority.
func TestSameModelOnSeveralInstances(t *testing.T) {
	cfg := testConfig()
	cfg.Instances = append(cfg.Instances, config.Instance{
		Name:      "glm-backup",
		APIKey:    "k3",
		Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://c/v1"}},
		Models:    []string{"glm-4.6"},
	})
	cfg.Models[0].Instances = []config.ModelInstance{
		{Name: "glm-backup", Upstream: "glm-4.6", Priority: 1},
		{Name: "glm", Upstream: "glm-4.6", Priority: 0},
	}
	r := New()
	if err := r.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	targets, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].Instance != "glm" || targets[1].Instance != "glm-backup" {
		t.Fatalf("candidates not ordered by priority: %+v", targets)
	}
	if targets[0].Priority != 0 || targets[1].Priority != 1 {
		t.Fatalf("priority not carried: %+v", targets)
	}
	// The backup only speaks OpenAI, so it does not show up on Anthropic.
	anthropic, err := r.Resolve(config.ProtocolAnthropic, "glm-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if len(anthropic) != 1 || anthropic[0].Instance != "glm" {
		t.Fatalf("anthropic targets = %+v", anthropic)
	}
}

func TestModelNameMustBeUnique(t *testing.T) {
	cfg := testConfig()
	cfg.Models = append(cfg.Models, config.Model{
		Name: "glm-4.6",
		Instances: []config.ModelInstance{
			{Name: "deepseek", Upstream: "something"},
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

func TestModelMustReferenceAnExistingInstance(t *testing.T) {
	cfg := testConfig()
	cfg.Models[0].Instances = []config.ModelInstance{{Name: "ghost"}}
	r := New()
	err := r.Reload(cfg)
	if err == nil {
		t.Fatal("expected unknown instance error")
	}
	if !strings.Contains(err.Error(), "does not exist") {
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
	bad.Instances[0].Preset = "does-not-exist"
	bad.Instances[0].Endpoints = nil
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
	cfg := &config.Config{
		Instances: []config.Instance{{
			Name:   "minimax",
			Preset: "minimax-mplan-global",
			APIKey: "sk-cp",
			Models: []string{"MiniMax-M3.1-Flash"},
		}},
		Models: []config.Model{{
			Name: "MiniMax-M3.1-Flash",
			Instances: []config.ModelInstance{
				{Name: "minimax", Upstream: "MiniMax-M3.1-Flash"},
			},
		}},
	}
	r := New()
	if err := r.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{config.ProtocolOpenAI, config.ProtocolAnthropic} {
		targets, err := r.Resolve(protocol, "MiniMax-M3.1-Flash")
		if err != nil {
			t.Fatalf("resolve %s: %v", protocol, err)
		}
		if len(targets) != 1 {
			t.Fatalf("resolve %s: %+v", protocol, targets)
		}
		if targets[0].BaseURL == "" || targets[0].Key != "sk-cp" {
			t.Fatalf("unexpected target: %+v", targets[0])
		}
	}
	targets, _ := r.Resolve(config.ProtocolOpenAI, "MiniMax-M3.1-Flash")
	if targets[0].BaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("openai base = %q", targets[0].BaseURL)
	}
}

func TestInstanceTimeoutAndBreakerParamsReachTheTarget(t *testing.T) {
	cfg := testConfig()
	cfg.Instances[0].TimeoutSecs = 30
	cfg.Instances[0].MaxFails = 5
	cfg.Instances[0].CooldownSecs = 15
	r := New()
	if err := r.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	targets, err := r.Resolve(config.ProtocolOpenAI, "glm-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if targets[0].Timeout != 30*time.Second {
		t.Fatalf("timeout = %v", targets[0].Timeout)
	}
	if targets[0].MaxFails != 5 || targets[0].Cooldown != 15*time.Second {
		t.Fatalf("breaker params = %+v", targets[0])
	}
}

func TestModelRejectsAnInstanceThatDoesNotDeclareTheUpstream(t *testing.T) {
	cfg := testConfig()
	cfg.Instances[1].Models = []string{"something-else"}
	r := New()
	err := r.Reload(cfg)
	if err == nil {
		t.Fatal("expected a declaration error")
	}
	if !strings.Contains(err.Error(), "does not declare upstream model") {
		t.Fatalf("unexpected error: %v", err)
	}
}
