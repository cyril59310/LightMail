package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lightmail/internal/store"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected format %s", h)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "wrong password!!") {
		t.Fatal("verify mismatch")
	}
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatal("weak password accepted")
	}
	if VerifyPassword("$argon2id$v=19$m=999999999,t=1,p=1$c2FsdA$aGFzaA", "x") {
		t.Fatal("absurd parameters accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, time.Second, time.Minute, time.Hour)
	cur := time.Unix(1000, 0)
	l.now = func() time.Time { return cur }
	l.Fail("k")
	l.Fail("k")
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("locked too early")
	}
	l.Fail("k")
	if ok, d := l.Allow("k"); ok || d != time.Second {
		t.Fatalf("expected 1s lock, got %v %v", ok, d)
	}
	l.Fail("k")
	if _, d := l.Allow("k"); d != 2*time.Second {
		t.Fatalf("expected doubling, got %v", d)
	}
	cur = cur.Add(3 * time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("lock should expire")
	}
	l.Succeed("k")
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("success should reset")
	}
}

func newDB(t *testing.T) *store.DB {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSessionsLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	created, err := EnsureAdmin(ctx, db, "admin", "very-secret-pass")
	if err != nil || !created {
		t.Fatal("bootstrap failed", err)
	}
	created, _ = EnsureAdmin(ctx, db, "admin", "another-password")
	if created {
		t.Fatal("bootstrap must not override existing users")
	}

	m := NewManager(db, time.Hour, 24*time.Hour)
	cur := time.Now()
	m.now = func() time.Time { return cur }

	if _, _, _, err := m.Login(ctx, "admin", "nope-nope-nope", "1.2.3.4", "ua"); err != ErrInvalidCredentials {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
	if _, _, _, err := m.Login(ctx, "ghost", "whatever-pass", "1.2.3.4", "ua"); err != ErrInvalidCredentials {
		t.Fatalf("unknown user must look like bad password, got %v", err)
	}
	s, tok, u, err := m.Login(ctx, "ADMIN", "very-secret-pass", "1.2.3.4", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == tok || len(tok) < 40 {
		t.Fatal("token must not be stored as-is")
	}
	if _, _, err := m.Validate(ctx, tok); err != nil {
		t.Fatal(err)
	}
	// idle expiry
	cur = cur.Add(2 * time.Hour)
	if _, _, err := m.Validate(ctx, tok); err != ErrNoSession {
		t.Fatal("idle session should expire")
	}
	cur = time.Now()
	_, tok1, _, _ := m.Login(ctx, "admin", "very-secret-pass", "1.2.3.4", "ua")
	s2, tok2, _, _ := m.Login(ctx, "admin", "very-secret-pass", "1.2.3.4", "ua")
	if err := m.LogoutOthers(ctx, u.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Validate(ctx, tok1); err != ErrNoSession {
		t.Fatal("other sessions should be revoked")
	}
	if _, _, err := m.Validate(ctx, tok2); err != nil {
		t.Fatal("current session should remain")
	}
}

func TestBruteForceLockout(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	EnsureAdmin(ctx, db, "admin", "very-secret-pass")
	m := NewManager(db, time.Hour, 24*time.Hour)
	for i := 0; i < 6; i++ {
		m.Login(ctx, "admin", "wrong-password-x", "9.9.9.9", "")
	}
	_, _, _, err := m.Login(ctx, "admin", "very-secret-pass", "9.9.9.9", "")
	var le *LockoutError
	if !errors.As(err, &le) || le.RetryAfter <= 0 {
		t.Fatalf("expected lockout even with right password, got %v", err)
	}
}
