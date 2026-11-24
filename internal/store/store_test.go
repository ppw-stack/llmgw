package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return testStoreAt(t, filepath.Join(t.TempDir(), "test.db"))
}

// testStoreAt opens a store on an existing database file.
func testStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Open(ctx, path,
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
		Status: 200, PromptTokens: 3, CompletionTokens: 4,
		CacheReadTokens: 5, CacheWriteTokens: 6,
	}) {
		t.Fatal("enqueue rejected")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(LogFilter{Limit: 10})
		if err == nil && len(rows) == 1 {
			if rows[0].PromptTokens != 3 || rows[0].CompletionTokens != 4 ||
				rows[0].CacheReadTokens != 5 || rows[0].CacheWriteTokens != 6 {
				t.Fatalf("unexpected row: %+v", rows[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("log row was not written")
}

func TestUsageAggregatesCacheTokens(t *testing.T) {
	st := testStore(t)
	if !st.Enqueue(LogEntry{
		TS: time.Now(), RequestID: "req-cache", APIKeyID: 1, IngressProtocol: "openai",
		Model: "m", Provider: "p", Endpoint: "openai", UpstreamModel: "m",
		Status: 200, PromptTokens: 10, CompletionTokens: 2,
		CacheReadTokens: 6, CacheWriteTokens: 3,
	}) {
		t.Fatal("enqueue rejected")
	}

	written := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.Logs(LogFilter{Limit: 10})
		if err == nil && len(rows) == 1 {
			written = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !written {
		t.Fatal("log row was not written")
	}

	rep, err := st.Usage(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Daily) != 1 {
		t.Fatalf("daily = %+v", rep.Daily)
	}
	if rep.Daily[0].CacheReadTokens != 6 || rep.Daily[0].CacheWriteTokens != 3 {
		t.Fatalf("daily cache tokens = %+v", rep.Daily[0])
	}
}

// TestUsageDailyCacheColumnsAreAdded checks that a database created before
// the cache counters existed still opens and gains both columns.
func TestUsageDailyCacheColumnsAreAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE usage_daily (
		day               TEXT    NOT NULL,
		api_key_id        INTEGER NOT NULL,
		model             TEXT    NOT NULL,
		provider          TEXT    NOT NULL,
		requests          INTEGER NOT NULL DEFAULT 0,
		prompt_tokens     INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		cost_usd          REAL    NOT NULL DEFAULT 0,
		PRIMARY KEY (day, api_key_id, model, provider)
	)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := testStoreAt(t, path)
	var count int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('usage_daily')
		 WHERE name IN ('cache_read_tokens', 'cache_write_tokens')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("cache columns = %d, want 2", count)
	}
}

// testQuotaCredentialValues are deliberately fake credential fixtures. They
// only exercise the encrypt/store/read path and never match a real account.
const (
	testQuotaAccessKeyID     = "test-access-key-id"
	testQuotaSecretAccessKey = "test-secret-access-key"
)

func TestInstanceQuotaCredentialRoundTrip(t *testing.T) {
	st := testStore(t)
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := encryptQuotaCredential(box, config.QuotaCredential{
		AccessKeyID:     testQuotaAccessKeyID,
		SecretAccessKey: testQuotaSecretAccessKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "" {
		t.Fatal("the credential was not encrypted")
	}
	if strings.Contains(encrypted, testQuotaSecretAccessKey) {
		t.Fatal("the credential is stored in the clear")
	}

	if _, err := st.CreateInstance(InstanceSpec{
		Name:                     "ark",
		Preset:                   "ark-coding-plan-cn",
		EncryptedQuotaCredential: encrypted,
		Models:                   []string{"ark-code-latest"},
	}); err != nil {
		t.Fatal(err)
	}
	instances, _, err := st.Topology(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %+v", instances)
	}
	if instances[0].QuotaCredential.AccessKeyID != testQuotaAccessKeyID ||
		instances[0].QuotaCredential.SecretAccessKey != testQuotaSecretAccessKey {
		t.Fatalf("credential = %+v", instances[0].QuotaCredential)
	}
}

// TestInstanceQuotaCredentialColumnIsAdded checks that a database from
// before the quota credential existed still opens and gains the column.
func TestInstanceQuotaCredentialColumnIsAdded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE instances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		preset TEXT NOT NULL DEFAULT '',
		key_enc TEXT NOT NULL DEFAULT '',
		endpoints_json TEXT NOT NULL DEFAULT '[]',
		models_json TEXT NOT NULL DEFAULT '[]',
		max_fails INTEGER NOT NULL DEFAULT 3,
		cooldown_secs INTEGER NOT NULL DEFAULT 30,
		timeout_secs INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := testStoreAt(t, path)
	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
	if _, err := st.CreateInstance(InstanceSpec{Name: "ark", Models: []string{"ark-code-latest"}}); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceAndModelCRUD(t *testing.T) {
	st := testStore(t)
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("sk-upstream")
	if err != nil {
		t.Fatal(err)
	}

	rec, err := st.CreateInstance(InstanceSpec{
		Name:         "glm",
		EncryptedKey: enc,
		Endpoints: []config.Endpoint{
			{Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v4"},
			{Protocol: config.ProtocolAnthropic, BaseURL: "https://a/anthropic"},
		},
		Models:       []string{"glm-4.6"},
		MaxFails:     2,
		CooldownSecs: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == 0 {
		t.Fatal("missing id")
	}

	instances, models, err := st.Topology(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].APIKey != "sk-upstream" {
		t.Fatalf("instances = %+v", instances)
	}
	if len(instances[0].Endpoints) != 2 {
		t.Fatalf("endpoints = %+v", instances[0].Endpoints)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v", models)
	}

	if err := st.UpdateInstance(rec.ID, InstanceSpec{
		Name:         "glm",
		Preset:       "minimax-mplan-global",
		EncryptedKey: enc,
		Endpoints:    []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://b/v1"}},
		Models:       []string{"glm-4.6"},
	}); err != nil {
		t.Fatal(err)
	}
	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Preset != "minimax-mplan-global" || len(records[0].Endpoints) != 1 {
		t.Fatalf("record = %+v", records[0])
	}

	modelRec, err := st.CreateModel(ModelSpec{
		Name:     "glm-4.6",
		Strategy: config.StrategyFailover,
		Instances: []ModelInstanceSpec{
			{Instance: "glm", Upstream: "glm-4.6", Priority: 0},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelRec.ID == 0 {
		t.Fatal("missing model id")
	}
	_, models, err = st.Topology(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || len(models[0].Instances) != 1 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Instances[0].Name != "glm" {
		t.Fatalf("model instances = %+v", models[0].Instances)
	}

	// An instance that still serves a model cannot be deleted.
	if err := st.DeleteInstance(rec.ID); err == nil {
		t.Fatal("expected the delete to be refused")
	}

	if err := st.DeleteModel(modelRec.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteInstance(rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteInstance(rec.ID); err != ErrNotFound {
		t.Fatalf("second delete = %v", err)
	}
}

func TestModelCannotReferenceAnUnknownInstance(t *testing.T) {
	st := testStore(t)
	_, err := st.CreateModel(ModelSpec{
		Name:      "m",
		Strategy:  config.StrategyFailover,
		Instances: []ModelInstanceSpec{{Instance: "ghost", Upstream: "m"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestModelWithSeveralInstancesReadsBackAsOneModel guards the join that
// produces one row per candidate.
func TestModelWithSeveralInstancesReadsBackAsOneModel(t *testing.T) {
	st := testStore(t)
	for i, name := range []string{"a", "b"} {
		if _, err := st.CreateInstance(InstanceSpec{
			Name:      name,
			Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://x/v1"}},
			Models:    []string{fmt.Sprintf("m-%d", i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateModel(ModelSpec{
		Name:     "m",
		Strategy: config.StrategyFailover,
		Instances: []ModelInstanceSpec{
			{Instance: "a", Upstream: "m-0", Priority: 0},
			{Instance: "b", Upstream: "m-1", Priority: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	records, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if len(records[0].Instances) != 2 {
		t.Fatalf("candidates = %+v", records[0].Instances)
	}
	if records[0].Instances[0].Instance != "a" || records[0].Instances[1].Instance != "b" {
		t.Fatalf("candidates not ordered by priority: %+v", records[0].Instances)
	}
	if records[0].Instances[0].Upstream != "m-0" || records[0].Instances[1].Upstream != "m-1" {
		t.Fatalf("candidate upstreams = %+v", records[0].Instances)
	}
}

func TestLegacyEndpointAcceptsBothJSONForms(t *testing.T) {
	cases := []string{
		`{"Protocol":"openai","BaseURL":"https://x/v1","Models":{"m":"m"}}`,
		`{"protocol":"openai","base_url":"https://x/v1","models":{"m":"m"}}`,
	}
	for _, raw := range cases {
		var ep legacyEndpoint
		if err := json.Unmarshal([]byte(raw), &ep); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if ep.Protocol != "openai" || ep.BaseURL != "https://x/v1" || ep.Models["m"] != "m" {
			t.Fatalf("%s -> %+v", raw, ep)
		}
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
	// The old release marshalled config.Endpoint with Go field names (it only
	// carried yaml tags), so the migration must accept BaseURL, not base_url.
	endpoints, _ := json.Marshal([]map[string]any{
		{"Name": "openai", "Protocol": "openai", "BaseURL": "https://a/v4",
			"Models": map[string]string{"glm-4.6": "glm-4.6"}},
		{"Name": "anthropic", "Protocol": "anthropic", "BaseURL": "https://a/anthropic/",
			"Models": map[string]string{"glm-4.6": "glm-4.6", "claude-sonnet-4-5": "glm-4.6"}},
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer st.Close()

	records, err := st.ListInstanceRecords()
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
	if len(rec.Endpoints) != 2 {
		t.Fatalf("endpoints = %+v", rec.Endpoints)
	}
	if rec.Endpoints[0].BaseURL != "https://a/v4" || rec.Endpoints[1].BaseURL != "https://a/anthropic" {
		t.Fatalf("urls = %+v", rec.Endpoints)
	}
	if rec.CreatedAt.IsZero() {
		t.Fatal("timestamp lost")
	}
	if len(rec.Models) != 1 || rec.Models[0] != "glm-4.6" {
		t.Fatalf("migrated declaration = %v", rec.Models)
	}

	models, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	names := map[string]string{}
	for _, m := range models {
		if len(m.Instances) != 1 || m.Instances[0].Instance != "glm" {
			t.Fatalf("model %q instances = %+v", m.Name, m.Instances)
		}
		names[m.Name] = m.Instances[0].Upstream
	}
	if names["glm-4.6"] != "glm-4.6" || names["claude-sonnet-4-5"] != "glm-4.6" {
		t.Fatalf("models = %v", names)
	}

	// Reopening must not migrate again.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	st2, err := Open(ctx2, path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if n, err := st2.CountInstances(); err != nil || n != 1 {
		t.Fatalf("count after reopen = %d, %v", n, err)
	}
	if n, err := st2.CountModels(); err != nil || n != 2 {
		t.Fatalf("model count after reopen = %d, %v", n, err)
	}
}

// TestPresetProviderTableIsMigrated covers the preset-aware legacy table.
func TestPresetProviderTableIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-preset.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE providers (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		name              TEXT NOT NULL UNIQUE,
		preset            TEXT NOT NULL DEFAULT '',
		key_enc           TEXT NOT NULL DEFAULT '',
		models_json       TEXT NOT NULL DEFAULT '{}',
		openai_base_url   TEXT NOT NULL DEFAULT '',
		anthropic_base_url TEXT NOT NULL DEFAULT '',
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	models, _ := json.Marshal(map[string]string{"glm-4.6": "glm-4.6"})
	if _, err := db.Exec(`INSERT INTO providers(name, preset, key_enc, models_json,
		openai_base_url, anthropic_base_url, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		"glm", "", "enc-key", string(models), "https://a/v4", "https://a/anthropic",
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer st.Close()

	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].EncryptedKey != "enc-key" {
		t.Fatalf("records = %+v", records)
	}
	if len(records[0].Endpoints) != 2 {
		t.Fatalf("endpoints = %+v", records[0].Endpoints)
	}
	if len(records[0].Models) != 1 || records[0].Models[0] != "glm-4.6" {
		t.Fatalf("declaration = %v", records[0].Models)
	}
	modelRecords, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(modelRecords) != 1 || modelRecords[0].Name != "glm-4.6" {
		t.Fatalf("models = %+v", modelRecords)
	}
}

// TestInstanceModelsAreBackfilled covers an upgrade from the release that had
// the instance table but not the capability column yet.
func TestInstanceModelsAreBackfilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-models.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE instances (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			name            TEXT    NOT NULL UNIQUE,
			preset          TEXT    NOT NULL DEFAULT '',
			key_enc         TEXT    NOT NULL DEFAULT '',
			endpoints_json  TEXT    NOT NULL DEFAULT '[]',
			max_fails       INTEGER NOT NULL DEFAULT 3,
			cooldown_secs   INTEGER NOT NULL DEFAULT 30,
			timeout_secs    INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT    NOT NULL,
			updated_at      TEXT    NOT NULL
		)`,
		`CREATE TABLE models (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT    NOT NULL UNIQUE,
			upstream    TEXT    NOT NULL DEFAULT '',
			strategy    TEXT    NOT NULL DEFAULT 'failover',
			created_at  TEXT    NOT NULL,
			updated_at  TEXT    NOT NULL
		)`,
		`CREATE TABLE model_instances (
			model_id    INTEGER NOT NULL,
			instance_id INTEGER NOT NULL,
			priority    INTEGER NOT NULL DEFAULT 0,
			weight      INTEGER NOT NULL DEFAULT 100,
			PRIMARY KEY (model_id, instance_id)
		)`,
		`INSERT INTO instances(id, name, created_at, updated_at)
			VALUES (1, 'old-instance', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO models(id, name, upstream, created_at, updated_at)
			VALUES (1, 'old-model', 'old-upstream', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO model_instances(model_id, instance_id) VALUES (1, 1)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if len(records[0].Models) != 1 || records[0].Models[0] != "old-upstream" {
		t.Fatalf("models = %v", records[0].Models)
	}

	// The old model-level upstream is moved onto the candidate and the
	// column is dropped from the models table.
	modelRecords, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(modelRecords) != 1 || len(modelRecords[0].Instances) != 1 {
		t.Fatalf("model records = %+v", modelRecords)
	}
	if got := modelRecords[0].Instances[0].Upstream; got != "old-upstream" {
		t.Fatalf("candidate upstream = %q", got)
	}
	modelCols, err := tableColumns(ctx, st.db, "models")
	if err != nil {
		t.Fatal(err)
	}
	if modelCols["upstream"] {
		t.Fatal("models.upstream column was not dropped")
	}
}

// TestWeightColumnIsDropped covers an upgrade from the release that still
// stored a per-candidate weight. The column never took part in the routing
// decision, so opening the database removes it and keeps the candidates.
func TestWeightColumnIsDropped(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "weight.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE instances (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			name            TEXT    NOT NULL UNIQUE,
			preset          TEXT    NOT NULL DEFAULT '',
			key_enc         TEXT    NOT NULL DEFAULT '',
			endpoints_json  TEXT    NOT NULL DEFAULT '[]',
			models_json     TEXT    NOT NULL DEFAULT '[]',
			max_fails       INTEGER NOT NULL DEFAULT 3,
			cooldown_secs   INTEGER NOT NULL DEFAULT 30,
			timeout_secs    INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT    NOT NULL,
			updated_at      TEXT    NOT NULL
		)`,
		`CREATE TABLE models (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT    NOT NULL UNIQUE,
			upstream    TEXT    NOT NULL DEFAULT '',
			strategy    TEXT    NOT NULL DEFAULT 'failover',
			created_at  TEXT    NOT NULL,
			updated_at  TEXT    NOT NULL
		)`,
		`CREATE TABLE model_instances (
			model_id    INTEGER NOT NULL,
			instance_id INTEGER NOT NULL,
			priority    INTEGER NOT NULL DEFAULT 0,
			weight      INTEGER NOT NULL DEFAULT 100,
			PRIMARY KEY (model_id, instance_id)
		)`,
		`INSERT INTO instances(id, name, created_at, updated_at)
			VALUES (1, 'old-instance', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO models(id, name, upstream, created_at, updated_at)
			VALUES (1, 'old-model', 'old-upstream', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO model_instances(model_id, instance_id, priority, weight)
			VALUES (1, 1, 7, 100)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	cols, err := tableColumns(ctx, st.db, "model_instances")
	if err != nil {
		t.Fatal(err)
	}
	if cols["weight"] {
		t.Fatal("weight column was not dropped")
	}
	if !cols["windows_json"] {
		t.Fatal("windows_json column is missing")
	}
	records, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Instances) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Instances[0].Priority != 7 {
		t.Fatalf("priority = %d", records[0].Instances[0].Priority)
	}
}

// TestCostColumnsAreDropped covers the upgrade path from a database that still
// has the obsolete cost_usd columns: they are dropped and the remaining rows
// stay readable.
func TestCostColumnsAreDropped(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cost.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Backstop for the t.Fatal exits below; the explicit Close stays so
	// testStoreAt can reopen the same file.
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	stmts := []string{
		`CREATE TABLE request_logs (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			ts                 TEXT    NOT NULL,
			request_id         TEXT    NOT NULL,
			api_key_id         INTEGER NOT NULL,
			ingress_protocol   TEXT    NOT NULL,
			model              TEXT    NOT NULL,
			provider           TEXT    NOT NULL,
			endpoint           TEXT    NOT NULL,
			upstream_model     TEXT    NOT NULL,
			status             INTEGER NOT NULL,
			latency_ms         INTEGER NOT NULL,
			ttft_ms            INTEGER NOT NULL,
			prompt_tokens      INTEGER NOT NULL,
			completion_tokens  INTEGER NOT NULL,
			cache_read_tokens  INTEGER NOT NULL,
			cache_write_tokens INTEGER NOT NULL,
			cost_usd           REAL    NOT NULL,
			usage_estimated    INTEGER NOT NULL,
			error_code         TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE usage_daily (
			day               TEXT    NOT NULL,
			api_key_id        INTEGER NOT NULL,
			model             TEXT    NOT NULL,
			provider          TEXT    NOT NULL,
			requests          INTEGER NOT NULL DEFAULT 0,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd          REAL    NOT NULL DEFAULT 0,
			PRIMARY KEY (day, api_key_id, model, provider)
		)`,
		fmt.Sprintf(`INSERT INTO request_logs(ts, request_id, api_key_id, ingress_protocol,
			model, provider, endpoint, upstream_model, status, latency_ms, ttft_ms,
			prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
			cost_usd, usage_estimated, error_code)
			VALUES ('%s', 'req-1', 1, 'openai', 'm', 'p', 'openai', 'm', 200, 10, 2,
			3, 4, 0, 0, 0.5, 0, '')`, now.Format(time.RFC3339Nano)),
		fmt.Sprintf(`INSERT INTO usage_daily(day, api_key_id, model, provider, requests,
			prompt_tokens, completion_tokens, cost_usd)
			VALUES ('%s', 1, 'm', 'p', 1, 3, 4, 0.5)`, now.Format("2006-01-02")),
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := testStoreAt(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, table := range []string{"request_logs", "usage_daily"} {
		cols, err := tableColumns(ctx, st.db, table)
		if err != nil {
			t.Fatal(err)
		}
		if cols["cost_usd"] {
			t.Fatalf("%s.cost_usd column was not dropped", table)
		}
	}
	rows, err := st.Logs(LogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PromptTokens != 3 || rows[0].CompletionTokens != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	report, err := st.Usage(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Daily) != 1 || report.Daily[0].PromptTokens != 3 {
		t.Fatalf("usage = %+v", report.Daily)
	}
}

// TestInstanceSessionAffinityColumnsAreAdded covers the upgrade path from a
// database that predates session_header and failover_statuses_json: the
// columns are added with the historical defaults and the fields round-trip.
func TestInstanceSessionAffinityColumnsAreAdded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Backstop for the t.Fatal exits above and below; the explicit Close
	// stays so the store can reopen the same file and take its lock.
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE instances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		preset TEXT NOT NULL DEFAULT '',
		key_enc TEXT NOT NULL DEFAULT '',
		endpoints_json TEXT NOT NULL DEFAULT '[]',
		models_json TEXT NOT NULL DEFAULT '[]',
		max_fails INTEGER NOT NULL DEFAULT 3,
		cooldown_secs INTEGER NOT NULL DEFAULT 30,
		timeout_secs INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := testStoreAt(t, path)
	if _, err := st.CreateInstance(InstanceSpec{
		Name:             "go",
		Models:           []string{"glm-5.3-flash"},
		SessionHeader:    "x-opencode-session",
		FailoverStatuses: []int{402, 429},
	}); err != nil {
		t.Fatal(err)
	}
	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].SessionHeader != "x-opencode-session" {
		t.Fatalf("session header = %q", records[0].SessionHeader)
	}
	if len(records[0].FailoverStatuses) != 2 || records[0].FailoverStatuses[0] != 402 {
		t.Fatalf("failover statuses = %v", records[0].FailoverStatuses)
	}
}

// TestInstanceSessionHeaderFallsBackToPreset keeps instances created before
// the column existed working: the preset supplies its required header.
func TestInstanceSessionHeaderFallsBackToPreset(t *testing.T) {
	t.Parallel()
	st := testStore(t)
	if _, err := st.CreateInstance(InstanceSpec{
		Name:   "go",
		Preset: "opencode-go",
		Models: []string{"glm-5.3-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	instances, _, err := st.Topology(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %+v", instances)
	}
	if instances[0].SessionHeader != "x-opencode-session" {
		t.Fatalf("session header = %q, want the preset default", instances[0].SessionHeader)
	}
}
