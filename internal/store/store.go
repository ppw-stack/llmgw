package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/observ"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    key_hash     TEXT    NOT NULL UNIQUE,
    prefix       TEXT    NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT    NOT NULL,
    last_used_at TEXT,
    key_enc      TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS request_logs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    ts                TEXT    NOT NULL,
    request_id        TEXT    NOT NULL,
    api_key_id        INTEGER NOT NULL,
    ingress_protocol  TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    provider          TEXT    NOT NULL,
    endpoint          TEXT    NOT NULL,
    upstream_model    TEXT    NOT NULL,
    status            INTEGER NOT NULL,
    latency_ms        INTEGER NOT NULL,
    ttft_ms           INTEGER NOT NULL,
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    cache_read_tokens INTEGER NOT NULL,
    cache_write_tokens INTEGER NOT NULL,
    cost_usd          REAL    NOT NULL,
    usage_estimated   INTEGER NOT NULL,
    error_code        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_logs_ts    ON request_logs(ts);
CREATE INDEX IF NOT EXISTS idx_logs_key   ON request_logs(api_key_id, ts);
CREATE INDEX IF NOT EXISTS idx_logs_model ON request_logs(model, ts);

CREATE TABLE IF NOT EXISTS usage_daily (
    day               TEXT    NOT NULL,
    api_key_id        INTEGER NOT NULL,
    model             TEXT    NOT NULL,
    provider          TEXT    NOT NULL,
    requests          INTEGER NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY (day, api_key_id, model, provider)
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS instances (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL UNIQUE,
    preset          TEXT    NOT NULL DEFAULT '',
    key_enc         TEXT    NOT NULL DEFAULT '',
    quota_cred_enc  TEXT    NOT NULL DEFAULT '',
    endpoints_json  TEXT    NOT NULL DEFAULT '[]',
    models_json     TEXT    NOT NULL DEFAULT '[]',
    allow_private   INTEGER NOT NULL DEFAULT 0,
    session_header  TEXT    NOT NULL DEFAULT '',
    failover_statuses_json TEXT NOT NULL DEFAULT '[]',
    max_fails       INTEGER NOT NULL DEFAULT 3,
    cooldown_secs   INTEGER NOT NULL DEFAULT 30,
    timeout_secs    INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS models (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    strategy    TEXT    NOT NULL DEFAULT 'failover',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS model_instances (
    model_id     INTEGER NOT NULL,
    instance_id  INTEGER NOT NULL,
    upstream     TEXT    NOT NULL DEFAULT '',
    priority     INTEGER NOT NULL DEFAULT 0,
    windows_json TEXT    NOT NULL DEFAULT '[]',
    PRIMARY KEY (model_id, instance_id)
);
CREATE INDEX IF NOT EXISTS idx_model_instances_instance ON model_instances(instance_id);

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   TEXT NOT NULL UNIQUE,
    username     TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL,
    actor      TEXT NOT NULL,
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);
`

type APIKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// EncryptedKey 是回显用的密文，只在校验与管理台列表里短暂出现，不参与鉴权。
	EncryptedKey string `json:"-"`
	// Key 是解密后的明文，仅在管理台列表响应中回显；旧 Key 没有密文时为空。
	Key string `json:"key,omitempty"`
}

type LogEntry struct {
	TS               time.Time
	RequestID        string
	APIKeyID         int64
	IngressProtocol  string
	Model            string
	Provider         string
	Endpoint         string
	UpstreamModel    string
	Status           int
	LatencyMS        int64
	TTFTMS           int64
	PromptTokens     int64
	CompletionTokens int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	UsageEstimated   bool
	ErrorCode        string
}

type LogRow struct {
	ID               int64     `json:"id"`
	TS               time.Time `json:"ts"`
	RequestID        string    `json:"request_id"`
	APIKeyID         int64     `json:"api_key_id"`
	KeyName          string    `json:"key_name"`
	IngressProtocol  string    `json:"ingress_protocol"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	Endpoint         string    `json:"endpoint"`
	UpstreamModel    string    `json:"upstream_model"`
	Status           int       `json:"status"`
	LatencyMS        int64     `json:"latency_ms"`
	TTFTMS           int64     `json:"ttft_ms"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	UsageEstimated   bool      `json:"usage_estimated"`
	ErrorCode        string    `json:"error_code"`
}

type Store struct {
	db      *sql.DB
	logs    chan LogEntry
	done    chan struct{}
	closeMu sync.RWMutex
	closed  bool
	log     *slog.Logger
}

// Open opens the database, applies the schema and runs the pending migrations.
// The context bounds the startup migration so a locked database can be
// interrupted instead of blocking the process forever.
func Open(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	if err := ensureDir(path); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrateModelInstanceUpstream(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate model instance upstream: %w", err)
	}
	if err := migrateProviders(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate providers: %w", err)
	}
	if err := migrateInstanceModels(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate instance models: %w", err)
	}
	if err := migrateInstanceQuotaCredential(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate instance quota credential: %w", err)
	}
	if err := migrateInstanceAllowPrivate(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate instance allow_private: %w", err)
	}
	if err := migrateInstanceSessionAffinity(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate instance session affinity: %w", err)
	}
	if err := migrateModelInstanceWindows(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate model instance windows: %w", err)
	}
	if err := migrateModelInstanceWeight(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate model instance weight: %w", err)
	}
	if err := migrateAPIKeySecret(ctx, db, log); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate api key secret: %w", err)
	}
	restrictFile(path)
	for _, suffix := range []string{"-wal", "-shm"} {
		restrictFile(path + suffix)
	}
	s := &Store{
		db:   db,
		logs: make(chan LogEntry, 8192),
		done: make(chan struct{}),
		log:  log,
	}
	go s.writer()
	return s, nil
}

func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	// The directory holds the database (session hashes, audit log, encrypted
	// upstream keys) and the master key, so it is not world readable.
	return os.MkdirAll(dir, 0o700)
}

// restrictFile trims a file to owner-only permissions. It is best effort: the
// database may live on a filesystem that does not support POSIX modes.
func restrictFile(path string) {
	if err := os.Chmod(path, 0o600); err != nil {
		return
	}
}

// migrateModelInstanceUpstream moves the upstream model name from the model
// row onto each candidate. Older releases stored one upstream name for the
// whole model; now a candidate is an instance plus one concrete model of that
// instance.
func migrateModelInstanceUpstream(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	cols, err := tableColumns(ctx, db, "model_instances")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	modelCols, err := tableColumns(ctx, db, "models")
	if err != nil {
		return err
	}
	hasLegacyUpstream := modelCols["upstream"]
	if !cols["upstream"] {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE model_instances ADD COLUMN upstream TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add upstream column: %w", err)
		}
		if hasLegacyUpstream {
			// The old alias mechanism stored the name the vendor receives in
			// models.upstream; every candidate of that model used it. Fall
			// back to the public model name when the column was empty.
			if _, err := db.ExecContext(ctx, `UPDATE model_instances SET upstream = COALESCE(
				NULLIF((SELECT m.upstream FROM models m WHERE m.id = model_instances.model_id), ''),
				(SELECT m.name FROM models m WHERE m.id = model_instances.model_id),
				'')`); err != nil {
				return fmt.Errorf("backfill candidate upstream: %w", err)
			}
		}
	}
	if hasLegacyUpstream {
		if _, err := db.ExecContext(ctx, `ALTER TABLE models DROP COLUMN upstream`); err != nil {
			return fmt.Errorf("drop models.upstream column: %w", err)
		}
	}
	if log != nil && (hasLegacyUpstream || !cols["upstream"]) {
		log.Info("moved the upstream model name from models to model_instances")
	}
	return nil
}

// migrateProviders converts the legacy providers table into the instance and
// model tables. Two legacy shapes are accepted: the pre-preset one with
// keys_enc/endpoints_json and the preset-aware one with models_json. The
// legacy table is dropped afterwards, so the migration runs only once.
func migrateProviders(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	cols, err := tableColumns(ctx, db, "providers")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}

	var providers []migratedProvider
	if cols["endpoints_json"] {
		providers, err = readLegacyProviders(ctx, db, log)
	} else {
		providers, err = readPresetProviders(ctx, db)
	}
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, mp := range providers {
		inst, models, err := config.ExpandProvider(mp.provider)
		if err != nil {
			return err
		}
		if err := config.NormalizeInstance(&inst); err != nil {
			return err
		}
		endpointsJSON, err := json.Marshal(inst.Endpoints)
		if err != nil {
			return err
		}
		modelsJSON, err := json.Marshal(inst.Models)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO instances(name, preset, key_enc, endpoints_json,
			models_json, max_fails, cooldown_secs, timeout_secs, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			inst.Name, inst.Preset, mp.encryptedKey, string(endpointsJSON), string(modelsJSON),
			inst.MaxFails, inst.CooldownSecs, inst.TimeoutSecs,
			nowStamp(), nowStamp()); err != nil {
			return err
		}
		var instanceID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM instances WHERE name = ?`, inst.Name).
			Scan(&instanceID); err != nil {
			return err
		}
		for _, m := range models {
			if err := config.NormalizeModel(&m); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO models(name, strategy, created_at, updated_at)
				VALUES(?,?,?,?)`,
				m.Name, m.Strategy, nowStamp(), nowStamp()); err != nil {
				return err
			}
			var modelID int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM models WHERE name = ?`, m.Name).
				Scan(&modelID); err != nil {
				return err
			}
			for _, mi := range m.Instances {
				if _, err := tx.ExecContext(ctx, `INSERT INTO model_instances(model_id, instance_id, upstream, priority)
					VALUES(?,?,?,?)`, modelID, instanceID, mi.Upstream, mi.Priority); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE providers`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Info("migrated providers into instances and models", "count", len(providers))
	return nil
}

// migratedProvider is one row of the legacy providers table on its way to the
// instance and model tables.
type migratedProvider struct {
	provider     config.Provider
	encryptedKey string
}

// readLegacyProviders reads the pre-preset table shape.
func readLegacyProviders(ctx context.Context, db *sql.DB, log *slog.Logger) ([]migratedProvider, error) {
	type legacyRow struct {
		id            int64
		name          string
		proxy         string
		timeoutMS     int
		keysJSON      string
		endpointsJSON string
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name, proxy, timeout_ms, keys_enc, endpoints_json
		FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []migratedProvider
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.name, &r.proxy, &r.timeoutMS, &r.keysJSON, &r.endpointsJSON); err != nil {
			return nil, err
		}
		spec, encryptedKey, err := convertLegacyProvider(r.name, r.proxy, r.timeoutMS, r.keysJSON, r.endpointsJSON, log)
		if err != nil {
			return nil, err
		}
		out = append(out, migratedProvider{
			provider:     spec,
			encryptedKey: encryptedKey,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// readPresetProviders reads the preset-aware table shape.
func readPresetProviders(ctx context.Context, db *sql.DB) ([]migratedProvider, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, preset, key_enc, models_json, openai_base_url, anthropic_base_url
		FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []migratedProvider
	for rows.Next() {
		var (
			p          config.Provider
			encrypted  string
			modelsJSON string
		)
		if err := rows.Scan(&p.Name, &p.Preset, &encrypted, &modelsJSON,
			&p.OpenAIBaseURL, &p.AnthropicBaseURL); err != nil {
			return nil, err
		}
		models := config.Models{}
		if modelsJSON != "" {
			if err := json.Unmarshal([]byte(modelsJSON), &models); err != nil {
				return nil, fmt.Errorf("provider %q: bad models: %w", p.Name, err)
			}
		}
		p.Models = models
		out = append(out, migratedProvider{provider: p, encryptedKey: encrypted})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// migrateInstanceModels adds the capability column and backfills it for rows
// that predate it. Backfilling keeps an existing deployment routable without
// asking the operator to re-enter every instance.
func migrateInstanceModels(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	cols, err := tableColumns(ctx, db, "instances")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	if !cols["models_json"] {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE instances ADD COLUMN models_json TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	return backfillInstanceModels(ctx, db, log)
}

// migrateInstanceQuotaCredential adds the column that holds the encrypted
// quota credential. Existing deployments keep working: the column starts
// empty, so an instance simply has no quota until the operator fills the
// credential in.
func migrateInstanceQuotaCredential(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	cols, err := tableColumns(ctx, db, "instances")
	if err != nil {
		return fmt.Errorf("read instances table columns: %w", err)
	}
	if len(cols) == 0 || cols["quota_cred_enc"] {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE instances ADD COLUMN quota_cred_enc TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add instances quota credential column: %w", err)
	}
	log.Info("added the quota credential column to instances")
	return nil
}

// migrateInstanceAllowPrivate adds the column that lets an instance opt into
// loopback or private endpoints. Existing rows keep the secure default: 0.
func migrateInstanceAllowPrivate(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	cols, err := tableColumns(ctx, db, "instances")
	if err != nil {
		return fmt.Errorf("read instances table columns: %w", err)
	}
	if len(cols) == 0 || cols["allow_private"] {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE instances ADD COLUMN allow_private INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add instances allow_private column: %w", err)
	}
	log.Info("added the allow_private column to instances")
	return nil
}

// migrateInstanceSessionAffinity adds the two instance columns that describe
// vendor-specific request behaviour: the session header an upstream requires
// and the extra status codes that mean "another candidate may succeed".
// Existing rows keep the old behaviour: no header, no extra codes.
func migrateInstanceSessionAffinity(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	cols, err := tableColumns(ctx, db, "instances")
	if err != nil {
		return fmt.Errorf("read instances table columns: %w", err)
	}
	if len(cols) == 0 {
		return nil
	}
	if !cols["session_header"] {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE instances ADD COLUMN session_header TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add instances session_header column: %w", err)
		}
		log.Info("added the session_header column to instances")
	}
	if !cols["failover_statuses_json"] {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE instances ADD COLUMN failover_statuses_json TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return fmt.Errorf("add instances failover_statuses_json column: %w", err)
		}
		log.Info("added the failover_statuses_json column to instances")
	}
	return nil
}

// migrateModelInstanceWindows adds the time window column to the candidate
// table. Rows that predate it keep an empty window list, so their candidates
// stay on the configured priority.
func migrateModelInstanceWindows(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	cols, err := tableColumns(ctx, db, "model_instances")
	if err != nil {
		return err
	}
	if len(cols) == 0 || cols["windows_json"] {
		return nil
	}
	_, err = db.ExecContext(ctx,
		`ALTER TABLE model_instances ADD COLUMN windows_json TEXT NOT NULL DEFAULT '[]'`)
	return err
}

// migrateModelInstanceWeight drops the weight column of the candidate table.
// Only the failover strategy is implemented, so the column never took part in
// the routing decision; the candidate order is decided by priority alone.
func migrateModelInstanceWeight(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	cols, err := tableColumns(ctx, db, "model_instances")
	if err != nil {
		return err
	}
	if len(cols) == 0 || !cols["weight"] {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE model_instances DROP COLUMN weight`); err != nil {
		return fmt.Errorf("drop weight column: %w", err)
	}
	if log != nil {
		log.Info("dropped the unused weight column from model_instances")
	}
	return nil
}

// migrateAPIKeySecret adds the column that keeps a decryptable copy of each
// gateway key, so the console can show a key again after creation. Keys made
// before the column existed stay hash-only and cannot be echoed.
func migrateAPIKeySecret(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	cols, err := tableColumns(ctx, db, "api_keys")
	if err != nil {
		return fmt.Errorf("read api_keys table columns: %w", err)
	}
	if len(cols) == 0 || cols["key_enc"] {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE api_keys ADD COLUMN key_enc TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add api_keys key_enc column: %w", err)
	}
	if log != nil {
		log.Info("added the key_enc column to api_keys")
	}
	return nil
}

type pendingInstance struct {
	id     int64
	name   string
	preset string
}

func backfillInstanceModels(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, preset FROM instances WHERE models_json = '' OR models_json = '[]'`)
	if err != nil {
		return err
	}
	var pending []pendingInstance
	for rows.Next() {
		var p pendingInstance
		if err := rows.Scan(&p.id, &p.name, &p.preset); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(pending) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range pending {
		names, err := instanceUpstreamNames(ctx, tx, p)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			log.Warn("instance declares no models and none could be derived; "+
				"set its models list before routing a model through it",
				"instance", p.name)
			continue
		}
		encoded, err := json.Marshal(names)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE instances SET models_json = ? WHERE id = ?`, string(encoded), p.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// instanceUpstreamNames unions the preset default models with the upstream
// names the instance currently serves. Both matter: the preset describes the
// account, the references describe what routing already relies on.
func instanceUpstreamNames(ctx context.Context, tx *sql.Tx, p pendingInstance) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	if p.preset != "" {
		names, err := config.PresetModels(p.preset)
		if err != nil {
			return nil, fmt.Errorf("instance %q: derive preset %q models: %w", p.name, p.preset, err)
		}
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT upstream FROM model_instances
		 WHERE instance_id = ? AND upstream <> '' ORDER BY upstream`, p.id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sort.Strings(out)
	return out, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			dataTyp string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &dataTyp, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type legacyEndpoint struct {
	Protocol string            `json:"protocol"`
	BaseURL  string            `json:"base_url"`
	Models   map[string]string `json:"models"`
}

// UnmarshalJSON accepts both the old Go field names (BaseURL, Protocol) and
// the later snake_case form. Old releases marshalled config.Endpoint with JSON
// field names because it only carried yaml tags.
func (e *legacyEndpoint) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		switch normalizeLegacyKey(k) {
		case "protocol":
			if err := json.Unmarshal(v, &e.Protocol); err != nil {
				return fmt.Errorf("legacy endpoint protocol: %w", err)
			}
		case "baseurl":
			if err := json.Unmarshal(v, &e.BaseURL); err != nil {
				return fmt.Errorf("legacy endpoint base url: %w", err)
			}
		case "models":
			if err := json.Unmarshal(v, &e.Models); err != nil {
				return fmt.Errorf("legacy endpoint models: %w", err)
			}
		}
	}
	return nil
}

func normalizeLegacyKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", "")
	return strings.ReplaceAll(k, "-", "")
}

// convertLegacyProvider turns one legacy row into the provider shape the
// instance/model migration understands. A broken keys/endpoints payload fails
// the migration instead of silently producing a provider without an upstream
// key.
func convertLegacyProvider(name, proxy string, timeoutMS int, keysJSON, endpointsJSON string,
	log *slog.Logger) (config.Provider, string, error) {
	p := config.Provider{Name: name}
	var keys []string
	if keysJSON != "" {
		if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
			return config.Provider{}, "", fmt.Errorf("decode legacy keys for provider %q: %w", name, err)
		}
	}
	encryptedKey := ""
	if len(keys) > 0 {
		encryptedKey = keys[0]
	}
	if len(keys) > 1 {
		log.Warn("provider migration: only the first upstream key is kept",
			"provider", name, "dropped", len(keys)-1)
	}
	if proxy != "" || (timeoutMS > 0 && timeoutMS != int(config.DefaultTimeout/time.Millisecond)) {
		log.Warn("provider migration: per-provider proxy/timeout removed",
			"provider", name, "proxy", proxy, "timeout_ms", timeoutMS)
	}
	var endpoints []legacyEndpoint
	if endpointsJSON != "" {
		if err := json.Unmarshal([]byte(endpointsJSON), &endpoints); err != nil {
			return config.Provider{}, "", fmt.Errorf("decode legacy endpoints for provider %q: %w", name, err)
		}
	}
	models := config.Models{}
	for _, ep := range endpoints {
		switch ep.Protocol {
		case config.ProtocolOpenAI:
			p.OpenAIBaseURL = strings.TrimRight(ep.BaseURL, "/")
		case config.ProtocolAnthropic:
			p.AnthropicBaseURL = strings.TrimRight(ep.BaseURL, "/")
		}
		for alias, upstream := range ep.Models {
			if prev, ok := models[alias]; ok && prev != upstream {
				log.Warn("provider migration: model alias maps to different upstream names; keeping the first",
					"provider", name, "model", alias, "kept", prev, "dropped", upstream)
				continue
			}
			models[alias] = upstream
		}
	}
	if len(models) > 0 {
		p.Models = models
	}
	return p, encryptedKey, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.logs)
	s.closeMu.Unlock()

	<-s.done
	return s.db.Close()
}

// ---- gateway keys ----

func (s *Store) CountKeys() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&n)
	return n, err
}

// KeyRecord pairs a stored hash with its public key metadata. It is only used
// to warm the in-process authentication cache.
type KeyRecord struct {
	Hash string
	Key  APIKey
}

func (s *Store) KeyRecords() ([]KeyRecord, error) {
	rows, err := s.db.Query(`SELECT key_hash, id, name, prefix, enabled, created_at, last_used_at
		FROM api_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyRecord
	for rows.Next() {
		var rec KeyRecord
		var enabled int
		var created string
		var last sql.NullString
		if err := rows.Scan(&rec.Hash, &rec.Key.ID, &rec.Key.Name, &rec.Key.Prefix,
			&enabled, &created, &last); err != nil {
			return nil, err
		}
		rec.Key.Enabled = enabled == 1
		rec.Key.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if last.Valid {
			if t, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
				rec.Key.LastUsedAt = &t
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) CreateKey(name, hash, prefix, encrypted string) (*APIKey, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO api_keys(name, key_hash, prefix, enabled, created_at, key_enc) VALUES(?,?,?,1,?,?)`,
		name, hash, prefix, now.Format(time.RFC3339Nano), encrypted)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &APIKey{ID: id, Name: name, Prefix: prefix, Enabled: true, CreatedAt: now}, nil
}

func (s *Store) ListKeys() ([]APIKey, error) {
	rows, err := s.db.Query(
		`SELECT id, name, prefix, enabled, created_at, last_used_at, key_enc
		 FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var enabled int
		var created string
		var last sql.NullString
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &enabled, &created, &last,
			&k.EncryptedKey); err != nil {
			return nil, err
		}
		k.Enabled = enabled == 1
		k.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if last.Valid {
			if t, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
				k.LastUsedAt = &t
			}
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) SetKeyEnabled(id int64, enabled bool) error {
	res, err := s.db.Exec(`UPDATE api_keys SET enabled=? WHERE id=?`, boolInt(enabled), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteKey(id int64) error {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) KeyByHash(hash string) (*APIKey, error) {
	var k APIKey
	var enabled int
	var created string
	var last sql.NullString
	err := s.db.QueryRow(
		`SELECT id, name, prefix, enabled, created_at, last_used_at
		 FROM api_keys WHERE key_hash=?`, hash).
		Scan(&k.ID, &k.Name, &k.Prefix, &enabled, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.Enabled = enabled == 1
	k.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if last.Valid {
		if t, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
			k.LastUsedAt = &t
		}
	}
	return &k, nil
}

func (s *Store) TouchKey(id int64) {
	now := time.Now()
	if v, ok := lastTouch.Load(id); ok {
		if now.Sub(v.(time.Time)) < time.Minute {
			return
		}
	}
	lastTouch.Store(id, now)
	_, _ = s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`,
		now.UTC().Format(time.RFC3339Nano), id)
}

var lastTouch sync.Map

// ---- logging ----

// Enqueue never blocks. It reports whether the entry was accepted.
func (s *Store) Enqueue(e LogEntry) bool {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return false
	}
	select {
	case s.logs <- e:
		return true
	default:
		observ.LogDropped.Inc()
		return false
	}
}

func (s *Store) writer() {
	defer close(s.done)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]LogEntry, 0, 256)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.writeBatch(batch); err != nil {
			s.log.Error("write request logs", "err", err, "entries", len(batch))
		}
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-s.logs:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= 200 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) writeBatch(batch []LogEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	insertLog, err := tx.PrepareContext(ctx, `INSERT INTO request_logs(
		ts, request_id, api_key_id, ingress_protocol, model, provider, endpoint,
		upstream_model, status, latency_ms, ttft_ms, prompt_tokens, completion_tokens,
		cache_read_tokens, cache_write_tokens, cost_usd, usage_estimated, error_code)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insertLog.Close()

	upsertUsage, err := tx.PrepareContext(ctx, `INSERT INTO usage_daily(
		day, api_key_id, model, provider, requests, prompt_tokens, completion_tokens, cost_usd)
		VALUES(?,?,?,?,1,?,?,?)
		ON CONFLICT(day, api_key_id, model, provider) DO UPDATE SET
		  requests = requests + 1,
		  prompt_tokens = prompt_tokens + excluded.prompt_tokens,
		  completion_tokens = completion_tokens + excluded.completion_tokens,
		  cost_usd = cost_usd + excluded.cost_usd`)
	if err != nil {
		return err
	}
	defer upsertUsage.Close()

	for _, e := range batch {
		ts := e.TS.UTC()
		day := ts.Format("2006-01-02")
		if _, err := insertLog.ExecContext(ctx,
			ts.Format(time.RFC3339Nano), e.RequestID, e.APIKeyID, e.IngressProtocol,
			e.Model, e.Provider, e.Endpoint, e.UpstreamModel, e.Status,
			e.LatencyMS, e.TTFTMS, e.PromptTokens, e.CompletionTokens,
			e.CacheReadTokens, e.CacheWriteTokens, e.CostUSD, boolInt(e.UsageEstimated),
			e.ErrorCode); err != nil {
			return err
		}
		if _, err := upsertUsage.ExecContext(ctx, day, e.APIKeyID, e.Model, e.Provider,
			e.PromptTokens, e.CompletionTokens, e.CostUSD); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneResult reports how many rows each retention pass removed.
type PruneResult struct {
	RequestLogs int64
	AuditLogs   int64
	UsageDays   int64
}

// pruneBatchSize bounds how many rows a single DELETE removes.
//
// A DELETE without a limit is one implicit transaction: it holds the write
// lock of the database until it commits. The store talks to SQLite over a
// single connection, so such a transaction blocks every other database
// operation - the asynchronous request log writer above all - for as long as
// it runs. On a public gateway with a 30 day retention the request log reaches
// millions of rows, so the rows go away in batches instead.
const pruneBatchSize = 5000

// pruneBatchPause is the pause between two prune batches. The write lock is
// free during the pause, so the log writer can take it before the next batch
// starts.
const pruneBatchPause = 50 * time.Millisecond

// Prune deletes request logs, audit entries and daily usage rows older than the
// cutoff. Without it the tables grow for the lifetime of the deployment, which
// on a public gateway ends in a full disk.
//
// The rows go away in batches of pruneBatchSize rows: every batch is its own
// transaction, so the write lock is released between the batches and the
// request log writer is not blocked for the whole table.
func (s *Store) Prune(cutoff time.Time) (PruneResult, error) {
	return s.prune(cutoff, pruneBatchSize)
}

// prune is Prune with a configurable batch size, so that the tests can
// exercise the batching without inserting thousands of rows.
func (s *Store) prune(cutoff time.Time, batchSize int) (PruneResult, error) {
	if batchSize <= 0 {
		batchSize = pruneBatchSize
	}
	var out PruneResult
	stamp := cutoff.UTC().Format(time.RFC3339Nano)

	n, err := s.deleteInBatches(
		`DELETE FROM request_logs WHERE id IN (SELECT id FROM request_logs WHERE ts < ? ORDER BY id LIMIT ?)`,
		stamp, batchSize)
	if err != nil {
		return out, fmt.Errorf("prune request logs: %w", err)
	}
	out.RequestLogs = n

	n, err = s.deleteInBatches(
		`DELETE FROM audit_logs WHERE id IN (SELECT id FROM audit_logs WHERE ts < ? ORDER BY id LIMIT ?)`,
		stamp, batchSize)
	if err != nil {
		return out, fmt.Errorf("prune audit logs: %w", err)
	}
	out.AuditLogs = n

	n, err = s.deleteInBatches(
		`DELETE FROM usage_daily WHERE rowid IN (SELECT rowid FROM usage_daily WHERE day < ? LIMIT ?)`,
		cutoff.UTC().Format("2006-01-02"), batchSize)
	if err != nil {
		return out, fmt.Errorf("prune daily usage: %w", err)
	}
	out.UsageDays = n

	return out, nil
}

// deleteInBatches runs one DELETE statement over and over until it removes
// fewer rows than the batch size. Every statement is its own transaction, so
// the write lock is held for a single batch only.
func (s *Store) deleteInBatches(stmt, arg string, batchSize int) (int64, error) {
	var total int64
	for {
		res, err := s.db.Exec(stmt, arg, batchSize)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batchSize) {
			return total, nil
		}
		// The lock is free again: let the writers in before the next batch.
		time.Sleep(pruneBatchPause)
	}
}

// ---- queries ----

type LogFilter struct {
	Limit int
	KeyID int64
	Model string
	Since time.Time
}

func (s *Store) Logs(f LogFilter) ([]LogRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	since := f.Since
	if since.IsZero() {
		since = time.Now().Add(-24 * time.Hour)
	}
	rows, err := s.db.Query(`SELECT l.id, l.ts, l.request_id, l.api_key_id,
		COALESCE(k.name, ''), l.ingress_protocol,
		l.model, l.provider, l.endpoint, l.upstream_model, l.status, l.latency_ms, l.ttft_ms,
		l.prompt_tokens, l.completion_tokens, l.cost_usd, l.usage_estimated, l.error_code
		FROM request_logs l LEFT JOIN api_keys k ON k.id = l.api_key_id
		WHERE l.ts >= ? AND (? = 0 OR l.api_key_id = ?) AND (? = '' OR l.model = ?)
		ORDER BY l.id DESC LIMIT ?`,
		since.UTC().Format(time.RFC3339Nano), f.KeyID, f.KeyID, f.Model, f.Model, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		var ts string
		var estimated int
		if err := rows.Scan(&r.ID, &ts, &r.RequestID, &r.APIKeyID, &r.KeyName, &r.IngressProtocol,
			&r.Model, &r.Provider, &r.Endpoint, &r.UpstreamModel, &r.Status,
			&r.LatencyMS, &r.TTFTMS, &r.PromptTokens, &r.CompletionTokens,
			&r.CostUSD, &estimated, &r.ErrorCode); err != nil {
			return nil, err
		}
		r.TS, _ = time.Parse(time.RFC3339Nano, ts)
		r.UsageEstimated = estimated == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

type UsageDay struct {
	Day              string  `json:"day"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type UsageByModel struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	CostUSD  float64 `json:"cost_usd"`
}

type UsageByKey struct {
	KeyID    int64   `json:"key_id"`
	Name     string  `json:"name"`
	Requests int64   `json:"requests"`
	CostUSD  float64 `json:"cost_usd"`
}

type UsageReport struct {
	Daily   []UsageDay     `json:"daily"`
	ByModel []UsageByModel `json:"by_model"`
	ByKey   []UsageByKey   `json:"by_key"`
}

func (s *Store) Usage(days int) (*UsageReport, error) {
	if days <= 0 {
		days = 7
	}
	since := time.Now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	rep := &UsageReport{}

	rows, err := s.db.Query(`SELECT day, SUM(requests), SUM(prompt_tokens),
		SUM(completion_tokens), SUM(cost_usd) FROM usage_daily
		WHERE day >= ? GROUP BY day ORDER BY day`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d UsageDay
		if err := rows.Scan(&d.Day, &d.Requests, &d.PromptTokens, &d.CompletionTokens, &d.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		rep.Daily = append(rep.Daily, d)
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT model, SUM(requests), SUM(cost_usd)
		FROM usage_daily WHERE day >= ? GROUP BY model ORDER BY SUM(requests) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m UsageByModel
		if err := rows.Scan(&m.Model, &m.Requests, &m.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		rep.ByModel = append(rep.ByModel, m)
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT u.api_key_id, COALESCE(k.name, ''), SUM(u.requests), SUM(u.cost_usd)
		FROM usage_daily u LEFT JOIN api_keys k ON k.id = u.api_key_id
		WHERE u.day >= ? GROUP BY u.api_key_id ORDER BY SUM(u.requests) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k UsageByKey
		if err := rows.Scan(&k.KeyID, &k.Name, &k.Requests, &k.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		rep.ByKey = append(rep.ByKey, k)
	}
	rows.Close()
	return rep, rows.Err()
}

// ---- settings ----

func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

var ErrNotFound = errors.New("not found")

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
