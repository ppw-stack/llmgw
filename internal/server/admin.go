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
	if !auth.EqualToken(bearerToken(r), s.adminToken) {
		writeAdminError(w, http.StatusUnauthorized, "invalid admin token")
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/api"), "/")
	switch {
	case path == "/keys":
		s.adminKeys(w, r)
	case strings.HasPrefix(path, "/keys/"):
		s.adminKeyItem(w, r, strings.TrimPrefix(path, "/keys/"))
	case path == "/providers":
		if r.Method != http.MethodGet {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snap := s.router.Snapshot()
		if snap == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, snap.Providers)
	case path == "/logs":
		s.adminLogs(w, r)
	case path == "/usage":
		s.adminUsage(w, r)
	default:
		writeAdminError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) adminKeys(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusOK, keys)

	case http.MethodPost:
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil && err.Error() != "EOF" {
			writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			in.Name = "default"
		}
		plain, hash, prefix, err := auth.Generate()
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "cannot generate key")
			return
		}
		k, err := s.store.CreateKey(in.Name, hash, prefix)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         k.ID,
			"name":       k.Name,
			"key":        plain,
			"prefix":     k.Prefix,
			"enabled":    k.Enabled,
			"created_at": k.CreatedAt,
		})

	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminKeyItem(w http.ResponseWriter, r *http.Request, rawID string) {
	id, ok := parseID(rawID)
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if in.Enabled == nil {
			writeAdminError(w, http.StatusBadRequest, "enabled is required")
			return
		}
		if err := s.store.SetKeyEnabled(id, *in.Enabled); err != nil {
			status := http.StatusInternalServerError
			if err == store.ErrNotFound {
				status = http.StatusNotFound
			}
			writeAdminError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "enabled": *in.Enabled})

	case http.MethodDelete:
		if err := s.store.DeleteKey(id); err != nil {
			status := http.StatusInternalServerError
			if err == store.ErrNotFound {
				status = http.StatusNotFound
			}
			writeAdminError(w, status, err.Error())
			return
		}
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
