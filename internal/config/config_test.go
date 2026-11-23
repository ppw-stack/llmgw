package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
providers:
  - name: p
    api_key: "k"
    models: [m]
    openai_base_url: https://example.com/v1
`

func TestLoadMinimal(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.AdminUsername != "admin" {
		t.Fatalf("default admin username = %q", cfg.Server.AdminUsername)
	}
	if cfg.Server.SessionTTL.Value() == 0 {
		t.Fatal("session ttl not defaulted")
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("instances = %v", cfg.Instances)
	}
	if cfg.Instances[0].Name != "p" || cfg.Instances[0].APIKey != "k" {
		t.Fatalf("instance = %+v", cfg.Instances[0])
	}
	if len(cfg.Instances[0].Endpoints) != 1 {
		t.Fatalf("endpoints = %v", cfg.Instances[0].Endpoints)
	}
	if cfg.Instances[0].Endpoints[0].Protocol != ProtocolOpenAI {
		t.Fatalf("endpoint = %+v", cfg.Instances[0].Endpoints[0])
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "m" {
		t.Fatalf("models = %v", cfg.Models)
	}
	if len(cfg.Models[0].Instances) != 1 || cfg.Models[0].Instances[0].Name != "p" {
		t.Fatalf("model instances = %v", cfg.Models[0].Instances)
	}
}

func TestUnsetEnvInPasswordIsAnError(t *testing.T) {
	body := strings.Replace(minimal, `  addr: ":0"`,
		"  addr: \":0\"\n  admin_password: \"${LLMGW_TEST_MISSING_PASSWORD}\"", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "environment variable") {
		t.Fatalf("expected env error, got %v", err)
	}
}

func TestNonHTTPSchemeRejected(t *testing.T) {
	body := strings.Replace(minimal, "https://example.com/v1", "file:///etc/passwd", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestTrustedProxyParsing(t *testing.T) {
	body := strings.Replace(minimal, `  addr: ":0"`,
		"  addr: \":0\"\n  trusted_proxies: [\"127.0.0.1/32\", \"::1\"]", 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.TrustedPrefixes) != 2 {
		t.Fatalf("trusted prefixes = %v", cfg.Server.TrustedPrefixes)
	}
}

func TestModelListForms(t *testing.T) {
	body := strings.Replace(minimal, "models: [m]", "models: glm-4.6", 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "glm-4.6" {
		t.Fatalf("scalar model not parsed: %v", cfg.Models)
	}

	body = strings.Replace(minimal, "models: [m]", "models:\n      - glm-4.6\n      - claude-sonnet-4-5=glm-4.6", 1)
	cfg, err = Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range cfg.Models {
		if m.Name == "claude-sonnet-4-5" && m.Upstream == "glm-4.6" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alias not parsed: %v", cfg.Models)
	}
}

func TestEmptyModelsRejected(t *testing.T) {
	body := strings.Replace(minimal, "    models: [m]\n", "", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "models") {
		t.Fatalf("expected models error, got %v", err)
	}
}

func TestCustomInstanceRequiresEndpoint(t *testing.T) {
	body := strings.Replace(minimal, "    openai_base_url: https://example.com/v1\n", "", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("expected endpoint error, got %v", err)
	}
}

// TestQuotaCredentialExpandsEnv checks that the environment variables of the
// quota credential are expanded like the other secrets.
func TestQuotaCredentialExpandsEnv(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
instances:
  - name: ark
    preset: ark-coding-plan-cn
    api_key: "ark-test"
    quota_credential:
      access_key_id: "${LLMGW_TEST_VOLC_AK}"
      secret_access_key: "${LLMGW_TEST_VOLC_SK}"
    models: [ark-code-latest]
`
	t.Setenv("LLMGW_TEST_VOLC_AK", "AKLT-test")
	t.Setenv("LLMGW_TEST_VOLC_SK", "sk-test")
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("instances = %v", cfg.Instances)
	}
	cred := cfg.Instances[0].QuotaCredential
	if cred.AccessKeyID != "AKLT-test" || cred.SecretAccessKey != "sk-test" {
		t.Fatalf("quota credential = %+v", cred)
	}
}

func TestPresetProvider(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
providers:
  - name: minimax
    preset: minimax-mplan-global
    api_key: "${LLMGW_TEST_MINIMAX_KEY}"
`
	t.Setenv("LLMGW_TEST_MINIMAX_KEY", "sk-cp-test")
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("instances = %v", cfg.Instances)
	}
	if cfg.Instances[0].APIKey != "sk-cp-test" {
		t.Fatalf("api key = %q", cfg.Instances[0].APIKey)
	}
	if len(cfg.Instances[0].Endpoints) != 2 {
		t.Fatalf("preset endpoints = %v", cfg.Instances[0].Endpoints)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "MiniMax-M3.1-Flash" {
		t.Fatalf("preset default models missing: %v", cfg.Models)
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Instances) != 1 || len(eff.Instances[0].Endpoints) != 2 {
		t.Fatalf("effective = %+v", eff)
	}
	if eff.Instances[0].Endpoints[0].BaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("effective url = %+v", eff.Instances[0].Endpoints[0])
	}
}

func TestPresetRejectsCustomURLs(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
providers:
  - name: minimax
    preset: minimax-mplan-global
    openai_base_url: https://example.com/v1
`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "supplies its own") {
		t.Fatalf("expected preset url error, got %v", err)
	}
}

func TestUnknownPresetRejected(t *testing.T) {
	body := strings.Replace(minimal,
		`    api_key: "k"`, "    preset: nope\n    api_key: \"k\"", 1)
	body = strings.Replace(body, "    openai_base_url: https://example.com/v1\n", "", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("expected preset error, got %v", err)
	}
}

func TestLegacyProviderFormIsRejectedWithHint(t *testing.T) {
	body := strings.Replace(minimal, "    api_key: \"k\"\n    models: [m]\n    openai_base_url: https://example.com/v1\n",
		"    keys: [\"k\"]\n    endpoints: []\n", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "keys/endpoints form was removed") {
		t.Fatalf("expected legacy hint, got %v", err)
	}
}

// ---- instance + model form ----

const topologyConfig = `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
instances:
  - name: cn-a
    api_key: "k-a"
    endpoints:
      - {protocol: openai, base_url: "https://api.minimax.cn/v1"}
      - {protocol: anthropic, base_url: "https://api.minimax.cn/anthropic"}
    models: [MiniMax-M3.1-Flash]
    max_fails: 2
    cooldown_secs: 10
  - name: cn-b
    api_key: "k-b"
    endpoints:
      - {protocol: openai, base_url: "https://api.minimax.cn/v1"}
    models: [MiniMax-M3.1-Flash]
models:
  - name: MiniMax-M3.1-Flash
    instances:
      - {name: cn-a, priority: 0}
      - {name: cn-b, priority: 1}
`

func TestTopologyFormLoads(t *testing.T) {
	cfg, err := Load(writeConfig(t, topologyConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances) != 2 || len(cfg.Models) != 1 {
		t.Fatalf("instances=%v models=%v", cfg.Instances, cfg.Models)
	}
	if cfg.Instances[0].MaxFails != 2 || cfg.Instances[0].CooldownSecs != 10 {
		t.Fatalf("breaker params = %+v", cfg.Instances[0])
	}
	if len(cfg.Models[0].Instances) != 2 {
		t.Fatalf("model instances = %v", cfg.Models[0].Instances)
	}
}

func TestModelCandidatesAreSortedByPriority(t *testing.T) {
	body := strings.Replace(topologyConfig,
		"      - {name: cn-a, priority: 0}\n      - {name: cn-b, priority: 1}",
		"      - {name: cn-b, priority: 1}\n      - {name: cn-a, priority: 0}", 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	cands := cfg.Models[0].Instances
	if cands[0].Name != "cn-a" || cands[1].Name != "cn-b" {
		t.Fatalf("candidates not sorted by priority: %v", cands)
	}
}

func TestModelNameMustBeUnique(t *testing.T) {
	body := topologyConfig + `
  - name: MiniMax-M3.1-Flash
    instances:
      - {name: cn-a}
`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "duplicate model") {
		t.Fatalf("expected duplicate model error, got %v", err)
	}
}

func TestModelMustReferenceAnExistingInstance(t *testing.T) {
	body := strings.Replace(topologyConfig, "{name: cn-b, priority: 1}", "{name: nope, priority: 1}", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected unknown instance error, got %v", err)
	}
}

func TestModelCannotListTheSameInstanceTwice(t *testing.T) {
	body := strings.Replace(topologyConfig, "{name: cn-b, priority: 1}", "{name: cn-a, priority: 1}", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Fatalf("expected duplicate instance error, got %v", err)
	}
}

func TestUnsupportedStrategyRejected(t *testing.T) {
	body := strings.Replace(topologyConfig, "  - name: MiniMax-M3.1-Flash",
		"  - name: MiniMax-M3.1-Flash\n    strategy: round_robin", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("expected strategy error, got %v", err)
	}
}

func TestInstanceNameMustBeUnique(t *testing.T) {
	body := strings.Replace(topologyConfig, "models:\n  - name: MiniMax-M3.1-Flash",
		"  - name: cn-a\n    api_key: \"k-c\"\n    models: [MiniMax-M3.1-Flash]\n    endpoints:\n"+
			"      - {protocol: openai, base_url: \"https://example.com/v1\"}\n"+
			"models:\n  - name: MiniMax-M3.1-Flash", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "duplicate instance") {
		t.Fatalf("expected duplicate instance error, got %v", err)
	}
}

func TestInstanceRequiresModels(t *testing.T) {
	body := strings.Replace(topologyConfig, "    models: [MiniMax-M3.1-Flash]\n    max_fails: 2",
		"    max_fails: 2", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "at least one supported model") {
		t.Fatalf("expected models error, got %v", err)
	}
}

func TestInstanceModelsAreSortedAndDeduplicated(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
instances:
  - name: only
    api_key: "k"
    endpoints:
      - {protocol: openai, base_url: "https://example.com/v1"}
    models: [z-model, a-model, z-model]
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Instances[0].Models
	if len(got) != 2 || got[0] != "a-model" || got[1] != "z-model" {
		t.Fatalf("models = %v", got)
	}
}

func TestModelUpstreamMustBeDeclaredByEveryCandidate(t *testing.T) {
	body := strings.Replace(topologyConfig, "    models: [MiniMax-M3.1-Flash]\nmodels:",
		"    models: [other-model]\nmodels:", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "does not declare upstream model") {
		t.Fatalf("expected declaration error, got %v", err)
	}
}

func TestDeclaredUpstreamMatchesAlias(t *testing.T) {
	// The declaration is matched against the upstream name, not the public
	// model name, because that is what the vendor receives.
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
instances:
  - name: glm
    api_key: "k"
    endpoints:
      - {protocol: openai, base_url: "https://example.com/v1"}
    models: [glm-4.6]
models:
  - name: claude-sonnet-4-5
    upstream: glm-4.6
    instances:
      - {name: glm}
`
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("upstream alias should match the declaration: %v", err)
	}

	body = strings.Replace(body, "models: [glm-4.6]", "models: [claude-sonnet-4-5]", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "does not declare upstream model") {
		t.Fatalf("public name must not satisfy the declaration: %v", err)
	}
}

func TestInstanceEndpointProtocolMustBeKnown(t *testing.T) {
	body := strings.Replace(topologyConfig, "{protocol: openai, base_url: \"https://api.minimax.cn/v1\"}",
		"{protocol: grpc, base_url: \"https://api.minimax.cn/v1\"}", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "unknown protocol") {
		t.Fatalf("expected protocol error, got %v", err)
	}
}

func TestPresetInstanceExpandsEndpoints(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
instances:
  - name: mm
    preset: minimax-mplan-cn
    api_key: "k"
    models: [MiniMax-M3.1-Flash]
models:
  - name: MiniMax-M3.1-Flash
    instances:
      - {name: mm}
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances[0].Endpoints) != 2 {
		t.Fatalf("preset endpoints = %v", cfg.Instances[0].Endpoints)
	}
	if cfg.Instances[0].Endpoints[0].BaseURL != "https://api.minimax.cn/v1" {
		t.Fatalf("preset url = %+v", cfg.Instances[0].Endpoints[0])
	}
}

func TestPresetInstanceRejectsCustomEndpoints(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
instances:
  - name: mm
    preset: minimax-mplan-cn
    api_key: "k"
    endpoints:
      - {protocol: openai, base_url: "https://example.com/v1"}
    models: [MiniMax-M3.1-Flash]
models:
  - name: MiniMax-M3.1-Flash
    instances:
      - {name: mm}
`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "supplies its own endpoints") {
		t.Fatalf("expected preset endpoint error, got %v", err)
	}
}

func TestPresetModelsEndpoint(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"minimax-mplan-global": "https://api.minimax.io/v1/models",
		"minimax-mplan-cn":     "https://api.minimax.cn/v1/models",
		"opencode-go":          "https://opencode.ai/inference/go/openai/v1/models",
		"ark-coding-plan-cn":   "https://ark.cn-beijing.volces.com/api/coding/v3/models",
	}
	for id, want := range cases {
		got, protocol, err := PresetModelsEndpoint(id)
		if err != nil {
			t.Fatalf("preset %q: %v", id, err)
		}
		if got != want {
			t.Fatalf("preset %q endpoint = %q, want %q", id, got, want)
		}
		if protocol != ProtocolOpenAI {
			t.Fatalf("preset %q protocol = %q", id, protocol)
		}
	}
	if _, _, err := PresetModelsEndpoint("missing"); err == nil {
		t.Fatal("expected an error for an unknown preset")
	}
}

// TestLegacyProviderExpandsToInstanceAndModel proves the old configuration
// form keeps working and produces the same routing shape.
func TestLegacyProviderExpandsToInstanceAndModel(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Instances) != 1 || len(eff.Models) != 1 {
		t.Fatalf("effective = %+v", eff)
	}
	if eff.Models[0].Name != "m" || eff.Models[0].Upstream != "m" {
		t.Fatalf("model = %+v", eff.Models[0])
	}
	if len(eff.Models[0].Instances) != 1 || eff.Models[0].Instances[0].Instance != "p" {
		t.Fatalf("candidates = %+v", eff.Models[0].Instances)
	}
}

func TestNoUpstreamIsRejected(t *testing.T) {
	body := `
server:
  addr: ":0"
database:
  path: "/tmp/x.db"
`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "at least one instance") {
		t.Fatalf("expected instance error, got %v", err)
	}
}

// TestAdminEnabledDefaultsToOpen: a config file that predates the option
// keeps the management surface open.
func TestAdminEnabledDefaultsToOpen(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Server.AdminIsEnabled() {
		t.Fatal("the management surface should be open by default")
	}
}

// TestAdminEnabledCanBeClosed: admin_enabled: false is the emergency switch
// that shuts the console down.
func TestAdminEnabledCanBeClosed(t *testing.T) {
	body := strings.Replace(minimal, `  addr: ":0"`,
		"  addr: \":0\"\n  admin_enabled: false", 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.AdminIsEnabled() {
		t.Fatal("admin_enabled: false should close the management surface")
	}

	body = strings.Replace(minimal, `  addr: ":0"`,
		"  addr: \":0\"\n  admin_enabled: true", 1)
	cfg, err = Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Server.AdminIsEnabled() {
		t.Fatal("admin_enabled: true should open the management surface")
	}
}
