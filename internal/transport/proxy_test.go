package transport

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

// proxyTestPassword returns the password used by the proxy tests. The
// environment variable injects a real one; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func proxyTestPassword() string {
	if v := strings.TrimSpace(os.Getenv("TEST_PROXY_PASSWORD")); v != "" {
		return v
	}
	return "test-proxy-placeholder-password"
}

func TestForwardUsesInstanceProxyWithBlockPrivateEnabled(t *testing.T) {
	t.Parallel()

	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	}))
	t.Cleanup(upstream.Close)

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxyuser:"+proxyTestPassword()))
	var proxyHits atomic.Int64
	var gotProxyAuth atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		gotProxyAuth.Store(r.Header.Get("Proxy-Authorization"))
		// Forward the plain HTTP request to the upstream mock.
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.URL+r.URL.Path, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			if strings.EqualFold(k, "Proxy-Authorization") {
				continue
			}
			for _, v := range vs {
				outReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxySrv.Close)

	parsedProxy, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyWithAuth := (&url.URL{
		Scheme: "http",
		User:   url.UserPassword("proxyuser", proxyTestPassword()),
		Host:   parsedProxy.Host,
	}).String()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "proxy.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Instances: []config.Instance{
			{
				Name:      "proxied",
				APIKey:    upstreamTestKey(),
				ProxyURL:  proxyWithAuth,
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "http://upstream.example.test/v1"}},
				Models:    []string{"m-up"},
			},
			{
				Name:      "direct-private",
				APIKey:    upstreamTestKey(),
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: upstream.URL}},
				Models:    []string{"m-up"},
			},
		},
		Models: []config.Model{
			{
				Name:      "m-proxied",
				Instances: []config.ModelInstance{{Name: "proxied", Upstream: "m-up"}},
			},
			{
				Name:      "m-direct",
				Instances: []config.ModelInstance{{Name: "direct-private", Upstream: "m-up"}},
			},
		},
	}

	rt := router.New()
	if err := rt.Reload(cfg); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	fw := NewWithTimeout(rt, st, logger, 5*time.Second)
	fw.SetBlockPrivate(true)

	// 1. Proxied instance reaches local 127.0.0.1 proxy even when blockPrivate is true.
	targets, err := rt.Resolve(config.ProtocolOpenAI, "m-proxied")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fw.Forward(rec, httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{}`)), Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m-proxied", RawBody: chatBody("m-proxied")},
		Targets:  targets,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("proxied status = %d body = %s", rec.Code, rec.Body.String())
	}
	if proxyHits.Load() != 1 || upstreamHits.Load() != 1 {
		t.Fatalf("hits: proxy=%d upstream=%d", proxyHits.Load(), upstreamHits.Load())
	}
	if got, _ := gotProxyAuth.Load().(string); got != wantAuth {
		t.Fatalf("Proxy-Authorization = %q, want %q", got, wantAuth)
	}

	// 2. Direct instance without proxy_url or allow_private_upstream is still blocked.
	directTargets, err := rt.Resolve(config.ProtocolOpenAI, "m-direct")
	if err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	fw.Forward(rec2, httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{}`)), Request{
		Protocol: config.ProtocolOpenAI,
		Meta:     ingress.Meta{Model: "m-direct", RawBody: chatBody("m-direct")},
		Targets:  directTargets,
	})
	if rec2.Code == http.StatusOK {
		t.Fatalf("direct private upstream should still be blocked, got %d", rec2.Code)
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("direct private upstream should not have been reached, hits=%d", upstreamHits.Load())
	}
}
