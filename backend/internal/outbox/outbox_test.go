package outbox

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lightmail/internal/accounts"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/events"
	"lightmail/internal/metrics"
	"lightmail/internal/mime"
	"lightmail/internal/netx"
	"lightmail/internal/oauth"
	"lightmail/internal/store"
	"lightmail/internal/syncer"
	"lightmail/internal/testmail"
)

type env struct {
	t    *testing.T
	db   *store.DB
	ob   *Service
	eng  *syncer.Engine
	imap *testmail.IMAPServer
	smtp *testmail.SMTPServer
	acct *store.Account
	user *store.User
}

func setup(t *testing.T) *env {
	cert, _ := testmail.NewCert()
	is, err := testmail.StartIMAP(cert, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(is.Close)
	is.AddUser("alice@test", "pw")
	ss, err := testmail.StartSMTP(cert, map[string]string{"alice@test": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ss.Close)

	dir := t.TempDir()
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(dir, "o.db"), 2)
	t.Cleanup(func() { db.Close() })
	u, _ := db.CreateUser(ctx, "admin", "x", true)
	kr, _ := crypto.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	pub, _ := url.Parse("https://x")
	as := &accounts.Service{DB: db, KR: kr, OAuth: oauth.New(&config.Config{PublicURL: pub}, db, kr, log), Policy: netx.Policy{Timeout: 5 * time.Second}, Metrics: m}
	cfg := &config.Config{SyncInterval: time.Hour, SyncMaxConcurrent: 2, IMAPMaxConnsPerAcct: 2, IMAPMaxConnsTotal: 10, SyncChunkSize: 50,
		SyncBackfillBudget: time.Second, SyncFlagRefreshPeriod: time.Hour, MaxFetchBytes: 10 << 20, SMTPMaxConcurrent: 2, MaxUploadBytes: 10 << 20}
	hub := events.NewHub()
	n := events.NewNotifier(db, hub, log, "")
	bs, _ := blobs.New(filepath.Join(dir, "blobs"))
	eng := syncer.New(cfg, db, as, hub, n, bs, m, log)
	a := &store.Account{UserID: u.ID, Provider: "custom", Email: "alice@test", IMAPHost: is.Host, IMAPPort: is.Port, IMAPSecurity: "tls",
		SMTPHost: ss.Host, SMTPPort: ss.Port, SMTPSecurity: "tls", AuthType: "password", Username: "alice@test", TLSCAPEM: cert.CAPEM, SyncEnabled: true}
	db.CreateAccount(ctx, a)
	a.PasswordEnc, _ = as.EncryptPassword(a.ID, "pw")
	db.UpdateAccountSettings(ctx, a)
	rctx, cancel := context.WithCancel(ctx)
	eng.Start(rctx)
	ob := New(cfg, db, as, eng, bs, hub, n, m, log)
	ob.UndoDelay = 0
	ob.AmbiguousCheckDelay = 10 * time.Millisecond
	go ob.Run(rctx)
	t.Cleanup(func() { cancel(); eng.Stop() })
	return &env{t: t, db: db, ob: ob, eng: eng, imap: is, smtp: ss, acct: a, user: u}
}

func (e *env) waitStatus(id int64, status string) *store.OutboxItem {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		it, _ := e.db.OutboxItem(context.Background(), id)
		if it != nil && it.Status == status {
			return it
		}
		time.Sleep(30 * time.Millisecond)
	}
	it, _ := e.db.OutboxItem(context.Background(), id)
	e.t.Fatalf("outbox %d: status %s, want %s (%s)", id, it.Status, status, it.LastError)
	return nil
}

func (e *env) message(subject string) *mime.Outgoing {
	return &mime.Outgoing{From: store.Address{Name: "Alice", Addr: "alice@test"}, To: []store.Address{{Addr: "bob@test"}},
		Bcc: []store.Address{{Addr: "hidden@test"}}, Subject: subject, Text: "hello",
		Attachments: []mime.OutAttachment{{Filename: "a.txt", ContentType: "text/plain", Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("file content")), nil
		}}}}
}

func TestSendCopiesToSentOnce(t *testing.T) {
	e := setup(t)
	it, err := e.ob.Enqueue(context.Background(), e.user.ID, e.acct, e.message("Hello"), Meta{Mode: "new"})
	if err != nil {
		t.Fatal(err)
	}
	got := e.waitStatus(it.ID, store.OutboxSent)
	if !got.CopiedToSent {
		t.Fatal("not copied to Sent")
	}
	rec := e.smtp.Received()
	if len(rec) != 1 || len(rec[0].Rcpts) != 2 || strings.Contains(string(rec[0].Data), "hidden@test") {
		t.Fatalf("delivery: %+v", rec)
	}
	// The Sent folder contains exactly one copy.
	u := e.imap.Users["alice@test"]
	st, _ := u.Status("Sent", &imapStatus)
	if *st.NumMessages != 1 {
		t.Fatalf("Sent has %d messages", *st.NumMessages)
	}
}

func TestUndoCancel(t *testing.T) {
	e := setup(t)
	e.ob.UndoDelay = 5 * time.Second
	it, _ := e.ob.Enqueue(context.Background(), e.user.ID, e.acct, e.message("Oops"), Meta{})
	if err := e.ob.Cancel(context.Background(), e.user.ID, it.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(e.smtp.Received()) != 0 {
		t.Fatal("canceled message was sent")
	}
	if err := e.ob.Cancel(context.Background(), e.user.ID+1, it.ID); err == nil {
		t.Fatal("other user could act on outbox item")
	}
}

func TestTemporaryFailureRetries(t *testing.T) {
	e := setup(t)
	e.smtp.Mode.Store(int32(testmail.SMTPTempFailRcpt))
	it, _ := e.ob.Enqueue(context.Background(), e.user.ID, e.acct, e.message("Later"), Meta{})
	got := e.waitStatus(it.ID, store.OutboxRetry)
	if got.NextAttemptAt <= time.Now().Unix() || got.Attempts != 1 {
		t.Fatalf("retry scheduling: %+v", got)
	}
}

func TestPermanentFailure(t *testing.T) {
	e := setup(t)
	e.smtp.Mode.Store(int32(testmail.SMTPPermFailRcpt))
	it, _ := e.ob.Enqueue(context.Background(), e.user.ID, e.acct, e.message("Nope"), Meta{})
	got := e.waitStatus(it.ID, store.OutboxFailed)
	if !strings.Contains(got.LastError, "550") {
		t.Fatalf("error: %s", got.LastError)
	}
	notes, _ := e.db.ListNotifications(context.Background(), e.user.ID, 5)
	if len(notes) == 0 || notes[0].Kind != events.KindSendFailed {
		t.Fatal("no failure notification")
	}
}

func TestAmbiguousIsNotRetriedAutomatically(t *testing.T) {
	e := setup(t)
	e.smtp.Mode.Store(int32(testmail.SMTPDropAfterData))
	it, _ := e.ob.Enqueue(context.Background(), e.user.ID, e.acct, e.message("Maybe"), Meta{})
	e.waitStatus(it.ID, store.OutboxAmbiguous)
	time.Sleep(1500 * time.Millisecond)
	if n := len(e.smtp.Received()); n != 1 {
		t.Fatalf("ambiguous message transmitted %d times", n)
	}
	got, _ := e.db.OutboxItem(context.Background(), it.ID)
	if got.Status != store.OutboxAmbiguous {
		t.Fatalf("status changed to %s", got.Status)
	}
	// The user explicitly decides to resend.
	e.smtp.Mode.Store(int32(testmail.SMTPOK))
	if err := e.ob.Retry(context.Background(), e.user.ID, it.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(it.ID, store.OutboxSent)
}
