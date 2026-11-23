package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
)

func newTestBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// TestPruneKeepsRecentRowsAndDropsOldOnes: the tables must not grow forever,
// but the retention window must not touch recent rows.
func TestPruneKeepsRecentRowsAndDropsOldOnes(t *testing.T) {
	t.Parallel()
	st := testStore(t)

	old := time.Now().UTC().AddDate(0, 0, -40)
	recent := time.Now().UTC().Add(-time.Hour)
	for _, ts := range []time.Time{old, recent} {
		if err := st.Audit(AuditEntry{TS: ts, Actor: "admin", Action: "login", Target: "admin", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}

	// Request logs go through the asynchronous writer.
	for _, ts := range []time.Time{old, recent} {
		if !st.Enqueue(LogEntry{TS: ts, RequestID: "r", Model: "m", Status: 200}) {
			t.Fatal("enqueue rejected")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := st.Logs(LogFilter{Limit: 10, Since: time.Now().AddDate(0, 0, -60)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request logs were not written, got %d rows", len(rows))
		}
		time.Sleep(20 * time.Millisecond)
	}

	res, err := st.Prune(time.Now().UTC().AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if res.AuditLogs != 1 {
		t.Fatalf("audit rows pruned = %d, want 1", res.AuditLogs)
	}
	if res.RequestLogs != 1 {
		t.Fatalf("request rows pruned = %d, want 1", res.RequestLogs)
	}

	audits, err := st.AuditLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 {
		t.Fatalf("audit rows left = %d, want 1", len(audits))
	}
	rows, err := st.Logs(LogFilter{Limit: 10, Since: time.Now().AddDate(0, 0, -60)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("request rows left = %d, want 1", len(rows))
	}
	if time.Since(rows[0].TS) > 24*time.Hour {
		t.Fatalf("prune kept the old row: %s", rows[0].TS)
	}
}

// TestPruneRemovesRowsInBatches: the cleanup deletes in batches, so that the
// write lock is released between them. A table with more rows than one batch
// must be emptied completely all the same.
func TestPruneRemovesRowsInBatches(t *testing.T) {
	t.Parallel()
	st := testStore(t)

	const rows = 5
	old := time.Now().UTC().AddDate(0, 0, -40)
	for i := 0; i < rows; i++ {
		if err := st.Audit(AuditEntry{TS: old, Actor: "admin", Action: "login", Target: "admin", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	// A distinct key per entry, so every entry gets a row of its own in the
	// daily usage table.
	for i := 0; i < rows; i++ {
		if !st.Enqueue(LogEntry{TS: old, RequestID: fmt.Sprintf("r-%d", i), APIKeyID: int64(i), Model: "m", Status: 200}) {
			t.Fatal("enqueue rejected")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		logRows, err := st.Logs(LogFilter{Limit: 10, Since: time.Now().AddDate(0, 0, -60)})
		if err != nil {
			t.Fatal(err)
		}
		if len(logRows) == rows {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request logs were not written, got %d rows", len(logRows))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Two rows per batch: the tables need more than one batch.
	res, err := st.prune(time.Now().UTC().AddDate(0, 0, -30), 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.AuditLogs != rows {
		t.Fatalf("audit rows pruned = %d, want %d", res.AuditLogs, rows)
	}
	if res.RequestLogs != rows {
		t.Fatalf("request rows pruned = %d, want %d", res.RequestLogs, rows)
	}
	if res.UsageDays != rows {
		t.Fatalf("usage days pruned = %d, want %d", res.UsageDays, rows)
	}

	audits, err := st.AuditLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 0 {
		t.Fatalf("audit rows left = %d, want 0", len(audits))
	}
	logRows, err := st.Logs(LogFilter{Limit: 10, Since: time.Now().AddDate(0, 0, -60)})
	if err != nil {
		t.Fatal(err)
	}
	if len(logRows) != 0 {
		t.Fatalf("request rows left = %d, want 0", len(logRows))
	}
}

// TestOpenRestrictsFilePermissions: the database holds session hashes, the
// audit log and encrypted upstream keys, so it is owner-only.
func TestOpenRestrictsFilePermissions(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "perm.db")
	testStoreAt(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("database permissions = %o, want no group/other access", mode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("database directory permissions = %o, want no group/other access", mode)
	}
}

// TestImportTopologyKeepsPrivateOptIn: the flag survives the config -> database
// -> routing round trip, otherwise a configured local mock stops working after
// the first restart.
func TestImportTopologyKeepsPrivateOptIn(t *testing.T) {
	t.Parallel()
	st := testStore(t)
	box := newTestBox(t)
	instances := []config.Instance{{
		Name:                 "local",
		APIKey:               topologyTestKey(),
		AllowPrivateUpstream: true,
		Endpoints:            []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "http://127.0.0.1:9000"}},
		Models:               []string{"m"},
	}}
	models := []config.Model{{
		Name:      "public",
		Instances: []config.ModelInstance{{Name: "local", Upstream: "m"}},
	}}
	if err := st.ImportTopology(instances, models, box); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.Topology(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].AllowPrivateUpstream {
		t.Fatalf("allow_private_upstream was lost: %+v", got)
	}

	// Updating an instance must keep the flag too.
	recs, err := st.ListInstanceRecords()
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %d, err = %v", len(recs), err)
	}
	spec := recs[0].InstanceSpec
	spec.MaxFails = 7
	if err := st.UpdateInstance(recs[0].ID, spec); err != nil {
		t.Fatal(err)
	}
	got, _, err = st.Topology(box)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].AllowPrivateUpstream {
		t.Fatal("update dropped allow_private_upstream")
	}
}
