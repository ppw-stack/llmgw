package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceUsesTargetProxyAndInvalidateClearsCache(t *testing.T) {
	t.Parallel()

	var proxyHits atomic.Int64
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-26T08:00:00.000Z"}}}`))
	}))
	t.Cleanup(proxySrv.Close)

	client := NewClient()
	// Point the quota endpoint to an HTTP URL so the test HTTP proxy receives
	// a plain proxy request directly without TLS CONNECT.
	client.ResolveURL = func(string) string { return "http://quota.example.test/usage" }
	svc := NewServiceWithClient(client, time.Minute)

	target := Target{
		ID:       7,
		Name:     "opencode-proxied",
		PresetID: "opencode-go",
		APIKey:   "sk-test-placeholder",
		ProxyURL: proxySrv.URL,
	}

	report := svc.Report(context.Background(), target)
	if report.Error != "" {
		t.Fatalf("report.Error = %q", report.Error)
	}
	if len(report.Windows) != 1 || report.Windows[0].UsedPct != 42 {
		t.Fatalf("windows = %+v", report.Windows)
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxyHits = %d, want 1", proxyHits.Load())
	}

	// Second call within TTL hits cache.
	_ = svc.Report(context.Background(), target)
	if proxyHits.Load() != 1 {
		t.Fatalf("expected cached hit, proxyHits = %d", proxyHits.Load())
	}

	// Invalidate drops the cached entry so the next call re-queries through the proxy.
	svc.Invalidate(target.ID)
	_ = svc.Report(context.Background(), target)
	if proxyHits.Load() != 2 {
		t.Fatalf("expected fresh query after Invalidate, proxyHits = %d", proxyHits.Load())
	}
}
