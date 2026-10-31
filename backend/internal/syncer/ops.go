package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/events"
	"lightmail/internal/imapx"
	"lightmail/internal/mime"
	"lightmail/internal/store"
)

var ErrNoTargetFolder = errors.New("the account has no such folder")

// FetchBody downloads and caches the text/HTML parts of a message.
func (e *Engine) FetchBody(ctx context.Context, m *store.Message) (*store.Body, error) {
	if m.BodyFetched {
		if b, err := e.db.MessageBody(ctx, m.ID); err == nil {
			return b, nil
		}
	}
	f, err := e.db.Folder(ctx, m.FolderID)
	if err != nil {
		return nil, err
	}
	st := mime.ParseStructure(m.Structure)
	body := &store.Body{}
	err = e.WithConn(ctx, m.AccountID, func(c *imapx.Client) error {
		if err := c.SelectIfNeeded(f.Name); err != nil {
			return err
		}
		for _, tp := range []*mime.TextPart{st.Plain, st.HTML} {
			if tp == nil {
				continue
			}
			path, ok := mime.PartPath(tp.Path)
			if !ok {
				continue
			}
			raw, err := c.FetchSection(imap.UID(m.UID), path, 0, 0)
			if err != nil {
				return err
			}
			if int64(len(raw)) > e.cfg.MaxFetchBytes {
				raw = raw[:e.cfg.MaxFetchBytes]
			}
			text := mime.DecodeText(raw, tp.Encoding, tp.Charset)
			if tp.Type == "text/html" {
				body.HTML = text
			} else {
				body.Text = text
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	index := body.Text
	if index == "" {
		index = mime.HTMLToText(body.HTML)
	}
	if err := e.db.SaveMessageBody(ctx, m, body, index); err != nil {
		return nil, err
	}
	return body, nil
}

// FetchAttachment returns the decoded content of an attachment, caching it
// in the blob store (deduplicated by hash).
func (e *Engine) FetchAttachment(ctx context.Context, m *store.Message, a *store.Attachment) (string, error) {
	if a.BlobSHA256 != "" && e.blobs.Exists(a.BlobSHA256) {
		_ = e.db.TouchBlob(ctx, a.BlobSHA256, a.Size)
		return a.BlobSHA256, nil
	}
	f, err := e.db.Folder(ctx, m.FolderID)
	if err != nil {
		return "", err
	}
	path, ok := mime.PartPath(a.Part)
	if !ok {
		return "", errors.New("invalid attachment part")
	}
	var sha string
	var size int64
	err = e.WithConn(ctx, m.AccountID, func(c *imapx.Client) error {
		if err := c.SelectIfNeeded(f.Name); err != nil {
			return err
		}
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			var err error
			sha, size, err = e.blobs.Put(mime.DecodeTransfer(pr, a.Encoding), e.cfg.MaxFetchBytes)
			pr.CloseWithError(err)
			done <- err
		}()
		_, err := c.StreamSection(imap.UID(m.UID), path, "", pw, e.cfg.MaxFetchBytes*2)
		pw.CloseWithError(err)
		if perr := <-done; err == nil {
			err = perr
		}
		return err
	})
	if err != nil {
		return "", err
	}
	_ = e.db.TouchBlob(ctx, sha, size)
	_ = e.db.SetAttachmentBlob(ctx, a.ID, sha)
	return sha, nil
}

// StreamRaw writes the full RFC 5322 message (EML export) or only its
// header to w.
func (e *Engine) StreamRaw(ctx context.Context, m *store.Message, headerOnly bool, w io.Writer) error {
	f, err := e.db.Folder(ctx, m.FolderID)
	if err != nil {
		return err
	}
	spec := imap.PartSpecifierNone
	if headerOnly {
		spec = imap.PartSpecifierHeader
	}
	return e.WithConn(ctx, m.AccountID, func(c *imapx.Client) error {
		if err := c.SelectIfNeeded(f.Name); err != nil {
			return err
		}
		_, err := c.StreamSection(imap.UID(m.UID), nil, spec, w, e.cfg.MaxFetchBytes)
		return err
	})
}

// Action identifiers for bulk operations.
const (
	ActRead    = "read"
	ActUnread  = "unread"
	ActStar    = "star"
	ActUnstar  = "unstar"
	ActArchive = "archive"
	ActTrash   = "trash"
	ActDelete  = "delete" // permanent
	ActMove    = "move"
	ActSpam    = "spam"
	ActNotSpam = "notspam"

	// internal: set after a reply / forward is sent
	ActAnswered  = "answered"
	ActForwarded = "forwarded"
)

// ActionResult reports per-message outcomes.
type ActionResult struct {
	OK     []int64          `json:"ok"`
	Failed map[int64]string `json:"failed,omitempty"`
}

// Apply performs an action on messages (grouped by folder) on the server
// first, then mirrors it locally.
func (e *Engine) Apply(ctx context.Context, userID int64, msgs []*store.Message, action string, destFolderID int64) *ActionResult {
	res := &ActionResult{Failed: map[int64]string{}}
	type group struct {
		folder *store.Folder
		msgs   []*store.Message
	}
	groups := map[int64]*group{}
	for _, m := range msgs {
		g := groups[m.FolderID]
		if g == nil {
			f, err := e.db.Folder(ctx, m.FolderID)
			if err != nil {
				res.Failed[m.ID] = "folder not found"
				continue
			}
			g = &group{folder: f}
			groups[m.FolderID] = g
		}
		g.msgs = append(g.msgs, m)
	}
	touched := map[int64]int64{} // folder -> account
	for _, g := range groups {
		err := e.applyGroup(ctx, g.folder, g.msgs, action, destFolderID, touched)
		for _, m := range g.msgs {
			if err != nil {
				res.Failed[m.ID] = imapx.Describe(err)
			} else {
				res.OK = append(res.OK, m.ID)
			}
		}
		touched[g.folder.ID] = g.folder.AccountID
	}
	for fid, aid := range touched {
		e.hub.Publish(userID, events.Event{Type: events.TypeMessages, Data: map[string]any{"accountId": aid, "folderId": fid}})
	}
	return res
}

func (e *Engine) applyGroup(ctx context.Context, f *store.Folder, msgs []*store.Message, action string, destFolderID int64, touched map[int64]int64) error {
	uids := make([]uint32, len(msgs))
	for i, m := range msgs {
		uids[i] = m.UID
	}
	set := imapx.UIDSetOf(uids)
	var dest *store.Folder
	switch action {
	case ActArchive, ActTrash, ActMove, ActSpam, ActNotSpam:
		var err error
		dest, err = e.destination(ctx, f, action, destFolderID)
		if err != nil {
			return err
		}
		if dest.ID == f.ID {
			if action == ActTrash {
				action = ActDelete // deleting from the trash is permanent
			} else {
				return nil
			}
		}
	}
	var moved imapx.MoveResult
	err := e.WithConn(ctx, f.AccountID, func(c *imapx.Client) error {
		if err := c.SelectIfNeeded(f.Name); err != nil {
			return err
		}
		switch action {
		case ActRead:
			return c.SetFlags(set, true, imap.FlagSeen)
		case ActUnread:
			return c.SetFlags(set, false, imap.FlagSeen)
		case ActStar:
			return c.SetFlags(set, true, imap.FlagFlagged)
		case ActUnstar:
			return c.SetFlags(set, false, imap.FlagFlagged)
		case ActAnswered:
			return c.SetFlags(set, true, imap.FlagAnswered)
		case ActForwarded:
			return c.SetFlags(set, true, imapx.FlagForwarded)
		case ActDelete:
			return c.DeletePermanently(set)
		default:
			var err error
			moved, err = c.MoveMessages(set, dest.Name)
			return err
		}
	})
	if err != nil {
		return err
	}
	for _, m := range msgs {
		switch action {
		case ActRead, ActUnread:
			err = e.db.SetMessageFlags(ctx, m.ID, "seen", action == ActRead)
		case ActStar, ActUnstar:
			err = e.db.SetMessageFlags(ctx, m.ID, "flagged", action == ActStar)
		case ActAnswered:
			err = e.db.SetMessageFlags(ctx, m.ID, "answered", true)
		case ActForwarded:
			err = e.db.SetMessageFlags(ctx, m.ID, "forwarded", true)
		case ActDelete:
			err = e.db.DeleteMessage(ctx, m.ID)
		default:
			newUID := uint32(0)
			if moved != nil && dest.UIDValidity != 0 && dest.MaxUID != 0 {
				newUID = uint32(moved[imap.UID(m.UID)])
			}
			err = e.db.MoveMessageLocal(ctx, m.ID, dest.ID, newUID)
			touched[dest.ID] = dest.AccountID
		}
		if err != nil {
			return err
		}
	}
	if dest != nil {
		e.Trigger(f.AccountID, false)
	}
	return nil
}

// destination resolves the target folder of an action, creating an
// "Archive" folder when the account has none.
func (e *Engine) destination(ctx context.Context, f *store.Folder, action string, destFolderID int64) (*store.Folder, error) {
	switch action {
	case ActMove:
		d, err := e.db.Folder(ctx, destFolderID)
		if err != nil || d.AccountID != f.AccountID {
			return nil, ErrNoTargetFolder
		}
		return d, nil
	case ActTrash:
		return e.byRole(ctx, f.AccountID, store.RoleTrash)
	case ActSpam:
		return e.byRole(ctx, f.AccountID, store.RoleJunk)
	case ActNotSpam:
		return e.byRole(ctx, f.AccountID, store.RoleInbox)
	case ActArchive:
		if d, err := e.byRole(ctx, f.AccountID, store.RoleArchive); err == nil {
			return d, nil
		}
		if d, err := e.byRole(ctx, f.AccountID, store.RoleAll); err == nil {
			return d, nil // Gmail: archiving = removing the Inbox label
		}
		return e.createFolder(ctx, f.AccountID, "Archive", store.RoleArchive)
	}
	return nil, fmt.Errorf("unknown action %q", action)
}

func (e *Engine) byRole(ctx context.Context, accountID int64, role string) (*store.Folder, error) {
	d, err := e.db.FolderByRole(ctx, accountID, role)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrNoTargetFolder, role)
	}
	return d, nil
}

// CreateFolder creates a mailbox on the server and locally.
func (e *Engine) CreateFolder(ctx context.Context, accountID int64, name string) (*store.Folder, error) {
	return e.createFolder(ctx, accountID, name, "")
}

func (e *Engine) createFolder(ctx context.Context, accountID int64, name, role string) (*store.Folder, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || strings.ContainsAny(name, "\r\n*%") {
		return nil, errors.New("invalid folder name")
	}
	err := e.WithConn(ctx, accountID, func(c *imapx.Client) error {
		return c.Create(name, nil).Wait()
	})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "exist") {
		return nil, err
	}
	return e.db.UpsertFolder(ctx, &store.Folder{AccountID: accountID, Name: name, Delimiter: "/", Role: role, Selectable: true, Subscribed: true, SyncEnabled: true})
}

// DeleteFolder removes a mailbox on the server and locally.
func (e *Engine) DeleteFolder(ctx context.Context, f *store.Folder) error {
	if f.Role == store.RoleInbox {
		return errors.New("the inbox cannot be deleted")
	}
	err := e.WithConn(ctx, f.AccountID, func(c *imapx.Client) error {
		if c.Selected() == f.Name {
			_ = c.Unselect().Wait()
		}
		return c.Delete(f.Name).Wait()
	})
	if err != nil {
		return err
	}
	return e.db.DeleteFoldersNotIn(ctx, f.AccountID, e.otherFolderIDs(ctx, f))
}

func (e *Engine) otherFolderIDs(ctx context.Context, f *store.Folder) []int64 {
	all, _ := e.db.AccountFolders(ctx, f.AccountID)
	var ids []int64
	for _, x := range all {
		if x.ID != f.ID {
			ids = append(ids, x.ID)
		}
	}
	return ids
}

// AppendTo stores a raw message in the folder with the given role and
// returns the folder and new UID (0 when the server does not report it).
func (e *Engine) AppendTo(ctx context.Context, accountID int64, role string, raw []byte, flags []imap.Flag) (*store.Folder, uint32, error) {
	f, err := e.byRole(ctx, accountID, role)
	if err != nil {
		if role != store.RoleSent && role != store.RoleDrafts {
			return nil, 0, err
		}
		name := map[string]string{store.RoleSent: "Sent", store.RoleDrafts: "Drafts"}[role]
		if f, err = e.createFolder(ctx, accountID, name, role); err != nil {
			return nil, 0, err
		}
	}
	var uid imap.UID
	err = e.WithConn(ctx, accountID, func(c *imapx.Client) error {
		var err error
		uid, err = c.AppendMessage(f.Name, raw, flags, time.Now())
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return f, uint32(uid), nil
}

// FolderHasMessageID checks whether a message with this Message-ID exists
// in the folder of the given role (used to resolve ambiguous sends and to
// avoid duplicate copies in Sent).
func (e *Engine) FolderHasMessageID(ctx context.Context, accountID int64, role, msgID string) (bool, error) {
	f, err := e.byRole(ctx, accountID, role)
	if err != nil {
		return false, err
	}
	var found bool
	err = e.WithConn(ctx, accountID, func(c *imapx.Client) error {
		if _, err := c.SelectMailbox(f.Name, false); err != nil {
			return err
		}
		uids, err := c.FindByMessageID(msgID)
		found = len(uids) > 0
		return err
	})
	return found, err
}

// DeleteUID permanently removes a message by folder/UID (draft replacement).
func (e *Engine) DeleteUID(ctx context.Context, folderID int64, uid uint32) error {
	f, err := e.db.Folder(ctx, folderID)
	if err != nil {
		return err
	}
	err = e.WithConn(ctx, f.AccountID, func(c *imapx.Client) error {
		if err := c.SelectIfNeeded(f.Name); err != nil {
			return err
		}
		return c.DeletePermanently(imapx.UIDSetOf([]uint32{uid}))
	})
	if err != nil {
		return err
	}
	_, err = e.db.DeleteMessagesByUID(ctx, folderID, []uint32{uid})
	return err
}

// ServerSearch runs an IMAP SEARCH in a folder and makes sure matching
// messages exist locally (fetching headers of messages outside the local
// window). It returns local message IDs.
func (e *Engine) ServerSearch(ctx context.Context, userID int64, f *store.Folder, criteria *imap.SearchCriteria, limit int) ([]int64, error) {
	a, err := e.db.Account(ctx, f.AccountID)
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	err = e.WithConn(ctx, f.AccountID, func(c *imapx.Client) error {
		if _, err := c.SelectMailbox(f.Name, false); err != nil {
			return err
		}
		data, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return err
		}
		uids = data.AllUIDs()
		if len(uids) > limit {
			uids = uids[len(uids)-limit:]
		}
		local, err := e.db.FolderUIDs(ctx, f.ID)
		if err != nil {
			return err
		}
		have := map[uint32]bool{}
		for _, u := range local {
			have[u] = true
		}
		var missing imap.UIDSet
		n := 0
		for _, u := range uids {
			if !have[uint32(u)] {
				missing.AddNum(u)
				n++
			}
		}
		if n > 0 {
			fs := &folderSync{w: &worker{e: e, id: a.ID, log: e.log}, a: a, c: c, ctx: ctx}
			if _, _, _, err := fs.fetchStore(f, missing); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(uids))
	for _, u := range uids {
		var id int64
		if err := e.db.R.QueryRowContext(ctx, `SELECT id FROM messages WHERE folder_id=? AND uid=?`, f.ID, uint32(u)).Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
