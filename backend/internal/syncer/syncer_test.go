package syncer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/accounts"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/events"
	"lightmail/internal/imapx"
	"lightmail/internal/mailauth"
	"lightmail/internal/metrics"
	"lightmail/internal/netx"
	"lightmail/internal/oauth"
	"lightmail/internal/store"
	"lightmail/internal/testmail"
)

type harness struct {
	t      *testing.T
	db     *store.DB
	eng    *Engine
	srv    *testmail.IMAPServer
	cert   *testmail.Cert
	accts  *accounts.Service
	user   *store.User
	hub    *events.Hub
	cfg    *config.Config
	dir    string
	ctx    context.Context
	cancel context.CancelFunc
}

func newHarness(t *testing.T, mutate func(*config.Config)) *harness {
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
	dir := t.TempDir()
	h := &harness{t: t, srv: srv, cert: cert, dir: dir}
	h.cfg = &config.Config{
		SyncInterval: time.Hour, SyncIdle: false, SyncMaxConcurrent: 2, IMAPMaxConnsPerAcct: 2, IMAPMaxConnsTotal: 20,
		SyncChunkSize: 50, SyncBackfillBudget: time.Minute, SyncFlagRefreshPeriod: time.Hour, MaxFetchBytes: 10 << 20,
		NetTimeout: 10 * time.Second,
	}
	if mutate != nil {
		mutate(h.cfg)
	}
	h.open()
	return h
}

func (h *harness) open() {
	t := h.t
	db, err := store.Open(context.Background(), filepath.Join(h.dir, "s.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	h.db = db
	if h.user == nil {
		h.user, _ = db.CreateUser(context.Background(), "admin", "x", true)
	}
	kr, _ := crypto.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	var logOut io.Writer = io.Discard
	if os.Getenv("LM_TEST_LOG") != "" {
		logOut = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := metrics.New()
	h.accts = &accounts.Service{DB: db, KR: kr, OAuth: oauth.New(&config.Config{PublicURL: mustURL()}, db, kr, log), Policy: netx.Policy{Timeout: 10 * time.Second}, Metrics: m}
	h.hub = events.NewHub()
	bs, _ := blobs.New(filepath.Join(h.dir, "blobs"))
	h.eng = New(h.cfg, db, h.accts, h.hub, events.NewNotifier(db, h.hub, log, ""), bs, m, log)
	h.ctx, h.cancel = context.WithCancel(context.Background())
	if err := h.eng.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.close)
}

func (h *harness) close() {
	if h.eng != nil {
		h.eng.Stop()
		h.cancel()
		h.db.Close()
		h.eng = nil
	}
}

func (h *harness) restart() {
	h.close()
	h.open()
}

func (h *harness) addAccount(user, password string) *store.Account {
	ctx := context.Background()
	a := &store.Account{UserID: h.user.ID, Provider: "custom", Email: user, IMAPHost: h.srv.Host, IMAPPort: h.srv.Port, IMAPSecurity: "tls",
		SMTPHost: "127.0.0.1", SMTPPort: 1, SMTPSecurity: "tls", AuthType: "password", Username: user, TLSCAPEM: h.cert.CAPEM, SyncEnabled: true}
	if err := h.db.CreateAccount(ctx, a); err != nil {
		h.t.Fatal(err)
	}
	enc, _ := h.accts.EncryptPassword(a.ID, password)
	a.PasswordEnc = enc
	if err := h.db.UpdateAccountSettings(ctx, a); err != nil {
		h.t.Fatal(err)
	}
	h.eng.AccountAdded(a.ID)
	return a
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s", what)
}

func (h *harness) count(a *store.Account, role string) int {
	f, err := h.db.FolderByRole(context.Background(), a.ID, role)
	if err != nil {
		return -1
	}
	n, _ := h.db.CountFolderMessages(context.Background(), f.ID)
	return n
}

func (h *harness) list(a *store.Account, role string) []*store.Message {
	f, _ := h.db.FolderByRole(context.Background(), a.ID, role)
	msgs, _ := h.db.ListMessages(context.Background(), store.MessageFilter{UserID: h.user.ID, FolderID: f.ID, Limit: 200})
	return msgs
}

// otherClient simulates another mail client acting on the same mailbox.
func (h *harness) otherClient(user, password string) *imapx.Client {
	c, err := imapx.Dial(context.Background(), imapx.DialOptions{Policy: netx.Policy{Timeout: 5 * time.Second},
		Endpoint: mailauth.Endpoint{Host: h.srv.Host, Port: h.srv.Port, Security: "tls", CAPEM: h.cert.CAPEM},
		Creds:    mailauth.Credentials{Username: user, Password: password}})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(c.Logout)
	return c
}

func mustURL() *urlT { u, _ := parseURL("https://mail.example.com"); return u }

func deliverN(h *harness, user string, n int, start time.Time) {
	for i := 0; i < n; i++ {
		raw := testmail.Message(fmt.Sprintf("sender%d@test", i), user, fmt.Sprintf("Message %d", i), fmt.Sprintf("Body of message number %d", i), start.Add(time.Duration(i)*time.Minute))
		if err := h.srv.Deliver(user, "INBOX", raw); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestSyncLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.AddUser("alice@test", "pw")
	deliverN(h, "alice@test", 120, time.Now().Add(-48*time.Hour)) // > chunk size: exercises backfill
	a := h.addAccount("alice@test", "pw")

	h.waitFor("initial sync + backfill", func() bool { return h.count(a, "inbox") == 120 })
	acct, _ := h.db.Account(context.Background(), a.ID)
	if acct.Status != store.StatusOK || acct.LastSyncOKAt == 0 {
		t.Fatalf("status %s %q", acct.Status, acct.LastError)
	}
	msgs := h.list(a, "inbox")
	if msgs[0].Subject != "Message 119" || msgs[0].Snippet != "Body of message number 119" {
		t.Fatalf("newest first / snippet: %q %q", msgs[0].Subject, msgs[0].Snippet)
	}
	folders, _ := h.db.AccountFolders(context.Background(), a.ID)
	if len(folders) != 5 {
		t.Fatalf("folders: %d", len(folders))
	}

	// New message + flag change + expunge performed by another client.
	other := h.otherClient("alice@test", "pw")
	if _, err := other.SelectMailbox("INBOX", false); err != nil {
		t.Fatal(err)
	}
	other.SetFlags(imapx.UIDSetOf([]uint32{msgs[0].UID}), true, imap.FlagSeen, imap.FlagFlagged)
	other.DeletePermanently(imapx.UIDSetOf([]uint32{msgs[1].UID}))
	deliverN(h, "alice@test", 1, time.Now())
	h.eng.Trigger(a.ID, true)
	h.waitFor("incremental sync", func() bool {
		m, err := h.db.MessageForUser(context.Background(), h.user.ID, msgs[0].ID)
		return err == nil && m.Seen && m.Flagged && h.count(a, "inbox") == 120
	})
	if _, err := h.db.MessageForUser(context.Background(), h.user.ID, msgs[1].ID); err != store.ErrNotFound {
		t.Fatal("expunged message still present")
	}

	// Body on demand, then actions.
	body, err := h.eng.FetchBody(context.Background(), msgs[0])
	if err != nil || body.Text == "" {
		t.Fatalf("body: %+v %v", body, err)
	}
	res := h.eng.Apply(context.Background(), h.user.ID, []*store.Message{msgs[2], msgs[3]}, ActArchive, 0)
	if len(res.OK) != 2 {
		t.Fatalf("archive: %+v", res)
	}
	res = h.eng.Apply(context.Background(), h.user.ID, []*store.Message{msgs[4]}, ActTrash, 0)
	if len(res.OK) != 1 {
		t.Fatalf("trash: %+v", res)
	}
	h.eng.Trigger(a.ID, true)
	h.waitFor("moves reflected", func() bool {
		return h.count(a, "inbox") == 117 && h.count(a, "archive") == 2 && h.count(a, "trash") == 1
	})
	// Deleting from the trash is permanent.
	tr := h.list(a, "trash")
	res = h.eng.Apply(context.Background(), h.user.ID, tr, ActTrash, 0)
	if len(res.OK) != 1 {
		t.Fatalf("delete from trash: %+v", res)
	}
	h.eng.Trigger(a.ID, true)
	h.waitFor("permanent delete", func() bool { return h.count(a, "trash") == 0 })

	// Restart: state persists, a resync creates no duplicate.
	h.restart()
	h.eng.Trigger(a.ID, true)
	time.Sleep(500 * time.Millisecond)
	h.waitFor("after restart", func() bool { return h.count(a, "inbox") == 117 && h.count(a, "archive") == 2 })
}

func TestUIDValidityChange(t *testing.T) {
	h := newHarness(t, nil)
	u := h.srv.AddUser("bob@test", "pw")
	deliverN(h, "bob@test", 5, time.Now().Add(-time.Hour))
	a := h.addAccount("bob@test", "pw")
	h.waitFor("sync", func() bool { return h.count(a, "inbox") == 5 })
	before := h.list(a, "inbox")[0].ID

	// Recreate the mailbox: the server assigns a new UIDVALIDITY.
	if err := u.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	u.Create("INBOX", nil)
	deliverN(h, "bob@test", 3, time.Now())
	h.eng.Trigger(a.ID, true)
	h.waitFor("reset after UIDVALIDITY change", func() bool { return h.count(a, "inbox") == 3 })
	if _, err := h.db.MessageForUser(context.Background(), h.user.ID, before); err != store.ErrNotFound {
		t.Fatal("stale message kept after UIDVALIDITY change")
	}
}

func TestAccountsAreIndependent(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.AddUser("good@test", "pw")
	deliverN(h, "good@test", 3, time.Now())
	bad := h.addAccount("bad@test", "wrong") // unknown user -> auth error
	good := h.addAccount("good@test", "pw")
	h.waitFor("good account synced", func() bool { return h.count(good, "inbox") == 3 })
	h.waitFor("bad account flagged", func() bool {
		a, _ := h.db.Account(context.Background(), bad.ID)
		return a.Status == store.StatusAuthError && a.LastError != ""
	})
	notes, _ := h.db.ListNotifications(context.Background(), h.user.ID, 10)
	if len(notes) == 0 || notes[0].Kind != events.KindAuthError {
		t.Fatalf("expected auth notification, got %+v", notes)
	}
}

func TestNetworkInterruption(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.AddUser("carol@test", "pw")
	deliverN(h, "carol@test", 2, time.Now())
	a := h.addAccount("carol@test", "pw")
	h.waitFor("sync", func() bool { return h.count(a, "inbox") == 2 })

	// Server becomes unreachable: pooled connections break, new ones fail.
	h.srv.Down.Store(true)
	h.eng.mu.Lock()
	w := h.eng.workers[a.ID]
	h.eng.mu.Unlock()
	w.pool.close()
	w.pool = newPool(2, time.Minute, w.dial, func() { <-h.eng.connSem; h.eng.m.IMAPConns.Dec() })
	h.eng.Trigger(a.ID, false)
	h.waitFor("error status", func() bool {
		acct, _ := h.db.Account(context.Background(), a.ID)
		return acct.Status == store.StatusError
	})
	// Recovery.
	h.srv.Down.Store(false)
	deliverN(h, "carol@test", 1, time.Now())
	h.eng.Trigger(a.ID, false)
	h.waitFor("recovered", func() bool {
		acct, _ := h.db.Account(context.Background(), a.ID)
		return acct.Status == store.StatusOK && h.count(a, "inbox") == 3
	})
}

func TestIdlePush(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SyncIdle = true })
	h.srv.AddUser("dave@test", "pw")
	a := h.addAccount("dave@test", "pw")
	acct, _ := h.db.Account(context.Background(), a.ID)
	if !acct.IdleEnabled {
		acct.IdleEnabled = true
		h.db.UpdateAccountSettings(context.Background(), acct)
		h.eng.AccountChanged(a.ID)
	}
	h.waitFor("idle connection", func() bool { return h.eng.m.IdleConns.Value() == 1 })
	ch, cancel := h.hub.Subscribe(h.user.ID)
	defer cancel()
	deliverN(h, "dave@test", 1, time.Now())
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == events.TypeNewMail {
				if h.count(a, "inbox") != 1 {
					t.Fatal("event before storage")
				}
				return
			}
		case <-deadline:
			t.Fatal("no push notification via IDLE (interval is 1h, so polling cannot explain it)")
		}
	}
}
