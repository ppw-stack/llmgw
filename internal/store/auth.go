package store

import (
	"database/sql"
	"errors"
	"sync"
	"time"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Session struct {
	ID        int64
	Username  string
	ExpiresAt time.Time
}

type AuditEntry struct {
	TS        time.Time `json:"ts"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Result    string    `json:"result"`
}

func (s *Store) UserByName(username string) (*User, error) {
	var u User
	var created, updated string
	err := s.db.QueryRow(`SELECT id, username, password_hash, created_at, updated_at
		FROM users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &u, nil
}

func (s *Store) CreateUser(username, passwordHash string) (*User, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO users(username, password_hash, created_at, updated_at)
		VALUES(?,?,?,?)`, username, passwordHash,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, PasswordHash: passwordHash,
		CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) UpdatePassword(username, passwordHash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash=?, updated_at=? WHERE username=?`,
		passwordHash, time.Now().UTC().Format(time.RFC3339Nano), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateSession(tokenHash, username, ip, userAgent string, expiresAt time.Time) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash, username, created_at, expires_at,
		last_seen_at, ip, user_agent) VALUES(?,?,?,?,?,?,?)`,
		tokenHash, username, now.Format(time.RFC3339Nano),
		expiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), ip, userAgent)
	return err
}

// SessionByHash returns a session only if it has not expired. Expiry is
// compared in Go rather than SQL because RFC3339Nano has variable precision
// and therefore does not sort lexicographically.
func (s *Store) SessionByHash(tokenHash string) (*Session, error) {
	var sess Session
	var expires string
	err := s.db.QueryRow(`SELECT id, username, expires_at FROM sessions
		WHERE token_hash=?`, tokenHash).
		Scan(&sess.ID, &sess.Username, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if !time.Now().Before(sess.ExpiresAt) {
		return nil, ErrNotFound
	}
	return &sess, nil
}

var lastSessionTouch sync.Map

func (s *Store) TouchSession(id int64) {
	now := time.Now()
	if v, ok := lastSessionTouch.Load(id); ok {
		if now.Sub(v.(time.Time)) < time.Minute {
			return
		}
	}
	lastSessionTouch.Store(id, now)
	_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`,
		now.UTC().Format(time.RFC3339Nano), id)
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) DeleteSessionsForUser(username string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE username=?`, username)
	return err
}

func (s *Store) DeleteExpiredSessions() error {
	rows, err := s.db.Query(`SELECT id, expires_at FROM sessions`)
	if err != nil {
		return err
	}
	now := time.Now()
	var expired []int64
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		exp, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil && !now.Before(exp) {
			expired = append(expired, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range expired {
		if _, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Audit(e AuditEntry) error {
	ts := e.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO audit_logs(ts, actor, action, target, ip, user_agent, result)
		VALUES(?,?,?,?,?,?,?)`,
		ts.UTC().Format(time.RFC3339Nano), e.Actor, e.Action, e.Target,
		e.IP, e.UserAgent, e.Result)
	return err
}

func (s *Store) AuditLogs(limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT ts, actor, action, target, ip, user_agent, result
		FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts string
		if err := rows.Scan(&ts, &e.Actor, &e.Action, &e.Target, &e.IP,
			&e.UserAgent, &e.Result); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}
