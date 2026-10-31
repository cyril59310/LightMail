// Package oauth implements the OAuth2 Authorization Code flow with PKCE
// against mail providers (Google, Microsoft) and the lifecycle of the
// resulting tokens (encrypted storage, coordinated refresh, revocation).
//
// This is distinct from the authentication to LightMail itself (package auth).
package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/providers"
	"lightmail/internal/store"
)

var (
	ErrNotConfigured = errors.New("oauth2 client credentials are not configured for this provider")
	ErrReauth        = errors.New("the account must be re-authenticated")
	ErrInvalidState  = errors.New("invalid or expired OAuth state")
)

// CallbackError carries a stable code shown to the user.
type CallbackError struct {
	Code   string
	Detail string
}

func (e *CallbackError) Error() string { return e.Code + ": " + e.Detail }

const (
	stateTTL      = 10 * time.Minute
	refreshMargin = 2 * time.Minute
)

type Manager struct {
	clients  map[string]config.OAuthClient
	redirect func(provider string) string
	db       *store.DB
	kr       *crypto.Keyring
	http     *http.Client
	log      *slog.Logger
	specs    map[string]*providers.OAuthSpec
	locks    sync.Map // account ID -> *sync.Mutex
	now      func() time.Time

	// OnReauth is called when a refresh token is rejected.
	OnReauth func(ctx context.Context, acct *store.Account, reason string)
	// Refreshes counts token refresh requests (metrics, tests).
	Refreshes atomicCounter
}

func New(cfg *config.Config, db *store.DB, kr *crypto.Keyring, log *slog.Logger) *Manager {
	m := &Manager{
		clients:  map[string]config.OAuthClient{"google": cfg.Google, "microsoft": cfg.Microsoft},
		redirect: cfg.OAuthRedirectURL,
		db:       db,
		kr:       kr,
		http:     &http.Client{Timeout: 20 * time.Second},
		log:      log,
		specs:    map[string]*providers.OAuthSpec{},
		now:      time.Now,
	}
	for _, p := range providers.All() {
		if p.OAuth != nil {
			m.specs[p.ID] = p.OAuth
		}
	}
	return m
}

// SetSpec overrides provider endpoints (tests).
func (m *Manager) SetSpec(provider string, spec *providers.OAuthSpec, client config.OAuthClient) {
	m.specs[provider] = spec
	m.clients[provider] = client
}

func (m *Manager) Configured(provider string) bool {
	_, ok := m.specs[provider]
	return ok && m.clients[provider].Configured()
}

func (m *Manager) config(provider, tenant string) (*oauth2.Config, *providers.OAuthSpec, error) {
	spec, ok := m.specs[provider]
	cl := m.clients[provider]
	if !ok || !cl.Configured() {
		return nil, nil, ErrNotConfigured
	}
	if tenant == "" {
		tenant = cl.Tenant
	}
	authURL, tokenURL := spec.Endpoints(tenant)
	return &oauth2.Config{
		ClientID:     cl.ClientID,
		ClientSecret: cl.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL:  m.redirect(provider),
		Scopes:       spec.Scopes,
	}, spec, nil
}

// Start creates a single-use state bound to the browser session and
// returns the provider authorisation URL. accountID is set for re-auth.
func (m *Manager) Start(ctx context.Context, userID int64, sessionID, provider string, accountID int64, loginHint string) (string, error) {
	cfg, spec, err := m.config(provider, "")
	if err != nil {
		return "", err
	}
	state := crypto.RandomToken(32)
	verifier := oauth2.GenerateVerifier()
	stateHash := crypto.HashToken(state)
	verEnc, err := m.kr.Encrypt(verifier, "oauth_state:"+stateHash)
	if err != nil {
		return "", err
	}
	if err := m.db.CreateOAuthState(ctx, &store.OAuthState{
		StateHash: stateHash, UserID: userID, SessionHash: sessionID, Provider: provider,
		VerifierEnc: verEnc, AccountID: accountID, ExpiresAt: m.now().Add(stateTTL).Unix(),
	}); err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	for k, v := range spec.AuthQuery {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	if loginHint != "" && len(loginHint) < 320 {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", loginHint))
	}
	return cfg.AuthCodeURL(state, opts...), nil
}

// Identity is the result of a successful authorisation.
type Identity struct {
	UserID    int64
	AccountID int64 // non-zero for re-authentication
	Provider  string
	Email     string
	Name      string
	Tenant    string
	Access    string
	Refresh   string
	Expiry    time.Time
	Scope     string
}

// Callback validates the redirect and exchanges the code for tokens.
func (m *Manager) Callback(ctx context.Context, sessionID, provider string, q url.Values) (*Identity, error) {
	state := q.Get("state")
	if state == "" || len(state) > 200 {
		return nil, &CallbackError{"invalid_state", "missing state"}
	}
	st, err := m.db.ConsumeOAuthState(ctx, crypto.HashToken(state))
	if err != nil {
		return nil, &CallbackError{"invalid_state", "unknown or already used state"}
	}
	if st.ExpiresAt < m.now().Unix() {
		return nil, &CallbackError{"invalid_state", "state expired"}
	}
	if !crypto.ConstantTimeEqual(st.SessionHash, sessionID) {
		return nil, &CallbackError{"invalid_state", "state was issued to another session"}
	}
	if st.Provider != provider {
		return nil, &CallbackError{"invalid_state", "provider mismatch"}
	}
	if e := q.Get("error"); e != "" {
		desc := q.Get("error_description")
		if len(desc) > 300 {
			desc = desc[:300]
		}
		return nil, &CallbackError{providerErrorCode(e), desc}
	}
	code := q.Get("code")
	if code == "" || len(code) > 4096 {
		return nil, &CallbackError{"invalid_request", "missing authorization code"}
	}
	verifier, err := m.kr.Decrypt(st.VerifierEnc, "oauth_state:"+st.StateHash)
	if err != nil {
		return nil, &CallbackError{"invalid_state", "cannot decrypt state"}
	}
	cfg, _, err := m.config(provider, "")
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, m.http)
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return nil, &CallbackError{"exchange_failed", re.ErrorCode + " " + re.ErrorDescription}
		}
		return nil, &CallbackError{"exchange_failed", "token endpoint unreachable"}
	}
	if tok.RefreshToken == "" {
		return nil, &CallbackError{"no_refresh_token", "the provider did not return a refresh token (offline access not granted)"}
	}
	id := &Identity{UserID: st.UserID, AccountID: st.AccountID, Provider: provider, Access: tok.AccessToken, Refresh: tok.RefreshToken, Expiry: tok.Expiry}
	if s, ok := tok.Extra("scope").(string); ok {
		id.Scope = s
	}
	rawID, _ := tok.Extra("id_token").(string)
	claims, err := parseIDToken(rawID)
	if err != nil {
		return nil, &CallbackError{"invalid_id_token", err.Error()}
	}
	if err := m.validateClaims(provider, cfg.ClientID, claims); err != nil {
		return nil, &CallbackError{"invalid_id_token", err.Error()}
	}
	id.Email, id.Name, id.Tenant = claimsIdentity(provider, claims)
	if id.Email == "" {
		return nil, &CallbackError{"email_missing", "the identity token does not contain an email address"}
	}
	return id, nil
}

func providerErrorCode(e string) string {
	switch e {
	case "access_denied", "consent_required", "interaction_required", "invalid_scope", "unauthorized_client":
		return e
	}
	return "provider_error"
}

type idClaims map[string]any

func (c idClaims) str(k string) string {
	s, _ := c[k].(string)
	return s
}

// parseIDToken decodes the ID token payload. The token was received
// directly from the provider token endpoint over a verified TLS connection,
// which OpenID Connect Core (§3.1.3.7) accepts in place of a signature
// check; issuer, audience and expiry are still validated.
func parseIDToken(raw string) (idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("missing id_token (openid scope not granted)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed id_token")
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("malformed id_token")
	}
	return c, nil
}

func (m *Manager) validateClaims(provider, clientID string, c idClaims) error {
	aud := false
	switch v := c["aud"].(type) {
	case string:
		aud = v == clientID
	case []any:
		for _, x := range v {
			if s, _ := x.(string); s == clientID {
				aud = true
			}
		}
	}
	if !aud {
		return errors.New("audience mismatch")
	}
	if exp, ok := c["exp"].(float64); !ok || int64(exp) < m.now().Add(-5*time.Minute).Unix() {
		return errors.New("id_token expired")
	}
	iss := c.str("iss")
	switch provider {
	case "google":
		if iss != "https://accounts.google.com" && iss != "accounts.google.com" {
			return errors.New("unexpected issuer")
		}
		if v, ok := c["email_verified"].(bool); ok && !v {
			return errors.New("email address not verified by Google")
		}
	case "microsoft":
		if !strings.HasPrefix(iss, "https://login.microsoftonline.com/") || !strings.HasSuffix(iss, "/v2.0") {
			return errors.New("unexpected issuer")
		}
		if tid := c.str("tid"); tid == "" || !strings.Contains(iss, tid) {
			return errors.New("tenant mismatch")
		}
	}
	return nil
}

func claimsIdentity(provider string, c idClaims) (email, name, tenant string) {
	email = c.str("email")
	if email == "" && provider == "microsoft" {
		email = c.str("preferred_username")
	}
	if !strings.Contains(email, "@") {
		email = ""
	}
	return strings.ToLower(strings.TrimSpace(email)), c.str("name"), c.str("tid")
}

// ---- token lifecycle ---------------------------------------------------

func accessCtx(id int64) string  { return "account:" + strconv.FormatInt(id, 10) + ":oauth_access" }
func refreshCtx(id int64) string { return "account:" + strconv.FormatInt(id, 10) + ":oauth_refresh" }

// SaveTokens encrypts and stores tokens for an account.
func (m *Manager) SaveTokens(ctx context.Context, accountID int64, access, refresh string, expiry time.Time, scope string) error {
	a, err := m.kr.Encrypt(access, accessCtx(accountID))
	if err != nil {
		return err
	}
	r, err := m.kr.Encrypt(refresh, refreshCtx(accountID))
	if err != nil {
		return err
	}
	return m.db.UpdateAccountTokens(ctx, accountID, a, r, expiry.Unix(), scope)
}

func (m *Manager) lock(id int64) *sync.Mutex {
	v, _ := m.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// AccessToken returns a valid access token, refreshing it if needed.
// Concurrent callers for the same account share a single refresh.
func (m *Manager) AccessToken(ctx context.Context, accountID int64) (string, error) {
	mu := m.lock(accountID)
	mu.Lock()
	defer mu.Unlock()
	a, err := m.db.AccountW(ctx, accountID)
	if err != nil {
		return "", err
	}
	if a.AuthType != providers.AuthOAuth2 {
		return "", errors.New("account does not use OAuth2")
	}
	if a.OAuthRefreshEnc == "" {
		return "", ErrReauth
	}
	if a.OAuthAccessEnc != "" && time.Unix(a.OAuthExpiry, 0).Sub(m.now()) > refreshMargin {
		return m.kr.Decrypt(a.OAuthAccessEnc, accessCtx(a.ID))
	}
	refresh, err := m.kr.Decrypt(a.OAuthRefreshEnc, refreshCtx(a.ID))
	if err != nil {
		return "", err
	}
	res, err := m.refresh(ctx, a, refresh)
	if err != nil {
		var ge *grantError
		if errors.As(err, &ge) {
			m.log.Warn("oauth refresh token rejected", "account_id", a.ID, "provider", a.Provider, "error_code", ge.code)
			// Tokens are useless now: wipe them.
			_ = m.db.UpdateAccountTokens(ctx, a.ID, "", "", 0, a.OAuthScope)
			if m.OnReauth != nil {
				m.OnReauth(ctx, a, ge.code)
			}
			return "", ErrReauth
		}
		return "", err
	}
	if res.refresh == "" {
		res.refresh = refresh // provider kept the same refresh token
	}
	if err := m.SaveTokens(ctx, a.ID, res.access, res.refresh, res.expiry, firstNonEmpty(res.scope, a.OAuthScope)); err != nil {
		return "", err
	}
	return res.access, nil
}

// Invalidate forces the next AccessToken call to refresh (used when a
// server rejects a token that looked valid).
func (m *Manager) Invalidate(ctx context.Context, accountID int64) error {
	mu := m.lock(accountID)
	mu.Lock()
	defer mu.Unlock()
	_, err := m.db.W.ExecContext(ctx, `UPDATE accounts SET oauth_access_enc='', oauth_expiry=0 WHERE id=?`, accountID)
	return err
}

type grantError struct{ code string }

func (e *grantError) Error() string { return "refresh rejected: " + e.code }

type refreshResult struct {
	access, refresh, scope string
	expiry                 time.Time
}

// refresh performs the refresh_token grant. It is implemented directly
// (instead of oauth2.TokenSource) to send the scope parameter Microsoft
// requires and to persist rotated refresh tokens.
func (m *Manager) refresh(ctx context.Context, a *store.Account, refresh string) (*refreshResult, error) {
	cfg, spec, err := m.config(a.Provider, a.OAuthTenant)
	if err != nil {
		return nil, err
	}
	m.Refreshes.Add(1)
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {cfg.ClientID},
	}
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	if spec.TenantTemplate { // Microsoft
		form.Set("scope", strings.Join(spec.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth refresh: token endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		ExpiresIn    json.Number `json:"expires_in"`
		Scope        string      `json:"scope"`
		Error        string      `json:"error"`
	}
	_ = json.Unmarshal(body, &r)
	if resp.StatusCode != http.StatusOK || r.AccessToken == "" {
		switch r.Error {
		case "invalid_grant", "unauthorized_client", "invalid_client":
			return nil, &grantError{r.Error}
		}
		return nil, fmt.Errorf("oauth refresh failed: HTTP %d %s", resp.StatusCode, r.Error)
	}
	secs, _ := r.ExpiresIn.Int64()
	if secs <= 0 {
		secs = 3600
	}
	return &refreshResult{access: r.AccessToken, refresh: r.RefreshToken, scope: r.Scope, expiry: m.now().Add(time.Duration(secs) * time.Second)}, nil
}

// Revoke revokes the refresh token at the provider when supported
// (Google) and deletes the local tokens in every case.
func (m *Manager) Revoke(ctx context.Context, a *store.Account) error {
	var revokeErr error
	if spec, ok := m.specs[a.Provider]; ok && spec.RevokeURL != "" && a.OAuthRefreshEnc != "" {
		if refresh, err := m.kr.Decrypt(a.OAuthRefreshEnc, refreshCtx(a.ID)); err == nil && refresh != "" {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.RevokeURL, strings.NewReader(url.Values{"token": {refresh}}.Encode()))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				resp, err := m.http.Do(req)
				if err != nil {
					revokeErr = fmt.Errorf("revocation endpoint unreachable: %w", err)
				} else {
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
						revokeErr = fmt.Errorf("revocation failed: HTTP %d", resp.StatusCode)
					}
				}
			}
		}
	}
	if err := m.db.UpdateAccountTokens(ctx, a.ID, "", "", 0, a.OAuthScope); err != nil {
		return err
	}
	return revokeErr
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type atomicCounter struct {
	mu sync.Mutex
	n  int64
}

func (c *atomicCounter) Add(d int64) {
	c.mu.Lock()
	c.n += d
	c.mu.Unlock()
}

func (c *atomicCounter) Load() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
