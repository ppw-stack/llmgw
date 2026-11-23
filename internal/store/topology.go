package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/preset"
	"github.com/pw/llmgw/internal/secret"
)

// InstanceSpec is the write form of an instance. The upstream key stays
// encrypted; callers must use Topology to obtain the plaintext form.
type InstanceSpec struct {
	Name         string
	Preset       string
	EncryptedKey string
	// EncryptedQuotaCredential holds the extra credential some vendors need
	// to read the subscription quota. It is empty when the instance has none.
	EncryptedQuotaCredential string
	Endpoints                []config.Endpoint
	Models                   []string
	MaxFails                 int
	CooldownSecs             int
	TimeoutSecs              int
	// AllowPrivateUpstream lets this instance dial loopback/private hosts.
	AllowPrivateUpstream bool
	// SessionHeader is the header the upstream needs for session affinity.
	SessionHeader string
	// FailoverStatuses are extra upstream status codes treated as retryable.
	FailoverStatuses []int
}

// InstanceRecord is the storage form of an instance.
type InstanceRecord struct {
	ID int64
	InstanceSpec
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ModelInstanceSpec binds a model to one concrete upstream model of one
// instance. The instance is referenced by name because that is how the
// configuration and the admin API speak.
type ModelInstanceSpec struct {
	Instance string
	// Upstream is the model name this candidate sends to the instance.
	Upstream string
	Priority int
	// Windows are the time ranges in which this candidate takes over
	// another priority or is the only candidate allowed to serve the model.
	Windows []config.TimeWindow
}

// ModelSpec is the write form of a model.
type ModelSpec struct {
	Name      string
	Strategy  string
	Instances []ModelInstanceSpec
}

// ModelRecord is the storage form of a model.
type ModelRecord struct {
	ID int64
	ModelSpec
	CreatedAt time.Time
	UpdatedAt time.Time
}

// sessionHeaderFor returns the session header an instance sends upstream. A
// stored value wins; otherwise a preset that needs session affinity supplies
// its own, so instances created before the column existed keep working.
func sessionHeaderFor(rec InstanceRecord) string {
	if rec.SessionHeader != "" || rec.Preset == "" {
		return rec.SessionHeader
	}
	if pr, ok := preset.Lookup(rec.Preset); ok {
		return pr.SessionHeader
	}
	return ""
}

func (s *Store) CountInstances() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM instances`).Scan(&n)
	return n, err
}

func (s *Store) ListInstanceRecords() ([]InstanceRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, preset, key_enc, quota_cred_enc, endpoints_json, models_json,
		allow_private, session_header, failover_statuses_json, max_fails, cooldown_secs, timeout_secs, created_at, updated_at
		FROM instances ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstanceRecord
	for rows.Next() {
		var (
			rec          InstanceRecord
			endpoints    string
			models       string
			allowPrivate int
			statuses     string
			created      string
			updated      string
		)
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Preset, &rec.EncryptedKey, &rec.EncryptedQuotaCredential,
			&endpoints, &models, &allowPrivate, &rec.SessionHeader, &statuses,
			&rec.MaxFails, &rec.CooldownSecs, &rec.TimeoutSecs,
			&created, &updated); err != nil {
			return nil, err
		}
		rec.AllowPrivateUpstream = allowPrivate != 0
		if endpoints != "" {
			if err := json.Unmarshal([]byte(endpoints), &rec.Endpoints); err != nil {
				return nil, fmt.Errorf("instance %q: bad endpoints: %w", rec.Name, err)
			}
		}
		if models != "" {
			if err := json.Unmarshal([]byte(models), &rec.Models); err != nil {
				return nil, fmt.Errorf("instance %q: bad models: %w", rec.Name, err)
			}
		}
		if statuses != "" {
			if err := json.Unmarshal([]byte(statuses), &rec.FailoverStatuses); err != nil {
				return nil, fmt.Errorf("instance %q: bad failover statuses: %w", rec.Name, err)
			}
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateInstance stores an instance and returns the stored record.
func (s *Store) CreateInstance(spec InstanceSpec) (*InstanceRecord, error) {
	endpointsJSON, err := json.Marshal(spec.Endpoints)
	if err != nil {
		return nil, err
	}
	if spec.Endpoints == nil {
		endpointsJSON = []byte("[]")
	}
	modelsJSON, err := json.Marshal(spec.Models)
	if err != nil {
		return nil, err
	}
	if spec.Models == nil {
		modelsJSON = []byte("[]")
	}
	statusesJSON, err := json.Marshal(spec.FailoverStatuses)
	if err != nil {
		return nil, err
	}
	if spec.FailoverStatuses == nil {
		statusesJSON = []byte("[]")
	}
	now := nowStamp()
	res, err := s.db.Exec(`INSERT INTO instances(name, preset, key_enc, quota_cred_enc, endpoints_json,
		models_json, allow_private, session_header, failover_statuses_json, max_fails, cooldown_secs, timeout_secs,
		created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		spec.Name, spec.Preset, spec.EncryptedKey, spec.EncryptedQuotaCredential, string(endpointsJSON),
		string(modelsJSON), boolInt(spec.AllowPrivateUpstream), spec.SessionHeader, string(statusesJSON),
		spec.MaxFails, spec.CooldownSecs, spec.TimeoutSecs, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &InstanceRecord{ID: id, InstanceSpec: spec, CreatedAt: nowTime(now), UpdatedAt: nowTime(now)}, nil
}

// UpdateInstance replaces the stored form of one instance.
func (s *Store) UpdateInstance(id int64, spec InstanceSpec) error {
	endpointsJSON, err := json.Marshal(spec.Endpoints)
	if err != nil {
		return err
	}
	if spec.Endpoints == nil {
		endpointsJSON = []byte("[]")
	}
	modelsJSON, err := json.Marshal(spec.Models)
	if err != nil {
		return err
	}
	if spec.Models == nil {
		modelsJSON = []byte("[]")
	}
	statusesJSON, err := json.Marshal(spec.FailoverStatuses)
	if err != nil {
		return err
	}
	if spec.FailoverStatuses == nil {
		statusesJSON = []byte("[]")
	}
	res, err := s.db.Exec(`UPDATE instances SET name=?, preset=?, key_enc=?, quota_cred_enc=?, endpoints_json=?,
		models_json=?, allow_private=?, session_header=?, failover_statuses_json=?, max_fails=?, cooldown_secs=?,
		timeout_secs=?, updated_at=? WHERE id=?`,
		spec.Name, spec.Preset, spec.EncryptedKey, spec.EncryptedQuotaCredential, string(endpointsJSON),
		string(modelsJSON), boolInt(spec.AllowPrivateUpstream), spec.SessionHeader, string(statusesJSON),
		spec.MaxFails, spec.CooldownSecs, spec.TimeoutSecs, nowStamp(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInstance removes an instance. An instance that still serves a model is
// refused, because a model without candidates cannot be routed.
func (s *Store) DeleteInstance(id int64) error {
	var used int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM model_instances WHERE instance_id = ?`, id).Scan(&used)
	if err != nil {
		return err
	}
	if used > 0 {
		return fmt.Errorf("instance is still used by %d model(s); remove it from those models first", used)
	}
	res, err := s.db.Exec(`DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CountModels() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM models`).Scan(&n)
	return n, err
}

func (s *Store) ListModelRecords() ([]ModelRecord, error) {
	rows, err := s.db.Query(`SELECT m.id, m.name, m.strategy, m.created_at, m.updated_at,
		i.name, mi.upstream, mi.priority, mi.windows_json
		FROM models m
		LEFT JOIN model_instances mi ON mi.model_id = m.id
		LEFT JOIN instances i ON i.id = mi.instance_id
		ORDER BY m.id, mi.priority, i.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelRecord
	for rows.Next() {
		var (
			rec      ModelRecord
			created  string
			updated  string
			instName sql.NullString
			upstream sql.NullString
			priority sql.NullInt64
			windows  sql.NullString
		)
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Strategy, &created, &updated,
			&instName, &upstream, &priority, &windows); err != nil {
			return nil, err
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if instName.Valid {
			spec := ModelInstanceSpec{
				Instance: instName.String,
				Upstream: upstream.String,
				Priority: int(priority.Int64),
			}
			if windows.Valid && windows.String != "" {
				if err := json.Unmarshal([]byte(windows.String), &spec.Windows); err != nil {
					return nil, fmt.Errorf("model %q: instance %q: bad windows: %w",
						rec.Name, instName.String, err)
				}
			}
			rec.Instances = append(rec.Instances, spec)
		}
		// The join produces one row per candidate, so the rows of one model
		// are folded into a single record.
		if len(out) > 0 && out[len(out)-1].ID == rec.ID {
			out[len(out)-1].Instances = append(out[len(out)-1].Instances, rec.Instances...)
			continue
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateModel stores a model together with its candidate list.
func (s *Store) CreateModel(spec ModelSpec) (*ModelRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowStamp()
	res, err := tx.Exec(`INSERT INTO models(name, strategy, created_at, updated_at)
		VALUES(?,?,?,?)`, spec.Name, spec.Strategy, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := replaceModelInstances(tx, id, spec.Instances); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ModelRecord{ID: id, ModelSpec: spec, CreatedAt: nowTime(now), UpdatedAt: nowTime(now)}, nil
}

// UpdateModel replaces the stored form of one model, including its candidate
// list.
func (s *Store) UpdateModel(id int64, spec ModelSpec) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE models SET name=?, strategy=?, updated_at=? WHERE id=?`,
		spec.Name, spec.Strategy, nowStamp(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := replaceModelInstances(tx, id, spec.Instances); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteModel removes a model and its candidate list.
func (s *Store) DeleteModel(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM model_instances WHERE model_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// replaceModelInstances rewrites the candidate list of one model.
func replaceModelInstances(tx *sql.Tx, modelID int64, instances []ModelInstanceSpec) error {
	if _, err := tx.Exec(`DELETE FROM model_instances WHERE model_id = ?`, modelID); err != nil {
		return err
	}
	for _, mi := range instances {
		var instanceID int64
		err := tx.QueryRow(`SELECT id FROM instances WHERE name = ?`, mi.Instance).Scan(&instanceID)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("instance %q does not exist", mi.Instance)
			}
			return err
		}
		windowsJSON, err := json.Marshal(mi.Windows)
		if err != nil {
			return fmt.Errorf("instance %q: marshal windows: %w", mi.Instance, err)
		}
		if mi.Windows == nil {
			windowsJSON = []byte("[]")
		}
		if _, err := tx.Exec(`INSERT INTO model_instances(model_id, instance_id, upstream, priority, windows_json)
			VALUES(?,?,?,?,?)`, modelID, instanceID, mi.Upstream, mi.Priority, string(windowsJSON)); err != nil {
			return fmt.Errorf("instance %q: store candidate: %w", mi.Instance, err)
		}
	}
	return nil
}

// Topology reads the whole upstream topology and decrypts the upstream keys.
func (s *Store) Topology(box *secret.Box) ([]config.Instance, []config.Model, error) {
	records, err := s.ListInstanceRecords()
	if err != nil {
		return nil, nil, err
	}
	instances := make([]config.Instance, 0, len(records))
	for _, rec := range records {
		key := ""
		if rec.EncryptedKey != "" {
			plain, err := box.Decrypt(rec.EncryptedKey)
			if err != nil {
				return nil, nil, fmt.Errorf("instance %q: cannot decrypt upstream key: %w", rec.Name, err)
			}
			key = plain
		}
		credential, err := decryptQuotaCredential(box, rec.EncryptedQuotaCredential)
		if err != nil {
			return nil, nil, fmt.Errorf("instance %q: cannot decrypt quota credential: %w", rec.Name, err)
		}
		instances = append(instances, config.Instance{
			Name:                 rec.Name,
			Preset:               rec.Preset,
			APIKey:               key,
			QuotaCredential:      credential,
			Endpoints:            rec.Endpoints,
			Models:               rec.Models,
			MaxFails:             rec.MaxFails,
			CooldownSecs:         rec.CooldownSecs,
			TimeoutSecs:          rec.TimeoutSecs,
			AllowPrivateUpstream: rec.AllowPrivateUpstream,
			SessionHeader:        sessionHeaderFor(rec),
			FailoverStatuses:     rec.FailoverStatuses,
		})
	}

	modelRecords, err := s.ListModelRecords()
	if err != nil {
		return nil, nil, err
	}
	models := make([]config.Model, 0, len(modelRecords))
	for _, rec := range modelRecords {
		m := config.Model{
			Name:     rec.Name,
			Strategy: rec.Strategy,
		}
		for _, mi := range rec.Instances {
			m.Instances = append(m.Instances, config.ModelInstance{
				Name:     mi.Instance,
				Upstream: mi.Upstream,
				Priority: mi.Priority,
				Windows:  mi.Windows,
			})
		}
		models = append(models, m)
	}
	return instances, models, nil
}

// ImportTopology seeds the database from config.yaml on first start.
func (s *Store) ImportTopology(instances []config.Instance, models []config.Model, box *secret.Box) error {
	for _, inst := range instances {
		encrypted := ""
		if inst.APIKey != "" {
			enc, err := box.Encrypt(inst.APIKey)
			if err != nil {
				return err
			}
			encrypted = enc
		}
		encryptedCredential, err := encryptQuotaCredential(box, inst.QuotaCredential)
		if err != nil {
			return fmt.Errorf("instance %q: encrypt quota credential: %w", inst.Name, err)
		}
		spec := InstanceSpec{
			Name:                     inst.Name,
			Preset:                   inst.Preset,
			EncryptedKey:             encrypted,
			EncryptedQuotaCredential: encryptedCredential,
			Endpoints:                inst.Endpoints,
			Models:                   inst.Models,
			MaxFails:                 inst.MaxFails,
			CooldownSecs:             inst.CooldownSecs,
			TimeoutSecs:              inst.TimeoutSecs,
			AllowPrivateUpstream:     inst.AllowPrivateUpstream,
			SessionHeader:            inst.SessionHeader,
			FailoverStatuses:         inst.FailoverStatuses,
		}
		if _, err := s.CreateInstance(spec); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
	}
	for _, m := range models {
		spec := ModelSpec{
			Name:     m.Name,
			Strategy: m.Strategy,
		}
		for _, mi := range m.Instances {
			spec.Instances = append(spec.Instances, ModelInstanceSpec{
				Instance: mi.Name,
				Upstream: mi.Upstream,
				Priority: mi.Priority,
				Windows:  mi.Windows,
			})
		}
		if _, err := s.CreateModel(spec); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func nowTime(stamp string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, stamp)
	return t
}

// encryptQuotaCredential encrypts the quota credential of an instance. An
// empty credential is stored as an empty string.
func encryptQuotaCredential(box *secret.Box, cred config.QuotaCredential) (string, error) {
	if cred.Empty() {
		return "", nil
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("encode quota credential: %w", err)
	}
	encrypted, err := box.Encrypt(string(raw))
	if err != nil {
		return "", fmt.Errorf("encrypt quota credential: %w", err)
	}
	return encrypted, nil
}

// decryptQuotaCredential reads the quota credential of an instance back.
func decryptQuotaCredential(box *secret.Box, encrypted string) (config.QuotaCredential, error) {
	if encrypted == "" {
		return config.QuotaCredential{}, nil
	}
	raw, err := box.Decrypt(encrypted)
	if err != nil {
		return config.QuotaCredential{}, err
	}
	var cred config.QuotaCredential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return config.QuotaCredential{}, fmt.Errorf("decode quota credential: %w", err)
	}
	return cred, nil
}

// isUniqueViolation reports whether the error is a unique constraint failure.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
