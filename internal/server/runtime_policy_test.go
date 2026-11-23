package server

import (
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

// TestAdminSurfaceCanBeClosedAtRuntime: the console can be shut down without
// a restart, which is the emergency lever when it is suspected to be
// compromised. The data plane keeps working while it is closed.
func TestAdminSurfaceCanBeClosedAtRuntime(t *testing.T) {
	t.Parallel()
	gw, _, key, srv := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil)

	// Open by default: the admin token reaches the API.
	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin request while open = %d, want 200", resp.StatusCode)
	}

	if !srv.SetAdminEnabled(false) {
		t.Fatal("closing the console should report a change")
	}
	if srv.SetAdminEnabled(false) {
		t.Fatal("closing the console twice should report no change")
	}

	// Neither the session cookie nor the static admin token opens it.
	for _, path := range []string{"/admin/api/keys", "/admin/api/login"} {
		resp := adminRequest(t, http.MethodGet, gw.URL+path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s while closed = %d, want 403", path, resp.StatusCode)
		}
	}

	// The console page is not served either.
	resp = adminRequest(t, http.MethodGet, gw.URL+"/", "")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read console response body: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("console page while closed = %d, want 403", resp.StatusCode)
	}
	if strings.Contains(string(body), "<html") {
		t.Fatal("the console shell was served while the console is closed")
	}

	// An unknown admin path says the console is closed, not that it is unknown.
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/nope", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown admin path while closed = %d, want 403", resp.StatusCode)
	}

	// The data plane and the health endpoints are untouched.
	resp = post(t, gw.URL+"/openai/v1/chat/completions", key,
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("data plane while closed = %d, want 200", resp.StatusCode)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		resp = adminRequest(t, http.MethodGet, gw.URL+path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s while closed = %d, want 200", path, resp.StatusCode)
		}
	}

	// Re-opening restores the console right away.
	if !srv.SetAdminEnabled(true) {
		t.Fatal("re-opening the console should report a change")
	}
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin request after re-opening = %d, want 200", resp.StatusCode)
	}
}

// TestAdminSurfaceStartsClosedWhenConfigured: a gateway configured with
// admin_enabled: false never opens the console, not even at boot.
func TestAdminSurfaceStartsClosedWhenConfigured(t *testing.T) {
	t.Parallel()
	gw, _, _, _ := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil, func(o *Options) {
		closed := false
		o.AdminEnabled = &closed
	})

	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin request at boot with the console closed = %d, want 403", resp.StatusCode)
	}
}

// TestAdminSurfaceOpenByDefault: an embedder that says nothing about the
// console keeps the previous behaviour.
func TestAdminSurfaceOpenByDefault(t *testing.T) {
	t.Parallel()
	gw, _, _, _ := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil)
	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin request with no option set = %d, want 200", resp.StatusCode)
	}
}

// TestAdminWhitelistCanBeChangedAtRuntime: the network policy of the console
// follows the config without a restart.
func TestAdminWhitelistCanBeChangedAtRuntime(t *testing.T) {
	t.Parallel()
	gw, _, _, srv := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil)

	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin request without a whitelist = %d, want 200", resp.StatusCode)
	}

	// A whitelist that excludes the caller closes the console.
	srv.SetAdminPrefixes([]netip.Prefix{prefix(t, "10.0.0.0/8")})
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin request outside the whitelist = %d, want 403", resp.StatusCode)
	}

	// A whitelist that includes the caller opens it again.
	srv.SetAdminPrefixes([]netip.Prefix{prefix(t, "127.0.0.0/8")})
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin request inside the whitelist = %d, want 200", resp.StatusCode)
	}
}

// TestKeyLimitsCanBeChangedAtRuntime: the per-key budget follows the config
// without a restart.
func TestKeyLimitsCanBeChangedAtRuntime(t *testing.T) {
	t.Parallel()
	gw, _, key, srv := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil)

	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, gw.URL+"/openai/v1/chat/completions", key, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlimited request = %d, want 200", resp.StatusCode)
	}

	// One request per minute: the first one after the change fits the budget,
	// the second does not.
	srv.SetKeyLimits(1, 0)
	resp = post(t, gw.URL+"/openai/v1/chat/completions", key, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request inside the new limit = %d, want 200", resp.StatusCode)
	}
	resp = post(t, gw.URL+"/openai/v1/chat/completions", key, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request over the new limit = %d, want 429", resp.StatusCode)
	}

	// Lifting the limit again lets traffic through.
	srv.SetKeyLimits(0, 0)
	resp = post(t, gw.URL+"/openai/v1/chat/completions", key, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request after lifting the limit = %d, want 200", resp.StatusCode)
	}
}

// TestFallbackPageListsTheMetricsEndpointOnlyWhenItIsOn: the page that is
// served when the console is not built lists the endpoints that exist.
func TestFallbackPageListsTheMetricsEndpointOnlyWhenItIsOn(t *testing.T) {
	t.Parallel()
	gw, _, _, _ := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil, func(o *Options) {
		o.UI = nil
	})

	resp := adminRequest(t, http.MethodGet, gw.URL+"/", "")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read fallback page: %v", err)
	}
	resp.Body.Close()
	if strings.Contains(string(body), "/metrics") {
		t.Fatal("the fallback page lists the metrics endpoint while it is disabled")
	}

	gw2, _, _, _ := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil, func(o *Options) {
		o.UI = nil
		o.MetricsToken = "metrics-secret"
	})
	resp = adminRequest(t, http.MethodGet, gw2.URL+"/", "")
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read fallback page: %v", err)
	}
	resp.Body.Close()
	if !strings.Contains(string(body), "/metrics") {
		t.Fatal("the fallback page does not list the metrics endpoint while it is enabled")
	}
}

// TestMetricsEndpointFollowsTheTokenAtRuntime: the metrics endpoint is
// switched on and off with the config, without a restart.
func TestMetricsEndpointFollowsTheTokenAtRuntime(t *testing.T) {
	t.Parallel()
	gw, _, _, srv := setupConfigServer(t, nil, openaiOK, []string{"k"}, nil)

	// Without a token the endpoint does not exist.
	resp := adminRequest(t, http.MethodGet, gw.URL+"/metrics", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics without a token = %d, want 404", resp.StatusCode)
	}

	// A token switches it on, but the token is still required.
	srv.SetMetricsToken("metrics-secret")
	resp = adminRequest(t, http.MethodGet, gw.URL+"/metrics", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics without the token = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-secret")
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("metrics with the token = %d, want 200", authed.StatusCode)
	}

	// Dropping the token switches the endpoint off again.
	srv.SetMetricsToken("")
	resp = adminRequest(t, http.MethodGet, gw.URL+"/metrics", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics after dropping the token = %d, want 404", resp.StatusCode)
	}
}
