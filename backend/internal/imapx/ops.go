package imapx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"lightmail/internal/mime"
	"lightmail/internal/store"
)

const FlagForwarded = imap.Flag("$Forwarded")

// ListMailboxes returns every mailbox with special-use attributes when the
// server supports them.
func (c *Client) ListMailboxes() ([]*imap.ListData, error) {
	var opts *imap.ListOptions
	if c.Caps.Has(imap.CapSpecialUse) && c.Caps.Has(imap.CapListExtended) {
		opts = &imap.ListOptions{ReturnSpecialUse: true}
	}
	return c.List("", "*", opts).Collect()
}

// RoleFor maps a mailbox to a LightMail role using SPECIAL-USE attributes,
// then well-known names as a fallback.
func RoleFor(l *imap.ListData) string {
	for _, a := range l.Attrs {
		switch strings.ToLower(string(a)) {
		case `\sent`:
			return store.RoleSent
		case `\drafts`:
			return store.RoleDrafts
		case `\trash`:
			return store.RoleTrash
		case `\junk`:
			return store.RoleJunk
		case `\archive`:
			return store.RoleArchive
		case `\all`:
			return store.RoleAll
		case `\flagged`:
			return store.RoleFlagged
		case `\important`:
			return store.RoleImportant
		}
	}
	name := l.Mailbox
	if strings.EqualFold(name, "INBOX") {
		return store.RoleInbox
	}
	leaf := name
	if l.Delim != 0 {
		if i := strings.LastIndexByte(name, byte(l.Delim)); i >= 0 {
			leaf = name[i+1:]
		}
	}
	switch strings.ToLower(leaf) {
	case "sent", "sent items", "sent messages", "sent mail", "envoyés", "éléments envoyés", "messages envoyés", "envoyes":
		return store.RoleSent
	case "drafts", "draft", "brouillons":
		return store.RoleDrafts
	case "trash", "deleted", "deleted items", "deleted messages", "corbeille", "éléments supprimés", "bin":
		return store.RoleTrash
	case "junk", "spam", "junk e-mail", "junk email", "courrier indésirable", "indésirables", "bulk mail":
		return store.RoleJunk
	case "archive", "archives":
		return store.RoleArchive
	}
	return ""
}

// IsSelectable reports whether a mailbox can hold messages.
func IsSelectable(l *imap.ListData) bool {
	for _, a := range l.Attrs {
		switch a {
		case imap.MailboxAttrNoSelect, imap.MailboxAttrNonExistent:
			return false
		}
	}
	return true
}

var headerFields = []string{"References", "In-Reply-To", "X-Priority", "Importance", "Priority"}

// HeaderFetchOptions is the minimal set of items needed for the list view.
func (c *Client) HeaderFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		InternalDate:  true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		ModSeq:        c.HasCondStore(),
		BodySection:   []*imap.FetchItemBodySection{{Specifier: imap.PartSpecifierHeader, HeaderFields: headerFields, Peek: true}},
	}
}

// FetchHeaders fetches envelope data for numSet and calls fn for each message.
func (c *Client) FetchHeaders(numSet imap.NumSet, fn func(*imapclient.FetchMessageBuffer) error) error {
	cmd := c.Fetch(numSet, c.HeaderFetchOptions())
	var cbErr error
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			cbErr = errors.Join(cbErr, err)
			continue
		}
		if cbErr == nil && buf.UID != 0 {
			cbErr = fn(buf)
		}
	}
	if err := cmd.Close(); err != nil {
		return err
	}
	return cbErr
}

// FlagState is the flag set of one message.
type FlagState struct {
	UID    imap.UID
	Flags  []imap.Flag
	ModSeq uint64
}

// FetchFlags returns flags for uids, optionally only those changed since
// modseq (CONDSTORE).
func (c *Client) FetchFlags(uids imap.UIDSet, changedSince uint64) ([]FlagState, error) {
	opts := &imap.FetchOptions{UID: true, Flags: true}
	if changedSince > 0 && c.HasCondStore() {
		opts.ModSeq = true
		opts.ChangedSince = changedSince
	}
	msgs, err := c.Fetch(uids, opts).Collect()
	if err != nil {
		return nil, err
	}
	out := make([]FlagState, 0, len(msgs))
	for _, m := range msgs {
		if m.UID != 0 {
			out = append(out, FlagState{UID: m.UID, Flags: m.Flags, ModSeq: m.ModSeq})
		}
	}
	return out, nil
}

// UIDsInRange returns the server UIDs within [from, to] (to=0 means *).
func (c *Client) UIDsInRange(from, to imap.UID) ([]imap.UID, error) {
	var set imap.UIDSet
	if to == 0 {
		set.AddRange(from, 0)
	} else {
		set.AddRange(from, to)
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

// SeqOfUID returns the sequence number of uid (0 if absent).
func (c *Client) SeqOfUID(uid imap.UID) (uint32, error) {
	data, err := c.Search(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(uid)}}, nil).Wait()
	if err != nil {
		return 0, err
	}
	seqs := data.AllSeqNums()
	if len(seqs) == 0 {
		return 0, nil
	}
	return seqs[0], nil
}

// FetchSection downloads one body section (without setting \Seen), up to
// maxBytes. Partial is used for snippets.
func (c *Client) FetchSection(uid imap.UID, part []int, offset, maxBytes int64) ([]byte, error) {
	sec := &imap.FetchItemBodySection{Part: part, Peek: true}
	if maxBytes > 0 {
		sec.Partial = &imap.SectionPartial{Offset: offset, Size: maxBytes}
	}
	cmd := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
	defer cmd.Close()
	msg := cmd.Next()
	if msg == nil {
		if err := cmd.Close(); err != nil {
			return nil, err
		}
		return nil, ErrMessageGone
	}
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		if bs, ok := item.(imapclient.FetchItemDataBodySection); ok && bs.Literal != nil {
			lim := maxBytes
			if lim <= 0 {
				lim = 64 << 20
			}
			data, err := io.ReadAll(io.LimitReader(bs.Literal, lim+1))
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > lim && maxBytes <= 0 {
				return nil, ErrTooLarge
			}
			return data, cmd.Close()
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, err
	}
	return nil, ErrMessageGone
}

// StreamSection copies a body section (or the whole message when part is
// nil) to w, refusing more than limit bytes.
func (c *Client) StreamSection(uid imap.UID, part []int, spec imap.PartSpecifier, w io.Writer, limit int64) (int64, error) {
	sec := &imap.FetchItemBodySection{Part: part, Specifier: spec, Peek: true}
	cmd := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
	defer cmd.Close()
	msg := cmd.Next()
	if msg == nil {
		if err := cmd.Close(); err != nil {
			return 0, err
		}
		return 0, ErrMessageGone
	}
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		if bs, ok := item.(imapclient.FetchItemDataBodySection); ok && bs.Literal != nil {
			if bs.Literal.Size() > limit {
				return 0, ErrTooLarge
			}
			n, err := io.Copy(w, io.LimitReader(bs.Literal, limit))
			if err != nil {
				return n, err
			}
			return n, cmd.Close()
		}
	}
	if err := cmd.Close(); err != nil {
		return 0, err
	}
	return 0, ErrMessageGone
}

var (
	ErrMessageGone = errors.New("message no longer exists on the server")
	ErrTooLarge    = errors.New("message part exceeds the configured size limit")
)

// SetFlags adds or removes flags on uids.
func (c *Client) SetFlags(uids imap.UIDSet, add bool, flags ...imap.Flag) error {
	op := imap.StoreFlagsDel
	if add {
		op = imap.StoreFlagsAdd
	}
	return c.Store(uids, &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close()
}

// MoveResult maps source UIDs to destination UIDs when the server reports
// COPYUID (UIDPLUS); empty otherwise.
type MoveResult map[imap.UID]imap.UID

// MoveMessages moves uids to dest using MOVE, or COPY + \Deleted + EXPUNGE.
func (c *Client) MoveMessages(uids imap.UIDSet, dest string) (MoveResult, error) {
	res := MoveResult{}
	if c.Caps.Has(imap.CapMove) {
		data, err := c.Move(uids, dest).Wait()
		if err != nil {
			return nil, err
		}
		if data != nil {
			mapUIDs(res, data.SourceUIDs, data.DestUIDs)
		}
		return res, nil
	}
	data, err := c.Copy(uids, dest).Wait()
	if err != nil {
		return nil, err
	}
	if data != nil {
		mapUIDs(res, data.SourceUIDs, data.DestUIDs)
	}
	if err := c.SetFlags(uids, true, imap.FlagDeleted); err != nil {
		return res, err
	}
	return res, c.ExpungeUIDs(uids)
}

func mapUIDs(res MoveResult, src, dst imap.NumSet) {
	s, ok1 := src.(imap.UIDSet)
	d, ok2 := dst.(imap.UIDSet)
	if !ok1 || !ok2 {
		return
	}
	sl, ok1 := s.Nums()
	dl, ok2 := d.Nums()
	if !ok1 || !ok2 || len(sl) != len(dl) {
		return
	}
	for i := range sl {
		res[sl[i]] = dl[i]
	}
}

// ExpungeUIDs permanently removes uids already flagged \Deleted. Without
// UIDPLUS a plain EXPUNGE is used (it also removes other \Deleted messages
// of the mailbox, which is the standard IMAP semantics).
func (c *Client) ExpungeUIDs(uids imap.UIDSet) error {
	if c.Caps.Has(imap.CapUIDPlus) {
		return c.UIDExpunge(uids).Close()
	}
	return c.Expunge().Close()
}

// DeletePermanently flags uids \Deleted and expunges them.
func (c *Client) DeletePermanently(uids imap.UIDSet) error {
	if err := c.SetFlags(uids, true, imap.FlagDeleted); err != nil {
		return err
	}
	return c.ExpungeUIDs(uids)
}

// AppendMessage stores raw in mailbox and returns the new UID when known.
func (c *Client) AppendMessage(mailbox string, raw []byte, flags []imap.Flag, t time.Time) (imap.UID, error) {
	cmd := c.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: t})
	if _, err := cmd.Write(raw); err != nil {
		cmd.Close()
		return 0, err
	}
	if err := cmd.Close(); err != nil {
		return 0, err
	}
	data, err := cmd.Wait()
	if err != nil {
		return 0, err
	}
	return data.UID, nil
}

// FindByMessageID searches the selected mailbox for a Message-ID header.
func (c *Client) FindByMessageID(msgID string) ([]imap.UID, error) {
	data, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: msgID}}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	return data.AllUIDs(), nil
}

// ---- conversion helpers -------------------------------------------------

// FlagsToState converts IMAP flags to booleans and remaining keywords.
func FlagsToState(flags []imap.Flag) (seen, flagged, answered, draft, forwarded bool, keywords string) {
	var kw []string
	for _, f := range flags {
		switch strings.ToLower(string(f)) {
		case `\seen`:
			seen = true
		case `\flagged`:
			flagged = true
		case `\answered`:
			answered = true
		case `\draft`:
			draft = true
		case `$forwarded`:
			forwarded = true
		case `\recent`, `\deleted`:
		default:
			if len(kw) < 20 {
				kw = append(kw, string(f))
			}
		}
	}
	sort.Strings(kw)
	return seen, flagged, answered, draft, forwarded, strings.Join(kw, " ")
}

func convAddrs(in []imap.Address) []store.Address {
	out := make([]store.Address, 0, len(in))
	for i := range in {
		a := in[i]
		if a.IsGroupStart() || a.IsGroupEnd() {
			continue
		}
		addr := a.Addr()
		if addr == "" {
			continue
		}
		out = append(out, store.Address{Name: mime.DecodeHeader(a.Name), Addr: strings.ToLower(addr)})
	}
	return out
}

// ToMessage converts a header fetch into a store.Message (without IDs).
func ToMessage(buf *imapclient.FetchMessageBuffer) (*store.Message, mime.Analysis) {
	m := &store.Message{UID: uint32(buf.UID), Size: buf.RFC822Size, ModSeq: buf.ModSeq}
	m.InternalDate = buf.InternalDate.Unix()
	if env := buf.Envelope; env != nil {
		m.Subject = strings.TrimSpace(env.Subject)
		m.MessageID = normalizeMsgID(env.MessageID)
		if len(env.InReplyTo) > 0 {
			m.InReplyTo = normalizeMsgID(env.InReplyTo[0])
		}
		from := convAddrs(env.From)
		if len(from) == 0 {
			from = convAddrs(env.Sender)
		}
		if len(from) > 0 {
			m.From = from[0]
		}
		m.To = convAddrs(env.To)
		m.Cc = convAddrs(env.Cc)
		m.Bcc = convAddrs(env.Bcc)
		m.ReplyTo = convAddrs(env.ReplyTo)
		if !env.Date.IsZero() && env.Date.Year() > 1980 {
			m.Date = env.Date.Unix()
		}
	}
	if m.Date == 0 || m.Date > time.Now().Add(48*time.Hour).Unix() {
		m.Date = m.InternalDate
	}
	m.Seen, m.Flagged, m.Answered, m.Draft, m.Forwarded, m.Keywords = FlagsToState(buf.Flags)
	for _, sec := range buf.BodySection {
		hdr := parseHeaderBlock(sec.Bytes)
		if refs := hdr.Get("References"); refs != "" {
			m.References = normalizeRefs(refs)
		}
		if m.InReplyTo == "" {
			if irt := hdr.Get("In-Reply-To"); irt != "" {
				m.InReplyTo = normalizeMsgID(strings.Fields(irt + " ")[0])
			}
		}
		m.Priority = priority(hdr)
	}
	a := mime.Analyze(buf.BodyStructure)
	m.Structure = a.Structure.JSON()
	m.HasAttachments = a.HasAttachments
	m.Attachments = a.Attachments
	return m, a
}

func parseHeaderBlock(b []byte) textproto.MIMEHeader {
	r := textproto.NewReader(bufio.NewReader(io.MultiReader(bytes.NewReader(b), strings.NewReader("\r\n\r\n"))))
	h, _ := r.ReadMIMEHeader()
	return h
}

func priority(h textproto.MIMEHeader) int {
	xp := strings.TrimSpace(h.Get("X-Priority"))
	imp := strings.ToLower(strings.TrimSpace(h.Get("Importance")))
	pr := strings.ToLower(strings.TrimSpace(h.Get("Priority")))
	switch {
	case strings.HasPrefix(xp, "1"), strings.HasPrefix(xp, "2"), imp == "high", pr == "urgent":
		return 1
	case strings.HasPrefix(xp, "4"), strings.HasPrefix(xp, "5"), imp == "low", pr == "non-urgent":
		return -1
	}
	return 0
}

func normalizeMsgID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Trim(s, "<>")
	if s == "" || len(s) > 900 {
		return ""
	}
	return "<" + s + ">"
}

func normalizeRefs(s string) string {
	var out []string
	for _, f := range strings.Fields(strings.NewReplacer(">", "> ", "<", " <").Replace(s)) {
		if strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">") && len(f) > 2 {
			out = append(out, f)
		}
	}
	if len(out) > 30 {
		out = append(out[:1], out[len(out)-29:]...)
	}
	return strings.Join(out, " ")
}

// PartPathOf parses an IMAP section path such as "1.2".
func PartPathOf(s string) ([]int, bool) { return mime.PartPath(s) }

// UIDSetOf builds a UID set from a list.
func UIDSetOf(uids []uint32) imap.UIDSet {
	var s imap.UIDSet
	for _, u := range uids {
		s.AddNum(imap.UID(u))
	}
	return s
}

// Describe returns a short human-readable reason for an IMAP error.
func Describe(err error) string {
	var ie *imap.Error
	if errors.As(err, &ie) {
		return fmt.Sprintf("%s %s", ie.Type, strings.TrimSpace(ie.Text))
	}
	return err.Error()
}
