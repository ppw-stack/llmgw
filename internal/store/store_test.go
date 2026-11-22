package store

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
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

func TestProviderCRUD(t *testing.T) {
	st := testStore(t)
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("sk-upstream")
	if err != nil {
		t.Fatal(err)
	}

	rec, err := st.CreateProvider(ProviderSpec{
		Name:             "glm",
		EncryptedKey:     enc,
		Models:           config.Models{"glm-4.6": "glm-4.6"},
		OpenAIBaseURL:    "https://a/v4",
		AnthropicBaseURL: "https://a/anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == 0 {
		t.Fatal("missing id")
	}

	providers, err := st.Providers(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].APIKey != "sk-upstream" {
		t.Fatalf("providers = %+v", providers)
	}

	if err := st.UpdateProvider(rec.ID, ProviderSpec{
		Name:          "glm",
		Preset:        "minimax-mplan-global",
		EncryptedKey:  enc,
		Models:        config.Models{"m": "m"},
		OpenAIBaseURL: "",
	}); err != nil {
		t.Fatal(err)
	}
	records, err := st.ListProviderRecords()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Preset != "minimax-mplan-global" || records[0].OpenAIBaseURL != "" {
		t.Fatalf("record = %+v", records[0])
	}

	if err := st.DeleteProvider(rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProvider(rec.ID); err != ErrNotFound {
		t.Fatalf("second delete = %v", err)
	}
}

func TestLegacyProviderTableIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE providers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		proxy TEXT NOT NULL DEFAULT '',
		timeout_ms INTEGER NOT NULL DEFAULT 120000,
		keys_enc TEXT NOT NULL DEFAULT '[]',
		endpoints_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := json.Marshal([]string{"enc-first", "enc-second"})
	endpoints, _ := json.Marshal([]map[string]any{
		{"name": "openai", "protocol": "openai", "base_url": "https://a/v4",
			"models": map[string]string{"glm-4.6": "glm-4.6"}},
		{"name": "anthropic", "protocol": "anthropic", "base_url": "https://a/anthropic/",
			"models": map[string]string{"glm-4.6": "glm-4.6", "claude-sonnet-4-5": "glm-4.6"}},
	})
	if _, err := db.Exec(`INSERT INTO providers(name, proxy, timeout_ms, keys_enc,
		endpoints_json, created_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
		"glm", "http://proxy:7890", 180000, string(keys), string(endpoints),
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := Open(path, logger)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer st.Close()

	records, err := st.ListProviderRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	rec := records[0]
	if rec.Name != "glm" || rec.Preset != "" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.EncryptedKey != "enc-first" {
		t.Fatalf("key = %q", rec.EncryptedKey)
	}
	if rec.OpenAIBaseURL != "https://a/v4" || rec.AnthropicBaseURL != "https://a/anthropic" {
		t.Fatalf("urls = %q %q", rec.OpenAIBaseURL, rec.AnthropicBaseURL)
	}
	if rec.Models["glm-4.6"] != "glm-4.6" || rec.Models["claude-sonnet-4-5"] != "glm-4.6" {
		t.Fatalf("models = %v", rec.Models)
	}
	if rec.CreatedAt.IsZero() {
		t.Fatal("timestamp lost")
	}

	// Reopening must not migrate again.
	st2, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if n, err := st2.CountProviders(); err != nil || n != 1 {
		t.Fatalf("count after reopen = %d, %v", n, err)
	}
}
