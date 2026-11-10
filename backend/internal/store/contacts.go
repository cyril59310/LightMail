package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// SavedContact is an address book entry. Every query is scoped by user:
// a user never sees nor changes the contacts of another user.
type SavedContact struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Notes     string `json:"notes"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// MaxSavedContacts bounds the address book of one user.
const MaxSavedContacts = 10000

const savedContactCols = `id,user_id,name,email,notes,created_at,updated_at`

func scanSavedContact(s scanner) (*SavedContact, error) {
	var c SavedContact
	err := s.Scan(&c.ID, &c.UserID, &c.Name, &c.Email, &c.Notes, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// SavedContacts lists the user's contacts sorted by name, optionally
// filtered by a text found in the name or the address.
func (db *DB) SavedContacts(ctx context.Context, userID int64, q string, limit int) ([]*SavedContact, error) {
	query := `SELECT ` + savedContactCols + ` FROM contacts WHERE user_id=?`
	args := []any{userID}
	if q = strings.TrimSpace(q); q != "" {
		p := likePattern(q)
		query += ` AND (name LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\')`
		args = append(args, p, p)
	}
	if limit <= 0 || limit > MaxSavedContacts {
		limit = MaxSavedContacts
	}
	query += ` ORDER BY CASE WHEN name='' THEN email ELSE name END COLLATE NOCASE, email COLLATE NOCASE LIMIT ?`
	args = append(args, limit)
	rows, err := db.R.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SavedContact{}
	for rows.Next() {
		c, err := scanSavedContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SavedContact returns one contact of the user.
func (db *DB) SavedContact(ctx context.Context, userID, id int64) (*SavedContact, error) {
	return scanSavedContact(db.R.QueryRowContext(ctx, `SELECT `+savedContactCols+` FROM contacts WHERE id=? AND user_id=?`, id, userID))
}

// ErrContactLimit is returned when the address book is full.
var ErrContactLimit = errors.New("address book is full")

// CreateSavedContact adds a contact; ErrConflict if the address is already saved.
func (db *DB) CreateSavedContact(ctx context.Context, c *SavedContact) error {
	var n int
	if err := db.W.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE user_id=?`, c.UserID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxSavedContacts {
		return ErrContactLimit
	}
	t := now()
	res, err := db.W.ExecContext(ctx, `INSERT INTO contacts(user_id,name,email,notes,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		c.UserID, c.Name, c.Email, c.Notes, t, t)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.CreatedAt, c.UpdatedAt = t, t
	return nil
}

// UpdateSavedContact changes a contact of the user.
func (db *DB) UpdateSavedContact(ctx context.Context, c *SavedContact) error {
	t := now()
	res, err := db.W.ExecContext(ctx, `UPDATE contacts SET name=?, email=?, notes=?, updated_at=? WHERE id=? AND user_id=?`,
		c.Name, c.Email, c.Notes, t, c.ID, c.UserID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	c.UpdatedAt = t
	return nil
}

// DeleteSavedContact removes a contact of the user.
func (db *DB) DeleteSavedContact(ctx context.Context, userID, id int64) error {
	res, err := db.W.ExecContext(ctx, `DELETE FROM contacts WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
