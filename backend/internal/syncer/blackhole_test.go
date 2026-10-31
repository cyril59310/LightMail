package syncer

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lightmail/internal/config"
	"lightmail/internal/store"
)

// blackhole is a TCP proxy that can silently stop forwarding traffic
// (like a network outage: no RST, packets just vanish).
type blackhole struct {
	ln     net.Listener
	target string
	frozen atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func newBlackhole(t *testing.T, target string) *blackhole {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &blackhole{ln: ln, target: target}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if b.frozen.Load() {
				// accept but never answer (SYN-ACK done by the kernel, then silence)
				b.mu.Lock()
				b.conns = append(b.conns, c)
				b.mu.Unlock()
				continue
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			b.mu.Lock()
			b.conns = append(b.conns, c, u)
			b.mu.Unlock()
			go b.pipe(c, u)
			go b.pipe(u, c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			c.Close()
		}
		b.mu.Unlock()
	})
	return b
}

func (b *blackhole) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !b.frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

func (b *blackhole) port() int { return b.ln.Addr().(*net.TCPAddr).Port }

func TestSilentNetworkOutage(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.NetTimeout = 2 * time.Second; c.SyncIdle = true })
	h.srv.AddUser("eve@test", "pw")
	deliverN(h, "eve@test", 3, time.Now())
	bh := newBlackhole(t, net.JoinHostPort(h.srv.Host, itoa(h.srv.Port)))
	// Rebuild the account policy with the short timeout.
	h.accts.Policy.Timeout = 2 * time.Second
	ctx := context.Background()
	a := &store.Account{UserID: h.user.ID, Provider: "custom", Email: "eve@test", IMAPHost: "127.0.0.1", IMAPPort: bh.port(), IMAPSecurity: "tls",
		SMTPHost: "127.0.0.1", SMTPPort: 1, SMTPSecurity: "tls", AuthType: "password", Username: "eve@test", TLSCAPEM: h.cert.CAPEM, SyncEnabled: true, IdleEnabled: true}
	h.db.CreateAccount(ctx, a)
	a.PasswordEnc, _ = h.accts.EncryptPassword(a.ID, "pw")
	h.db.UpdateAccountSettings(ctx, a)
	h.eng.AccountAdded(a.ID)
	h.waitFor("initial sync", func() bool { return h.count(a, "inbox") == 3 })

	bh.frozen.Store(true)
	start := time.Now()
	h.eng.Trigger(a.ID, true)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		acct, _ := h.db.Account(ctx, a.ID)
		if acct.Status == store.StatusError {
			t.Logf("outage detected after %s: %s", time.Since(start).Round(100*time.Millisecond), acct.LastError)
			if time.Since(start) > 5*time.Second {
				t.Fatalf("detection took %s, expected about one NetTimeout (2s)", time.Since(start))
			}
			bh.frozen.Store(false)
			deliverN(h, "eve@test", 1, time.Now())
			h.eng.Trigger(a.ID, false)
			h.waitFor("recovery", func() bool {
				acct, _ := h.db.Account(ctx, a.ID)
				return acct.Status == store.StatusOK && h.count(a, "inbox") == 4
			})
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("silent outage not detected within 20s (NetTimeout=2s)")
}

func itoa(n int) string { return fmtInt(n) }
