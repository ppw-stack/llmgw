package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// proxyTestPassword returns the proxy password used by the config tests. Set
// TEST_PROXY_PASSWORD to inject a real one; otherwise a random one-off
// password is generated at runtime so no credential literal is stored in the
// repository. The generated value is cached so every call in the same test
// binary returns the same password.
func proxyTestPassword() string {
	if v := strings.TrimSpace(os.Getenv("TEST_PROXY_PASSWORD")); v != "" {
		return v
	}
	proxyTestPasswordOnce.Do(func() {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			panic(fmt.Sprintf("generate the random proxy test password: %v", err))
		}
		proxyTestPasswordValue = base64.RawURLEncoding.EncodeToString(buf)
	})
	return proxyTestPasswordValue
}

var (
	proxyTestPasswordOnce  sync.Once
	proxyTestPasswordValue string
)

func TestNormalizeProxyURL(t *testing.T) {
	t.Parallel()
	proxyPassword := proxyTestPassword()
	withPassword := func(scheme, user, host string) string {
		return (&url.URL{
			Scheme: scheme,
			User:   url.UserPassword(user, proxyPassword),
			Host:   host,
		}).String()
	}
	valid := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"http://127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"HTTP://localhost:7890/", "http://localhost:7890"},
		{"https://192.168.1.10:8443", "https://192.168.1.10:8443"},
		{"socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{withPassword("socks5h", "user", "proxy.example.com:1080"),
			withPassword("socks5h", "user", "proxy.example.com:1080")},
		{"http://[::1]:7890", "http://[::1]:7890"},
		{"http://[fd00::1]:7890", "http://[fd00::1]:7890"},
	}
	for _, tc := range valid {
		got, err := NormalizeProxyURL("inst", "proxy_url", tc.in)
		if err != nil {
			t.Errorf("NormalizeProxyURL(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeProxyURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		"ftp://127.0.0.1:21",
		"127.0.0.1:7890",
		"http://",
		"http://127.0.0.1:7890/v1",
		"http://127.0.0.1:7890?a=1",
		"http://127.0.0.1:7890#frag",
		"http://169.254.169.254:80",
		"http://metadata.google.internal:80",
		"http://[fe80::1]:8080",
		"http://0.0.0.0:7890",
	}
	for _, raw := range invalid {
		if got, err := NormalizeProxyURL("inst", "proxy_url", raw); err == nil {
			t.Errorf("NormalizeProxyURL(%q) succeeded with %q, want error", raw, got)
		}
	}
}

func TestRedactProxyURL(t *testing.T) {
	t.Parallel()
	proxyPassword := proxyTestPassword()
	withPassword := func(scheme, user, host string) string {
		return (&url.URL{
			Scheme: scheme,
			User:   url.UserPassword(user, proxyPassword),
			Host:   host,
		}).String()
	}
	cases := []struct {
		in       string
		want     string
		wantAuth bool
	}{
		{"", "", false},
		{"http://127.0.0.1:7890", "http://127.0.0.1:7890", false},
		{"http://alice@127.0.0.1:7890", "http://alice@127.0.0.1:7890", false},
		{withPassword("http", "alice", "127.0.0.1:7890"), "http://alice:****@127.0.0.1:7890", true},
		{withPassword("socks5", "", "127.0.0.1:1080"), "socks5://****@127.0.0.1:1080", true},
	}
	for _, tc := range cases {
		got, hasAuth := RedactProxyURL(tc.in)
		if got != tc.want || hasAuth != tc.wantAuth {
			t.Errorf("RedactProxyURL(%q) = (%q, %v), want (%q, %v)",
				tc.in, got, hasAuth, tc.want, tc.wantAuth)
		}
		if strings.Contains(got, proxyPassword) {
			t.Errorf("RedactProxyURL(%q) leaked password: %q", tc.in, got)
		}
	}
}

func TestConfigLoadExpandsInstanceProxyURL(t *testing.T) {
	t.Setenv("TEST_INSTANCE_PROXY_URL", "http://127.0.0.1:7890")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
instances:
  - name: upstream
    api_key: placeholder
    proxy_url: "${TEST_INSTANCE_PROXY_URL}"
    endpoints:
      - protocol: openai
        base_url: https://api.example.com/v1
    models: [m1]
models:
  - name: m1
    instances:
      - name: upstream
        upstream: m1
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Instances) != 1 || cfg.Instances[0].ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("instances = %+v", cfg.Instances)
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if eff.Instances[0].ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("effective proxy = %q", eff.Instances[0].ProxyURL)
	}
}

// An unset environment variable must not silently turn the proxy off: the
// instance would then reach the upstream straight from the gateway.
func TestConfigLoadRejectsUnsetProxyURLEnv(t *testing.T) {
	t.Setenv("TEST_INSTANCE_PROXY_URL", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
instances:
  - name: upstream
    api_key: placeholder
    proxy_url: "${TEST_INSTANCE_PROXY_URL}"
    endpoints:
      - protocol: openai
        base_url: https://api.example.com/v1
    models: [m1]
models:
  - name: m1
    instances:
      - name: upstream
        upstream: m1
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with an unset proxy_url environment variable")
	}
}

// A proxy URL that cannot be parsed must not leak its password into the
// error, because the error reaches the admin console and the log.
func TestNormalizeProxyURLDoesNotLeakPassword(t *testing.T) {
	t.Parallel()
	proxyPassword := proxyTestPassword()
	// A port that is not a number makes url.Parse fail, and the raw URL
	// carries the password.
	raw := "http://alice:" + proxyPassword + "@host:port"
	_, err := NormalizeProxyURL("inst", "proxy_url", raw)
	if err == nil {
		t.Fatal("NormalizeProxyURL succeeded with an invalid URL")
	}
	if strings.Contains(err.Error(), proxyPassword) {
		t.Fatalf("error leaked the proxy password: %v", err)
	}
}
