package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func prefix(t *testing.T, raw string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		t.Fatalf("parse prefix %s: %v", raw, err)
	}
	return p.Masked()
}

func openaiOK(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
}

// TestAdminGuardRejectsUntrustedNetworks: the admin surface can be pinned to
// the operator's network so a public listener does not expose the console.
func TestAdminGuardRejectsUntrustedNetworks(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, openaiOK, nil, func(o *Options) {
		o.AdminPrefixes = []netip.Prefix{prefix(t, "10.0.0.0/8")}
	})
	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("untrusted admin request = %d, want 403", resp.StatusCode)
	}

	gw, _, _ = setup(t, nil, openaiOK, nil, func(o *Options) {
		o.AdminPrefixes = []netip.Prefix{prefix(t, "127.0.0.0/8")}
	})
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trusted admin request = %d, want 200", resp.StatusCode)
	}
}

// TestKeyRateLimit: one key cannot spend without bound.
func TestKeyRateLimit(t *testing.T) {
	t.Parallel()
	gw, _, key := setup(t, nil, openaiOK, nil, func(o *Options) {
		o.KeyRPM = 2
	})
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`
	for i := 1; i <= 2; i++ {
		resp := post(t, gw.URL+"/openai/v1/chat/completions", key, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, resp.StatusCode)
		}
	}
	resp := post(t, gw.URL+"/openai/v1/chat/completions", key, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 should advertise Retry-After")
	}
}

// TestKeyConcurrencyCap: a key cannot hold unlimited streams open.
func TestKeyConcurrencyCap(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	gw, _, key := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		openaiOK(w, r)
	}, nil, func(o *Options) {
		o.KeyMaxConcurrent = 1
	})

	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp := post(t, gw.URL+"/openai/v1/chat/completions", key, body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("first request never reached the upstream")
	}

	resp := post(t, gw.URL+"/openai/v1/chat/completions", key, body)
	resp.Body.Close()
	close(release)
	wg.Wait()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("concurrent request = %d, want 429", resp.StatusCode)
	}
}

// TestMetricsAreDisabledWithoutToken: without metrics_token the endpoint
// does not exist at all. There is no credential to check, so serving the
// traffic volumes to whoever asks is not an option - not even to the host
// itself.
func TestMetricsAreDisabledWithoutToken(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, openaiOK, nil)
	handler := gw.Config.Handler

	public := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	public.RemoteAddr = "203.0.113.20:5555"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, public)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public metrics = %d, want 404", rec.Code)
	}

	local := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	local.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, local)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("local metrics = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Fatal("the disabled metrics endpoint leaked the metrics")
	}
}

// TestForwardedProtoOnlyTrustedFromProxy: a client cannot claim to be on HTTPS
// to influence cookie flags or HSTS.
func TestForwardedProtoOnlyTrustedFromProxy(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, openaiOK, nil, func(o *Options) {
		o.TrustedProxies = []netip.Prefix{prefix(t, "10.0.0.0/8")}
	})
	handler := gw.Config.Handler

	spoofed := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	spoofed.RemoteAddr = "203.0.113.30:5555"
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, spoofed)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("spoofed X-Forwarded-Proto produced HSTS: %q", got)
	}

	proxied := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	proxied.RemoteAddr = "10.0.0.5:5555"
	proxied.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, proxied)
	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("trusted proxy X-Forwarded-Proto should enable HSTS")
	}
}
