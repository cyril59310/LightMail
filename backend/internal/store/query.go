package store

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
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

	// Sort is one of the Sort* keys (default SortSent); Asc reverses the
	// default newest/largest/Z-first order.
	Sort   string
	Asc    bool
	Cursor *Cursor
	Limit  int
}

// Cursor is the sort key and id of the last row of the previous page.
// Num is used by numeric sorts, Text by text sorts.
type Cursor struct {
	Num  int64
	Text string
	ID   int64
}

// Sort keys of message lists.
const (
	SortSent    = "date"    // Date header of the message
	SortArrival = "arrival" // reception date on the server (IMAP INTERNALDATE)
	SortSubject = "subject"
	SortFrom    = "from"
	SortTo      = "to" // first recipient
	SortCc      = "cc" // first copy recipient
	SortSize    = "size"
)

type sortKey struct {
	expr string // SQL expression; text keys are compared with NOCASE
	text bool
}

var sortKeys = map[string]sortKey{
	SortSent:    {"m.date", false},
	SortArrival: {"m.internal_date", false},
	SortSubject: {"m.subject", true},
	SortFrom:    {"COALESCE(NULLIF(m.from_name,''), m.from_addr, '')", true},
	SortTo:      {"COALESCE(NULLIF(json_extract(m.to_json,'$[0].name'),''), json_extract(m.to_json,'$[0].addr'), '')", true},
	SortCc:      {"COALESCE(NULLIF(json_extract(m.cc_json,'$[0].name'),''), json_extract(m.cc_json,'$[0].addr'), '')", true},
	SortSize:    {"m.size", false},
}

// ValidSort reports whether s is a known sort key ("" means the default).
func ValidSort(s string) bool {
	_, ok := sortKeys[s]
	return ok || s == ""
}

// SortIsText reports whether the sort key compares text values.
func SortIsText(s string) bool { return sortKeys[s].text }

func firstAddr(a []Address) string {
	if len(a) == 0 {
		return ""
	}
	if a[0].Name != "" {
		return a[0].Name
	}
	return a[0].Addr
}

// SortValue returns the value of m for the sort key, identical to the SQL
// expression (before NOCASE folding) so that it can be used as a cursor.
func SortValue(m *Message, sort string) (int64, string) {
	switch sort {
	case SortArrival:
		return m.InternalDate, ""
	case SortSize:
		return m.Size, ""
	case SortSubject:
		return 0, m.Subject
	case SortFrom:
		if m.From.Name != "" {
			return 0, m.From.Name
		}
		return 0, m.From.Addr
	case SortTo:
		return 0, firstAddr(m.To)
	case SortCc:
		return 0, firstAddr(m.Cc)
	default:
		return m.Date, ""
	}
}

// foldASCII mirrors SQLite's NOCASE collation (ASCII letters only).
func foldASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// SortMessages orders messages in memory like ListMessages does.
func SortMessages(msgs []*Message, sort string, asc bool) {
	text := SortIsText(sort)
	slices.SortFunc(msgs, func(a, b *Message) int {
		an, at := SortValue(a, sort)
		bn, bt := SortValue(b, sort)
		var r int
		if text {
			r = strings.Compare(foldASCII(at), foldASCII(bt))
		} else {
			r = cmp.Compare(an, bn)
		}
		if r == 0 {
			r = cmp.Compare(a.ID, b.ID)
		}
		if !asc {
			r = -r
		}
		return r
	})
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

// ListMessages returns messages ordered by f.Sort (default: date, newest
// first) using keyset pagination: pass the sort value and id of the last row
// as cursor.
//
// Folders are resolved first. A single folder uses the (folder, date) index;
// for the date sort, several folders scan the global date index, which avoids
// sorting the whole mailbox (measured: 89 ms -> a few ms at 100k messages).
// Other sorts have no index and sort the matching rows.
func (db *DB) ListMessages(ctx context.Context, f MessageFilter) ([]*Message, error) {
	folders, err := db.candidateFolders(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(folders) == 0 {
		return []*Message{}, nil
	}
	if f.Sort == "" {
		f.Sort = SortSent
	}
	key, ok := sortKeys[f.Sort]
	if !ok {
		key = sortKeys[SortSent]
	}
	sortExpr := key.expr
	if key.text {
		sortExpr = "(" + key.expr + ") COLLATE NOCASE"
	}
	dir, cmp := "DESC", "<"
	if f.Asc {
		dir, cmp = "ASC", ">"
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
		case f.FTS != "", f.Sort != SortSent:
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
	if c := f.Cursor; c != nil {
		// Row-value comparison lets SQLite seek in the index.
		where = append(where, "("+sortExpr+", m.id) "+cmp+" (?, ?)")
		if key.text {
			args = append(args, c.Text, c.ID)
		} else {
			args = append(args, c.Num, c.ID)
		}
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := "SELECT " + messageCols + from + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + sortExpr + " " + dir + ", m.id " + dir + " LIMIT ?"
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
