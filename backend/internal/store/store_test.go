package store

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"lightmail/internal/crypto"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedAccount(t *testing.T, db *DB) (*User, *Account, *Folder) {
	t.Helper()
	ctx := context.Background()
	u, err := db.CreateUser(ctx, "admin", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	a := &Account{UserID: u.ID, Provider: "custom", Email: "Me@Example.com", IMAPHost: "imap.example.com", IMAPPort: 993, IMAPSecurity: "tls",
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: "tls", AuthType: "password", Username: "me", SyncEnabled: true}
	if err := db.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	f, err := db.UpsertFolder(ctx, &Folder{AccountID: a.ID, Name: "INBOX", Delimiter: "/", Role: RoleInbox, Selectable: true, Subscribed: true, SyncEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return u, a, f
}

func TestMigrationsIdempotent(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(ctx, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, p, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := db.SchemaVersion(ctx)
	if err != nil || v < 1 {
		t.Fatalf("version %d %v", v, err)
	}
}

func TestAccountsDefaultAndConflict(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, a, _ := seedAccount(t, db)
	if !a.IsDefault || a.Email != "Me@Example.com" {
		t.Fatalf("first account should be default: %+v", a)
	}
	dup := *a
	dup.ID = 0
	if err := db.CreateAccount(ctx, &dup); err != ErrConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	b := *a
	b.ID, b.Email, b.IsDefault = 0, "other@example.com", false
	if err := db.CreateAccount(ctx, &b); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAccount(ctx, u.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Account(ctx, b.ID)
	if !got.IsDefault {
		t.Fatal("remaining account should become default")
	}
	if _, err := db.AccountForUser(ctx, u.ID+1, b.ID); err != ErrNotFound {
		t.Fatal("account visible to another user")
	}
}

func TestUpsertThreadsAndSearch(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, a, f := seedAccount(t, db)

	parent := &Message{AccountID: a.ID, FolderID: f.ID, UID: 1, MessageID: "<p@x>", Subject: "Facture janvier", From: Address{"Alice", "alice@example.com"}, Date: 100, InternalDate: 100, Snippet: "Veuillez trouver la facture",
		Attachments: []Attachment{{Part: "2", Filename: "facture.pdf", ContentType: "application/pdf", Size: 1000}}, HasAttachments: true}
	reply := &Message{AccountID: a.ID, FolderID: f.ID, UID: 2, MessageID: "<r@x>", InReplyTo: "<p@x>", References: "<p@x>", Subject: "Re: Facture janvier", From: Address{"Bob", "bob@example.com"}, Date: 200, InternalDate: 200}
	ids, err := db.UpsertMessages(ctx, []*Message{parent, reply})
	if err != nil || len(ids) != 2 {
		t.Fatalf("insert: %v %v", ids, err)
	}
	if reply.ThreadID != parent.ThreadID || parent.ThreadID == 0 {
		t.Fatalf("threading failed: %d %d", parent.ThreadID, reply.ThreadID)
	}

	// Re-sync the same UIDs: no duplicates, flags updated.
	reply2 := *reply
	reply2.Seen = true
	ids, err = db.UpsertMessages(ctx, []*Message{&reply2})
	if err != nil || len(ids) != 0 {
		t.Fatalf("re-insert created rows: %v %v", ids, err)
	}
	if n, _ := db.CountFolderMessages(ctx, f.ID); n != 2 {
		t.Fatalf("count = %d", n)
	}

	// FTS with diacritics-insensitive matching
	res, err := db.ListMessages(ctx, MessageFilter{UserID: u.ID, FTS: `"facture"*`})
	if err != nil || len(res) != 2 {
		t.Fatalf("fts: %d %v", len(res), err)
	}
	has := true
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, HasAtt: &has})
	if len(res) != 1 || res[0].UID != 1 {
		t.Fatalf("has attachment filter: %+v", res)
	}
	unread := true
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, Role: RoleInbox, Unread: &unread})
	if len(res) != 1 || res[0].UID != 1 {
		t.Fatalf("unread filter: %d", len(res))
	}
	// Keyset pagination
	page1, _ := db.ListMessages(ctx, MessageFilter{UserID: u.ID, Limit: 1})
	page2, _ := db.ListMessages(ctx, MessageFilter{UserID: u.ID, Limit: 1, Cursor: &Cursor{Num: page1[0].Date, ID: page1[0].ID}})
	if len(page1) != 1 || len(page2) != 1 || page1[0].ID == page2[0].ID {
		t.Fatal("pagination broken")
	}
	// LIKE escaping: '%' must not match everything
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, From: []string{"%"}})
	if len(res) != 0 {
		t.Fatal("LIKE wildcard injection")
	}

	// Body indexing then deletion cleans FTS
	if err := db.SaveMessageBody(ctx, parent, &Body{Text: "contenu détaillé"}, "contenu détaillé"); err != nil {
		t.Fatal(err)
	}
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, FTS: `"detaille"`})
	if len(res) != 1 {
		t.Fatalf("body fts: %d", len(res))
	}
	if _, err := db.DeleteMessagesByUID(ctx, f.ID, []uint32{1}); err != nil {
		t.Fatal(err)
	}
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, FTS: `"detaille"`})
	if len(res) != 0 {
		t.Fatal("fts not cleaned")
	}
	// Other user cannot see messages
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID + 1})
	if len(res) != 0 {
		t.Fatal("cross-user leak")
	}
}

func TestOAuthStateSingleUse(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, _, _ := seedAccount(t, db)
	if err := db.CreateOAuthState(ctx, &OAuthState{StateHash: "h", UserID: u.ID, SessionHash: "s", Provider: "google", VerifierEnc: "v", ExpiresAt: now() + 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConsumeOAuthState(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConsumeOAuthState(ctx, "h"); err != ErrNotFound {
		t.Fatal("state reusable")
	}
}

func TestOutboxClaim(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, a, _ := seedAccount(t, db)
	o := &OutboxItem{UserID: u.ID, AccountID: a.ID, Status: OutboxScheduled, MessageIDHdr: "<m@x>", EnvelopeFrom: "me@example.com", Rcpts: []string{"a@b.c"}, RawSHA256: "abc", Mode: "new", NextAttemptAt: now() - 1}
	if err := db.CreateOutbox(ctx, o); err != nil {
		t.Fatal(err)
	}
	items, err := db.ClaimDueOutbox(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != OutboxSending {
		t.Fatalf("claim: %v %v", items, err)
	}
	items, _ = db.ClaimDueOutbox(ctx, 10)
	if len(items) != 0 {
		t.Fatal("double claim")
	}
	n, _ := db.RecoverInterruptedSends(ctx)
	if n != 1 {
		t.Fatal("recover")
	}
	got, _ := db.OutboxItem(ctx, o.ID)
	if got.Status != OutboxAmbiguous {
		t.Fatalf("status %s", got.Status)
	}
}

func TestContacts(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, a, f := seedAccount(t, db)
	sent, _ := db.UpsertFolder(ctx, &Folder{AccountID: a.ID, Name: "Sent", Role: RoleSent, Selectable: true, SyncEnabled: true})
	db.UpsertMessages(ctx, []*Message{
		{AccountID: a.ID, FolderID: f.ID, UID: 1, From: Address{"Bob Martin", "bob@example.com"}, Date: 1, InternalDate: 1},
		{AccountID: a.ID, FolderID: f.ID, UID: 2, From: Address{"Bob Martin", "bob@example.com"}, Date: 2, InternalDate: 2},
		{AccountID: a.ID, FolderID: sent.ID, UID: 1, From: Address{"", "me@example.com"}, To: []Address{{"Béatrice", "bea@example.org"}}, Date: 3, InternalDate: 3},
	})
	got, err := db.Contacts(ctx, u.ID, "b", 8)
	if err != nil || len(got) != 2 || got[0].Addr != "bob@example.com" || got[0].Count != 2 {
		t.Fatalf("contacts: %+v %v", got, err)
	}
	if got, _ := db.Contacts(ctx, u.ID+1, "b", 8); len(got) != 0 {
		t.Fatal("cross-user contacts")
	}
}

func TestListMessagesSort(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, a, f := seedAccount(t, db)
	other, _ := db.UpsertFolder(ctx, &Folder{AccountID: a.ID, Name: "Other", Selectable: true, SyncEnabled: true})
	msgs := []*Message{
		{FolderID: f.ID, UID: 1, Subject: "banane", From: Address{"", "zoe@x.org"}, To: []Address{{"Marc", "m@x.org"}}, Date: 50, InternalDate: 300, Size: 10},
		{FolderID: f.ID, UID: 2, Subject: "Ananas", From: Address{"Bob", "bob@x.org"}, To: []Address{{"", "alice@x.org"}}, Cc: []Address{{"Carl", "c@x.org"}}, Date: 40, InternalDate: 100, Size: 500},
		{FolderID: other.ID, UID: 1, Subject: "cerise", From: Address{"alice", "a@x.org"}, Date: 60, InternalDate: 200, Size: 30},
		{FolderID: other.ID, UID: 2, Subject: "ananas", From: Address{"Bob", "bob2@x.org"}, To: []Address{{"zed", "z@x.org"}}, Date: 40, InternalDate: 400, Size: 30},
		{FolderID: f.ID, UID: 3, Subject: "", From: Address{"", "yan@x.org"}, Cc: []Address{{"", "aa@x.org"}}, Date: 70, InternalDate: 50, Size: 1},
	}
	for _, m := range msgs {
		m.AccountID = a.ID
	}
	if _, err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	ids := func(l []*Message) []int64 {
		out := make([]int64, len(l))
		for i, m := range l {
			out[i] = m.ID
		}
		return out
	}
	for _, sort := range []string{SortSent, SortArrival, SortSubject, SortFrom, SortTo, SortCc, SortSize} {
		for _, asc := range []bool{false, true} {
			want, err := db.ListMessages(ctx, MessageFilter{UserID: u.ID, Sort: sort, Asc: asc, Limit: 50})
			if err != nil || len(want) != len(msgs) {
				t.Fatalf("%s asc=%v: %d %v", sort, asc, len(want), err)
			}
			// In-memory sort used by server-side search must agree with SQL.
			mem := append([]*Message(nil), want...)
			SortMessages(mem, sort, !asc) // scramble
			SortMessages(mem, sort, asc)
			if fmt.Sprint(ids(mem)) != fmt.Sprint(ids(want)) {
				t.Fatalf("%s asc=%v: memory %v, sql %v", sort, asc, ids(mem), ids(want))
			}
			// Keyset pagination returns the same order, page by page.
			var got []*Message
			var cur *Cursor
			for range len(msgs) + 1 {
				page, err := db.ListMessages(ctx, MessageFilter{UserID: u.ID, Sort: sort, Asc: asc, Limit: 2, Cursor: cur})
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, page...)
				if len(page) < 2 {
					break
				}
				last := page[len(page)-1]
				n, txt := SortValue(last, sort)
				cur = &Cursor{Num: n, Text: txt, ID: last.ID}
			}
			if fmt.Sprint(ids(got)) != fmt.Sprint(ids(want)) {
				t.Fatalf("%s asc=%v: paged %v, full %v", sort, asc, ids(got), ids(want))
			}
		}
	}
	// Spot checks of the expected order.
	res, _ := db.ListMessages(ctx, MessageFilter{UserID: u.ID, Sort: SortSubject, Asc: true})
	if res[0].Subject != "" || !strings.EqualFold(res[1].Subject, "ananas") || res[3].Subject != "banane" || res[4].Subject != "cerise" {
		t.Fatalf("subject asc: %v", ids(res))
	}
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, Sort: SortSize})
	if res[0].Size != 500 || res[4].Size != 1 {
		t.Fatalf("size desc: %v", ids(res))
	}
	res, _ = db.ListMessages(ctx, MessageFilter{UserID: u.ID, Sort: SortArrival})
	if res[0].InternalDate != 400 {
		t.Fatalf("arrival desc: %v", ids(res))
	}
}

func TestRotateKeys(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	_, a, _ := seedAccount(t, db)
	oldKey, _ := crypto.NewKeyring(bytes.Repeat([]byte{1}, 32))
	if err := db.CheckKey(ctx, oldKey); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(a.ID)
	pw, _ := oldKey.Encrypt("mail-password", "account:"+id+":password")
	rf, _ := oldKey.Encrypt("refresh-token", "account:"+id+":oauth_refresh")
	if err := db.UpdateAccountSecrets(ctx, a.ID, pw, "", rf); err != nil {
		t.Fatal(err)
	}
	newOnly, _ := crypto.NewKeyring(bytes.Repeat([]byte{2}, 32))
	if err := db.CheckKey(ctx, newOnly); err == nil {
		t.Fatal("new key accepted before rotation")
	}
	rotating, _ := crypto.NewKeyring(bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{1}, 32))
	n, err := db.RotateKeys(ctx, rotating)
	if err != nil || n != 2 {
		t.Fatalf("rotated %d %v", n, err)
	}
	if err := db.CheckKey(ctx, newOnly); err != nil {
		t.Fatalf("new key refused after rotation: %v", err)
	}
	got, _ := db.Account(ctx, a.ID)
	if p, err := newOnly.Decrypt(got.PasswordEnc, "account:"+id+":password"); err != nil || p != "mail-password" {
		t.Fatalf("password after rotation: %q %v", p, err)
	}
	if r, err := newOnly.Decrypt(got.OAuthRefreshEnc, "account:"+id+":oauth_refresh"); err != nil || r != "refresh-token" {
		t.Fatalf("refresh token after rotation: %q %v", r, err)
	}
}
