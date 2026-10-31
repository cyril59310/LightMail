package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"lightmail/internal/crypto"
	"lightmail/migrations"
)

const keyCheckPlain = "lightmail-key-check"

// LatestSchema returns the highest migration number shipped in the binary.
func LatestSchema() int {
	entries, _ := fs.ReadDir(migrations.FS, ".")
	latest := 0
	for _, e := range entries {
		var v int
		if _, err := fmt.Sscanf(e.Name(), "%04d_", &v); err == nil && v > latest {
			latest = v
		}
	}
	return latest
}

// CheckKey makes sure the configured key can read this database. The first
// start records an encrypted marker; later starts must be able to open it.
func (db *DB) CheckKey(ctx context.Context, kr *crypto.Keyring) error {
	v, err := db.GetMeta(ctx, "key_check")
	if errors.Is(err, ErrNotFound) {
		enc, err := kr.Encrypt(keyCheckPlain, "meta:key_check")
		if err != nil {
			return err
		}
		return db.SetMeta(ctx, "key_check", enc)
	}
	if err != nil {
		return err
	}
	pt, err := kr.Decrypt(v, "meta:key_check")
	if err != nil || pt != keyCheckPlain {
		keyID := "unknown"
		if parts := strings.SplitN(v, ".", 3); len(parts) == 3 {
			keyID = parts[1]
		}
		return fmt.Errorf("LIGHTMAIL_MASTER_KEY does not match this database (encrypted with key %s, configured key %s): restore the right key or set the old one in LIGHTMAIL_MASTER_KEY_OLD", keyID, kr.PrimaryID())
	}
	return nil
}

// RotateKeys re-encrypts every secret with the primary key. Old keys must
// be present in the keyring for decryption.
func (db *DB) RotateKeys(ctx context.Context, kr *crypto.Keyring) (int, error) {
	n := 0
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,password_enc,oauth_access_enc,oauth_refresh_enc FROM accounts`)
		if err != nil {
			return err
		}
		type row struct {
			id         int64
			pw, ac, rf string
		}
		var list []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.pw, &r.ac, &r.rf); err != nil {
				rows.Close()
				return err
			}
			list = append(list, r)
		}
		rows.Close()
		re := func(ct, context string) (string, error) {
			if ct == "" || !kr.NeedsRotation(ct) {
				return ct, nil
			}
			pt, err := kr.Decrypt(ct, context)
			if err != nil {
				return "", fmt.Errorf("%s: %w", context, err)
			}
			n++
			return kr.Encrypt(pt, context)
		}
		for _, r := range list {
			id := fmt.Sprint(r.id)
			pw, err := re(r.pw, "account:"+id+":password")
			if err != nil {
				return err
			}
			ac, err := re(r.ac, "account:"+id+":oauth_access")
			if err != nil {
				return err
			}
			rf, err := re(r.rf, "account:"+id+":oauth_refresh")
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE accounts SET password_enc=?, oauth_access_enc=?, oauth_refresh_enc=? WHERE id=?`, pw, ac, rf, r.id); err != nil {
				return err
			}
		}
		// Pending OAuth states are short-lived: drop them instead of rotating.
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_states`); err != nil {
			return err
		}
		enc, err := kr.Encrypt(keyCheckPlain, "meta:key_check")
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('key_check',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, enc)
		return err
	})
	return n, err
}
