package store

import (
	"context"
	"database/sql"
	"strings"
)

// Account statuses.
const (
	StatusPending   = "pending"
	StatusOK        = "ok"
	StatusSyncing   = "syncing"
	StatusError     = "error"
	StatusAuthError = "auth_error" // needs re-authentication
	StatusPaused    = "paused"
)

type Account struct {
	ID              int64
	UserID          int64
	Provider        string
	Email           string
	DisplayName     string
	SenderName      string
	Color           string
	IMAPHost        string
	IMAPPort        int
	IMAPSecurity    string
	SMTPHost        string
	SMTPPort        int
	SMTPSecurity    string
	AuthType        string
	Username        string
	PasswordEnc     string
	OAuthAccessEnc  string
	OAuthRefreshEnc string
	OAuthExpiry     int64
	OAuthScope      string
	OAuthTenant     string
	TLSCAPEM        string
	SignatureHTML   string
	IsDefault       bool
	SyncEnabled     bool
	SyncIntervalSec int
	IdleEnabled     bool
	SMTPSavesSent   bool
	Status          string
	LastError       string
	LastErrorAt     int64
	LastSyncAt      int64
	LastSyncOKAt    int64
	CreatedAt       int64
	UpdatedAt       int64
}

const accountCols = `id,user_id,provider,email,display_name,sender_name,color,imap_host,imap_port,imap_security,smtp_host,smtp_port,smtp_security,auth_type,username,password_enc,oauth_access_enc,oauth_refresh_enc,oauth_expiry,oauth_scope,oauth_tenant,tls_ca_pem,signature_html,is_default,sync_enabled,sync_interval_sec,idle_enabled,smtp_saves_sent,status,last_error,last_error_at,last_sync_at,last_sync_ok_at,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanAccount(s scanner) (*Account, error) {
	var a Account
	var def, syncEn, idle, saves int
	err := s.Scan(&a.ID, &a.UserID, &a.Provider, &a.Email, &a.DisplayName, &a.SenderName, &a.Color,
		&a.IMAPHost, &a.IMAPPort, &a.IMAPSecurity, &a.SMTPHost, &a.SMTPPort, &a.SMTPSecurity,
		&a.AuthType, &a.Username, &a.PasswordEnc, &a.OAuthAccessEnc, &a.OAuthRefreshEnc, &a.OAuthExpiry,
		&a.OAuthScope, &a.OAuthTenant, &a.TLSCAPEM, &a.SignatureHTML, &def, &syncEn, &a.SyncIntervalSec, &idle, &saves,
		&a.Status, &a.LastError, &a.LastErrorAt, &a.LastSyncAt, &a.LastSyncOKAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	a.IsDefault, a.SyncEnabled, a.IdleEnabled, a.SMTPSavesSent = def == 1, syncEn == 1, idle == 1, saves == 1
	return &a, nil
}

func (db *DB) CreateAccount(ctx context.Context, a *Account) error {
	t := now()
	a.CreatedAt, a.UpdatedAt = t, t
	return db.Tx(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE user_id=?`, a.UserID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			a.IsDefault = true
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO accounts(user_id,provider,email,display_name,sender_name,color,imap_host,imap_port,imap_security,smtp_host,smtp_port,smtp_security,auth_type,username,password_enc,oauth_access_enc,oauth_refresh_enc,oauth_expiry,oauth_scope,oauth_tenant,tls_ca_pem,signature_html,is_default,sync_enabled,sync_interval_sec,idle_enabled,smtp_saves_sent,status,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.UserID, a.Provider, strings.ToLower(a.Email), a.DisplayName, a.SenderName, a.Color, a.IMAPHost, a.IMAPPort, a.IMAPSecurity,
			a.SMTPHost, a.SMTPPort, a.SMTPSecurity, a.AuthType, a.Username, a.PasswordEnc, a.OAuthAccessEnc, a.OAuthRefreshEnc,
			a.OAuthExpiry, a.OAuthScope, a.OAuthTenant, a.TLSCAPEM, a.SignatureHTML, b2i(a.IsDefault), b2i(a.SyncEnabled), a.SyncIntervalSec,
			b2i(a.IdleEnabled), b2i(a.SMTPSavesSent), StatusPending, t, t)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return err
		}
		a.ID, _ = res.LastInsertId()
		a.Status = StatusPending
		return nil
	})
}

func (db *DB) Account(ctx context.Context, id int64) (*Account, error) {
	return scanAccount(db.R.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=?`, id))
}

// AccountForUser returns an account only if it belongs to userID.
func (db *DB) AccountForUser(ctx context.Context, userID, id int64) (*Account, error) {
	return scanAccount(db.R.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=? AND user_id=?`, id, userID))
}

// AccountW reads through the writer connection (read-your-writes inside
// token refresh critical sections).
func (db *DB) AccountW(ctx context.Context, id int64) (*Account, error) {
	return scanAccount(db.W.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=?`, id))
}

func (db *DB) ListAccounts(ctx context.Context, userID int64) ([]*Account, error) {
	q := `SELECT ` + accountCols + ` FROM accounts`
	var args []any
	if userID != 0 {
		q += ` WHERE user_id=?`
		args = append(args, userID)
	}
	rows, err := db.R.QueryContext(ctx, q+` ORDER BY is_default DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAccountSettings persists user-editable settings.
func (db *DB) UpdateAccountSettings(ctx context.Context, a *Account) error {
	_, err := db.W.ExecContext(ctx, `UPDATE accounts SET display_name=?,sender_name=?,color=?,imap_host=?,imap_port=?,imap_security=?,smtp_host=?,smtp_port=?,smtp_security=?,username=?,password_enc=?,tls_ca_pem=?,signature_html=?,sync_enabled=?,sync_interval_sec=?,idle_enabled=?,smtp_saves_sent=?,updated_at=? WHERE id=?`,
		a.DisplayName, a.SenderName, a.Color, a.IMAPHost, a.IMAPPort, a.IMAPSecurity, a.SMTPHost, a.SMTPPort, a.SMTPSecurity,
		a.Username, a.PasswordEnc, a.TLSCAPEM, a.SignatureHTML, b2i(a.SyncEnabled), a.SyncIntervalSec, b2i(a.IdleEnabled), b2i(a.SMTPSavesSent), now(), a.ID)
	return err
}

func (db *DB) SetDefaultAccount(ctx context.Context, userID, id int64) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET is_default=0 WHERE user_id=?`, userID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET is_default=1 WHERE user_id=? AND id=?`, userID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (db *DB) UpdateAccountTokens(ctx context.Context, id int64, accessEnc, refreshEnc string, expiry int64, scope string) error {
	_, err := db.W.ExecContext(ctx, `UPDATE accounts SET oauth_access_enc=?, oauth_refresh_enc=?, oauth_expiry=?, oauth_scope=?, updated_at=? WHERE id=?`, accessEnc, refreshEnc, expiry, scope, now(), id)
	return err
}

func (db *DB) UpdateAccountSecrets(ctx context.Context, id int64, passwordEnc, accessEnc, refreshEnc string) error {
	_, err := db.W.ExecContext(ctx, `UPDATE accounts SET password_enc=?, oauth_access_enc=?, oauth_refresh_enc=? WHERE id=?`, passwordEnc, accessEnc, refreshEnc, id)
	return err
}

func (db *DB) SetAccountStatus(ctx context.Context, id int64, status, lastErr string) error {
	t := now()
	if lastErr != "" {
		_, err := db.W.ExecContext(ctx, `UPDATE accounts SET status=?, last_error=?, last_error_at=? WHERE id=?`, status, truncate(lastErr, 1000), t, id)
		return err
	}
	_, err := db.W.ExecContext(ctx, `UPDATE accounts SET status=? WHERE id=?`, status, id)
	return err
}

func (db *DB) MarkAccountSynced(ctx context.Context, id int64) error {
	t := now()
	_, err := db.W.ExecContext(ctx, `UPDATE accounts SET status=CASE WHEN status IN ('auth_error','paused') THEN status ELSE 'ok' END, last_error='', last_sync_at=?, last_sync_ok_at=? WHERE id=?`, t, t, id)
	return err
}

func (db *DB) MarkAccountSyncAttempt(ctx context.Context, id int64) error {
	_, err := db.W.ExecContext(ctx, `UPDATE accounts SET last_sync_at=? WHERE id=?`, now(), id)
	return err
}

func (db *DB) DeleteAccount(ctx context.Context, userID, id int64) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		var wasDefault int
		if err := tx.QueryRowContext(ctx, `SELECT is_default FROM accounts WHERE id=? AND user_id=?`, id, userID).Scan(&wasDefault); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id=? AND user_id=?`, id, userID); err != nil {
			return err
		}
		if wasDefault == 1 {
			_, err := tx.ExecContext(ctx, `UPDATE accounts SET is_default=1 WHERE id=(SELECT MIN(id) FROM accounts WHERE user_id=?)`, userID)
			return err
		}
		return nil
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// avoid cutting in the middle of a UTF-8 sequence
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}
