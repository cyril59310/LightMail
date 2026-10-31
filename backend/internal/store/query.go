package store

import (
	"context"
	"database/sql"
	"strings"
)

// MessageFilter selects messages for list and search views. All string
// criteria are matched with LIKE against cached metadata; FTS is an FTS5
// MATCH expression built by the search package (never raw user input).
type MessageFilter struct {
	UserID    int64
	AccountID int64
	// AccountIDs restricts global views to these accounts (unified inbox setting).
	AccountIDs []int64
	FolderID   int64
	Role       string // unified view across accounts: inbox, sent, drafts, ...
	Starred    bool
	Unread     *bool
	Flagged    *bool
	HasAtt     *bool
	From       []string
	To         []string
	Subject    []string
	After      int64
	Before     int64
	FTS        string
	// Exclude trash/junk from cross-folder views (search, starred).
	ExcludeTrash bool

	CursorDate int64
	CursorID   int64
	Limit      int
}

// candidateFolders resolves the folders a query may return messages from,
// enforcing ownership by the user.
func (db *DB) candidateFolders(ctx context.Context, f MessageFilter) ([]int64, error) {
	q := `SELECT f.id FROM folders f JOIN accounts a ON a.id=f.account_id WHERE a.user_id=?`
	args := []any{f.UserID}
	if f.FolderID != 0 {
		q += ` AND f.id=?`
		args = append(args, f.FolderID)
	} else {
		// never show messages of folders excluded from sync in global views
		q += ` AND f.sync_enabled=1`
	}
	if f.AccountID != 0 {
		q += ` AND f.account_id=?`
		args = append(args, f.AccountID)
	} else if len(f.AccountIDs) > 0 {
		q += ` AND f.account_id IN (` + placeholders(len(f.AccountIDs)) + `)`
		args = append(args, int64Args(f.AccountIDs)...)
	}
	if f.Role != "" {
		q += ` AND f.role=?`
		args = append(args, f.Role)
	}
	if f.ExcludeTrash {
		q += ` AND f.role NOT IN ('trash','junk')`
	}
	rows, err := db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListMessages returns messages ordered by date (newest first) using keyset
// pagination: pass the date and id of the last row as cursor.
//
// Folders are resolved first. A single folder uses the (folder, date) index;
// several folders scan the global date index newest-first, which avoids
// sorting the whole mailbox (measured: 89 ms -> a few ms at 100k messages).
func (db *DB) ListMessages(ctx context.Context, f MessageFilter) ([]*Message, error) {
	folders, err := db.candidateFolders(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(folders) == 0 {
		return []*Message{}, nil
	}
	var where []string
	args := []any{}
	from := " FROM messages m"
	if f.FTS != "" {
		// Full-text matches drive the query (CROSS JOIN fixes the loop order):
		// rowid lookups then a sort of the matches only.
		from = " FROM (SELECT rowid AS fid FROM messages_fts WHERE messages_fts MATCH ?) ft CROSS JOIN messages m ON m.id=ft.fid"
		args = append(args, f.FTS)
	}
	if len(folders) == 1 {
		where = append(where, "m.folder_id=?")
		args = append(args, folders[0])
	} else {
		where = append(where, "m.folder_id IN ("+placeholders(len(folders))+")")
		args = append(args, int64Args(folders)...)
		switch {
		case f.FTS != "":
		case f.Starred:
			from = " FROM messages m INDEXED BY messages_flagged"
		default:
			from = " FROM messages m INDEXED BY messages_date"
		}
	}
	if f.Starred {
		where = append(where, "m.flagged=1")
	}
	if f.Unread != nil {
		where = append(where, "m.seen=?")
		args = append(args, b2i(!*f.Unread))
	}
	if f.Flagged != nil {
		where = append(where, "m.flagged=?")
		args = append(args, b2i(*f.Flagged))
	}
	if f.HasAtt != nil {
		where = append(where, "m.has_attachments=?")
		args = append(args, b2i(*f.HasAtt))
	}
	for _, v := range f.From {
		where = append(where, "(m.from_addr LIKE ? ESCAPE '\\' OR m.from_name LIKE ? ESCAPE '\\')")
		p := likePattern(v)
		args = append(args, p, p)
	}
	for _, v := range f.To {
		where = append(where, "(m.to_json LIKE ? ESCAPE '\\' OR m.cc_json LIKE ? ESCAPE '\\')")
		p := likePattern(v)
		args = append(args, p, p)
	}
	for _, v := range f.Subject {
		where = append(where, "m.subject LIKE ? ESCAPE '\\'")
		args = append(args, likePattern(v))
	}
	if f.After != 0 {
		where = append(where, "m.date>=?")
		args = append(args, f.After)
	}
	if f.Before != 0 {
		where = append(where, "m.date<?")
		args = append(args, f.Before)
	}
	if f.CursorDate != 0 || f.CursorID != 0 {
		// Row-value comparison lets SQLite seek in the index.
		where = append(where, "(m.date, m.id) < (?, ?)")
		args = append(args, f.CursorDate, f.CursorID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := "SELECT " + messageCols + from + " WHERE " + strings.Join(where, " AND ") + " ORDER BY m.date DESC, m.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Message, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func likePattern(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(v) + "%"
}

// Counters for sidebar badges.
type Counters struct {
	UnifiedUnread int           `json:"unifiedUnread"`
	Starred       int           `json:"starred"`
	Accounts      map[int64]int `json:"accounts"`
}

// Counters are computed per folder so that SQLite uses the partial
// "unseen" and "flagged" indexes instead of scanning whole accounts.
func (db *DB) Counters(ctx context.Context, userID int64) (*Counters, error) {
	c := &Counters{Accounts: map[int64]int{}}
	rows, err := db.R.QueryContext(ctx, `SELECT f.account_id,
		(SELECT COUNT(*) FROM messages m INDEXED BY messages_unseen WHERE m.folder_id=f.id AND m.seen=0)
		FROM folders f JOIN accounts a ON a.id=f.account_id WHERE a.user_id=? AND f.role='inbox'`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		c.Accounts[id] += n
		c.UnifiedUnread += n
	}
	rows.Close()
	err = db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m INDEXED BY messages_flagged WHERE m.flagged=1
		AND m.folder_id IN (SELECT f.id FROM folders f JOIN accounts a ON a.id=f.account_id WHERE a.user_id=? AND f.role NOT IN ('trash','junk'))`, userID).Scan(&c.Starred)
	return c, err
}

// Stats returns global counters for status and metrics endpoints.
type Stats struct {
	Accounts    int   `json:"accounts"`
	Folders     int   `json:"folders"`
	Messages    int   `json:"messages"`
	Bodies      int   `json:"bodies"`
	Attachments int   `json:"attachments"`
	Blobs       int   `json:"blobs"`
	BlobBytes   int64 `json:"blobBytes"`
	DBBytes     int64 `json:"dbBytes"`
}

func (db *DB) Stats(ctx context.Context) (*Stats, error) {
	s := &Stats{}
	err := db.R.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM accounts),(SELECT COUNT(*) FROM folders),(SELECT COUNT(*) FROM messages),
		(SELECT COUNT(*) FROM message_bodies),(SELECT COUNT(*) FROM attachments),(SELECT COUNT(*) FROM blobs),(SELECT COALESCE(SUM(size),0) FROM blobs),
		(SELECT page_count*page_size FROM pragma_page_count(), pragma_page_size())`).
		Scan(&s.Accounts, &s.Folders, &s.Messages, &s.Bodies, &s.Attachments, &s.Blobs, &s.BlobBytes, &s.DBBytes)
	return s, err
}

// Contact is an address seen in the mailbox, for recipient autocompletion.
type Contact struct {
	Name  string `json:"name,omitempty"`
	Addr  string `json:"addr"`
	Count int    `json:"count"`
}

// Contacts returns frequent correspondents matching prefix: senders of
// received messages and recipients of sent messages.
func (db *DB) Contacts(ctx context.Context, userID int64, prefix string, limit int) ([]Contact, error) {
	p := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(prefix, `\`, `\`), `%`, `\%`), `_`, `\_`) + "%"
	rows, err := db.R.QueryContext(ctx, `
		SELECT name, addr, COUNT(*) AS n FROM (
			SELECT m.from_name AS name, m.from_addr AS addr FROM messages m JOIN accounts a ON a.id=m.account_id
			WHERE a.user_id=? AND m.from_addr<>'' AND (m.from_addr LIKE ? ESCAPE '\' OR m.from_name LIKE ? ESCAPE '\')
			UNION ALL
			SELECT json_extract(j.value,'$.name'), json_extract(j.value,'$.addr') FROM messages m JOIN accounts a ON a.id=m.account_id
			JOIN folders f ON f.id=m.folder_id, json_each(m.to_json) j
			WHERE a.user_id=? AND f.role='sent' AND (json_extract(j.value,'$.addr') LIKE ? ESCAPE '\' OR json_extract(j.value,'$.name') LIKE ? ESCAPE '\')
		) GROUP BY lower(addr) ORDER BY n DESC LIMIT ?`, userID, p, p, userID, p, p, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		var c Contact
		var name sql.NullString
		if err := rows.Scan(&name, &c.Addr, &c.Count); err != nil {
			return nil, err
		}
		c.Name = name.String
		out = append(out, c)
	}
	return out, rows.Err()
}
