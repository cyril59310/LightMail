package store

import (
	"context"
	"database/sql"
)

// Folder roles (from SPECIAL-USE attributes or name heuristics).
const (
	RoleInbox     = "inbox"
	RoleSent      = "sent"
	RoleDrafts    = "drafts"
	RoleTrash     = "trash"
	RoleJunk      = "junk"
	RoleArchive   = "archive"
	RoleAll       = "all"
	RoleFlagged   = "flagged"
	RoleImportant = "important"
)

type Folder struct {
	ID             int64  `json:"id"`
	AccountID      int64  `json:"accountId"`
	Name           string `json:"name"`
	Delimiter      string `json:"delimiter"`
	Role           string `json:"role"`
	Attrs          string `json:"-"`
	Selectable     bool   `json:"selectable"`
	Subscribed     bool   `json:"subscribed"`
	SyncEnabled    bool   `json:"syncEnabled"`
	UIDValidity    uint32 `json:"-"`
	UIDNext        uint32 `json:"-"`
	HighestModSeq  uint64 `json:"-"`
	NumMessages    uint32 `json:"total"`
	MinUID         uint32 `json:"-"`
	MaxUID         uint32 `json:"-"`
	BackfillDone   bool   `json:"backfillDone"`
	LastFlagSyncAt int64  `json:"-"`
	LastSyncAt     int64  `json:"lastSyncAt"`
	Unread         int    `json:"unread"`
	Local          int    `json:"local"`
}

const folderCols = `id,account_id,name,delimiter,role,attrs,selectable,subscribed,sync_enabled,uidvalidity,uidnext,highest_modseq,num_messages,min_uid,max_uid,backfill_done,last_flag_sync_at,last_sync_at`

func scanFolder(s scanner) (*Folder, error) {
	var f Folder
	var sel, sub, syncEn, bf int
	err := s.Scan(&f.ID, &f.AccountID, &f.Name, &f.Delimiter, &f.Role, &f.Attrs, &sel, &sub, &syncEn,
		&f.UIDValidity, &f.UIDNext, &f.HighestModSeq, &f.NumMessages, &f.MinUID, &f.MaxUID, &bf, &f.LastFlagSyncAt, &f.LastSyncAt)
	if err != nil {
		return nil, notFound(err)
	}
	f.Selectable, f.Subscribed, f.SyncEnabled, f.BackfillDone = sel == 1, sub == 1, syncEn == 1, bf == 1
	return &f, nil
}

func (db *DB) Folder(ctx context.Context, id int64) (*Folder, error) {
	return scanFolder(db.R.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE id=?`, id))
}

// FolderForUser returns a folder only if its account belongs to userID.
func (db *DB) FolderForUser(ctx context.Context, userID, id int64) (*Folder, error) {
	return scanFolder(db.R.QueryRowContext(ctx, `SELECT `+prefixed("f.", folderCols)+` FROM folders f JOIN accounts a ON a.id=f.account_id WHERE f.id=? AND a.user_id=?`, id, userID))
}

func (db *DB) FolderByRole(ctx context.Context, accountID int64, role string) (*Folder, error) {
	return scanFolder(db.R.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id=? AND role=? ORDER BY id LIMIT 1`, accountID, role))
}

func (db *DB) FolderByName(ctx context.Context, accountID int64, name string) (*Folder, error) {
	return scanFolder(db.R.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id=? AND name=?`, accountID, name))
}

// ListFolders returns the folders of an account (or all user folders when
// accountID is 0) with local unread/total counters.
func (db *DB) ListFolders(ctx context.Context, userID, accountID int64) ([]*Folder, error) {
	q := `SELECT ` + prefixed("f.", folderCols) + `,
		(SELECT COUNT(*) FROM messages m WHERE m.folder_id=f.id AND m.seen=0),
		(SELECT COUNT(*) FROM messages m WHERE m.folder_id=f.id)
		FROM folders f JOIN accounts a ON a.id=f.account_id WHERE a.user_id=?`
	args := []any{userID}
	if accountID != 0 {
		q += ` AND f.account_id=?`
		args = append(args, accountID)
	}
	rows, err := db.R.QueryContext(ctx, q+` ORDER BY f.account_id, CASE f.role WHEN 'inbox' THEN 0 WHEN 'drafts' THEN 1 WHEN 'sent' THEN 2 WHEN 'archive' THEN 3 WHEN 'junk' THEN 4 WHEN 'trash' THEN 5 WHEN '' THEN 7 ELSE 6 END, f.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Folder
	for rows.Next() {
		var f Folder
		var sel, sub, syncEn, bf int
		if err := rows.Scan(&f.ID, &f.AccountID, &f.Name, &f.Delimiter, &f.Role, &f.Attrs, &sel, &sub, &syncEn,
			&f.UIDValidity, &f.UIDNext, &f.HighestModSeq, &f.NumMessages, &f.MinUID, &f.MaxUID, &bf, &f.LastFlagSyncAt, &f.LastSyncAt,
			&f.Unread, &f.Local); err != nil {
			return nil, err
		}
		f.Selectable, f.Subscribed, f.SyncEnabled, f.BackfillDone = sel == 1, sub == 1, syncEn == 1, bf == 1
		out = append(out, &f)
	}
	return out, rows.Err()
}

func (db *DB) AccountFolders(ctx context.Context, accountID int64) ([]*Folder, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id=? ORDER BY CASE role WHEN 'inbox' THEN 0 ELSE 1 END, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertFolder inserts or updates a folder discovered via LIST. Sync state
// columns are preserved for existing folders.
func (db *DB) UpsertFolder(ctx context.Context, f *Folder) (*Folder, error) {
	_, err := db.W.ExecContext(ctx, `INSERT INTO folders(account_id,name,delimiter,role,attrs,selectable,subscribed,sync_enabled)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id,name) DO UPDATE SET delimiter=excluded.delimiter, role=excluded.role, attrs=excluded.attrs, selectable=excluded.selectable, subscribed=excluded.subscribed`,
		f.AccountID, f.Name, f.Delimiter, f.Role, f.Attrs, b2i(f.Selectable), b2i(f.Subscribed), b2i(f.SyncEnabled))
	if err != nil {
		return nil, err
	}
	return scanFolder(db.W.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id=? AND name=?`, f.AccountID, f.Name))
}

// DeleteFoldersNotIn removes local folders that no longer exist on the server.
func (db *DB) DeleteFoldersNotIn(ctx context.Context, accountID int64, keep []int64) error {
	if len(keep) == 0 {
		_, err := db.W.ExecContext(ctx, `DELETE FROM folders WHERE account_id=?`, accountID)
		return err
	}
	args := append([]any{accountID}, int64Args(keep)...)
	_, err := db.W.ExecContext(ctx, `DELETE FROM folders WHERE account_id=? AND id NOT IN (`+placeholders(len(keep))+`)`, args...)
	return err
}

func (db *DB) SetFolderSyncEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := db.W.ExecContext(ctx, `UPDATE folders SET sync_enabled=? WHERE id=?`, b2i(enabled), id)
	return err
}

// SaveFolderState persists the IMAP synchronisation cursor of a folder.
func (db *DB) SaveFolderState(ctx context.Context, f *Folder) error {
	_, err := db.W.ExecContext(ctx, `UPDATE folders SET uidvalidity=?, uidnext=?, highest_modseq=?, num_messages=?, min_uid=?, max_uid=?, backfill_done=?, last_flag_sync_at=?, last_sync_at=? WHERE id=?`,
		f.UIDValidity, f.UIDNext, f.HighestModSeq, f.NumMessages, f.MinUID, f.MaxUID, b2i(f.BackfillDone), f.LastFlagSyncAt, f.LastSyncAt, f.ID)
	return err
}

// ResetFolder drops every cached message of a folder (UIDVALIDITY change).
func (db *DB) ResetFolder(ctx context.Context, f *Folder) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE folder_id=?`, f.ID); err != nil {
			return err
		}
		f.UIDNext, f.HighestModSeq, f.NumMessages, f.MinUID, f.MaxUID, f.BackfillDone, f.LastFlagSyncAt, f.LastSyncAt = 0, 0, 0, 0, 0, false, 0, 0
		_, err := tx.ExecContext(ctx, `UPDATE folders SET uidvalidity=?, uidnext=0, highest_modseq=0, num_messages=0, min_uid=0, max_uid=0, backfill_done=0, last_flag_sync_at=0, last_sync_at=0 WHERE id=?`, f.UIDValidity, f.ID)
		return err
	})
}

func prefixed(p, cols string) string {
	out := make([]byte, 0, len(cols)*2)
	out = append(out, p...)
	for i := 0; i < len(cols); i++ {
		out = append(out, cols[i])
		if cols[i] == ',' {
			out = append(out, p...)
		}
	}
	return string(out)
}
