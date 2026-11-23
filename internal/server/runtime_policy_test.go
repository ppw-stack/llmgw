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
	body, _ := io.ReadAll(resp.Body)
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
