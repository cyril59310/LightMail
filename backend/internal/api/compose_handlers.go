package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lightmail/internal/blobs"
	"lightmail/internal/crypto"
	lmime "lightmail/internal/mime"
	"lightmail/internal/outbox"
	"lightmail/internal/store"
)

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.MaxAttachmentBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "multipart/form-data body expected")
		return
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			badRequest(w, "invalid multipart body")
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name := lmime.SanitizeFilename(part.FileName())
		ct := part.Header.Get("Content-Type")
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt == "" {
			ct = "application/octet-stream"
		} else {
			ct = mt
		}
		sha, size, err := s.Blobs.Put(part, s.Cfg.MaxAttachmentBytes)
		part.Close()
		if errors.Is(err, blobs.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "attachment exceeds the maximum size of "+strconv.FormatInt(s.Cfg.MaxAttachmentBytes>>20, 10)+" MB")
			return
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, "too_large", "attachment too large")
				return
			}
			s.fail(w, r, err)
			return
		}
		_ = s.DB.TouchBlob(r.Context(), sha, size)
		up := &store.Upload{ID: crypto.RandomToken(16), UserID: userFrom(r).ID, Filename: name, ContentType: ct, Size: size, BlobSHA256: sha}
		if err := s.DB.CreateUpload(r.Context(), up); err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, up)
		return
	}
	badRequest(w, "missing file field")
}

func (s *Server) deleteUpload(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.DeleteUpload(r.Context(), userFrom(r).ID, r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- drafts -----------------------------------------------------------

type draftInput struct {
	AccountID    int64           `json:"accountId"`
	Mode         string          `json:"mode"`
	RelatedMsg   int64           `json:"relatedMessageId"`
	To           []store.Address `json:"to"`
	Cc           []store.Address `json:"cc"`
	Bcc          []store.Address `json:"bcc"`
	Subject      string          `json:"subject"`
	BodyHTML     string          `json:"bodyHtml"`
	BodyText     string          `json:"bodyText"`
	IsHTML       bool            `json:"isHtml"`
	Uploads      []string        `json:"uploads"`
	KeepOrigAtts bool            `json:"keepOriginalAttachments"`
}

func validMode(m string) string {
	switch m {
	case "reply", "replyall", "forward":
		return m
	}
	return "new"
}

func (s *Server) checkDraftRefs(r *http.Request, in *draftInput) error {
	u := userFrom(r)
	if in.AccountID != 0 {
		if _, err := s.DB.AccountForUser(r.Context(), u.ID, in.AccountID); err != nil {
			return errors.New("invalid account")
		}
	}
	if in.RelatedMsg != 0 {
		if _, err := s.DB.MessageForUser(r.Context(), u.ID, in.RelatedMsg); err != nil {
			return errors.New("invalid related message")
		}
	}
	for _, id := range in.Uploads {
		if _, err := s.DB.UploadForUser(r.Context(), u.ID, id); err != nil {
			return errors.New("invalid upload")
		}
	}
	if len(in.To)+len(in.Cc)+len(in.Bcc) > 500 || len(in.Uploads) > 50 {
		return errors.New("too many recipients or attachments")
	}
	return nil
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) {
	var in draftInput
	if !readJSON(w, r, &in, s.Cfg.MaxUploadBytes) {
		return
	}
	if err := s.checkDraftRefs(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	u := userFrom(r)
	d := &store.Draft{UserID: u.ID}
	if r.Method == http.MethodPut {
		id, ok := pathID(r, "id")
		if !ok {
			badRequest(w, "invalid draft id")
			return
		}
		existing, err := s.DB.DraftForUser(r.Context(), u.ID, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d = existing
	}
	d.AccountID, d.Mode, d.RelatedMsg = in.AccountID, validMode(in.Mode), in.RelatedMsg
	d.To, d.Cc, d.Bcc, d.Subject = in.To, in.Cc, in.Bcc, in.Subject
	d.BodyHTML, d.BodyText, d.IsHTML, d.Uploads, d.KeepOrigAtts = in.BodyHTML, in.BodyText, in.IsHTML, in.Uploads, in.KeepOrigAtts
	if d.Uploads == nil {
		d.Uploads = []string{}
	}
	if err := s.DB.SaveDraft(r.Context(), d); err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, d)
}

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListDrafts(r.Context(), userFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid draft id")
		return
	}
	d, err := s.DB.DraftForUser(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ups := make([]*store.Upload, 0, len(d.Uploads))
	for _, uid := range d.Uploads {
		if up, err := s.DB.UploadForUser(r.Context(), d.UserID, uid); err == nil {
			ups = append(ups, up)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": d, "uploads": ups})
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid draft id")
		return
	}
	u := userFrom(r)
	d, err := s.DB.DraftForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.ServerFolder != 0 && d.ServerUID != 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.Engine.DeleteUID(ctx, d.ServerFolder, d.ServerUID); err != nil {
			s.Log.Warn("server draft deletion failed", "draft_id", d.ID, "err", err)
		}
	}
	if err := s.DB.DeleteDraft(r.Context(), u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// draftFromMessage opens a server-side draft (e.g. created by another
// client) for editing; the server copy is replaced when saved or sent.
func (s *Server) draftFromMessage(w http.ResponseWriter, r *http.Request) {
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
	html := body.HTML
	if html != "" {
		html = lmime.SanitizeHTML(html, lmime.SanitizeOptions{}).HTML
	}
	d := &store.Draft{UserID: userFrom(r).ID, AccountID: m.AccountID, Mode: "new", To: m.To, Cc: m.Cc, Bcc: m.Bcc,
		Subject: m.Subject, BodyHTML: html, BodyText: body.Text, IsHTML: html != "", Uploads: []string{}}
	if d.Bcc == nil {
		d.Bcc = []store.Address{}
	}
	if err := s.DB.SaveDraft(r.Context(), d); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.DB.SetDraftServerCopy(r.Context(), d.ID, m.FolderID, m.UID, d.UpdatedAt)
	d.ServerFolder, d.ServerUID = m.FolderID, m.UID
	writeJSON(w, http.StatusCreated, d)
}

// BuildDraft converts a stored draft to an outgoing message (server copy).
func (s *Server) BuildDraft(ctx context.Context, d *store.Draft) (*lmime.Outgoing, *store.Account, error) {
	a, err := s.DB.AccountForUser(ctx, d.UserID, d.AccountID)
	if err != nil {
		return nil, nil, err
	}
	out := &lmime.Outgoing{From: store.Address{Name: senderName(a), Addr: a.Email}, To: d.To, Cc: d.Cc, Subject: d.Subject}
	if d.IsHTML {
		out.HTML = d.BodyHTML
	} else {
		out.Text = d.BodyText
	}
	return out, a, nil
}

func senderName(a *store.Account) string {
	if a.SenderName != "" {
		return a.SenderName
	}
	if a.DisplayName != a.Email {
		return a.DisplayName
	}
	return ""
}

// ---- sending -----------------------------------------------------------

type sendInput struct {
	draftInput
	DraftID        int64 `json:"draftId"`
	RequestReceipt bool  `json:"requestReceipt"`
	UndoSeconds    *int  `json:"undoSeconds"`
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var in sendInput
	if !readJSON(w, r, &in, s.Cfg.MaxUploadBytes) {
		return
	}
	if in.AccountID == 0 {
		badRequest(w, "accountId is required")
		return
	}
	if err := s.checkDraftRefs(r, &in.draftInput); err != nil {
		badRequest(w, err.Error())
		return
	}
	u := userFrom(r)
	if in.DraftID != 0 {
		if _, err := s.DB.DraftForUser(r.Context(), u.ID, in.DraftID); err != nil {
			badRequest(w, "invalid draft")
			return
		}
	}
	a, err := s.DB.AccountForUser(r.Context(), u.ID, in.AccountID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := &lmime.Outgoing{
		From: store.Address{Name: senderName(a), Addr: a.Email}, To: in.To, Cc: in.Cc, Bcc: in.Bcc,
		Subject: in.Subject, RequestReadReceipt: in.RequestReceipt,
	}
	if in.IsHTML {
		out.HTML = in.BodyHTML
		out.Text = in.BodyText
	} else {
		out.Text = in.BodyText
	}
	mode := validMode(in.Mode)
	if in.RelatedMsg != 0 {
		rel, err := s.DB.MessageForUser(r.Context(), u.ID, in.RelatedMsg)
		if err != nil {
			badRequest(w, "invalid related message")
			return
		}
		if mode == "reply" || mode == "replyall" {
			out.InReplyTo = rel.MessageID
			out.References = lmime.BuildReferences(rel.References, rel.MessageID)
		}
		if mode == "forward" && in.KeepOrigAtts {
			atts, _ := s.DB.Attachments(r.Context(), rel.ID)
			for i := range atts {
				att := atts[i]
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
				sha, err := s.Engine.FetchAttachment(ctx, rel, &att)
				cancel()
				if err != nil {
					s.mailError(w, r, err)
					return
				}
				out.Attachments = append(out.Attachments, lmime.OutAttachment{
					Filename: att.Filename, ContentType: att.ContentType, Inline: att.Inline, ContentID: att.ContentID,
					Open: func() (io.ReadCloser, error) { return s.Blobs.Open(sha) },
				})
			}
		}
	}
	var total int64
	for _, id := range in.Uploads {
		up, err := s.DB.UploadForUser(r.Context(), u.ID, id)
		if err != nil {
			badRequest(w, "invalid upload")
			return
		}
		total += up.Size
		sha := up.BlobSHA256
		out.Attachments = append(out.Attachments, lmime.OutAttachment{Filename: up.Filename, ContentType: up.ContentType,
			Open: func() (io.ReadCloser, error) { return s.Blobs.Open(sha) }})
	}
	if total > s.Cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "attachments exceed the maximum message size")
		return
	}
	meta := outbox.Meta{Mode: mode, RelatedMsg: in.RelatedMsg, DraftID: in.DraftID}
	if in.UndoSeconds != nil {
		meta.SendAt = time.Now().Add(time.Duration(clamp(*in.UndoSeconds, 0, 30)) * time.Second)
	}
	item, err := s.Outbox.Enqueue(r.Context(), u.ID, a, out, meta)
	if err != nil {
		if errors.Is(err, outbox.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
			return
		}
		if strings.Contains(err.Error(), "address") || errors.Is(err, lmime.ErrNoRecipients) || strings.Contains(err.Error(), "recipients") {
			badRequest(w, err.Error())
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) listOutbox(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListOutbox(r.Context(), userFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) cancelOutbox(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid id")
		return
	}
	if err := s.Outbox.Cancel(r.Context(), userFrom(r).ID, id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "too_late", "the message is already being sent")
			return
		}
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) retryOutbox(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid id")
		return
	}
	if err := s.Outbox.Retry(r.Context(), userFrom(r).ID, id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "invalid_state", "only failed or uncertain messages can be resent")
			return
		}
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteOutbox(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid id")
		return
	}
	if err := s.DB.DeleteOutbox(r.Context(), userFrom(r).ID, id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "invalid_state", "pending messages cannot be deleted, cancel them first")
			return
		}
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
