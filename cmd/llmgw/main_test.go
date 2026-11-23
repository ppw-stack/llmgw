package main

import (
	"context"
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
    api_key: "k"
    endpoints:
      - {protocol: openai, base_url: "https://example.com/v1"}
    models: [gpt-test]
models:
  - name: gpt-test
    upstream: gpt-test
    instances:
      - name: mock
`, adminEnabled, dbPath)
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
		AdminToken:   "admin-test",
		AdminEnabled: cfg.Server.AdminEnabled,
		Log:          logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	go watchConfig(ctx, cfgPath, logger, func(c *config.Config) error {
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
		AdminToken:   "admin-test",
		AdminEnabled: cfg.Server.AdminEnabled,
		Log:          logger,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	go watchConfig(ctx, cfgPath, logger, func(c *config.Config) error {
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
		AdminToken:   "admin-test",
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
