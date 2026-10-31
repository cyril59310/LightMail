package imapx

import (
	"context"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"lightmail/internal/mailauth"
	"lightmail/internal/netx"
	"lightmail/internal/testmail"
)

func setup(t *testing.T) (*testmail.IMAPServer, *testmail.Cert) {
	t.Helper()
	cert, err := testmail.NewCert()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := testmail.StartIMAP(cert, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.AddUser("alice@test", "pw-alice")
	return srv, cert
}

func dial(srv *testmail.IMAPServer, ca string, creds mailauth.Credentials) (*Client, error) {
	return Dial(context.Background(), DialOptions{
		Policy:   netx.Policy{Timeout: 5 * time.Second},
		Endpoint: mailauth.Endpoint{Host: srv.Host, Port: srv.Port, Security: "tls", CAPEM: ca},
		Creds:    creds,
	})
}

func TestRejectsUntrustedCertificate(t *testing.T) {
	srv, _ := setup(t)
	_, err := dial(srv, "", mailauth.Credentials{Username: "alice@test", Password: "pw-alice"})
	if err == nil || !netx.IsCertificateError(err) {
		t.Fatalf("expected certificate error, got %v", err)
	}
}

func TestPlaintextRefused(t *testing.T) {
	_, err := Dial(context.Background(), DialOptions{Endpoint: mailauth.Endpoint{Host: "127.0.0.1", Port: 143, Security: "none"}})
	if err == nil {
		t.Fatal("plaintext accepted")
	}
}

func TestLoginAndOps(t *testing.T) {
	srv, cert := setup(t)
	if _, err := dial(srv, cert.CAPEM, mailauth.Credentials{Username: "alice@test", Password: "wrong"}); !mailauth.IsAuthError(err) {
		t.Fatalf("expected auth error, got %v", err)
	}
	now := time.Now()
	srv.Deliver("alice@test", "INBOX", testmail.Message("bob@test", "alice@test", "Hello", "first body", now.Add(-time.Hour), "X-Priority: 1"))
	srv.Deliver("alice@test", "INBOX", testmail.MessageWithAttachment("carol@test", "alice@test", "Doc", "<p>see attached</p>", "doc.pdf", []byte("%PDF-1.4 test"), now))

	c, err := dial(srv, cert.CAPEM, mailauth.Credentials{Username: "alice@test", Password: "pw-alice"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()

	boxes, err := c.ListMailboxes()
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, b := range boxes {
		roles[b.Mailbox] = RoleFor(b)
	}
	if roles["INBOX"] != "inbox" || roles["Sent"] != "sent" || roles["Trash"] != "trash" || roles["Archive"] != "archive" {
		t.Fatalf("roles: %v", roles)
	}

	sel, err := c.SelectMailbox("INBOX", true)
	if err != nil || sel.NumMessages != 2 {
		t.Fatalf("select: %+v %v", sel, err)
	}
	var got []*imapclient.FetchMessageBuffer
	err = c.FetchHeaders(imap.SeqSetNum(1, 2), func(b *imapclient.FetchMessageBuffer) error {
		got = append(got, b)
		return nil
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("fetch: %d %v", len(got), err)
	}
	m1, _ := ToMessage(got[0])
	m2, a2 := ToMessage(got[1])
	if m1.Subject != "Hello" || m1.From.Addr != "bob@test" || m1.Priority != 1 || m1.Seen {
		t.Fatalf("m1: %+v", m1)
	}
	if !m2.HasAttachments || len(a2.Attachments) != 1 || a2.Attachments[0].Filename != "doc.pdf" || a2.Structure.HTML == nil {
		t.Fatalf("m2: %+v %+v", m2, a2)
	}

	// Download attachment section and decode it.
	part, _ := PartPathOf(a2.Attachments[0].Part)
	data, err := c.FetchSection(got[1].UID, part, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty section")
	}

	// Flags
	if err := c.SetFlags(UIDSetOf([]uint32{uint32(got[0].UID)}), true, imap.FlagSeen, imap.FlagFlagged); err != nil {
		t.Fatal(err)
	}
	fl, err := c.FetchFlags(UIDSetOf([]uint32{uint32(got[0].UID)}), 0)
	if err != nil || len(fl) != 1 {
		t.Fatal(err)
	}
	seen, flagged, _, _, _, _ := FlagsToState(fl[0].Flags)
	if !seen || !flagged {
		t.Fatal("flags not stored")
	}

	// Move with COPYUID mapping
	res, err := c.MoveMessages(UIDSetOf([]uint32{uint32(got[0].UID)}), "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if res[got[0].UID] == 0 {
		t.Fatalf("no COPYUID mapping: %v", res)
	}
	uids, err := c.UIDsInRange(1, 0)
	if err != nil || len(uids) != 1 || uids[0] != got[1].UID {
		t.Fatalf("after move: %v %v", uids, err)
	}

	// Append + search by Message-ID + permanent delete
	raw := testmail.Message("alice@test", "x@test", "Draft", "draft body", now)
	if _, err := c.AppendMessage("Drafts", raw, []imap.Flag{imap.FlagDraft, imap.FlagSeen}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SelectMailbox("Drafts", false); err != nil {
		t.Fatal(err)
	}
	if err := c.DeletePermanently(UIDSetOf([]uint32{1})); err != nil {
		t.Fatal(err)
	}
	uids, _ = c.UIDsInRange(1, 0)
	if len(uids) != 0 {
		t.Fatalf("delete failed: %v", uids)
	}
}

func TestXOAuth2(t *testing.T) {
	srv, cert := setup(t)
	srv.AddToken("tok-123", "alice@test", "pw-alice")
	c, err := dial(srv, cert.CAPEM, mailauth.Credentials{Username: "alice@test", AccessToken: "tok-123"})
	if err != nil {
		t.Fatal(err)
	}
	c.Logout()
	srv.RevokeToken("tok-123")
	_, err = dial(srv, cert.CAPEM, mailauth.Credentials{Username: "alice@test", AccessToken: "tok-123"})
	if !mailauth.IsAuthError(err) {
		t.Fatalf("revoked token: expected auth error, got %v", err)
	}
}
