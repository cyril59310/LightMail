package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"lightmail/internal/crypto"
	"lightmail/internal/store"
)

// Two-factor authentication with time-based one-time passwords (RFC 6238:
// HMAC-SHA1, 30 s steps, 6 digits), compatible with the usual
// authenticator apps, plus single-use recovery codes.

var (
	ErrTOTPRequired   = errors.New("a two-factor authentication code is required")
	ErrInvalidTOTP    = errors.New("invalid two-factor authentication code")
	ErrTOTPEnabled    = errors.New("two-factor authentication is already enabled")
	ErrTOTPNotEnabled = errors.New("two-factor authentication is not enabled")
	ErrNoTOTPSetup    = errors.New("start the two-factor authentication set-up first")
	ErrNoKeyring      = errors.New("two-factor authentication needs the master key")
)

const (
	totpPeriod        = 30
	totpDigits        = 6
	totpSkew          = 1 // accepted steps before/after the current one
	recoveryCodeCount = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32-encoded.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure: %v", err))
	}
	return b32.EncodeToString(b)
}

// TOTPCode computes the code of a time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000), nil
}

// matchTOTP returns the time step matching code around t, or 0.
func matchTOTP(secret, code string, t time.Time) int64 {
	code = normalizeCode(code)
	if len(code) != totpDigits {
		return 0
	}
	cur := t.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		want, err := TOTPCode(secret, cur+d)
		if err == nil && hmac.Equal([]byte(want), []byte(code)) {
			return cur + d
		}
	}
	return 0
}

// TOTPURI is the otpauth:// URI shown as a QR code to authenticator apps.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func normalizeCode(c string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(c)))
}

// Recovery codes: 10 characters from an unambiguous alphabet (~50 bits),
// shown as XXXXX-XXXXX and stored as SHA-256 hashes.
const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newRecoveryCodes() (codes, hashes []string) {
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			panic(fmt.Sprintf("crypto/rand failure: %v", err))
		}
		for j := range b {
			b[j] = recoveryAlphabet[int(b[j])%len(recoveryAlphabet)]
		}
		codes = append(codes, string(b[:5])+"-"+string(b[5:]))
		hashes = append(hashes, crypto.HashToken(string(b)))
	}
	return codes, hashes
}

func totpContext(userID int64) string { return fmt.Sprintf("user:%d:totp", userID) }

// verifySecondFactor accepts a current TOTP code (never twice) or an unused
// recovery code (consumed).
func (m *Manager) verifySecondFactor(ctx context.Context, userID int64, code string) (bool, error) {
	if m.Keyring == nil {
		return false, ErrNoKeyring
	}
	st, err := m.db.TOTPState(ctx, userID)
	if err != nil {
		return false, err
	}
	if st.SecretEnc == "" {
		return false, ErrTOTPNotEnabled
	}
	secret, err := m.Keyring.Decrypt(st.SecretEnc, totpContext(userID))
	if err != nil {
		return false, err
	}
	if step := matchTOTP(secret, code, m.now()); step != 0 {
		return m.db.ConsumeTOTPStep(ctx, userID, step)
	}
	if c := normalizeCode(code); len(c) == 10 {
		return m.db.ConsumeRecoveryCode(ctx, userID, crypto.HashToken(c))
	}
	return false, nil
}

// checkSecondFactor wraps verifySecondFactor with the per-user brute-force
// limiter.
func (m *Manager) checkSecondFactor(ctx context.Context, u *store.User, code string) error {
	key := "u:" + strings.ToLower(u.Username)
	if ok, d := m.userLimiter.Allow(key); !ok {
		return &LockoutError{d}
	}
	ok, err := m.verifySecondFactor(ctx, u.ID, code)
	if err != nil {
		return err
	}
	if !ok {
		m.userLimiter.Fail(key)
		return ErrInvalidTOTP
	}
	return nil
}

// TOTPStatus reports whether 2FA is on and how many recovery codes remain.
func (m *Manager) TOTPStatus(ctx context.Context, u *store.User) (enabled bool, recoveryLeft int, err error) {
	st, err := m.db.TOTPState(ctx, u.ID)
	if err != nil {
		return false, 0, err
	}
	return st.SecretEnc != "", len(st.Recovery), nil
}

// TOTPSetup checks the password and generates a secret awaiting
// confirmation. It returns the secret and its otpauth:// URI.
func (m *Manager) TOTPSetup(ctx context.Context, u *store.User, password, issuer string) (secret, uri string, err error) {
	if m.Keyring == nil {
		return "", "", ErrNoKeyring
	}
	if u.TOTPEnabled {
		return "", "", ErrTOTPEnabled
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return "", "", ErrInvalidCredentials
	}
	secret = NewTOTPSecret()
	enc, err := m.Keyring.Encrypt(secret, totpContext(u.ID))
	if err != nil {
		return "", "", err
	}
	if err := m.db.SetTOTPPending(ctx, u.ID, enc); err != nil {
		return "", "", err
	}
	return secret, TOTPURI(issuer, u.Username, secret), nil
}

// TOTPEnable confirms the pending secret with a first code, turns 2FA on,
// revokes the other sessions and returns fresh recovery codes.
func (m *Manager) TOTPEnable(ctx context.Context, u *store.User, code, keepSession string) ([]string, error) {
	if m.Keyring == nil {
		return nil, ErrNoKeyring
	}
	if u.TOTPEnabled {
		return nil, ErrTOTPEnabled
	}
	st, err := m.db.TOTPState(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if st.PendingEnc == "" {
		return nil, ErrNoTOTPSetup
	}
	secret, err := m.Keyring.Decrypt(st.PendingEnc, totpContext(u.ID))
	if err != nil {
		return nil, err
	}
	step := matchTOTP(secret, code, m.now())
	if step == 0 {
		return nil, ErrInvalidTOTP
	}
	codes, hashes := newRecoveryCodes()
	if err := m.db.EnableTOTP(ctx, u.ID, st.PendingEnc, step, hashes); err != nil {
		return nil, err
	}
	return codes, m.db.DeleteUserSessions(ctx, u.ID, keepSession)
}

// TOTPDisable turns 2FA off after checking the password and a code.
func (m *Manager) TOTPDisable(ctx context.Context, u *store.User, password, code string) error {
	if !u.TOTPEnabled {
		return ErrTOTPNotEnabled
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return ErrInvalidCredentials
	}
	if err := m.checkSecondFactor(ctx, u, code); err != nil {
		return err
	}
	return m.db.DisableTOTP(ctx, u.ID)
}

// TOTPRegenerateRecovery replaces the recovery codes after checking a code.
func (m *Manager) TOTPRegenerateRecovery(ctx context.Context, u *store.User, code string) ([]string, error) {
	if !u.TOTPEnabled {
		return nil, ErrTOTPNotEnabled
	}
	if err := m.checkSecondFactor(ctx, u, code); err != nil {
		return nil, err
	}
	codes, hashes := newRecoveryCodes()
	return codes, m.db.SetRecoveryCodes(ctx, u.ID, hashes)
}
