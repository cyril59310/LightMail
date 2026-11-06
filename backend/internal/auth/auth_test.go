package auth

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lightmail/internal/crypto"
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

func TestTOTPCodeRFC6238(t *testing.T) {
	// RFC 6238 appendix B, SHA-1 key "12345678901234567890" (8 digits there:
	// the 6-digit codes are their last six digits).
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := TOTPCode(secret, ts/totpPeriod)
		if err != nil || got != want {
			t.Errorf("t=%d: got %s, want %s (%v)", ts, got, want, err)
		}
	}
}

func TestTOTPLogin(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	EnsureAdmin(ctx, db, "admin", "very-secret-pass")
	m := NewManager(db, time.Hour, 24*time.Hour)
	kr, err := crypto.NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m.Keyring = kr
	cur := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return cur }
	code := func(secret string) string {
		c, _ := TOTPCode(secret, cur.Unix()/totpPeriod)
		return c
	}

	u, _ := db.UserByName(ctx, "admin")
	if _, _, err := m.TOTPSetup(ctx, u, "wrong-password-x", "LightMail"); err != ErrInvalidCredentials {
		t.Fatalf("setup with a wrong password: %v", err)
	}
	secret, uri, err := m.TOTPSetup(ctx, u, "very-secret-pass", "LightMail")
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/LightMail:admin?") {
		t.Fatalf("setup: %v %s", err, uri)
	}
	if _, err := m.TOTPEnable(ctx, u, "000000", ""); err != ErrInvalidTOTP {
		t.Fatalf("enable with a wrong code: %v", err)
	}
	recovery, err := m.TOTPEnable(ctx, u, code(secret), "")
	if err != nil || len(recovery) != recoveryCodeCount {
		t.Fatalf("enable: %v %v", err, recovery)
	}
	if st, _ := db.TOTPState(ctx, u.ID); strings.Contains(st.SecretEnc, secret) {
		t.Fatal("TOTP secret stored in clear")
	}

	if _, _, _, err := m.LoginWithCode(ctx, "admin", "very-secret-pass", "", "1.1.1.1", ""); err != ErrTOTPRequired {
		t.Fatalf("login without code: %v", err)
	}
	if _, _, _, err := m.LoginWithCode(ctx, "admin", "wrong-password-x", "", "1.1.1.1", ""); err != ErrInvalidCredentials {
		t.Fatalf("a wrong password must not reveal the second factor: %v", err)
	}
	// The enable code was the current step: it cannot be replayed.
	if _, _, _, err := m.LoginWithCode(ctx, "admin", "very-secret-pass", code(secret), "1.1.1.1", ""); err != ErrInvalidTOTP {
		t.Fatalf("replayed code: %v", err)
	}
	cur = cur.Add(totpPeriod * time.Second)
	if _, _, _, err := m.LoginWithCode(ctx, "admin", "very-secret-pass", code(secret), "1.1.1.1", ""); err != nil {
		t.Fatalf("login with code: %v", err)
	}
	// Recovery codes work once, with or without the dash.
	rc := strings.ToLower(strings.ReplaceAll(recovery[0], "-", ""))
	if _, _, _, err := m.LoginWithCode(ctx, "admin", "very-secret-pass", rc, "1.1.1.1", ""); err != nil {
		t.Fatalf("login with recovery code: %v", err)
	}
	if _, _, _, err := m.LoginWithCode(ctx, "admin", "very-secret-pass", recovery[0], "1.1.1.1", ""); err != ErrInvalidTOTP {
		t.Fatalf("reused recovery code: %v", err)
	}
	u, _ = db.UserByName(ctx, "admin")
	if _, left, _ := m.TOTPStatus(ctx, u); left != recoveryCodeCount-1 {
		t.Fatalf("recovery codes left: %d", left)
	}

	cur = cur.Add(totpPeriod * time.Second)
	if err := m.TOTPDisable(ctx, u, "very-secret-pass", "123"); err != ErrInvalidTOTP {
		t.Fatalf("disable with a wrong code: %v", err)
	}
	if err := m.TOTPDisable(ctx, u, "very-secret-pass", code(secret)); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, _, _, err := m.Login(ctx, "admin", "very-secret-pass", "1.1.1.1", ""); err != nil {
		t.Fatalf("login after disabling: %v", err)
	}
}
