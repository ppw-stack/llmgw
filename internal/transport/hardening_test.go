package transport

import (
	"context"
	"encoding/json"
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
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

// testHeaderValue returns the value of a header the tests set. The
// environment variable injects a real one; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func testHeaderValue(env, placeholder string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return placeholder
}

func chatBody(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
}

// TestSensitiveHeadersAreNotForwarded makes sure the gateway's own credentials
// and the caller's network identity stay on the gateway side. The admin console
// and the data plane share one origin, so a browser attaches the admin session
// cookie to API calls; forwarding it would hand a console session to the model
// vendor.
func TestSensitiveHeadersAreNotForwarded(t *testing.T) {
	t.Parallel()
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, `{"ok":true,"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)

	fw, _, targets := newForwarder(t, upstream.URL, 5*time.Second)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fw.Forward(w, r, Request{
			Protocol: config.ProtocolOpenAI,
			Meta:     ingress.Meta{Model: "m", RawBody: chatBody("m")},
			Targets:  targets,
		})
	}))
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodPost, proxy.URL, strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	session := testHeaderValue("LLMGW_TEST_SESSION_COOKIE", "session-placeholder")
	req.Header.Set("Cookie", "llmgw_session="+session)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("X-Real-IP", "203.0.113.8")
	req.Header.Set("X-Forwarded-Proto", "https")
	internalToken := testHeaderValue("LLMGW_TEST_INTERNAL_TOKEN", "header-placeholder")
	req.Header.Set("X-Internal-Token", internalToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	for _, name := range []string{"Cookie", "X-Forwarded-For", "X-Real-Ip", "X-Forwarded-Proto"} {
		if v := got.Get(name); v != "" {
			t.Errorf("%s leaked upstream: %q", name, v)
		}
	}
	if got.Get("X-Internal-Token") != internalToken {
		t.Error("ordinary client headers must still be forwarded")
	}
	if got.Get("Authorization") != "Bearer "+upstreamTestKey() {
		t.Errorf("upstream key not set: %q", got.Get("Authorization"))
	}
}

// TestUpstreamResponseHeadersCannotOverrideGateway checks that an upstream
// cannot plant cookies on the gateway origin or replace its security headers.
func TestUpstreamResponseHeadersCannotOverrideGateway(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		planted := testHeaderValue("LLMGW_TEST_UPSTREAM_COOKIE", "planted-placeholder")
		w.Header().Set("Set-Cookie", "llmgw_session="+planted+"; Path=/")
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Header().Set("Strict-Transport-Security", "max-age=0")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	fw, _, targets := newForwarder(t, upstream.URL, 5*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{}`))
	fw.Forward(rec, req, Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m", RawBody: chatBody("m")},
		Targets:  targets,
	})

	if v := rec.Header().Get("Set-Cookie"); v != "" {
		t.Errorf("upstream Set-Cookie was forwarded: %q", v)
	}
	if v := rec.Header().Get("Content-Security-Policy"); v != "" {
		t.Errorf("upstream CSP was forwarded: %q", v)
	}
	if v := rec.Header().Get("Strict-Transport-Security"); v != "" {
		t.Errorf("upstream HSTS was forwarded: %q", v)
	}
	if v := rec.Header().Get("Cache-Control"); v != "no-store" {
		t.Errorf("prompt/completion response must not be cacheable, got %q", v)
	}
}

// TestRedirectsAreNotFollowed: replaying a request to whatever an upstream
// points at would also replay the upstream key.
func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	var followed bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(elsewhere.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(upstream.Close)

	fw, _, targets := newForwarder(t, upstream.URL, 5*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{}`))
	fw.Forward(rec, req, Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m", RawBody: chatBody("m")},
		Targets:  targets,
	})
	if followed {
		t.Fatal("the gateway followed an upstream redirect")
	}
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want the redirect passed through", rec.Code)
	}
}

// TestDialGuardBlocksPrivateUnlessAllowed covers the runtime half of the SSRF
// defence: even if an operator saves a private endpoint, the instance must opt
// in before the request is dialed.
func TestDialGuardBlocksPrivateUnlessAllowed(t *testing.T) {
	t.Parallel()
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "guard.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	build := func(allowPrivate bool) (*Forwarder, []router.Target) {
		cfg := &config.Config{
			Instances: []config.Instance{{
				Name:                 "local",
				APIKey:               upstreamTestKey(),
				AllowPrivateUpstream: allowPrivate,
				Endpoints:            []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: upstream.URL}},
				Models:               []string{"upstream-m"},
			}},
			Models: []config.Model{{
				Name:      "m",
				Upstream:  "upstream-m",
				Instances: []config.ModelInstance{{Name: "local"}},
			}},
		}
		rt := router.New()
		if err := rt.Reload(cfg); err != nil {
			t.Fatal(err)
		}
		targets, err := rt.Resolve(config.ProtocolOpenAI, "m")
		if err != nil {
			t.Fatal(err)
		}
		fw := NewWithTimeout(rt, st, logger, 2*time.Second)
		fw.SetBlockPrivate(true)
		return fw, targets
	}

	fw, targets := build(false)
	rec := httptest.NewRecorder()
	fw.Forward(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`)), Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m", RawBody: chatBody("m")},
		Targets:  targets,
	})
	if hits != 0 {
		t.Fatal("a private upstream was dialed without allow_private_upstream")
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("blocked request reported success: %d", rec.Code)
	}

	fw, targets = build(true)
	rec = httptest.NewRecorder()
	fw.Forward(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`)), Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m", RawBody: chatBody("m")},
		Targets:  targets,
	})
	if hits != 1 || rec.Code != http.StatusOK {
		t.Fatalf("opted-in instance should reach its upstream: hits=%d status=%d body=%s",
			hits, rec.Code, rec.Body.String())
	}
}

// TestForwardedErrorBodyIsJSON guards the client contract while the header
// filtering above changes.
func TestForwardedErrorBodyIsJSON(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow down"}`)
	}))
	t.Cleanup(upstream.Close)

	fw, _, targets := newForwarder(t, upstream.URL, 5*time.Second)
	rec := httptest.NewRecorder()
	fw.Forward(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`)), Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m", RawBody: chatBody("m")},
		Targets:  targets,
	})
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("upstream error body was not passed through: %q", rec.Body.String())
	}
}
