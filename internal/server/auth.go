package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/clientip"
	"github.com/pw/llmgw/internal/store"
)

type adminIdentity struct {
	Username string
	API      bool
}

// clientIP resolves the peer address, honoring trusted proxies.
func (s *Server) clientIP(r *http.Request) string {
	return clientip.Resolve(r, s.trustedProxies)
}

func (s *Server) audit(r *http.Request, actor, action, target, result string) {
	_ = s.store.Audit(store.AuditEntry{
		TS:        time.Now(),
		Actor:     actor,
		Action:    action,
		Target:    target,
		IP:        s.clientIP(r),
		UserAgent: r.UserAgent(),
		Result:    result,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ip := s.clientIP(r)
	if !s.limiter.allow(ip) {
		retry := int(s.limiter.retryAfter(ip).Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeAdminError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	username := strings.TrimSpace(in.Username)
	user, err := s.store.UserByName(username)
	if err != nil || !auth.CheckPassword(user.PasswordHash, in.Password) {
		s.limiter.fail(ip)
		s.audit(r, "anonymous", "login", username, "failure")
		writeAdminError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	s.limiter.reset(ip)
	plain, hash, err := auth.NewSessionToken()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "cannot create session")
		return
	}
	expires := time.Now().Add(s.sessionTTL)
	if err := s.store.CreateSession(hash, user.Username, ip, r.UserAgent(), expires); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "cannot create session")
		return
	}
	s.setSessionCookie(w, r, plain, expires)
	s.audit(r, user.Username, "login", user.Username, "success")
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   user.Username,
		"expires_at": expires,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
		if sess, err := s.store.SessionByHash(auth.Hash(c.Value)); err == nil {
			s.audit(r, sess.Username, "logout", sess.Username, "success")
		}
		_ = s.store.DeleteSession(auth.Hash(c.Value))
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request, id *adminIdentity) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if id.API {
		writeAdminError(w, http.StatusBadRequest, "password changes require a user session")
		return
	}
	var in struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	user, err := s.store.UserByName(id.Username)
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, "session user no longer exists")
		return
	}
	if !auth.CheckPassword(user.PasswordHash, in.OldPassword) {
		s.audit(r, id.Username, "password_change", id.Username, "failure")
		writeAdminError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if len(in.NewPassword) < 12 {
		writeAdminError(w, http.StatusBadRequest, "new password must be at least 12 characters")
		return
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "cannot hash password")
		return
	}
	if err := s.store.UpdatePassword(id.Username, hash); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "cannot update password")
		return
	}
	// All existing sessions are invalidated, including this one.
	_ = s.store.DeleteSessionsForUser(id.Username)
	s.audit(r, id.Username, "password_change", id.Username, "success")
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := s.store.AuditLogs(limit)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// requireAdmin accepts either a valid session cookie or the optional legacy
// API token (only when explicitly configured).
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (*adminIdentity, bool) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
		sess, err := s.store.SessionByHash(auth.Hash(c.Value))
		if err == nil {
			s.store.TouchSession(sess.ID)
			return &adminIdentity{Username: sess.Username}, true
		}
	}
	if token := bearerToken(r); token != "" && s.adminToken != "" &&
		auth.EqualToken(token, s.adminToken) {
		return &adminIdentity{Username: "api-token", API: true}, true
	}
	writeAdminError(w, http.StatusUnauthorized, "authentication required")
	return nil, false
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time) {
	secure := s.secureCookie || r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
