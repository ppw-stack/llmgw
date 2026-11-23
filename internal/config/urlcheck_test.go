package config

import (
	"net/netip"
	"os"
	"strings"
	"testing"
)

// urlCheckTestKey returns the upstream key for the URL check tests. Set
// TEST_URLCHECK_API_KEY to inject a real one; without it the instance carries
// no key at all, because the check under test never looks at it and no
// credential literal belongs in the repository.
func urlCheckTestKey() string {
	return strings.TrimSpace(os.Getenv("TEST_URLCHECK_API_KEY"))
}

func TestCheckUpstreamHostRejectsPrivateTargets(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"http://127.0.0.1:8080",
		"http://localhost:9000",
		"https://10.0.0.5/v1",
		"https://192.168.1.10/v1",
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"https://[::1]/v1",
		"https://[fd00::1]/v1",
		"http://0.0.0.0:8080",
		"http://100.64.0.1/v1",
		"http://service.internal/v1",
		"http://printer.local/v1",
	}
	for _, raw := range blocked {
		if err := CheckUpstreamHost("inst", "endpoints[].base_url", raw, false); err == nil {
			t.Errorf("%s should be rejected", raw)
		}
	}

	allowed := []string{
		"https://api.example.com/v1",
		"https://api.minimax.cn/anthropic",
		"http://93.184.216.34/v1",
	}
	for _, raw := range allowed {
		if err := CheckUpstreamHost("inst", "endpoints[].base_url", raw, false); err != nil {
			t.Errorf("%s should be accepted: %v", raw, err)
		}
	}
}

func TestCheckUpstreamHostOptIn(t *testing.T) {
	t.Parallel()
	if err := CheckUpstreamHost("inst", "endpoints[].base_url", "http://127.0.0.1:9000", true); err != nil {
		t.Fatalf("allow_private_upstream should accept a local upstream: %v", err)
	}
}

func TestNormalizeInstancePrivateOptIn(t *testing.T) {
	t.Parallel()
	inst := Instance{
		Name:      "mock",
		APIKey:    urlCheckTestKey(),
		Endpoints: []Endpoint{{Protocol: ProtocolOpenAI, BaseURL: "http://127.0.0.1:9000"}},
		Models:    []string{"m"},
	}
	err := NormalizeInstance(&inst)
	if err == nil {
		t.Fatal("a private endpoint must be rejected without the opt-in")
	}
	if !strings.Contains(err.Error(), "allow_private_upstream") {
		t.Fatalf("error should name the opt-in: %v", err)
	}

	inst.AllowPrivateUpstream = true
	if err := NormalizeInstance(&inst); err != nil {
		t.Fatalf("opted-in instance rejected: %v", err)
	}
}

func TestBlockedAddress(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254",
		"100.64.1.1", "198.18.0.1", "0.0.0.0", "::1", "fc00::1", "fe80::1",
		"203.0.113.9",
	} {
		addr := mustAddr(t, raw)
		if !BlockedAddress(addr) {
			t.Errorf("%s should be blocked", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		addr := mustAddr(t, raw)
		if BlockedAddress(addr) {
			t.Errorf("%s should be allowed", raw)
		}
	}
}

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return addr
}
