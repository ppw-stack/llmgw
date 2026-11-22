package store

import (
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestEnqueueDuringCloseDoesNotPanic(t *testing.T) {
	st := testStore(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				st.Enqueue(LogEntry{TS: time.Now(), RequestID: "x", Model: "m"})
			}
		}()
	}

	time.Sleep(5 * time.Millisecond)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	wg.Wait()

	if st.Enqueue(LogEntry{}) {
		t.Fatal("enqueue after close should be rejected")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestSessionExpiryUsesParsedTime(t *testing.T) {
	st := testStore(t)

	expired := "expired-token"
	if err := st.CreateSession(expired, "admin", "", "", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash(expired); err != ErrNotFound {
		t.Fatalf("expired session should be rejected, got %v", err)
	}

	// A session expiring shortly after the current second boundary must still
	// be accepted (the old string comparison could reject or accept wrongly).
	valid := "valid-token"
	if err := st.CreateSession(valid, "admin", "", "", time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash(valid); err != nil {
		t.Fatalf("valid session rejected: %v", err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	st := testStore(t)
	if err := st.CreateSession("old", "admin", "", "", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession("new", "admin", "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteExpiredSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash("old"); err != ErrNotFound {
		t.Fatalf("old session should be deleted, got %v", err)
	}
	if _, err := st.SessionByHash("new"); err != nil {
		t.Fatalf("new session should survive: %v", err)
	}
}

func TestRequestLogsAreWrittenAsynchronously(t *testing.T) {
	st := testStore(t)
	if !st.Enqueue(LogEntry{
		TS: time.Now(), RequestID: "req-1", APIKeyID: 1, IngressProtocol: "openai",
		Model: "m", Provider: "p", Endpoint: "openai", UpstreamModel: "m",
		Status: 200, PromptTokens: 3, CompletionTokens: 4, CostUSD: 0.5,
	}) {
		t.Fatal("enqueue rejected")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(LogFilter{Limit: 10})
		if err == nil && len(rows) == 1 {
			if rows[0].PromptTokens != 3 || rows[0].CompletionTokens != 4 {
				t.Fatalf("unexpected row: %+v", rows[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("log row was not written")
}
