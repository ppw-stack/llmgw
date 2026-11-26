package alert

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNotifierDeliversAndDeduplicates(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var received []Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err == nil {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := New(srv.URL, time.Minute, logger)

	n.Emit(Event{Type: EventBreakerOpen, Severity: "warning", Instance: "a", Title: "breaker", Message: "tripped"})
	// Duplicate within cooldown must be suppressed.
	n.Emit(Event{Type: EventBreakerOpen, Severity: "warning", Instance: "a", Title: "breaker", Message: "tripped again"})
	// Different instance must be delivered.
	n.Emit(Event{Type: EventUpstreamAuthError, Severity: "critical", Instance: "b", Status: 401, Title: "auth", Message: "401"})

	n.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("received %d events, want 2: %+v", len(received), received)
	}
	if received[0].Instance != "a" || received[0].Text == "" {
		t.Fatalf("unexpected first event: %+v", received[0])
	}
	if received[1].Instance != "b" || received[1].Status != 401 {
		t.Fatalf("unexpected second event: %+v", received[1])
	}
}
