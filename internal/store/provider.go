package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
)

// ProviderRecord is the storage form of a provider. Upstream keys are kept
// encrypted; callers must use Providers to obtain plaintext config.
type ProviderRecord struct {
	ID            int64
	Name          string
	Proxy         string
	TimeoutMS     int
	EncryptedKeys []string
	Endpoints     []config.Endpoint
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (s *Store) CountProviders() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&n)
	return n, err
}

func (s *Store) ListProviderRecords() ([]ProviderRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, proxy, timeout_ms, keys_enc, endpoints_json,
		created_at, updated_at FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderRecord
	for rows.Next() {
		var rec ProviderRecord
		var keysJSON, endpointsJSON, created, updated string
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Proxy, &rec.TimeoutMS,
			&keysJSON, &endpointsJSON, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(keysJSON), &rec.EncryptedKeys); err != nil {
			return nil, fmt.Errorf("provider %q: bad keys: %w", rec.Name, err)
		}
		if err := json.Unmarshal([]byte(endpointsJSON), &rec.Endpoints); err != nil {
			return nil, fmt.Errorf("provider %q: bad endpoints: %w", rec.Name, err)
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) CreateProvider(name, proxy string, timeoutMS int, encryptedKeys []string,
	endpoints []config.Endpoint) (*ProviderRecord, error) {
	keysJSON, err := json.Marshal(encryptedKeys)
	if err != nil {
		return nil, err
	}
	endpointsJSON, err := json.Marshal(endpoints)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO providers(name, proxy, timeout_ms, keys_enc,
		endpoints_json, created_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
		name, proxy, timeoutMS, string(keysJSON), string(endpointsJSON),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &ProviderRecord{
		ID: id, Name: name, Proxy: proxy, TimeoutMS: timeoutMS,
		EncryptedKeys: encryptedKeys, Endpoints: endpoints,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *Store) UpdateProvider(id int64, name, proxy string, timeoutMS int,
	encryptedKeys []string, endpoints []config.Endpoint) error {
	keysJSON, err := json.Marshal(encryptedKeys)
	if err != nil {
		return err
	}
	endpointsJSON, err := json.Marshal(endpoints)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE providers SET name=?, proxy=?, timeout_ms=?,
		keys_enc=?, endpoints_json=?, updated_at=? WHERE id=?`,
		name, proxy, timeoutMS, string(keysJSON), string(endpointsJSON),
		time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProvider(id int64) error {
	res, err := s.db.Exec(`DELETE FROM providers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Providers decrypts and converts every stored provider for the router.
func (s *Store) Providers(box *secret.Box) ([]config.Provider, error) {
	records, err := s.ListProviderRecords()
	if err != nil {
		return nil, err
	}
	out := make([]config.Provider, 0, len(records))
	for _, rec := range records {
		keys := make([]string, 0, len(rec.EncryptedKeys))
		for _, enc := range rec.EncryptedKeys {
			plain, err := box.Decrypt(enc)
			if err != nil {
				return nil, fmt.Errorf("provider %q: cannot decrypt upstream key: %w", rec.Name, err)
			}
			keys = append(keys, plain)
		}
		out = append(out, config.Provider{
			Name:      rec.Name,
			Keys:      keys,
			Timeout:   config.Duration(time.Duration(rec.TimeoutMS) * time.Millisecond),
			Proxy:     rec.Proxy,
			Endpoints: rec.Endpoints,
		})
	}
	return out, nil
}

// ImportProviders seeds the database from config.yaml on first start.
func (s *Store) ImportProviders(providers []config.Provider, box *secret.Box) error {
	for _, p := range providers {
		encrypted := make([]string, 0, len(p.Keys))
		for _, key := range p.Keys {
			enc, err := box.Encrypt(key)
			if err != nil {
				return err
			}
			encrypted = append(encrypted, enc)
		}
		timeoutMS := int(p.Timeout.Value() / time.Millisecond)
		if timeoutMS <= 0 {
			timeoutMS = 120000
		}
		if _, err := s.CreateProvider(p.Name, p.Proxy, timeoutMS, encrypted, p.Endpoints); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
