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
	if got := cfg.Providers[0].Models["m"]; got != "m" {
		t.Fatalf("models = %v", cfg.Providers[0].Models)
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
	if _, ok := cfg.Providers[0].Models["glm-4.6"]; !ok {
		t.Fatalf("scalar model not parsed: %v", cfg.Providers[0].Models)
	}

	body = strings.Replace(minimal, "models: [m]", "models:\n      - glm-4.6\n      - claude-sonnet-4-5=glm-4.6", 1)
	cfg, err = Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers[0].Models["claude-sonnet-4-5"]; got != "glm-4.6" {
		t.Fatalf("alias = %q", got)
	}
}

func TestEmptyModelsRejected(t *testing.T) {
	body := strings.Replace(minimal, "    models: [m]\n", "", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "models") {
		t.Fatalf("expected models error, got %v", err)
	}
}

func TestCustomProviderRequiresBaseURL(t *testing.T) {
	body := strings.Replace(minimal, "    openai_base_url: https://example.com/v1\n", "", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("expected base url error, got %v", err)
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
	if cfg.Providers[0].APIKey != "sk-cp-test" {
		t.Fatalf("api key = %q", cfg.Providers[0].APIKey)
	}
	if _, ok := cfg.Providers[0].Models["MiniMax-M3.1-Flash"]; !ok {
		t.Fatalf("preset default models missing: %v", cfg.Providers[0].Models)
	}
	eff, err := cfg.Providers[0].Effective()
	if err != nil {
		t.Fatal(err)
	}
	if eff.OpenAIBaseURL != "https://api.minimax.io/v1" ||
		eff.AnthropicBaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("effective urls = %+v", eff)
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
	if err == nil || !strings.Contains(err.Error(), "supplies its own base URLs") {
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
