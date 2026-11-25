package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// An in-flight query must not write its stale report back after the entry has
// been invalidated, otherwise the console keeps showing the result of the old
// proxy or credentials for a whole TTL.
func TestInvalidateWinsOverInFlightReport(t *testing.T) {
	t.Parallel()

	// The upstream blocks until the test releases it, so the report is
	// guaranteed to be in flight while Invalidate runs.
	released := make(chan struct{})
	release := make(chan struct{})
	var upstreamHit sync.WaitGroup
	upstreamHit.Add(1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit.Done()
		select {
		case <-release:
		case <-released:
		case <-time.After(5 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-26T08:00:00.000Z"}}}`))
	}))
	t.Cleanup(upstream.Close)

	client := NewClient()
	client.ResolveURL = func(string) string { return upstream.URL }
	svc := NewServiceWithClient(client, time.Minute)

	target := Target{
		ID:       9,
		Name:     "opencode-inflight",
		PresetID: "opencode-go",
		APIKey:   "sk-test-placeholder",
	}

	done := make(chan Report, 1)
	go func() {
		done <- svc.Report(context.Background(), target)
	}()

	// Wait for the upstream request to be in flight.
	upstreamHit.Wait()

	// Invalidate runs while the query is in flight. It may wait for the
	// per-instance lock, so the upstream is released from another goroutine
	// to let the in-flight query finish.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(released)
	}()
	svc.Invalidate(target.ID)

	<-done

	// The invalidated entry must not come back.
	if _, ok := svc.cache.get(target.ID, svc.ttl); ok {
		t.Fatal("the invalidated report was written back by the in-flight query")
	}
}
