package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"

	"lightmail/internal/store"
)

// XSS vectors commonly found in malicious emails. None of them may survive.
var xssVectors = []string{
	`<script>alert(1)</script>`,
	`<img src=x onerror=alert(1)>`,
	`<a href="javascript:alert(1)">x</a>`,
	`<a href="JaVaScRiPt:alert(1)">x</a>`,
	`<a href="&#106;avascript:alert(1)">x</a>`,
	`<svg onload=alert(1)><script>alert(1)</script></svg>`,
	`<iframe src="https://evil.example"></iframe>`,
	`<object data="evil.swf"></object>`,
	`<embed src="evil.swf">`,
	`<form action="https://evil.example"><input name=p></form>`,
	`<meta http-equiv="refresh" content="0;url=https://evil.example">`,
	`<base href="https://evil.example/">`,
	`<link rel="stylesheet" href="https://evil.example/x.css">`,
	`<body onload=alert(1)>`,
	`<div onmouseover="alert(1)">x</div>`,
	`<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`,
	`<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`,
	`<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`,
	`<noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
	`<style></style><script>alert(1)</script>`,
	`<a href="vbscript:msgbox(1)">x</a>`,
	`<isindex action="javascript:alert(1)">`,
}

func TestSanitizeXSS(t *testing.T) {
	for _, v := range xssVectors {
		out := SanitizeHTML(v, SanitizeOptions{}).HTML
		low := strings.ToLower(out)
		for _, bad := range []string{"<script", "onerror", "onload", "onmouseover", "javascript:", "vbscript:", "<iframe", "<object", "<embed", "<form", "<meta", "<base", "<link", "data:text/html", "<svg", "image/svg"} {
			if strings.Contains(low, bad) {
				t.Errorf("vector %q produced %q (contains %q)", v, out, bad)
			}
		}
	}
}

func TestRemoteImagesBlocked(t *testing.T) {
	in := `<p>Hi</p><img src="https://tracker.example/p.gif?u=1"><img src="cid:logo@x"><div style="background:url(https://t.example/a.png)">x</div>`
	res := SanitizeHTML(in, SanitizeOptions{CIDURL: func(cid string) (string, bool) {
		if cid == "logo@x" {
			return "/api/v1/messages/1/attachments/2?inline=1", true
		}
		return "", false
	}})
	if strings.Contains(res.HTML, "tracker.example") {
		t.Fatalf("remote image kept: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, `/api/v1/messages/1/attachments/2?inline=1`) {
		t.Fatalf("cid not rewritten: %s", res.HTML)
	}
	if res.RemoteBlocked != 2 {
		t.Fatalf("blocked count = %d", res.RemoteBlocked)
	}
	res = SanitizeHTML(in, SanitizeOptions{AllowRemote: true})
	if !strings.Contains(res.HTML, "tracker.example") || res.RemoteBlocked != 0 {
		t.Fatalf("remote allowed but missing: %s", res.HTML)
	}
}

func TestLinksGetTargetBlankAndNoReferrer(t *testing.T) {
	out := SanitizeHTML(`<a href="https://example.com">x</a>`, SanitizeOptions{}).HTML
	if !strings.Contains(out, `target="_blank"`) || !strings.Contains(out, "noreferrer") {
		t.Fatalf("got %s", out)
	}
}

func TestStyleKeptButCSSIntact(t *testing.T) {
	out := SanitizeHTML(`<style>a > b { color: red }</style><p style="color:red">x</p>`, SanitizeOptions{}).HTML
	if !strings.Contains(out, "a > b") || !strings.Contains(out, `style="color:red"`) {
		t.Fatalf("styles mangled: %s", out)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":           "passwd",
		`..\..\windows\system32.dll`: "system32.dll",
		"invoice‮fdp.exe":            "invoicefdp.exe",
		"a\x00b\r\n.txt":             "ab.txt",
		"":                           "attachment",
		"..":                         "attachment",
		"con:tent?.pdf":              "con_tent_.pdf",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 300) + ".pdf"
	if got := SanitizeFilename(long); len([]rune(got)) > 180 || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("long name: %d %q", len([]rune(got)), got[len(got)-8:])
	}
}

func TestAnalyze(t *testing.T) {
	bs := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
		&imap.BodyStructureMultiPart{Subtype: "alternative", Children: []imap.BodyStructure{
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Params: map[string]string{"charset": "iso-8859-1"}, Encoding: "quoted-printable", Size: 10},
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "html", Params: map[string]string{"charset": "utf-8"}, Encoding: "base64", Size: 20},
		}},
		&imap.BodyStructureSinglePart{Type: "image", Subtype: "png", ID: "<logo@x>", Encoding: "base64", Size: 400},
		&imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf", Encoding: "base64", Size: 4000,
			Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{Value: "attachment", Params: map[string]string{"filename": "=?utf-8?q?facture_=C3=A9t=C3=A9.pdf?="}}}},
	}}
	a := Analyze(bs)
	if a.Structure.Plain == nil || a.Structure.Plain.Path != "1.1" || a.Structure.HTML.Path != "1.2" {
		t.Fatalf("text parts: %+v", a.Structure)
	}
	if len(a.Attachments) != 2 || !a.HasAttachments {
		t.Fatalf("attachments: %+v", a.Attachments)
	}
	if !a.Attachments[0].Inline || a.Attachments[0].ContentID != "logo@x" {
		t.Fatalf("inline image: %+v", a.Attachments[0])
	}
	if a.Attachments[1].Filename != "facture été.pdf" || a.Attachments[1].Part != "3" || a.Attachments[1].Size != 3000 {
		t.Fatalf("pdf: %+v", a.Attachments[1])
	}
	// Single-part message: body is section 1
	a = Analyze(&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "7bit"})
	if a.Structure.Plain == nil || a.Structure.Plain.Path != "1" || a.HasAttachments {
		t.Fatalf("single part: %+v", a)
	}
}

func TestDecodePartial(t *testing.T) {
	full := base64.StdEncoding.EncodeToString([]byte("Bonjour, voici la facture du mois de janvier. Éléments joints."))
	got := DecodePartial([]byte(full[:30]+"\r\n"), "base64", "utf-8")
	if !strings.HasPrefix("Bonjour, voici la facture du mois de janvier.", got) || len(got) < 15 {
		t.Fatalf("got %q", got)
	}
	if got := DecodePartial([]byte("Caf=C3=A9 cr=C3=A8me =C3"), "quoted-printable", "utf-8"); got != "Café crème " {
		t.Fatalf("qp partial: %q", got)
	}
	if got := DecodeText([]byte("caf\xe9"), "8bit", "iso-8859-1"); got != "café" {
		t.Fatalf("charset: %q", got)
	}
	if got := Snippet("<style>x{}</style><p>Hello&nbsp;<b>world</b></p>\n\n  again", true, 100); got != "Hello world again" {
		t.Fatalf("snippet %q", got)
	}
}

func TestComposeBccHiddenAndHeadersSafe(t *testing.T) {
	o := &Outgoing{
		From:       store.Address{Name: "Me", Addr: "me@example.com"},
		To:         []store.Address{{Name: "Évelyne", Addr: "eve@example.com"}},
		Bcc:        []store.Address{{Addr: "secret@example.com"}},
		Subject:    "Hello\r\nBcc: injected@evil.example",
		Text:       "Body",
		HTML:       "<p>Body</p>",
		MessageID:  NewMessageID("me@example.com"),
		InReplyTo:  "<p@x>",
		References: []string{"<a@x>", "<p@x>"},
		Attachments: []OutAttachment{{Filename: "../a.txt", ContentType: "text/plain", Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("attached")), nil
		}}},
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := o.Build(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if strings.Contains(raw, "secret@example.com") {
		t.Fatal("Bcc leaked in headers")
	}
	if strings.Contains(raw, "\nBcc:") {
		t.Fatal("header injection")
	}
	if got := o.Recipients(); len(got) != 2 {
		t.Fatalf("recipients %v", got)
	}
	r, err := mail.CreateReader(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := r.Header.Subject()
	if !strings.HasPrefix(subj, "Hello") || strings.ContainsAny(subj, "\r\n") {
		t.Fatalf("subject %q", subj)
	}
	if id, _ := r.Header.MessageID(); !strings.HasSuffix(id, "@example.com") {
		t.Fatalf("message-id %q", id)
	}
	if got := r.Header.Get("References"); got != "<a@x> <p@x>" {
		t.Fatalf("references %q", got)
	}
	parts := 0
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		parts++
		if ah, ok := p.Header.(*mail.AttachmentHeader); ok {
			fn, _ := ah.Filename()
			b, _ := io.ReadAll(p.Body)
			if fn != "a.txt" || string(b) != "attached" {
				t.Fatalf("attachment %q %q", fn, b)
			}
		}
	}
	if parts != 3 {
		t.Fatalf("expected text+html+attachment, got %d parts", parts)
	}
}

func TestValidateAddress(t *testing.T) {
	for _, bad := range []string{"", "a", "a@b,c@d", "a@b\r\nBcc: x@y", "<a@b>", "a b@c.d"} {
		if _, err := ValidateAddress(store.Address{Addr: bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ValidateAddress(store.Address{Addr: "jean.dupont+tag@example.fr"}); err != nil {
		t.Error(err)
	}
}

func TestReplyHelpers(t *testing.T) {
	if ReplySubject("Re: x") != "Re: x" || ReplySubject("x") != "Re: x" || ForwardSubject("TR: x") != "TR: x" {
		t.Fatal("subject helpers")
	}
	refs := BuildReferences("<a@x> <b@x>", "<c@x>")
	if strings.Join(refs, " ") != "<a@x> <b@x> <c@x>" {
		t.Fatalf("%v", refs)
	}
}

func TestStripStyles(t *testing.T) {
	out := SanitizeHTML(`<style>body{display:none}</style><p>kept</p>`, SanitizeOptions{StripStyles: true}).HTML
	if strings.Contains(out, "display:none") || strings.Contains(out, "<style") || !strings.Contains(out, "<p>kept</p>") {
		t.Fatalf("got %q", out)
	}
}
