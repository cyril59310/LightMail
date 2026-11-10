package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lightmail/internal/events"
	"lightmail/internal/imapx"
	lmime "lightmail/internal/mime"
	"lightmail/internal/search"
	"lightmail/internal/store"
	"lightmail/internal/syncer"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A cursor is "<id>.<value>": the sort value of the last row, a number for
// numeric sorts or base64url text for text sorts.
func parseCursor(s, sort string) *store.Cursor {
	id, v, ok := strings.Cut(s, ".")
	if !ok {
		return nil
	}
	c := &store.Cursor{}
	var err error
	if c.ID, err = strconv.ParseInt(id, 10, 64); err != nil {
		return nil
	}
	if store.SortIsText(sort) {
		b, err := base64.RawURLEncoding.DecodeString(v)
		if err != nil {
			return nil
		}
		c.Text = string(b)
	} else if c.Num, err = strconv.ParseInt(v, 10, 64); err != nil {
		return nil
	}
	return c
}

func cursorOf(msgs []*store.Message, limit int, sort string) string {
	if len(msgs) < limit || len(msgs) == 0 {
		return ""
	}
	last := msgs[len(msgs)-1]
	n, txt := store.SortValue(last, sort)
	if store.SortIsText(sort) {
		return itoa(last.ID) + "." + base64.RawURLEncoding.EncodeToString([]byte(txt))
	}
	return itoa(last.ID) + "." + itoa(n)
}

// listSort reads the sort and order query parameters into f.
func listSort(w http.ResponseWriter, r *http.Request, f *store.MessageFilter) bool {
	q := r.URL.Query()
	f.Sort = q.Get("sort")
	if !store.ValidSort(f.Sort) {
		badRequest(w, "invalid sort")
		return false
	}
	switch q.Get("order") {
	case "", "desc":
	case "asc":
		f.Asc = true
	default:
		badRequest(w, "invalid order")
		return false
	}
	if c := q.Get("cursor"); c != "" {
		if f.Cursor = parseCursor(c, f.Sort); f.Cursor == nil {
			badRequest(w, "invalid cursor")
			return false
		}
	}
	return true
}

func pageLimit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 {
		return 50
	}
	return min(n, 200)
}

// listMessages serves folder, unified and starred views.
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	u := userFrom(r)
	f := store.MessageFilter{UserID: u.ID, AccountID: queryID(r, "accountId"), Limit: pageLimit(r)}
	if !listSort(w, r, &f) {
		return
	}
	switch view := q.Get("view"); view {
	case "", "folder":
		fid := queryID(r, "folderId")
		if fid == 0 {
			badRequest(w, "folderId is required")
			return
		}
		if _, err := s.DB.FolderForUser(r.Context(), u.ID, fid); err != nil {
			s.fail(w, r, err)
			return
		}
		f.FolderID = fid
	case "unified":
		role := q.Get("role")
		if role == "" {
			role = store.RoleInbox
		}
		switch role {
		case store.RoleInbox, store.RoleSent, store.RoleDrafts, store.RoleTrash, store.RoleJunk, store.RoleArchive:
		default:
			badRequest(w, "invalid role")
			return
		}
		f.Role = role
		if f.AccountID == 0 {
			f.AccountIDs = s.unifiedAccounts(r.Context(), u.ID)
		}
	case "starred":
		f.Starred, f.ExcludeTrash = true, true
	default:
		badRequest(w, "invalid view")
		return
	}
	if q.Get("unread") == "1" {
		t := true
		f.Unread = &t
	}
	if q.Get("attachments") == "1" {
		t := true
		f.HasAtt = &t
	}
	msgs, err := s.DB.ListMessages(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": msgs, "nextCursor": cursorOf(msgs, f.Limit, f.Sort)})
}

func (s *Server) userLocation(ctx context.Context, userID int64) *time.Location {
	if st, err := s.DB.Settings(ctx, userID); err == nil {
		if tz := st["timezone"]; tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				return loc
			}
		}
	}
	return time.Local
}

// searchMessages runs a local search (cached metadata + indexed bodies), or
// an IMAP SEARCH in one folder when server=1.
func (s *Server) searchMessages(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	qs := r.URL.Query()
	raw := strings.TrimSpace(qs.Get("q"))
	if len(raw) > 500 {
		badRequest(w, "query too long")
		return
	}
	parsed := search.Parse(raw, s.userLocation(r.Context(), u.ID))
	f := store.MessageFilter{UserID: u.ID, AccountID: queryID(r, "accountId"), FolderID: queryID(r, "folderId"), Limit: pageLimit(r)}
	if !listSort(w, r, &f) {
		return
	}
	if parsed.Account != "" && f.AccountID == 0 {
		if list, err := s.DB.ListAccounts(r.Context(), u.ID); err == nil {
			for _, a := range list {
				if strings.Contains(a.Email, parsed.Account) {
					f.AccountID = a.ID
				}
			}
		}
	}
	if f.FolderID != 0 {
		if _, err := s.DB.FolderForUser(r.Context(), u.ID, f.FolderID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if qs.Get("server") == "1" {
		if f.FolderID == 0 {
			badRequest(w, "server-side search requires a folder")
			return
		}
		fo, _ := s.DB.Folder(r.Context(), f.FolderID)
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		ids, err := s.Engine.ServerSearch(ctx, u.ID, fo, parsed.IMAP(), 500)
		if err != nil {
			writeError(w, http.StatusBadGateway, "imap_error", "server search failed: "+imapx.Describe(err))
			return
		}
		msgs, err := s.DB.MessagesForUser(r.Context(), u.ID, ids)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if parsed.HasAttachment != nil {
			kept := msgs[:0]
			for _, m := range msgs {
				if m.HasAttachments == *parsed.HasAttachment {
					kept = append(kept, m)
				}
			}
			msgs = kept
		}
		store.SortMessages(msgs, f.Sort, f.Asc)
		writeJSON(w, http.StatusOK, map[string]any{"items": msgs, "nextCursor": "", "server": true, "warning": parsed.Describe()})
		return
	}
	parsed.Filter(&f)
	msgs, err := s.DB.ListMessages(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": msgs, "nextCursor": cursorOf(msgs, f.Limit, f.Sort), "warning": parsed.Describe()})
}

func (s *Server) messageParam(w http.ResponseWriter, r *http.Request) (*store.Message, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid message id")
		return nil, false
	}
	m, err := s.DB.MessageForUser(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return m, true
}

func (s *Server) mailError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, imapx.ErrMessageGone):
		writeError(w, http.StatusGone, "gone", err.Error())
	case errors.Is(err, imapx.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout", "the mail server did not answer in time")
	default:
		writeError(w, http.StatusBadGateway, "mail_server", "mail server error: "+imapx.Describe(err))
	}
}

// getMessage returns metadata, attachments, the text body (downloaded on
// first access) and the conversation.
func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.messageParam(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	body, err := s.Engine.FetchBody(ctx, m)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	atts, err := s.DB.Attachments(r.Context(), m.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	folder, _ := s.DB.Folder(r.Context(), m.FolderID)
	remote := 0
	if body.HTML != "" {
		remote = lmime.SanitizeHTML(body.HTML, lmime.SanitizeOptions{}).RemoteBlocked
	}
	thread, _ := s.DB.ThreadMessages(r.Context(), userFrom(r).ID, m.AccountID, m.ThreadID)
	text := body.Text
	if text == "" && body.HTML != "" {
		text = lmime.HTMLToText(body.HTML)
	}
	m.BodyFetched = true
	resp := map[string]any{
		"message":       m,
		"folder":        folder,
		"attachments":   atts,
		"text":          text,
		"hasHtml":       body.HTML != "",
		"remoteContent": remote,
		"thread":        thread,
	}
	if r.URL.Query().Get("quote") == "1" && body.HTML != "" {
		// Sanitised fragment without <style> blocks, for quoting in replies.
		resp["quoteHtml"] = lmime.SanitizeHTML(body.HTML, lmime.SanitizeOptions{StripStyles: true}).HTML
	}
	writeJSON(w, http.StatusOK, resp)
}

// messageHTML serves the sanitised HTML body as an isolated document for a
// sandboxed iframe. Scripts are removed by the sanitiser, forbidden by the
// CSP and by the iframe sandbox. Remote content is blocked unless remote=1.
func (s *Server) messageHTML(w http.ResponseWriter, r *http.Request) {
	m, ok := s.messageParam(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	body, err := s.Engine.FetchBody(ctx, m)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	allowRemote := r.URL.Query().Get("remote") == "1"
	atts, _ := s.DB.Attachments(r.Context(), m.ID)
	cids := map[string]int64{}
	for _, a := range atts {
		if a.ContentID != "" {
			cids[a.ContentID] = a.ID
		}
	}
	var html string
	if body.HTML != "" {
		html = lmime.SanitizeHTML(body.HTML, lmime.SanitizeOptions{AllowRemote: allowRemote, CIDURL: func(cid string) (string, bool) {
			id, ok := cids[cid]
			if !ok {
				return "", false
			}
			return fmt.Sprintf("/api/v1/messages/%d/attachments/%d?inline=1", m.ID, id), true
		}}).HTML
	} else {
		html = lmime.TextToHTML(body.Text)
	}
	img := "'self' data:"
	style := "'unsafe-inline'"
	font := "data:"
	if allowRemote {
		img += " https: http:"
		style += " https:"
		font += " https:"
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; img-src "+img+"; style-src "+style+"; font-src "+font+
		"; media-src 'none'; object-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-same-origin allow-popups allow-popups-to-escape-sandbox")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "private, no-store")
	_, _ = io.WriteString(w, lmime.WrapDocument(html))
}

var inlineSafeTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

func contentDisposition(kind, filename string) string {
	return mime.FormatMediaType(kind, map[string]string{"filename": filename})
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	m, ok := s.messageParam(w, r)
	if !ok {
		return
	}
	attID, ok := pathID(r, "att")
	if !ok {
		badRequest(w, "invalid attachment id")
		return
	}
	atts, err := s.DB.Attachments(r.Context(), m.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var att *store.Attachment
	for i := range atts {
		if atts[i].ID == attID {
			att = &atts[i]
		}
	}
	if att == nil {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	sha, err := s.Engine.FetchAttachment(ctx, m, att)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	f, err := s.Blobs.Open(sha)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer f.Close()
	ct := strings.ToLower(att.ContentType)
	h := w.Header()
	// Never let a downloaded file render as an active document in our origin.
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Get("inline") == "1" && inlineSafeTypes[ct] {
		h.Set("Content-Type", ct)
		h.Set("Content-Disposition", contentDisposition("inline", att.Filename))
		h.Set("X-Frame-Options", "SAMEORIGIN")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", contentDisposition("attachment", att.Filename))
	}
	http.ServeContent(w, r, "", time.Unix(m.InternalDate, 0), f)
}

func (s *Server) messageRaw(w http.ResponseWriter, r *http.Request) {
	m, ok := s.messageParam(w, r)
	if !ok {
		return
	}
	name := lmime.SanitizeFilename(strings.TrimSpace(m.Subject))
	if name == "attachment" || name == "" {
		name = "message"
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", name+".eml"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := s.Engine.StreamRaw(ctx, m, false, w); err != nil {
		s.Log.Warn("raw export failed", "message_id", m.ID, "err", err)
	}
}

func (s *Server) messageHeaders(w http.ResponseWriter, r *http.Request) {
	m, ok := s.messageParam(w, r)
	if !ok {
		return
	}
	var sb strings.Builder
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.Engine.StreamRaw(ctx, m, true, &limitedBuilder{b: &sb, max: 256 << 10}); err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"headers": sb.String(), "size": m.Size, "uid": m.UID, "messageId": m.MessageID})
}

type limitedBuilder struct {
	b   *strings.Builder
	max int
}

func (l *limitedBuilder) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func (s *Server) messageActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs      []int64 `json:"ids"`
		Action   string  `json:"action"`
		FolderID int64   `json:"folderId"`
	}
	if !readJSON(w, r, &req, 256<<10) {
		return
	}
	switch req.Action {
	case syncer.ActRead, syncer.ActUnread, syncer.ActStar, syncer.ActUnstar, syncer.ActArchive, syncer.ActTrash,
		syncer.ActDelete, syncer.ActMove, syncer.ActSpam, syncer.ActNotSpam:
	default:
		badRequest(w, "invalid action")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		badRequest(w, "between 1 and 1000 message ids are required")
		return
	}
	u := userFrom(r)
	msgs, err := s.DB.MessagesForUser(r.Context(), u.ID, req.IDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(msgs) == 0 {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	if req.Action == syncer.ActMove {
		dest, err := s.DB.FolderForUser(r.Context(), u.ID, req.FolderID)
		if err != nil {
			badRequest(w, "invalid destination folder")
			return
		}
		for _, m := range msgs {
			if m.AccountID != dest.AccountID {
				badRequest(w, "messages can only be moved within the same account")
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res := s.Engine.Apply(ctx, u.ID, msgs, req.Action, req.FolderID)
	status := http.StatusOK
	if len(res.OK) == 0 {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, res)
}

// ---- folders -----------------------------------------------------------

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListFolders(r.Context(), userFrom(r).ID, queryID(r, "accountId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if list == nil {
		list = []*store.Folder{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) counters(w http.ResponseWriter, r *http.Request) {
	c, err := s.DB.Counters(r.Context(), userFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID int64  `json:"accountId"`
		ParentID  int64  `json:"parentId"`
		Name      string `json:"name"`
	}
	if !readJSON(w, r, &req, 4<<10) {
		return
	}
	if _, err := s.DB.AccountForUser(r.Context(), userFrom(r).ID, req.AccountID); err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := s.Engine.CreateFolder(r.Context(), req.AccountID, req.ParentID, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "folder", imapx.Describe(err))
		return
	}
	s.Hub.Publish(userFrom(r).ID, events.Event{Type: events.TypeFolders, Data: map[string]any{"accountId": f.AccountID}})
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) folderParam(w http.ResponseWriter, r *http.Request) (*store.Folder, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid folder id")
		return nil, false
	}
	f, err := s.DB.FolderForUser(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return f, true
}

func (s *Server) updateFolder(w http.ResponseWriter, r *http.Request) {
	f, ok := s.folderParam(w, r)
	if !ok {
		return
	}
	var req struct {
		SyncEnabled *bool `json:"syncEnabled"`
	}
	if !readJSON(w, r, &req, 4<<10) {
		return
	}
	if req.SyncEnabled != nil {
		if err := s.DB.SetFolderSyncEnabled(r.Context(), f.ID, *req.SyncEnabled); err != nil {
			s.fail(w, r, err)
			return
		}
		if *req.SyncEnabled {
			s.Engine.Trigger(f.AccountID, false)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	f, ok := s.folderParam(w, r)
	if !ok {
		return
	}
	if f.Role != "" {
		badRequest(w, "special folders cannot be deleted")
		return
	}
	if err := s.Engine.DeleteFolder(r.Context(), f); err != nil {
		s.mailError(w, r, err)
		return
	}
	s.Hub.Publish(userFrom(r).ID, events.Event{Type: events.TypeFolders, Data: map[string]any{"accountId": f.AccountID}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) contacts(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 1 || len(q) > 100 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	list, err := s.DB.Contacts(r.Context(), userFrom(r).ID, q, 8)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// unifiedAccounts returns the accounts selected for unified views (nil = all).
func (s *Server) unifiedAccounts(ctx context.Context, userID int64) []int64 {
	st, err := s.DB.Settings(ctx, userID)
	if err != nil || st["unifiedAccounts"] == "" || st["unifiedAccounts"] == "all" {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(st["unifiedAccounts"]), &ids); err != nil {
		return nil
	}
	if len(ids) == 0 {
		return []int64{-1} // explicit empty selection
	}
	return ids
}
