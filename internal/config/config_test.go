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
    keys: ["k"]
    endpoints:
      - name: e
        protocol: openai
        base_url: https://example.com/v1
        models:
          m: m
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

func TestEmptyKeysAreDropped(t *testing.T) {
	body := strings.Replace(minimal, `keys: ["k"]`, `keys: ["k", "${LLMGW_TEST_MISSING_KEY}"]`, 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers[0].Keys) != 1 {
		t.Fatalf("keys = %v", cfg.Providers[0].Keys)
	}
}
