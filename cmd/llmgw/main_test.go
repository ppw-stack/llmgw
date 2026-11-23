package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/secret"
	"github.com/pw/llmgw/internal/server"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/transport"
)

// testAPIKey returns the upstream key the test config carries. Set
// LLMGW_TEST_API_KEY to inject a real one; without it the instance carries no
// key at all, because the checks under test never look at it and no
// credential literal belongs in the repository.
func testAPIKey() string {
	return strings.TrimSpace(os.Getenv("LLMGW_TEST_API_KEY"))
}

// testAdminToken returns the admin token the test server accepts. Set
// LLMGW_TEST_ADMIN_TOKEN to inject a real one; without it the server runs
// without the legacy token, which these tests never present.
func testAdminToken() string {
	return strings.TrimSpace(os.Getenv("LLMGW_TEST_ADMIN_TOKEN"))
}

// testMetricsToken returns the token that guards the metrics endpoint. Set
// LLMGW_TEST_METRICS_TOKEN to inject a real one; without it a random token is
// generated at runtime, so the repository holds no credential literal.
func testMetricsToken(t *testing.T) string {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv("LLMGW_TEST_METRICS_TOKEN")); token != "" {
		return token
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate a metrics token: %v", err)
	}
	return hex.EncodeToString(buf)
}

// testConfig renders a config file whose management surface state is
// admin_enabled.
func testConfig(dbPath string, adminEnabled bool) string {
	return fmt.Sprintf(`
server:
  addr: ":0"
  admin_enabled: %t
database:
  path: %q
instances:
  - name: mock
    api_key: %q
    endpoints:
      - {protocol: openai, base_url: "https://example.com/v1"}
    models: [gpt-test]
models:
  - name: gpt-test
    instances:
      - name: mock
        upstream: gpt-test
`, adminEnabled, dbPath, testAPIKey())
}

// adminResponse asks the admin API for its status code without credentials:
// 401 means the console is open, 403 means it is closed. The body says why.
func adminResponse(handler http.Handler) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// waitFor polls until the condition holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition did not hold before the deadline")
}

// TestWatchConfigClosesTheManagementSurface: editing the config file shuts
// the console down and re-opens it, without restarting the gateway.
func TestWatchConfigClosesTheManagementSurface(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, testConfig(dbPath, true))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatalf("reload router: %v", err)
	}
	srv, err := server.New(server.Options{
		Router:       rt,
		Forwarder:    transport.New(rt, st, logger),
		Store:        st,
		Box:          box,
		AdminToken:   testAdminToken(),
		AdminEnabled: cfg.Server.AdminEnabled,
		Log:          logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	// The watcher takes its baseline when it is armed, so the edits below,
	// which land right after it was armed, are not missed.
	watchConfig(ctx, cfgPath, logger, func(c *config.Config) error {
		return reloadConfig(rt, st, box, srv, c)
	})

	code, _ := adminResponse(handler)
	if code != http.StatusUnauthorized {
		t.Fatalf("admin API before the change = %d, want 401", code)
	}

	writeFile(t, cfgPath, testConfig(dbPath, false))
	waitFor(t, 10*time.Second, func() bool {
		code, _ := adminResponse(handler)
		return code == http.StatusForbidden
	})

	writeFile(t, cfgPath, testConfig(dbPath, true))
	waitFor(t, 10*time.Second, func() bool {
		code, _ := adminResponse(handler)
		return code == http.StatusUnauthorized
	})
}

// TestWatchConfigAppliesTheAdminWhitelist: the network policy of the console
// follows the config file.
func TestWatchConfigAppliesTheAdminWhitelist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, testConfig(dbPath, true))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatalf("reload router: %v", err)
	}
	srv, err := server.New(server.Options{
		Router:       rt,
		Forwarder:    transport.New(rt, st, logger),
		Store:        st,
		Box:          box,
		AdminToken:   testAdminToken(),
		AdminEnabled: cfg.Server.AdminEnabled,
		Log:          logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	watchConfig(ctx, cfgPath, logger, func(c *config.Config) error {
		return reloadConfig(rt, st, box, srv, c)
	})

	// A whitelist that excludes the caller closes the console. The request
	// comes from 192.0.2.1, the address httptest uses.
	body := strings.Replace(testConfig(dbPath, true), "  admin_enabled: true",
		"  admin_enabled: true\n  admin_allowed_cidrs: [\"10.0.0.0/8\"]", 1)
	writeFile(t, cfgPath, body)
	waitFor(t, 10*time.Second, func() bool {
		code, _ := adminResponse(handler)
		return code == http.StatusForbidden
	})
	code, msg := adminResponse(handler)
	if code != http.StatusForbidden {
		t.Fatalf("admin API outside the whitelist = %d, want 403", code)
	}
	if !strings.Contains(msg, "network policy") {
		t.Fatalf("admin API was closed for the wrong reason: %s", msg)
	}
}

// TestReloadConfigKeepsTheSurfaceWhenTheTopologyIsRejected: a configuration
// the gateway refuses must not move the management surface either.
func TestReloadConfigKeepsTheSurfaceWhenTheTopologyIsRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, testConfig(dbPath, false))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatalf("reload router: %v", err)
	}
	srv, err := server.New(server.Options{
		Router:       rt,
		Forwarder:    transport.New(rt, st, logger),
		Store:        st,
		Box:          box,
		AdminToken:   testAdminToken(),
		AdminEnabled: cfg.Server.AdminEnabled,
		Log:          logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	// The console starts closed, as the file says.
	code, _ := adminResponse(handler)
	if code != http.StatusForbidden {
		t.Fatalf("admin API at boot = %d, want 403", code)
	}

	// A configuration with an unusable topology is rejected, so the console
	// stays as it was.
	broken := testConfig(dbPath, true)
	broken = broken + "\ninvalid_field: true\n"
	writeFile(t, cfgPath, broken)
	time.Sleep(1500 * time.Millisecond)
	code, _ = adminResponse(handler)
	if code != http.StatusForbidden {
		t.Fatalf("admin API after a rejected config = %d, want 403", code)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// metricsResponse asks the metrics endpoint for its status code without
// credentials.
func metricsResponse(handler http.Handler) int {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// TestWatchConfigEnablesTheMetricsEndpoint: the metrics endpoint follows the
// config file. Without a token it does not exist, and setting one switches it
// on without a restart.
func TestWatchConfigEnablesTheMetricsEndpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeFile(t, cfgPath, testConfig(dbPath, true))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatalf("reload router: %v", err)
	}
	srv, err := server.New(server.Options{
		Router:       rt,
		Forwarder:    transport.New(rt, st, logger),
		Store:        st,
		Box:          box,
		AdminToken:   testAdminToken(),
		AdminEnabled: cfg.Server.AdminEnabled,
		Log:          logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	watchConfig(ctx, cfgPath, logger, func(c *config.Config) error {
		return reloadConfig(rt, st, box, srv, c)
	})

	if code := metricsResponse(handler); code != http.StatusNotFound {
		t.Fatalf("metrics without a token = %d, want 404", code)
	}

	metricsToken := testMetricsToken(t)
	withToken := strings.Replace(testConfig(dbPath, true), "  admin_enabled: true",
		"  admin_enabled: true\n  metrics_token: \""+metricsToken+"\"", 1)
	writeFile(t, cfgPath, withToken)
	waitFor(t, 10*time.Second, func() bool {
		return metricsResponse(handler) == http.StatusUnauthorized
	})
	if code := metricsResponse(handler); code != http.StatusUnauthorized {
		t.Fatalf("metrics with a token but without the credential = %d, want 401", code)
	}
}
