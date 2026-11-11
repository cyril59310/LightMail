package store

import (
	"context"
	"database/sql"
)

// ---- OAuth states -------------------------------------------------------

type OAuthState struct {
	StateHash   string
	UserID      int64
	SessionHash string
	Provider    string
	VerifierEnc string
	AccountID   int64
	ExpiresAt   int64
}

func (db *DB) CreateOAuthState(ctx context.Context, s *OAuthState) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO oauth_states(state_hash,user_id,session_hash,provider,verifier_enc,account_id,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		s.StateHash, s.UserID, s.SessionHash, s.Provider, s.VerifierEnc, s.AccountID, now(), s.ExpiresAt)
	return err
}

// ConsumeOAuthState atomically fetches and deletes a state (single use).
func (db *DB) ConsumeOAuthState(ctx context.Context, stateHash string) (*OAuthState, error) {
	var s OAuthState
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT state_hash,user_id,session_hash,provider,verifier_enc,COALESCE(account_id,0),expires_at FROM oauth_states WHERE state_hash=?`, stateHash).
			Scan(&s.StateHash, &s.UserID, &s.SessionHash, &s.Provider, &s.VerifierEnc, &s.AccountID, &s.ExpiresAt)
		if err != nil {
			return notFound(err)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE state_hash=?`, stateHash)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ---- Notifications ------------------------------------------------------

type Notification struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	AccountID int64  `json:"accountId,omitempty"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"createdAt"`
	ReadAt    int64  `json:"readAt,omitempty"`
}

func (db *DB) AddNotification(ctx context.Context, n *Notification) error {
	n.CreatedAt = now()
	var acct any
	if n.AccountID != 0 {
		acct = n.AccountID
	}
	res, err := db.W.ExecContext(ctx, `INSERT INTO notifications(user_id,account_id,kind,title,body,created_at) VALUES(?,?,?,?,?,?)`, n.UserID, acct, n.Kind, truncate(n.Title, 300), truncate(n.Body, 2000), n.CreatedAt)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

// RecentNotificationExists avoids flooding with identical alerts.
func (db *DB) RecentNotificationExists(ctx context.Context, userID, accountID int64, kind string, since int64) (bool, error) {
	var n int
	err := db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id=? AND COALESCE(account_id,0)=? AND kind=? AND created_at>=? AND read_at=0`, userID, accountID, kind, since).Scan(&n)
	return n > 0, err
}

func (db *DB) ListNotifications(ctx context.Context, userID int64, limit int) ([]Notification, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT id,user_id,COALESCE(account_id,0),kind,title,body,created_at,read_at FROM notifications WHERE user_id=? ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.AccountID, &n.Kind, &n.Title, &n.Body, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (db *DB) MarkNotificationsRead(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		_, err := db.W.ExecContext(ctx, `UPDATE notifications SET read_at=? WHERE user_id=? AND read_at=0`, now(), userID)
		return err
	}
	args := append([]any{now(), userID}, int64Args(ids)...)
	_, err := db.W.ExecContext(ctx, `UPDATE notifications SET read_at=? WHERE user_id=? AND id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// DeleteNotifications removes the given notifications (empty ids = all of the user's).
func (db *DB) DeleteNotifications(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		_, err := db.W.ExecContext(ctx, `DELETE FROM notifications WHERE user_id=?`, userID)
		return err
	}
	args := append([]any{userID}, int64Args(ids)...)
	_, err := db.W.ExecContext(ctx, `DELETE FROM notifications WHERE user_id=? AND id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// ---- Blobs & uploads ----------------------------------------------------

func (db *DB) TouchBlob(ctx context.Context, sha string, size int64) error {
	t := now()
	_, err := db.W.ExecContext(ctx, `INSERT INTO blobs(sha256,size,created_at,last_access_at) VALUES(?,?,?,?) ON CONFLICT(sha256) DO UPDATE SET last_access_at=excluded.last_access_at`, sha, size, t, t)
	return err
}

// BlobsToEvict returns blobs not referenced by uploads or the outbox, least
// recently used first, beyond the size budget or older than cutoff.
func (db *DB) BlobsToEvict(ctx context.Context, maxBytes int64, accessCutoff int64) ([]string, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT sha256,size,last_access_at FROM blobs
		WHERE sha256 NOT IN (SELECT blob_sha256 FROM uploads) AND sha256 NOT IN (SELECT raw_sha256 FROM outbox WHERE status NOT IN ('sent','canceled','failed') OR copied_to_sent=0)
		ORDER BY last_access_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	var total int64
	for rows.Next() {
		var sha string
		var size, last int64
		if err := rows.Scan(&sha, &size, &last); err != nil {
			return nil, err
		}
		total += size
		if total > maxBytes || last < accessCutoff {
			out = append(out, sha)
		}
	}
	return out, rows.Err()
}

func (db *DB) DeleteBlobs(ctx context.Context, shas []string) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		for _, s := range shas {
			if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE sha256=?`, s); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE attachments SET blob_sha256='' WHERE blob_sha256=?`, s); err != nil {
				return err
			}
		}
		return nil
	})
}

func (db *DB) AllBlobSHAs(ctx context.Context) (map[string]bool, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT sha256 FROM blobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = true
	}
	return out, rows.Err()
}

type Upload struct {
	ID          string `json:"id"`
	UserID      int64  `json:"-"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	BlobSHA256  string `json:"-"`
	CreatedAt   int64  `json:"createdAt"`
}

func (db *DB) CreateUpload(ctx context.Context, u *Upload) error {
	u.CreatedAt = now()
	_, err := db.W.ExecContext(ctx, `INSERT INTO uploads(id,user_id,filename,content_type,size,blob_sha256,created_at) VALUES(?,?,?,?,?,?,?)`, u.ID, u.UserID, u.Filename, u.ContentType, u.Size, u.BlobSHA256, u.CreatedAt)
	return err
}

func (db *DB) UploadForUser(ctx context.Context, userID int64, id string) (*Upload, error) {
	var u Upload
	err := db.R.QueryRowContext(ctx, `SELECT id,user_id,filename,content_type,size,blob_sha256,created_at FROM uploads WHERE id=? AND user_id=?`, id, userID).
		Scan(&u.ID, &u.UserID, &u.Filename, &u.ContentType, &u.Size, &u.BlobSHA256, &u.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (db *DB) DeleteUpload(ctx context.Context, userID int64, id string) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM uploads WHERE id=? AND user_id=?`, id, userID)
	return err
}

// PurgeUploads removes uploads older than cutoff not referenced by drafts.
func (db *DB) PurgeUploads(ctx context.Context, cutoff int64) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM uploads WHERE created_at<? AND NOT EXISTS (SELECT 1 FROM drafts d, json_each(d.uploads_json) j WHERE j.value=uploads.id)`, cutoff)
	return err
}

// ---- Drafts -------------------------------------------------------------

type Draft struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"-"`
	AccountID    int64     `json:"accountId"`
	Mode         string    `json:"mode"`
	RelatedMsg   int64     `json:"relatedMessageId,omitempty"`
	To           []Address `json:"to"`
	Cc           []Address `json:"cc"`
	Bcc          []Address `json:"bcc"`
	Subject      string    `json:"subject"`
	BodyHTML     string    `json:"bodyHtml"`
	BodyText     string    `json:"bodyText"`
	IsHTML       bool      `json:"isHtml"`
	Uploads      []string  `json:"uploads"`
	KeepOrigAtts bool      `json:"keepOriginalAttachments"`
	ServerFolder int64     `json:"-"`
	ServerUID    uint32    `json:"-"`
	ServerSynced bool      `json:"serverSynced"`
	CreatedAt    int64     `json:"createdAt"`
	UpdatedAt    int64     `json:"updatedAt"`
}

const draftCols = `id,user_id,COALESCE(account_id,0),mode,COALESCE(related_msg,0),to_json,cc_json,bcc_json,subject,body_html,body_text,is_html,uploads_json,keep_orig_atts,COALESCE(server_folder,0),server_uid,server_synced,created_at,updated_at`

func scanDraft(s scanner) (*Draft, error) {
	var d Draft
	var to, cc, bcc, ups string
	var isHTML, keep, synced int
	err := s.Scan(&d.ID, &d.UserID, &d.AccountID, &d.Mode, &d.RelatedMsg, &to, &cc, &bcc, &d.Subject, &d.BodyHTML, &d.BodyText, &isHTML, &ups, &keep, &d.ServerFolder, &d.ServerUID, &synced, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	d.IsHTML, d.KeepOrigAtts, d.ServerSynced = isHTML == 1, keep == 1, synced == 1
	unmarshalAddrs(to, &d.To)
	unmarshalAddrs(cc, &d.Cc)
	unmarshalAddrs(bcc, &d.Bcc)
	d.Uploads = []string{}
	_ = jsonUnmarshal(ups, &d.Uploads)
	return &d, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (db *DB) SaveDraft(ctx context.Context, d *Draft) error {
	t := now()
	d.UpdatedAt = t
	if d.ID == 0 {
		d.CreatedAt = t
		res, err := db.W.ExecContext(ctx, `INSERT INTO drafts(user_id,account_id,mode,related_msg,to_json,cc_json,bcc_json,subject,body_html,body_text,is_html,uploads_json,keep_orig_atts,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			d.UserID, nullID(d.AccountID), d.Mode, nullID(d.RelatedMsg), addrJSON(d.To), addrJSON(d.Cc), addrJSON(d.Bcc), d.Subject, d.BodyHTML, d.BodyText, b2i(d.IsHTML), jsonString(d.Uploads), b2i(d.KeepOrigAtts), t, t)
		if err != nil {
			return err
		}
		d.ID, _ = res.LastInsertId()
		return nil
	}
	res, err := db.W.ExecContext(ctx, `UPDATE drafts SET account_id=?,mode=?,related_msg=?,to_json=?,cc_json=?,bcc_json=?,subject=?,body_html=?,body_text=?,is_html=?,uploads_json=?,keep_orig_atts=?,server_synced=0,updated_at=? WHERE id=? AND user_id=?`,
		nullID(d.AccountID), d.Mode, nullID(d.RelatedMsg), addrJSON(d.To), addrJSON(d.Cc), addrJSON(d.Bcc), d.Subject, d.BodyHTML, d.BodyText, b2i(d.IsHTML), jsonString(d.Uploads), b2i(d.KeepOrigAtts), t, d.ID, d.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) SetDraftServerCopy(ctx context.Context, id, folderID int64, uid uint32, updatedAt int64) error {
	// Only mark as synced if the draft did not change while uploading.
	_, err := db.W.ExecContext(ctx, `UPDATE drafts SET server_folder=?, server_uid=?, server_synced=CASE WHEN updated_at=? THEN 1 ELSE 0 END WHERE id=?`, nullID(folderID), uid, updatedAt, id)
	return err
}

func (db *DB) DraftForUser(ctx context.Context, userID, id int64) (*Draft, error) {
	return scanDraft(db.R.QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE id=? AND user_id=?`, id, userID))
}

func (db *DB) DraftW(ctx context.Context, id int64) (*Draft, error) {
	return scanDraft(db.W.QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE id=?`, id))
}

func (db *DB) ListDrafts(ctx context.Context, userID int64) ([]*Draft, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE user_id=? ORDER BY updated_at DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UnsyncedDrafts returns drafts whose latest version is not on the server.
func (db *DB) UnsyncedDrafts(ctx context.Context, olderThan int64) ([]*Draft, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE server_synced=0 AND account_id IS NOT NULL AND updated_at<? LIMIT 20`, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Draft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (db *DB) DeleteDraft(ctx context.Context, userID, id int64) error {
	res, err := db.W.ExecContext(ctx, `DELETE FROM drafts WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Outbox -------------------------------------------------------------

const (
	OutboxScheduled = "scheduled"
	OutboxSending   = "sending"
	OutboxRetry     = "retry"
	OutboxSent      = "sent"
	OutboxFailed    = "failed"
	OutboxAmbiguous = "ambiguous"
	OutboxCanceled  = "canceled"
)

type OutboxItem struct {
	ID            int64    `json:"id"`
	UserID        int64    `json:"-"`
	AccountID     int64    `json:"accountId"`
	Status        string   `json:"status"`
	MessageIDHdr  string   `json:"messageId"`
	Subject       string   `json:"subject"`
	EnvelopeFrom  string   `json:"from"`
	Rcpts         []string `json:"recipients"`
	RawSHA256     string   `json:"-"`
	RelatedMsg    int64    `json:"relatedMessageId,omitempty"`
	Mode          string   `json:"mode"`
	DraftID       int64    `json:"draftId,omitempty"`
	Attempts      int      `json:"attempts"`
	NextAttemptAt int64    `json:"nextAttemptAt"`
	LastError     string   `json:"lastError,omitempty"`
	SentAt        int64    `json:"sentAt,omitempty"`
	CopiedToSent  bool     `json:"copiedToSent"`
	CreatedAt     int64    `json:"createdAt"`
}

const outboxCols = `id,user_id,account_id,status,message_id_hdr,subject,envelope_from,rcpts_json,raw_sha256,COALESCE(related_msg,0),mode,COALESCE(draft_id,0),attempts,next_attempt_at,last_error,sent_at,copied_to_sent,created_at`

func scanOutbox(s scanner) (*OutboxItem, error) {
	var o OutboxItem
	var rc string
	var copied int
	err := s.Scan(&o.ID, &o.UserID, &o.AccountID, &o.Status, &o.MessageIDHdr, &o.Subject, &o.EnvelopeFrom, &rc, &o.RawSHA256, &o.RelatedMsg, &o.Mode, &o.DraftID, &o.Attempts, &o.NextAttemptAt, &o.LastError, &o.SentAt, &copied, &o.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	o.CopiedToSent = copied == 1
	_ = jsonUnmarshal(rc, &o.Rcpts)
	return &o, nil
}

func (db *DB) CreateOutbox(ctx context.Context, o *OutboxItem) error {
	t := now()
	o.CreatedAt = t
	res, err := db.W.ExecContext(ctx, `INSERT INTO outbox(user_id,account_id,status,message_id_hdr,subject,envelope_from,rcpts_json,raw_sha256,related_msg,mode,draft_id,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.UserID, o.AccountID, o.Status, o.MessageIDHdr, o.Subject, o.EnvelopeFrom, jsonString(o.Rcpts), o.RawSHA256, nullID(o.RelatedMsg), o.Mode, nullID(o.DraftID), o.NextAttemptAt, t, t)
	if err != nil {
		return err
	}
	o.ID, _ = res.LastInsertId()
	return nil
}

func (db *DB) OutboxItem(ctx context.Context, id int64) (*OutboxItem, error) {
	return scanOutbox(db.W.QueryRowContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE id=?`, id))
}

func (db *DB) ListOutbox(ctx context.Context, userID int64) ([]*OutboxItem, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE user_id=? AND (status<>'sent' OR sent_at>?) ORDER BY created_at DESC LIMIT 100`, userID, now()-86400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OutboxItem{}
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ClaimDueOutbox atomically moves due items to "sending".
func (db *DB) ClaimDueOutbox(ctx context.Context, limit int) ([]*OutboxItem, error) {
	var out []*OutboxItem
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE status IN ('scheduled','retry') AND next_attempt_at<=? ORDER BY next_attempt_at LIMIT ?`, now(), limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			o, err := scanOutbox(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, o)
		}
		rows.Close()
		for _, o := range out {
			if _, err := tx.ExecContext(ctx, `UPDATE outbox SET status='sending', attempts=attempts+1, updated_at=? WHERE id=?`, now(), o.ID); err != nil {
				return err
			}
			o.Status = OutboxSending
			o.Attempts++
		}
		return nil
	})
	return out, err
}

// PendingSentCopies returns sent items not yet appended to the Sent folder.
func (db *DB) PendingSentCopies(ctx context.Context) ([]*OutboxItem, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE status='sent' AND copied_to_sent=0 AND sent_at>? LIMIT 20`, now()-7*86400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OutboxItem
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (db *DB) UpdateOutbox(ctx context.Context, o *OutboxItem) error {
	_, err := db.W.ExecContext(ctx, `UPDATE outbox SET status=?, next_attempt_at=?, last_error=?, sent_at=?, copied_to_sent=?, updated_at=? WHERE id=?`,
		o.Status, o.NextAttemptAt, truncate(o.LastError, 1000), o.SentAt, b2i(o.CopiedToSent), now(), o.ID)
	return err
}

// TransitionOutbox changes status only if the current status matches from.
func (db *DB) TransitionOutbox(ctx context.Context, userID, id int64, from []string, to string, nextAttempt int64) error {
	args := []any{to, nextAttempt, now(), id, userID}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := db.W.ExecContext(ctx, `UPDATE outbox SET status=?, next_attempt_at=?, updated_at=? WHERE id=? AND user_id=? AND status IN (`+placeholders(len(from))+`)`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	return nil
}

// RecoverInterruptedSends handles items left in "sending" by a crash: we
// cannot know whether the server accepted them, so they become ambiguous.
func (db *DB) RecoverInterruptedSends(ctx context.Context) (int64, error) {
	res, err := db.W.ExecContext(ctx, `UPDATE outbox SET status='ambiguous', last_error='interrupted while sending (process restart)', updated_at=? WHERE status='sending'`, now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (db *DB) DeleteOutbox(ctx context.Context, userID, id int64) error {
	res, err := db.W.ExecContext(ctx, `DELETE FROM outbox WHERE id=? AND user_id=? AND status IN ('sent','failed','canceled','ambiguous')`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	return nil
}
