package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type Address struct {
	Name string `json:"name,omitempty"`
	Addr string `json:"addr"`
}

type Message struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"accountId"`
	FolderID       int64     `json:"folderId"`
	UID            uint32    `json:"uid"`
	MessageID      string    `json:"messageId"`
	InReplyTo      string    `json:"inReplyTo,omitempty"`
	References     string    `json:"-"`
	ThreadID       int64     `json:"threadId"`
	Subject        string    `json:"subject"`
	From           Address   `json:"from"`
	To             []Address `json:"to"`
	Cc             []Address `json:"cc"`
	Bcc            []Address `json:"bcc,omitempty"`
	ReplyTo        []Address `json:"replyTo,omitempty"`
	Date           int64     `json:"date"`
	InternalDate   int64     `json:"internalDate"`
	Size           int64     `json:"size"`
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged"`
	Answered       bool      `json:"answered"`
	Draft          bool      `json:"draft"`
	Forwarded      bool      `json:"forwarded"`
	Keywords       string    `json:"-"`
	HasAttachments bool      `json:"hasAttachments"`
	Priority       int       `json:"priority"`
	Snippet        string    `json:"snippet"`
	ModSeq         uint64    `json:"-"`
	Structure      string    `json:"-"`
	BodyFetched    bool      `json:"bodyFetched"`

	// Populated on insert only.
	Attachments []Attachment `json:"-"`
}

type Attachment struct {
	ID          int64  `json:"id"`
	MessageID   int64  `json:"-"`
	Part        string `json:"-"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Encoding    string `json:"-"`
	Size        int64  `json:"size"`
	ContentID   string `json:"contentId,omitempty"`
	Inline      bool   `json:"inline"`
	BlobSHA256  string `json:"-"`
}

const messageCols = `m.id,m.account_id,m.folder_id,m.uid,m.message_id,m.in_reply_to,m.refs,m.thread_id,m.subject,m.from_name,m.from_addr,m.to_json,m.cc_json,m.bcc_json,m.reply_to_json,m.date,m.internal_date,m.size,m.seen,m.flagged,m.answered,m.draft,m.forwarded,m.keywords,m.has_attachments,m.priority,m.snippet,m.modseq,m.structure,m.body_fetched`

func scanMessage(s scanner) (*Message, error) {
	var m Message
	var to, cc, bcc, rt string
	var seen, flagged, answered, draft, fwd, att, body int
	err := s.Scan(&m.ID, &m.AccountID, &m.FolderID, &m.UID, &m.MessageID, &m.InReplyTo, &m.References, &m.ThreadID, &m.Subject,
		&m.From.Name, &m.From.Addr, &to, &cc, &bcc, &rt, &m.Date, &m.InternalDate, &m.Size, &seen, &flagged, &answered, &draft, &fwd,
		&m.Keywords, &att, &m.Priority, &m.Snippet, &m.ModSeq, &m.Structure, &body)
	if err != nil {
		return nil, notFound(err)
	}
	m.Seen, m.Flagged, m.Answered, m.Draft, m.Forwarded, m.HasAttachments, m.BodyFetched = seen == 1, flagged == 1, answered == 1, draft == 1, fwd == 1, att == 1, body == 1
	_ = json.Unmarshal([]byte(to), &m.To)
	_ = json.Unmarshal([]byte(cc), &m.Cc)
	_ = json.Unmarshal([]byte(bcc), &m.Bcc)
	_ = json.Unmarshal([]byte(rt), &m.ReplyTo)
	if m.To == nil {
		m.To = []Address{}
	}
	if m.Cc == nil {
		m.Cc = []Address{}
	}
	return &m, nil
}

func addrJSON(a []Address) string {
	if len(a) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func addrText(a []Address) string {
	var sb strings.Builder
	for _, x := range a {
		sb.WriteString(x.Name)
		sb.WriteByte(' ')
		sb.WriteString(x.Addr)
		sb.WriteByte(' ')
	}
	return sb.String()
}

// UpsertMessages stores messages fetched during a sync. Existing rows
// (same folder and UID) only get their mutable state (flags) refreshed,
// which makes interrupted syncs safe to resume. It returns the IDs of newly
// inserted messages.
func (db *DB) UpsertMessages(ctx context.Context, msgs []*Message) ([]int64, error) {
	var inserted []int64
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		ins, err := tx.PrepareContext(ctx, `INSERT INTO messages(account_id,folder_id,uid,message_id,in_reply_to,refs,thread_id,subject,from_name,from_addr,to_json,cc_json,bcc_json,reply_to_json,date,internal_date,size,seen,flagged,answered,draft,forwarded,keywords,has_attachments,priority,snippet,modseq,structure)
			VALUES(?,?,?,?,?,?,0,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(folder_id,uid) DO NOTHING`)
		if err != nil {
			return err
		}
		defer ins.Close()
		upd, err := tx.PrepareContext(ctx, `UPDATE messages SET seen=?,flagged=?,answered=?,draft=?,forwarded=?,keywords=?,modseq=? WHERE folder_id=? AND uid=?`)
		if err != nil {
			return err
		}
		defer upd.Close()
		fts, err := tx.PrepareContext(ctx, `INSERT INTO messages_fts(rowid,subject,sender,recipients,body) VALUES(?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer fts.Close()
		att, err := tx.PrepareContext(ctx, `INSERT INTO attachments(message_id,part,filename,content_type,encoding,size,content_id,inline) VALUES(?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer att.Close()

		for _, m := range msgs {
			res, err := ins.ExecContext(ctx, m.AccountID, m.FolderID, m.UID, m.MessageID, m.InReplyTo, m.References, m.Subject,
				m.From.Name, m.From.Addr, addrJSON(m.To), addrJSON(m.Cc), addrJSON(m.Bcc), addrJSON(m.ReplyTo), m.Date, m.InternalDate, m.Size,
				b2i(m.Seen), b2i(m.Flagged), b2i(m.Answered), b2i(m.Draft), b2i(m.Forwarded), m.Keywords, b2i(m.HasAttachments), m.Priority,
				m.Snippet, m.ModSeq, m.Structure)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				if _, err := upd.ExecContext(ctx, b2i(m.Seen), b2i(m.Flagged), b2i(m.Answered), b2i(m.Draft), b2i(m.Forwarded), m.Keywords, m.ModSeq, m.FolderID, m.UID); err != nil {
					return err
				}
				continue
			}
			id, _ := res.LastInsertId()
			m.ID = id
			inserted = append(inserted, id)
			tid, err := findThread(ctx, tx, m)
			if err != nil {
				return err
			}
			if tid == 0 {
				tid = id
			}
			m.ThreadID = tid
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET thread_id=? WHERE id=?`, tid, id); err != nil {
				return err
			}
			if _, err := fts.ExecContext(ctx, id, m.Subject, m.From.Name+" "+m.From.Addr, addrText(m.To)+addrText(m.Cc), m.Snippet); err != nil {
				return err
			}
			for _, a := range m.Attachments {
				if _, err := att.ExecContext(ctx, id, a.Part, a.Filename, a.ContentType, a.Encoding, a.Size, a.ContentID, b2i(a.Inline)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return inserted, err
}

// findThread looks for an existing conversation containing this message's
// Message-ID, its parent or any referenced message.
func findThread(ctx context.Context, tx *sql.Tx, m *Message) (int64, error) {
	ids := make([]any, 0, 8)
	ids = append(ids, m.AccountID)
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] && len(ids) < 40 {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	add(m.MessageID)
	add(m.InReplyTo)
	for _, r := range strings.Fields(m.References) {
		add(r)
	}
	var tid int64
	if len(ids) > 1 {
		// INDEXED BY: without it SQLite may walk the (account, date) index, one
		// full scan per inserted message (quadratic initial sync).
		err := tx.QueryRowContext(ctx, `SELECT thread_id FROM messages INDEXED BY messages_msgid WHERE account_id=? AND message_id IN (`+placeholders(len(ids)-1)+`) AND thread_id<>0 LIMIT 1`, ids...).Scan(&tid)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
	}
	if tid == 0 && m.MessageID != "" {
		// a reply may have arrived before its parent
		err := tx.QueryRowContext(ctx, `SELECT thread_id FROM messages INDEXED BY messages_irt WHERE account_id=? AND in_reply_to=? AND thread_id<>0 LIMIT 1`, m.AccountID, m.MessageID).Scan(&tid)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
	}
	return tid, nil
}

// FlagUpdate is a flag change observed on the server.
type FlagUpdate struct {
	UID                                       uint32
	Seen, Flagged, Answered, Draft, Forwarded bool
	Keywords                                  string
	ModSeq                                    uint64
}

func (db *DB) ApplyFlagUpdates(ctx context.Context, folderID int64, ups []FlagUpdate) (int, error) {
	changed := 0
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `UPDATE messages SET seen=?,flagged=?,answered=?,draft=?,forwarded=?,keywords=?,modseq=? WHERE folder_id=? AND uid=? AND (seen<>? OR flagged<>? OR answered<>? OR draft<>? OR forwarded<>? OR keywords<>?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, u := range ups {
			res, err := st.ExecContext(ctx, b2i(u.Seen), b2i(u.Flagged), b2i(u.Answered), b2i(u.Draft), b2i(u.Forwarded), u.Keywords, u.ModSeq, folderID, u.UID,
				b2i(u.Seen), b2i(u.Flagged), b2i(u.Answered), b2i(u.Draft), b2i(u.Forwarded), u.Keywords)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			changed += int(n)
		}
		return nil
	})
	return changed, err
}

// FolderUIDs returns local UIDs of a folder in ascending order.
func (db *DB) FolderUIDs(ctx context.Context, folderID int64) ([]uint32, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT uid FROM messages WHERE folder_id=? ORDER BY uid`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var u uint32
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (db *DB) CountFolderMessages(ctx context.Context, folderID int64) (int, error) {
	var n int
	err := db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE folder_id=?`, folderID).Scan(&n)
	return n, err
}

func (db *DB) DeleteMessagesByUID(ctx context.Context, folderID int64, uids []uint32) (int, error) {
	total := 0
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		for len(uids) > 0 {
			n := min(len(uids), 500)
			args := []any{folderID}
			for _, u := range uids[:n] {
				args = append(args, u)
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE folder_id=? AND uid IN (`+placeholders(n)+`)`, args...)
			if err != nil {
				return err
			}
			c, _ := res.RowsAffected()
			total += int(c)
			uids = uids[n:]
		}
		return nil
	})
	return total, err
}

func (db *DB) DeleteMessage(ctx context.Context, id int64) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id)
	return err
}

// MessageForUser loads a message if it belongs to userID.
func (db *DB) MessageForUser(ctx context.Context, userID, id int64) (*Message, error) {
	return scanMessage(db.R.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages m JOIN accounts a ON a.id=m.account_id WHERE m.id=? AND a.user_id=?`, id, userID))
}

func (db *DB) MessageW(ctx context.Context, id int64) (*Message, error) {
	return scanMessage(db.W.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages m WHERE m.id=?`, id))
}

func (db *DB) MessagesForUser(ctx context.Context, userID int64, ids []int64) ([]*Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]any{userID}, int64Args(ids)...)
	rows, err := db.R.QueryContext(ctx, `SELECT `+messageCols+` FROM messages m JOIN accounts a ON a.id=m.account_id WHERE a.user_id=? AND m.id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (db *DB) MessageByMessageID(ctx context.Context, accountID int64, msgID string) (*Message, error) {
	return scanMessage(db.R.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages m WHERE m.account_id=? AND m.message_id=? LIMIT 1`, accountID, msgID))
}

func (db *DB) SetMessageFlags(ctx context.Context, id int64, field string, value bool) error {
	switch field {
	case "seen", "flagged", "answered", "forwarded":
	default:
		panic("invalid flag field " + field)
	}
	_, err := db.W.ExecContext(ctx, `UPDATE messages SET `+field+`=? WHERE id=?`, b2i(value), id)
	return err
}

// MoveMessageLocal records a message moved to dest with a new UID (when the
// server reported COPYUID), so it appears immediately in the target folder.
func (db *DB) MoveMessageLocal(ctx context.Context, id, destFolder int64, newUID uint32) error {
	if newUID == 0 {
		return db.DeleteMessage(ctx, id)
	}
	_, err := db.W.ExecContext(ctx, `UPDATE OR REPLACE messages SET folder_id=?, uid=? WHERE id=?`, destFolder, newUID, id)
	return err
}

func (db *DB) Attachments(ctx context.Context, messageID int64) ([]Attachment, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT id,message_id,part,filename,content_type,encoding,size,content_id,inline,blob_sha256 FROM attachments WHERE message_id=? ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		var inl int
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Part, &a.Filename, &a.ContentType, &a.Encoding, &a.Size, &a.ContentID, &inl, &a.BlobSHA256); err != nil {
			return nil, err
		}
		a.Inline = inl == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

func (db *DB) SetAttachmentBlob(ctx context.Context, id int64, sha string) error {
	_, err := db.W.ExecContext(ctx, `UPDATE attachments SET blob_sha256=? WHERE id=?`, sha, id)
	return err
}

type Body struct {
	Text string
	HTML string
}

func (db *DB) MessageBody(ctx context.Context, messageID int64) (*Body, error) {
	var b Body
	err := db.R.QueryRowContext(ctx, `SELECT text_body, html_body FROM message_bodies WHERE message_id=?`, messageID).Scan(&b.Text, &b.HTML)
	if err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

// SaveMessageBody stores a fetched body and indexes its text for search.
func (db *DB) SaveMessageBody(ctx context.Context, m *Message, b *Body, indexText string) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_bodies(message_id,text_body,html_body,fetched_at) VALUES(?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET text_body=excluded.text_body, html_body=excluded.html_body, fetched_at=excluded.fetched_at`,
			m.ID, b.Text, b.HTML, now()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET body_fetched=1 WHERE id=?`, m.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages_fts WHERE rowid=?`, m.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO messages_fts(rowid,subject,sender,recipients,body) VALUES(?,?,?,?,?)`,
			m.ID, m.Subject, m.From.Name+" "+m.From.Addr, addrText(m.To)+addrText(m.Cc), truncate(indexText, 200_000))
		return err
	})
}

// PruneBodies drops cached bodies of old messages to bound disk usage while
// keeping them searchable by metadata.
func (db *DB) PruneBodies(ctx context.Context, olderThan int64) (int64, error) {
	res, err := db.W.ExecContext(ctx, `DELETE FROM message_bodies WHERE fetched_at<?`, olderThan)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = db.W.ExecContext(ctx, `UPDATE messages SET body_fetched=0 WHERE body_fetched=1 AND id NOT IN (SELECT message_id FROM message_bodies)`)
	return n, err
}

func (db *DB) ThreadMessages(ctx context.Context, userID, accountID, threadID int64) ([]*Message, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+messageCols+` FROM messages m JOIN accounts a ON a.id=m.account_id WHERE a.user_id=? AND m.account_id=? AND m.thread_id=? ORDER BY m.date LIMIT 200`, userID, accountID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
