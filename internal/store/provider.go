package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/secret"
)

// ProviderSpec is the write form of a provider. The upstream key stays
// encrypted; callers must use Providers to obtain plaintext config.
type ProviderSpec struct {
	Name             string
	Preset           string
	EncryptedKey     string
	Models           config.Models
	OpenAIBaseURL    string
	AnthropicBaseURL string
}

// ProviderRecord is the storage form of a provider.
type ProviderRecord struct {
	ID int64
	ProviderSpec
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) CountProviders() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&n)
	return n, err
}

func (s *Store) ListProviderRecords() ([]ProviderRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, preset, key_enc, models_json,
		openai_base_url, anthropic_base_url, created_at, updated_at
		FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderRecord
	for rows.Next() {
		var rec ProviderRecord
		var modelsJSON, created, updated string
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Preset, &rec.EncryptedKey,
			&modelsJSON, &rec.OpenAIBaseURL, &rec.AnthropicBaseURL, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(modelsJSON), &rec.Models); err != nil {
			return nil, fmt.Errorf("provider %q: bad models: %w", rec.Name, err)
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CreateProvider stores a provider and returns the stored record.
func (s *Store) CreateProvider(spec ProviderSpec) (*ProviderRecord, error) {
	modelsJSON, err := json.Marshal(spec.Models)
	if err != nil {
		return nil, err
	}
	if spec.Models == nil {
		modelsJSON = []byte("{}")
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO providers(name, preset, key_enc, models_json,
		openai_base_url, anthropic_base_url, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		spec.Name, spec.Preset, spec.EncryptedKey, string(modelsJSON),
		spec.OpenAIBaseURL, spec.AnthropicBaseURL,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &ProviderRecord{ID: id, ProviderSpec: spec, CreatedAt: now, UpdatedAt: now}, nil
}

// UpdateProvider replaces the stored form of one provider.
func (s *Store) UpdateProvider(id int64, spec ProviderSpec) error {
	modelsJSON, err := json.Marshal(spec.Models)
	if err != nil {
		return err
	}
	if spec.Models == nil {
		modelsJSON = []byte("{}")
	}
	res, err := s.db.Exec(`UPDATE providers SET name=?, preset=?, key_enc=?, models_json=?,
		openai_base_url=?, anthropic_base_url=?, updated_at=? WHERE id=?`,
		spec.Name, spec.Preset, spec.EncryptedKey, string(modelsJSON),
		spec.OpenAIBaseURL, spec.AnthropicBaseURL,
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
		key := ""
		if rec.EncryptedKey != "" {
			plain, err := box.Decrypt(rec.EncryptedKey)
			if err != nil {
				return nil, fmt.Errorf("provider %q: cannot decrypt upstream key: %w", rec.Name, err)
			}
			key = plain
		}
		out = append(out, config.Provider{
			Name:             rec.Name,
			Preset:           rec.Preset,
			APIKey:           key,
			Models:           rec.Models,
			OpenAIBaseURL:    rec.OpenAIBaseURL,
			AnthropicBaseURL: rec.AnthropicBaseURL,
		})
	}
	return out, nil
}

// ImportProviders seeds the database from config.yaml on first start.
func (s *Store) ImportProviders(providers []config.Provider, box *secret.Box) error {
	for _, p := range providers {
		spec := ProviderSpec{
			Name:             p.Name,
			Preset:           p.Preset,
			Models:           p.Models,
			OpenAIBaseURL:    p.OpenAIBaseURL,
			AnthropicBaseURL: p.AnthropicBaseURL,
		}
		if p.APIKey != "" {
			enc, err := box.Encrypt(p.APIKey)
			if err != nil {
				return err
			}
			spec.EncryptedKey = enc
		}
		if _, err := s.CreateProvider(spec); err != nil {
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
