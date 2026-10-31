package store

import (
	"context"
	"database/sql"
	"errors"
)

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	IsAdmin      bool   `json:"isAdmin"`
	CreatedAt    int64  `json:"createdAt"`
}

type Session struct {
	ID         string `json:"-"`
	UserID     int64  `json:"-"`
	CSRFToken  string `json:"-"`
	CreatedAt  int64  `json:"createdAt"`
	LastSeenAt int64  `json:"lastSeenAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	UserAgent  string `json:"userAgent"`
	IP         string `json:"ip"`
}

func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (db *DB) CreateUser(ctx context.Context, username, hash string, admin bool) (*User, error) {
	t := now()
	res, err := db.W.ExecContext(ctx, `INSERT INTO users(username,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,?)`, username, hash, b2i(admin), t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, PasswordHash: hash, IsAdmin: admin, CreatedAt: t}, nil
}

func (db *DB) UserByName(ctx context.Context, username string) (*User, error) {
	return db.scanUser(db.R.QueryRowContext(ctx, `SELECT id,username,password_hash,is_admin,created_at FROM users WHERE username=?`, username))
}

func (db *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	return db.scanUser(db.R.QueryRowContext(ctx, `SELECT id,username,password_hash,is_admin,created_at FROM users WHERE id=?`, id))
}

func (db *DB) scanUser(row *sql.Row) (*User, error) {
	var u User
	var admin int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &admin, &u.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	u.IsAdmin = admin == 1
	return &u, nil
}

func (db *DB) SetUserPassword(ctx context.Context, id int64, hash string) error {
	res, err := db.W.ExecContext(ctx, `UPDATE users SET password_hash=?, updated_at=? WHERE id=?`, hash, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) CreateSession(ctx context.Context, s *Session) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO sessions(id,user_id,csrf_token,created_at,last_seen_at,expires_at,user_agent,ip) VALUES(?,?,?,?,?,?,?,?)`,
		s.ID, s.UserID, s.CSRFToken, s.CreatedAt, s.LastSeenAt, s.ExpiresAt, s.UserAgent, s.IP)
	return err
}

func (db *DB) SessionByID(ctx context.Context, id string) (*Session, error) {
	var s Session
	err := db.R.QueryRowContext(ctx, `SELECT id,user_id,csrf_token,created_at,last_seen_at,expires_at,user_agent,ip FROM sessions WHERE id=?`, id).
		Scan(&s.ID, &s.UserID, &s.CSRFToken, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.UserAgent, &s.IP)
	if err != nil {
		return nil, notFound(err)
	}
	return &s, nil
}

func (db *DB) TouchSession(ctx context.Context, id string, t int64) error {
	_, err := db.W.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id=?`, t, id)
	return err
}

func (db *DB) DeleteSession(ctx context.Context, id string) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}

func (db *DB) DeleteUserSessions(ctx context.Context, userID int64, except string) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND id<>?`, userID, except)
	return err
}

func (db *DB) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT id,user_id,csrf_token,created_at,last_seen_at,expires_at,user_agent,ip FROM sessions WHERE user_id=? ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.CSRFToken, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.UserAgent, &s.IP); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PurgeExpired removes expired sessions, OAuth states and old notifications.
func (db *DB) PurgeExpired(ctx context.Context, idleCutoff int64) error {
	t := now()
	_, err := db.W.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<? OR last_seen_at<?`, t, idleCutoff)
	if err != nil {
		return err
	}
	if _, err := db.W.ExecContext(ctx, `DELETE FROM oauth_states WHERE expires_at<?`, t); err != nil {
		return err
	}
	_, err = db.W.ExecContext(ctx, `DELETE FROM notifications WHERE created_at<?`, t-90*86400)
	return err
}

func (db *DB) Settings(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT key,value FROM settings WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (db *DB) SetSettings(ctx context.Context, userID int64, kv map[string]string) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range kv {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(user_id,key,value) VALUES(?,?,?) ON CONFLICT(user_id,key) DO UPDATE SET value=excluded.value`, userID, k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

var ErrConflict = errors.New("conflict")
