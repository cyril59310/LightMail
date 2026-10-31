package mime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"

	"lightmail/internal/store"
)

type OutAttachment struct {
	Filename    string
	ContentType string
	Inline      bool
	ContentID   string
	Open        func() (io.ReadCloser, error)
}

// Outgoing describes a message to send.
type Outgoing struct {
	From        store.Address
	To, Cc, Bcc []store.Address
	ReplyTo     []store.Address
	Subject     string
	Text        string
	HTML        string // optional; when set a multipart/alternative is built
	MessageID   string // without angle brackets
	InReplyTo   string // with angle brackets
	References  []string
	Date        time.Time
	Attachments []OutAttachment
	// RequestReadReceipt adds Disposition-Notification-To.
	RequestReadReceipt bool
}

var ErrNoRecipients = errors.New("at least one recipient is required")

// ValidateAddress parses a single address strictly (no display-name
// injection, no header folding characters).
func ValidateAddress(a store.Address) (store.Address, error) {
	addr := strings.TrimSpace(a.Addr)
	if addr == "" || strings.ContainsAny(addr, "\r\n<>,; \t\"") || len(addr) > 320 {
		return a, fmt.Errorf("invalid email address %q", a.Addr)
	}
	parsed, err := netmail.ParseAddress(addr)
	if err != nil || parsed.Address != addr {
		return a, fmt.Errorf("invalid email address %q", a.Addr)
	}
	return store.Address{Name: cleanHeader(a.Name), Addr: addr}, nil
}

func cleanHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// NewMessageID creates a unique Message-ID using the sender's domain.
func NewMessageID(from string) string {
	domain := "lightmail.local"
	if at := strings.LastIndexByte(from, '@'); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d.%s@%s", time.Now().UnixNano(), hex.EncodeToString(b), domain)
}

func toMailAddrs(in []store.Address) []*mail.Address {
	out := make([]*mail.Address, 0, len(in))
	for _, a := range in {
		out = append(out, &mail.Address{Name: a.Name, Address: a.Addr})
	}
	return out
}

// Recipients returns the SMTP envelope recipients (To + Cc + Bcc), deduplicated.
func (o *Outgoing) Recipients() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range [][]store.Address{o.To, o.Cc, o.Bcc} {
		for _, a := range l {
			k := strings.ToLower(a.Addr)
			if !seen[k] {
				seen[k] = true
				out = append(out, a.Addr)
			}
		}
	}
	return out
}

// Validate normalises and checks every address.
func (o *Outgoing) Validate() error {
	var err error
	if o.From, err = ValidateAddress(o.From); err != nil {
		return err
	}
	for _, l := range []*[]store.Address{&o.To, &o.Cc, &o.Bcc, &o.ReplyTo} {
		for i := range *l {
			if (*l)[i], err = ValidateAddress((*l)[i]); err != nil {
				return err
			}
		}
	}
	if len(o.Recipients()) == 0 {
		return ErrNoRecipients
	}
	if len(o.Recipients()) > 500 {
		return errors.New("too many recipients")
	}
	o.Subject = cleanHeader(o.Subject)
	return nil
}

// Build writes the RFC 5322 message. Bcc recipients are never written to
// the headers.
func (o *Outgoing) Build(w io.Writer) error {
	var h mail.Header
	if o.Date.IsZero() {
		o.Date = time.Now()
	}
	h.SetDate(o.Date)
	h.SetAddressList("From", toMailAddrs([]store.Address{o.From}))
	if len(o.To) > 0 {
		h.SetAddressList("To", toMailAddrs(o.To))
	}
	if len(o.Cc) > 0 {
		h.SetAddressList("Cc", toMailAddrs(o.Cc))
	}
	if len(o.ReplyTo) > 0 {
		h.SetAddressList("Reply-To", toMailAddrs(o.ReplyTo))
	}
	h.SetSubject(o.Subject)
	h.SetMessageID(o.MessageID)
	if o.InReplyTo != "" {
		h.Set("In-Reply-To", cleanHeader(o.InReplyTo))
	}
	if len(o.References) > 0 {
		refs := make([]string, 0, len(o.References))
		for _, r := range o.References {
			if r = cleanHeader(r); r != "" {
				refs = append(refs, r)
			}
		}
		if len(refs) > 0 {
			h.Set("References", strings.Join(refs, " "))
		}
	}
	if o.RequestReadReceipt {
		h.SetAddressList("Disposition-Notification-To", toMailAddrs([]store.Address{o.From}))
	}
	h.Set("MIME-Version", "1.0")

	textBody := o.Text
	if textBody == "" && o.HTML != "" {
		textBody = HTMLToText(o.HTML)
	}

	if len(o.Attachments) == 0 {
		if o.HTML == "" {
			h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
			wc, err := mail.CreateSingleInlineWriter(w, h)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(wc, textBody); err != nil {
				return err
			}
			return wc.Close()
		}
		iw, err := mail.CreateInlineWriter(w, h)
		if err != nil {
			return err
		}
		if err := writeAlternatives(iw, textBody, o.HTML); err != nil {
			return err
		}
		return iw.Close()
	}

	mw, err := mail.CreateWriter(w, h)
	if err != nil {
		return err
	}
	iw, err := mw.CreateInline()
	if err != nil {
		return err
	}
	if o.HTML != "" {
		if err := writeAlternatives(iw, textBody, o.HTML); err != nil {
			return err
		}
	} else {
		var ph mail.InlineHeader
		ph.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		pw, err := iw.CreatePart(ph)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(pw, textBody); err != nil {
			return err
		}
		pw.Close()
	}
	if err := iw.Close(); err != nil {
		return err
	}
	for _, a := range o.Attachments {
		var ah mail.AttachmentHeader
		ct := a.ContentType
		if _, _, err := mime.ParseMediaType(ct); err != nil || ct == "" {
			ct = "application/octet-stream"
		}
		ah.SetContentType(ct, nil)
		ah.SetFilename(SanitizeFilename(a.Filename))
		if a.Inline && a.ContentID != "" {
			ah.Set("Content-ID", "<"+cleanHeader(a.ContentID)+">")
			ah.SetContentDisposition("inline", map[string]string{"filename": SanitizeFilename(a.Filename)})
		}
		aw, err := mw.CreateAttachment(ah)
		if err != nil {
			return err
		}
		rc, err := a.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(aw, rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := aw.Close(); err != nil {
			return err
		}
	}
	return mw.Close()
}

func writeAlternatives(iw *mail.InlineWriter, text, html string) error {
	var th mail.InlineHeader
	th.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	tw, err := iw.CreatePart(th)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(tw, text); err != nil {
		return err
	}
	tw.Close()
	var hh mail.InlineHeader
	hh.SetContentType("text/html", map[string]string{"charset": "utf-8"})
	hw, err := iw.CreatePart(hh)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(hw, html); err != nil {
		return err
	}
	return hw.Close()
}

// ReplySubject prefixes "Re: " once.
func ReplySubject(s string) string {
	t := strings.TrimSpace(s)
	l := strings.ToLower(t)
	if strings.HasPrefix(l, "re:") || strings.HasPrefix(l, "re :") {
		return t
	}
	return "Re: " + t
}

func ForwardSubject(s string) string {
	t := strings.TrimSpace(s)
	l := strings.ToLower(t)
	if strings.HasPrefix(l, "fwd:") || strings.HasPrefix(l, "tr:") || strings.HasPrefix(l, "fw:") {
		return t
	}
	return "Fwd: " + t
}

// BuildReferences returns the References chain for a reply.
func BuildReferences(parentRefs, parentMsgID string) []string {
	refs := strings.Fields(parentRefs)
	if len(refs) > 20 { // keep the first and the most recent ones
		refs = append(refs[:1], refs[len(refs)-19:]...)
	}
	if parentMsgID != "" {
		refs = append(refs, parentMsgID)
	}
	return refs
}
