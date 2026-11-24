package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/modelmeta"
)

func TestModelMetadataRoundTrip(t *testing.T) {
	st := testStore(t)
	if _, err := st.CreateInstance(InstanceSpec{
		Name:   "i",
		Models: []string{"m"},
	}); err != nil {
		t.Fatal(err)
	}
	meta := modelmeta.Metadata{
		DisplayName:     "Demo",
		ContextLength:   200000,
		MaxOutputTokens: 64000,
		Capabilities:    []string{"tools", "vision"},
		Protocols: map[string]modelmeta.Override{
			"anthropic": {Capabilities: []string{"tools", "thinking"}},
		},
	}
	rec, err := st.CreateModel(ModelSpec{
		Name:      "m",
		Strategy:  "failover",
		Metadata:  meta,
		Instances: []ModelInstanceSpec{{Instance: "i", Upstream: "m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Metadata.ContextLength != 200000 {
		t.Fatalf("created metadata = %+v", rec.Metadata)
	}

	records, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	got := records[0].Metadata
	if got.DisplayName != "Demo" || got.ContextLength != 200000 || got.MaxOutputTokens != 64000 {
		t.Fatalf("metadata = %+v", got)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[1] != "vision" {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
	if ov := got.Protocols["anthropic"]; len(ov.Capabilities) != 2 {
		t.Fatalf("protocol override = %+v", ov)
	}

	// Clearing the metadata must persist as an empty object.
	if err := st.UpdateModel(rec.ID, ModelSpec{
		Name:      "m",
		Strategy:  "failover",
		Instances: []ModelInstanceSpec{{Instance: "i", Upstream: "m"}},
	}); err != nil {
		t.Fatal(err)
	}
	records, err = st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if !records[0].Metadata.IsZero() {
		t.Fatalf("metadata should be cleared: %+v", records[0].Metadata)
	}
}

func TestModelMetadataColumnIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE instances (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT NOT NULL UNIQUE,
			preset         TEXT NOT NULL DEFAULT '',
			key_enc        TEXT NOT NULL DEFAULT '',
			endpoints_json TEXT NOT NULL DEFAULT '[]',
			models_json    TEXT NOT NULL DEFAULT '[]',
			max_fails      INTEGER NOT NULL DEFAULT 3,
			cooldown_secs  INTEGER NOT NULL DEFAULT 30,
			timeout_secs   INTEGER NOT NULL DEFAULT 0,
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL
		)`,
		`CREATE TABLE models (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL UNIQUE,
			strategy   TEXT NOT NULL DEFAULT 'failover',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE model_instances (
			model_id    INTEGER NOT NULL,
			instance_id INTEGER NOT NULL,
			upstream    TEXT NOT NULL DEFAULT '',
			priority    INTEGER NOT NULL DEFAULT 0,
			windows_json TEXT NOT NULL DEFAULT '[]',
			PRIMARY KEY (model_id, instance_id)
		)`,
		`INSERT INTO instances(id, name, created_at, updated_at)
			VALUES (1, 'i', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO models(id, name, created_at, updated_at)
			VALUES (1, 'm', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO model_instances(model_id, instance_id, upstream) VALUES (1, 1, 'm')`,
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

	records, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if !records[0].Metadata.IsZero() {
		t.Fatalf("old rows should have empty metadata: %+v", records[0].Metadata)
	}
	// The migrated column accepts a write.
	if err := st.UpdateModel(1, ModelSpec{
		Name:      "m",
		Strategy:  "failover",
		Metadata:  modelmeta.Metadata{ContextLength: 1000},
		Instances: []ModelInstanceSpec{{Instance: "i", Upstream: "m"}},
	}); err != nil {
		t.Fatal(err)
	}
	records, err = st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Metadata.ContextLength != 1000 {
		t.Fatalf("metadata after update = %+v", records[0].Metadata)
	}
}
