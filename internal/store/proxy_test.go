package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
)

// proxyTestPassword returns the proxy password used by the store tests. Set
// TEST_PROXY_PASSWORD to inject a real one; otherwise a random one-off
// password is generated at runtime so no credential literal is stored in the
// repository. The generated value is cached so every call in the same test
// binary returns the same password.
func proxyTestPassword() string {
	if v := strings.TrimSpace(os.Getenv("TEST_PROXY_PASSWORD")); v != "" {
		return v
	}
	proxyTestPasswordOnce.Do(func() {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			panic(fmt.Sprintf("generate the random proxy test password: %v", err))
		}
		proxyTestPasswordValue = base64.RawURLEncoding.EncodeToString(buf)
	})
	return proxyTestPasswordValue
}

var (
	proxyTestPasswordOnce  sync.Once
	proxyTestPasswordValue string
)

func TestInstanceProxyURLEncryptedRoundTrip(t *testing.T) {
	t.Parallel()
	st := testStore(t)
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}

	proxyPassword := proxyTestPassword()
	proxyPlain := (&url.URL{
		Scheme: "socks5",
		User:   url.UserPassword("alice", proxyPassword),
		Host:   "127.0.0.1:1080",
	}).String()
	if err := st.ImportTopology([]config.Instance{{
		Name:      "proxied",
		APIKey:    topologyTestKey(),
		ProxyURL:  proxyPlain,
		Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://api.example.com/v1"}},
		Models:    []string{"m1"},
	}}, nil, box); err != nil {
		t.Fatalf("ImportTopology: %v", err)
	}

	// Ensure the stored column is encrypted and never holds the plaintext password.
	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatalf("ListInstanceRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].EncryptedProxyURL == "" {
		t.Fatal("expected encrypted proxy_url to be stored")
	}
	if strings.Contains(records[0].EncryptedProxyURL, proxyPassword) {
		t.Fatalf("stored proxy_url_enc contains plaintext password: %q", records[0].EncryptedProxyURL)
	}

	instances, _, err := st.Topology(box)
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	if len(instances) != 1 || instances[0].ProxyURL != proxyPlain {
		t.Fatalf("decrypted proxy_url = %q, want %q", instances[0].ProxyURL, proxyPlain)
	}
}

func TestMigrateInstanceProxyURLColumn(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an existing instances table created before proxy_url_enc existed.
	_, err = db.Exec(`CREATE TABLE instances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		preset TEXT NOT NULL DEFAULT '',
		key_enc TEXT NOT NULL DEFAULT '',
		quota_cred_enc TEXT NOT NULL DEFAULT '',
		endpoints_json TEXT NOT NULL DEFAULT '[]',
		models_json TEXT NOT NULL DEFAULT '[]',
		allow_private INTEGER NOT NULL DEFAULT 0,
		session_header TEXT NOT NULL DEFAULT '',
		failover_statuses_json TEXT NOT NULL DEFAULT '[]',
		max_fails INTEGER NOT NULL DEFAULT 3,
		cooldown_secs INTEGER NOT NULL DEFAULT 30,
		timeout_secs INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Open(ctx, path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.CreateInstance(InstanceSpec{
		Name:              "after-migrate",
		Endpoints:         []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://api.example.com/v1"}},
		Models:            []string{"m1"},
		EncryptedProxyURL: "enc-value",
	}); err != nil {
		t.Fatalf("CreateInstance after migration: %v", err)
	}
	records, err := st.ListInstanceRecords()
	if err != nil {
		t.Fatalf("ListInstanceRecords: %v", err)
	}
	if len(records) != 1 || records[0].EncryptedProxyURL != "enc-value" {
		t.Fatalf("records = %+v", records)
	}
}
