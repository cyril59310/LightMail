package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"lightmail/internal/crypto"
	"lightmail/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrLocked             = errors.New("too many failed attempts, try again later")
	ErrNoSession          = errors.New("no valid session")
)

const CookieName = "lm_session"

type Manager struct {
	db          *store.DB
	idleTTL     time.Duration
	absoluteTTL time.Duration
	ipLimiter   *Limiter
	userLimiter *Limiter
	now         func() time.Time
	// Keyring encrypts the TOTP secrets; two-factor authentication is
	// unavailable without it.
	Keyring *crypto.Keyring
}

func NewManager(db *store.DB, idleTTL, absoluteTTL time.Duration) *Manager {
	return &Manager{
		db:          db,
		idleTTL:     idleTTL,
		absoluteTTL: absoluteTTL,
		// IP: 10 free failures then 1 min doubling up to 1 h.
		ipLimiter: NewLimiter(10, time.Minute, time.Hour, time.Hour),
		// Username: 5 free failures then 30 s doubling up to 15 min.
		userLimiter: NewLimiter(5, 30*time.Second, 15*time.Minute, time.Hour),
		now:         time.Now,
	}
}

// LockoutError carries the remaining lock duration.
type LockoutError struct{ RetryAfter time.Duration }

func (e *LockoutError) Error() string { return ErrLocked.Error() }
func (e *LockoutError) Unwrap() error { return ErrLocked }

// Login verifies credentials and creates a session. It returns the bearer
// token to store in the cookie (only its hash is persisted).
func (m *Manager) Login(ctx context.Context, username, password, ip, ua string) (*store.Session, string, *store.User, error) {
	return m.LoginWithCode(ctx, username, password, "", ip, ua)
}

// LoginWithCode is Login with the second factor of users who enabled it
// (a TOTP or recovery code). Without a code such users get ErrTOTPRequired,
// only after a correct password.
func (m *Manager) LoginWithCode(ctx context.Context, username, password, code, ip, ua string) (*store.Session, string, *store.User, error) {
	username = strings.TrimSpace(username)
	userKey := "u:" + strings.ToLower(username)
	ipKey := "ip:" + ip
	if ok, d := m.ipLimiter.Allow(ipKey); !ok {
		return nil, "", nil, &LockoutError{d}
	}
	if ok, d := m.userLimiter.Allow(userKey); !ok {
		return nil, "", nil, &LockoutError{d}
	}
	u, err := m.db.UserByName(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, "", nil, err
	}
	hash := dummyHash()
	if u != nil {
		hash = u.PasswordHash
	}
	if !VerifyPassword(hash, password) || u == nil {
		m.ipLimiter.Fail(ipKey)
		m.userLimiter.Fail(userKey)
		return nil, "", nil, ErrInvalidCredentials
	}
	if u.TOTPEnabled {
		if strings.TrimSpace(code) == "" {
			return nil, "", nil, ErrTOTPRequired
		}
		ok, err := m.verifySecondFactor(ctx, u.ID, code)
		if err != nil {
			return nil, "", nil, err
		}
		if !ok {
			m.ipLimiter.Fail(ipKey)
			m.userLimiter.Fail(userKey)
			return nil, "", nil, ErrInvalidTOTP
		}
	}
	m.ipLimiter.Succeed(ipKey)
	m.userLimiter.Succeed(userKey)
	s, tok, err := m.create(ctx, u.ID, ip, ua)
	return s, tok, u, err
}

func (m *Manager) create(ctx context.Context, userID int64, ip, ua string) (*store.Session, string, error) {
	tok := crypto.RandomToken(32)
	t := m.now().Unix()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	s := &store.Session{
		ID:         crypto.HashToken(tok),
		UserID:     userID,
		CSRFToken:  crypto.RandomToken(32),
		CreatedAt:  t,
		LastSeenAt: t,
		ExpiresAt:  t + int64(m.absoluteTTL.Seconds()),
		UserAgent:  ua,
		IP:         ip,
	}
	if err := m.db.CreateSession(ctx, s); err != nil {
		return nil, "", err
	}
	return s, tok, nil
}

// Validate returns the session and user for a cookie token, enforcing idle
// and absolute expiry, and slides the idle window.
func (m *Manager) Validate(ctx context.Context, token string) (*store.Session, *store.User, error) {
	if token == "" || len(token) > 200 {
		return nil, nil, ErrNoSession
	}
	s, err := m.db.SessionByID(ctx, crypto.HashToken(token))
	if err != nil {
		return nil, nil, ErrNoSession
	}
	t := m.now().Unix()
	if t >= s.ExpiresAt || t-s.LastSeenAt > int64(m.idleTTL.Seconds()) {
		_ = m.db.DeleteSession(ctx, s.ID)
		return nil, nil, ErrNoSession
	}
	u, err := m.db.UserByID(ctx, s.UserID)
	if err != nil {
		return nil, nil, ErrNoSession
	}
	// Limit write amplification: refresh last_seen at most once a minute.
	if t-s.LastSeenAt > 60 {
		_ = m.db.TouchSession(ctx, s.ID, t)
		s.LastSeenAt = t
	}
	return s, u, nil
}

func (m *Manager) Logout(ctx context.Context, sessionID string) error {
	return m.db.DeleteSession(ctx, sessionID)
}

// LogoutOthers revokes every session of the user except the current one.
func (m *Manager) LogoutOthers(ctx context.Context, userID int64, keep string) error {
	return m.db.DeleteUserSessions(ctx, userID, keep)
}

// ChangePassword verifies the current password, stores the new hash and
// revokes other sessions.
func (m *Manager) ChangePassword(ctx context.Context, u *store.User, current, next, keepSession string) error {
	if !VerifyPassword(u.PasswordHash, current) {
		return ErrInvalidCredentials
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := m.db.SetUserPassword(ctx, u.ID, h); err != nil {
		return err
	}
	return m.db.DeleteUserSessions(ctx, u.ID, keepSession)
}

// IdleCutoff is used by periodic purges.
func (m *Manager) IdleCutoff() int64 { return m.now().Add(-m.idleTTL).Unix() }

func (m *Manager) GC() {
	m.ipLimiter.GC()
	m.userLimiter.GC()
}

// EnsureAdmin creates or resets the bootstrap admin account from the
// environment (LIGHTMAIL_ADMIN_USER / LIGHTMAIL_ADMIN_PASSWORD).
func EnsureAdmin(ctx context.Context, db *store.DB, username, password string) (created bool, err error) {
	if password == "" {
		return false, nil
	}
	n, err := db.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil // never override an existing installation
	}
	h, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	_, err = db.CreateUser(ctx, username, h, true)
	return err == nil, err
}
