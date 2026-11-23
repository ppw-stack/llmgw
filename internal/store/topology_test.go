package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
)

// topologyTestKey returns the upstream key used by the topology tests. Set
// TEST_TOPOLOGY_API_KEY to inject a real one; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func topologyTestKey() string {
	if v := strings.TrimSpace(os.Getenv("TEST_TOPOLOGY_API_KEY")); v != "" {
		return v
	}
	return "sk-test-placeholder-topology"
}

func TestModelWindowsRoundTrip(t *testing.T) {
	t.Parallel()
	st := testStore(t)

	if _, err := st.CreateInstance(InstanceSpec{
		Name:      "a",
		Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v1"}},
		Models:    []string{"u"},
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	low := 0
	spec := ModelSpec{
		Name:     "m",
		Upstream: "u",
		Strategy: config.StrategyFailover,
		Instances: []ModelInstanceSpec{
			{
				Instance: "a",
				Priority: 5,
				Windows: []config.TimeWindow{
					{Start: "00:00", End: "08:00", Priority: &low},
					{Start: "22:00", End: "06:00", Only: true},
				},
			},
		},
	}
	if _, err := st.CreateModel(spec); err != nil {
		t.Fatalf("create model: %v", err)
	}

	records, err := st.ListModelRecords()
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	windows := records[0].Instances[0].Windows
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].Start != "00:00" || windows[0].End != "08:00" {
		t.Fatalf("first window = %+v", windows[0])
	}
	if windows[0].Priority == nil || *windows[0].Priority != 0 {
		t.Fatalf("first priority = %+v", windows[0].Priority)
	}
	if !windows[1].Only || windows[1].Start != "22:00" || windows[1].End != "06:00" {
		t.Fatalf("second window = %+v", windows[1])
	}
}

func TestTopologyKeepsWindows(t *testing.T) {
	t.Parallel()
	st := testStore(t)
	if _, err := st.CreateInstance(InstanceSpec{
		Name:      "a",
		Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v1"}},
		Models:    []string{"u"},
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	low := 0
	if _, err := st.CreateModel(ModelSpec{
		Name:     "m",
		Upstream: "u",
		Strategy: config.StrategyFailover,
		Instances: []ModelInstanceSpec{{
			Instance: "a",
			Priority: 5,
			Windows:  []config.TimeWindow{{Start: "00:00", End: "08:00", Priority: &low}},
		}},
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}

	_, models, err := st.Topology(nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	windows := models[0].Instances[0].Windows
	if len(windows) != 1 || windows[0].Start != "00:00" || windows[0].End != "08:00" {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestImportTopologyKeepsWindows(t *testing.T) {
	t.Parallel()
	st := testStore(t)
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	low := 0
	instances := []config.Instance{{
		Name:      "a",
		APIKey:    topologyTestKey(),
		Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v1"}},
		Models:    []string{"u"},
	}}
	models := []config.Model{{
		Name:     "m",
		Upstream: "u",
		Strategy: config.StrategyFailover,
		Instances: []config.ModelInstance{{
			Name:     "a",
			Priority: 5,
			Windows:  []config.TimeWindow{{Start: "00:00", End: "08:00", Priority: &low}},
		}},
	}}
	if err := st.ImportTopology(instances, models, box); err != nil {
		t.Fatalf("import topology: %v", err)
	}

	_, imported, err := st.Topology(box)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(imported) != 1 {
		t.Fatalf("models = %+v", imported)
	}
	windows := imported[0].Instances[0].Windows
	if len(windows) != 1 || windows[0].Start != "00:00" || windows[0].End != "08:00" {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].Priority == nil || *windows[0].Priority != 0 {
		t.Fatalf("priority = %+v", windows[0].Priority)
	}
}

func TestMigrateModelInstanceWindows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
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

	records, err := st.ListModelRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Instances) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if len(records[0].Instances[0].Windows) != 0 {
		t.Fatalf("windows = %+v", records[0].Instances[0].Windows)
	}
}
