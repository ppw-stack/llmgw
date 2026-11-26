package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/store"
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/api"), "/")

	if path == "/login" {
		s.handleLogin(w, r)
		return
	}
	if path == "/logout" {
		s.handleLogout(w, r)
		return
	}

	id, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}

	switch {
	case path == "/me":
		writeJSON(w, http.StatusOK, map[string]any{
			"username":  id.Username,
			"api_token": id.API,
		})
	case path == "/password":
		s.handlePassword(w, r, id)
	case path == "/audit":
		s.handleAudit(w, r)
	case path == "/keys":
		s.adminKeys(w, r, id)
	case strings.HasPrefix(path, "/keys/"):
		s.adminKeyItem(w, r, id, strings.TrimPrefix(path, "/keys/"))
	case path == "/providers":
		s.adminProviders(w, r)
	case path == "/instances":
		s.adminInstances(w, r, id, "")
	case strings.HasPrefix(path, "/instances/"):
		s.adminInstances(w, r, id, strings.TrimPrefix(path, "/instances/"))
	case path == "/models":
		s.adminModels(w, r, id, "")
	case strings.HasPrefix(path, "/models/"):
		s.adminModels(w, r, id, strings.TrimPrefix(path, "/models/"))
	case path == "/provider-presets", path == "/instance-presets":
		s.adminInstancePresets(w, r)
	case path == "/preset-models":
		s.adminPresetModels(w, r)
	case path == "/logs":
		s.adminLogs(w, r)
	case path == "/usage":
		s.adminUsage(w, r)
	case path == "/quota":
		s.adminQuota(w, r)
	default:
		writeAdminError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) adminKeys(w http.ResponseWriter, r *http.Request, id *adminIdentity) {
	switch r.Method {
	case http.MethodGet:
		keys, err := s.store.ListKeys()
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if keys == nil {
			keys = []store.APIKey{}
		}
		for i := range keys {
			if keys[i].EncryptedKey == "" {
				continue
			}
			plain, err := s.box.Decrypt(keys[i].EncryptedKey)
			if err != nil {
				// 不因单个 Key 解不开就整页失败：列表照常返回，只隐藏这一条。
				s.log.Warn("decrypt gateway key for echo", "id", keys[i].ID, "err", err)
				continue
			}
			keys[i].Key = plain
		}
		writeJSON(w, http.StatusOK, keys)

	case http.MethodPost:
		var in struct {
			Name          string   `json:"name"`
			RPMLimit      int      `json:"rpm_limit"`
			MaxConcurrent int      `json:"max_concurrent"`
			ExpiresAt     string   `json:"expires_at"`
			AllowedModels []string `json:"allowed_models"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil && err.Error() != "EOF" {
			writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if in.RPMLimit < 0 || in.MaxConcurrent < 0 {
			writeAdminError(w, http.StatusBadRequest, "rpm_limit and max_concurrent must be non-negative")
			return
		}
		var expiresAt *time.Time
		if raw := strings.TrimSpace(in.ExpiresAt); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeAdminError(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp")
				return
			}
			u := parsed.UTC()
			expiresAt = &u
		}
		if strings.TrimSpace(in.Name) == "" {
			in.Name = "default"
		}
		plain, hash, prefix, err := auth.Generate()
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "cannot generate key")
			return
		}
		encrypted, err := s.box.Encrypt(plain)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "cannot encrypt key")
			return
		}
		k, err := s.store.CreateKeyWithPolicy(strings.TrimSpace(in.Name), hash, prefix, encrypted, store.KeyPolicy{
			RPMLimit:      in.RPMLimit,
			MaxConcurrent: in.MaxConcurrent,
			ExpiresAt:     expiresAt,
			AllowedModels: in.AllowedModels,
		})
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.keys.put(hash, *k)
		s.audit(r, id.Username, "key.create", k.Prefix, "success")
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":             k.ID,
			"name":           k.Name,
			"key":            plain,
			"prefix":         k.Prefix,
			"enabled":        k.Enabled,
			"rpm_limit":      k.RPMLimit,
			"max_concurrent": k.MaxConcurrent,
			"expires_at":     k.ExpiresAt,
			"allowed_models": k.AllowedModels,
			"created_at":     k.CreatedAt,
		})

	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminKeyItem(w http.ResponseWriter, r *http.Request, id *adminIdentity, rawID string) {
	keyID, ok := parseID(rawID)
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var in struct {
			Name          *string   `json:"name"`
			Enabled       *bool     `json:"enabled"`
			RPMLimit      *int      `json:"rpm_limit"`
			MaxConcurrent *int      `json:"max_concurrent"`
			ExpiresAt     *string   `json:"expires_at"`
			AllowedModels *[]string `json:"allowed_models"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if in.Name == nil && in.Enabled == nil && in.RPMLimit == nil &&
			in.MaxConcurrent == nil && in.ExpiresAt == nil && in.AllowedModels == nil {
			writeAdminError(w, http.StatusBadRequest, "at least one field to update is required")
			return
		}
		patch := store.KeyPatch{
			Enabled:       in.Enabled,
			RPMLimit:      in.RPMLimit,
			MaxConcurrent: in.MaxConcurrent,
			AllowedModels: in.AllowedModels,
		}
		if in.Name != nil {
			trimmed := strings.TrimSpace(*in.Name)
			if trimmed == "" {
				trimmed = "default"
			}
			patch.Name = &trimmed
		}
		if in.RPMLimit != nil && *in.RPMLimit < 0 {
			writeAdminError(w, http.StatusBadRequest, "rpm_limit must be non-negative")
			return
		}
		if in.MaxConcurrent != nil && *in.MaxConcurrent < 0 {
			writeAdminError(w, http.StatusBadRequest, "max_concurrent must be non-negative")
			return
		}
		if in.ExpiresAt != nil {
			patch.SetExpiresAt = true
			if raw := strings.TrimSpace(*in.ExpiresAt); raw != "" {
				parsed, err := time.Parse(time.RFC3339, raw)
				if err != nil {
					writeAdminError(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp")
					return
				}
				u := parsed.UTC()
				patch.ExpiresAt = &u
			}
		}
		updated, err := s.store.UpdateKey(keyID, patch)
		if err != nil {
			status := http.StatusInternalServerError
			if err == store.ErrNotFound {
				status = http.StatusNotFound
			}
			writeAdminError(w, status, err.Error())
			return
		}
		s.keys.update(*updated)
		s.audit(r, id.Username, "key.update", strconv.FormatInt(keyID, 10), "success")
		writeJSON(w, http.StatusOK, updated)

	case http.MethodDelete:
		if err := s.store.DeleteKey(keyID); err != nil {
			status := http.StatusInternalServerError
			if err == store.ErrNotFound {
				status = http.StatusNotFound
			}
			writeAdminError(w, status, err.Error())
			return
		}
		s.keys.delete(keyID)
		s.audit(r, id.Username, "key.delete", strconv.FormatInt(keyID, 10), "success")
		w.WriteHeader(http.StatusNoContent)

	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	f := store.LogFilter{Model: q.Get("model")}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("key_id"); v != "" {
		if id, ok := parseID(v); ok {
			f.KeyID = id
		}
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	rows, err := s.store.Logs(f)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []store.LogRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) adminUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, ok := parseID(v); ok {
			days = int(n)
		}
	}
	rep, err := s.store.Usage(days)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(h)
}
