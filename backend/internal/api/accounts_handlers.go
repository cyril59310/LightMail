package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lightmail/internal/accounts"
	"lightmail/internal/mailauth"
	"lightmail/internal/mime"
	"lightmail/internal/oauth"
	"lightmail/internal/providers"
	"lightmail/internal/store"
)

type endpointDTO struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
}

type accountDTO struct {
	ID              int64       `json:"id"`
	Provider        string      `json:"provider"`
	Email           string      `json:"email"`
	DisplayName     string      `json:"displayName"`
	SenderName      string      `json:"senderName"`
	Color           string      `json:"color"`
	IMAP            endpointDTO `json:"imap"`
	SMTP            endpointDTO `json:"smtp"`
	AuthType        string      `json:"authType"`
	Username        string      `json:"username"`
	HasPassword     bool        `json:"hasPassword"`
	OAuthConnected  bool        `json:"oauthConnected"`
	OAuthExpiresAt  int64       `json:"oauthExpiresAt,omitempty"`
	OAuthScope      string      `json:"oauthScope,omitempty"`
	HasCustomCA     bool        `json:"hasCustomCa"`
	SignatureHTML   string      `json:"signatureHtml"`
	IsDefault       bool        `json:"isDefault"`
	SyncEnabled     bool        `json:"syncEnabled"`
	SyncIntervalSec int         `json:"syncIntervalSec"`
	IdleEnabled     bool        `json:"idleEnabled"`
	SMTPSavesSent   bool        `json:"smtpSavesSent"`
	Status          string      `json:"status"`
	LastError       string      `json:"lastError,omitempty"`
	LastErrorAt     int64       `json:"lastErrorAt,omitempty"`
	LastSyncAt      int64       `json:"lastSyncAt,omitempty"`
	LastSyncOKAt    int64       `json:"lastSyncOkAt,omitempty"`
	CreatedAt       int64       `json:"createdAt"`
}

// toDTO never exposes passwords or tokens.
func toDTO(a *store.Account) accountDTO {
	return accountDTO{
		ID: a.ID, Provider: a.Provider, Email: a.Email, DisplayName: a.DisplayName, SenderName: a.SenderName, Color: a.Color,
		IMAP: endpointDTO{a.IMAPHost, a.IMAPPort, a.IMAPSecurity}, SMTP: endpointDTO{a.SMTPHost, a.SMTPPort, a.SMTPSecurity},
		AuthType: a.AuthType, Username: a.Username, HasPassword: a.PasswordEnc != "",
		OAuthConnected: a.OAuthRefreshEnc != "", OAuthExpiresAt: a.OAuthExpiry, OAuthScope: a.OAuthScope,
		HasCustomCA: a.TLSCAPEM != "", SignatureHTML: a.SignatureHTML, IsDefault: a.IsDefault, SyncEnabled: a.SyncEnabled,
		SyncIntervalSec: a.SyncIntervalSec, IdleEnabled: a.IdleEnabled, SMTPSavesSent: a.SMTPSavesSent,
		Status: a.Status, LastError: a.LastError, LastErrorAt: a.LastErrorAt, LastSyncAt: a.LastSyncAt, LastSyncOKAt: a.LastSyncOKAt, CreatedAt: a.CreatedAt,
	}
}

type providerDTO struct {
	*providers.Provider
	OAuthConfigured bool `json:"oauthConfigured"`
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	var out []providerDTO
	for _, p := range providers.All() {
		out = append(out, providerDTO{p, s.OAuth.Configured(p.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) detectProvider(w http.ResponseWriter, r *http.Request) {
	p := providers.Detect(r.URL.Query().Get("email"))
	writeJSON(w, http.StatusOK, providerDTO{p, s.OAuth.Configured(p.ID)})
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListAccounts(r.Context(), userFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]accountDTO, 0, len(list))
	for _, a := range list {
		out = append(out, toDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) accountParam(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		badRequest(w, "invalid account id")
		return nil, false
	}
	a, err := s.DB.AccountForUser(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return a, true
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.accountParam(w, r); ok {
		writeJSON(w, http.StatusOK, toDTO(a))
	}
}

type accountInput struct {
	Provider        string       `json:"provider"`
	Email           string       `json:"email"`
	DisplayName     string       `json:"displayName"`
	SenderName      *string      `json:"senderName"`
	Color           *string      `json:"color"`
	Username        string       `json:"username"`
	Password        string       `json:"password"`
	IMAP            *endpointDTO `json:"imap"`
	SMTP            *endpointDTO `json:"smtp"`
	TLSCAPEM        *string      `json:"tlsCaPem"`
	SignatureHTML   *string      `json:"signatureHtml"`
	SyncEnabled     *bool        `json:"syncEnabled"`
	SyncIntervalSec *int         `json:"syncIntervalSec"`
	IdleEnabled     *bool        `json:"idleEnabled"`
	SMTPSavesSent   *bool        `json:"smtpSavesSent"`
	AllowSMTPError  bool         `json:"allowSmtpError"`
}

func (in *accountInput) apply(a *store.Account) {
	if in.DisplayName != "" {
		a.DisplayName = strings.TrimSpace(in.DisplayName)
	}
	if in.SenderName != nil {
		a.SenderName = strings.TrimSpace(*in.SenderName)
	}
	if in.Color != nil && len(*in.Color) <= 16 {
		a.Color = *in.Color
	}
	if in.Username != "" {
		a.Username = strings.TrimSpace(in.Username)
	}
	if in.IMAP != nil {
		a.IMAPHost, a.IMAPPort, a.IMAPSecurity = strings.TrimSpace(in.IMAP.Host), in.IMAP.Port, in.IMAP.Security
	}
	if in.SMTP != nil {
		a.SMTPHost, a.SMTPPort, a.SMTPSecurity = strings.TrimSpace(in.SMTP.Host), in.SMTP.Port, in.SMTP.Security
	}
	if in.TLSCAPEM != nil {
		a.TLSCAPEM = strings.TrimSpace(*in.TLSCAPEM)
	}
	if in.SignatureHTML != nil {
		a.SignatureHTML = mime.SanitizeHTML(*in.SignatureHTML, mime.SanitizeOptions{AllowRemote: true}).HTML
	}
	if in.SyncEnabled != nil {
		a.SyncEnabled = *in.SyncEnabled
	}
	if in.SyncIntervalSec != nil {
		a.SyncIntervalSec = clamp(*in.SyncIntervalSec, 0, 86400)
		if a.SyncIntervalSec > 0 && a.SyncIntervalSec < 60 {
			a.SyncIntervalSec = 60
		}
	}
	if in.IdleEnabled != nil {
		a.IdleEnabled = *in.IdleEnabled
	}
	if in.SMTPSavesSent != nil {
		a.SMTPSavesSent = *in.SMTPSavesSent
	}
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }

// newPasswordAccount builds an account from input and provider presets.
func newPasswordAccount(userID int64, in *accountInput) *store.Account {
	p := providers.Get(in.Provider)
	if p == nil {
		p = providers.Detect(in.Email)
	}
	a := &store.Account{
		UserID: userID, Provider: p.ID, Email: strings.ToLower(strings.TrimSpace(in.Email)), AuthType: providers.AuthPassword,
		IMAPHost: p.IMAP.Host, IMAPPort: p.IMAP.Port, IMAPSecurity: p.IMAP.Security,
		SMTPHost: p.SMTP.Host, SMTPPort: p.SMTP.Port, SMTPSecurity: p.SMTP.Security,
		Username: strings.TrimSpace(in.Email), SyncEnabled: true, IdleEnabled: true, SMTPSavesSent: p.SMTPSavesSent,
	}
	a.DisplayName = a.Email
	in.apply(a)
	return a
}

func (s *Server) testAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if !readJSON(w, r, &in, 64<<10) {
		return
	}
	a := newPasswordAccount(userFrom(r).ID, &in)
	if err := accounts.Validate(a); err != nil {
		badRequest(w, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res := s.Accounts.Test(ctx, a, mailauth.Credentials{Username: a.Username, Password: in.Password})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if !readJSON(w, r, &in, 64<<10) {
		return
	}
	u := userFrom(r)
	if list, err := s.DB.ListAccounts(r.Context(), u.ID); err == nil && len(list) >= s.Cfg.MaxAccounts {
		writeError(w, http.StatusConflict, "limit", "maximum number of accounts reached")
		return
	}
	a := newPasswordAccount(u.ID, &in)
	if p := providers.Get(a.Provider); p != nil && !p.SupportsAuth(providers.AuthPassword) {
		writeError(w, http.StatusBadRequest, "oauth_required", "this provider requires OAuth2 sign-in")
		return
	}
	if err := accounts.Validate(a); err != nil {
		badRequest(w, err.Error())
		return
	}
	if in.Password == "" {
		badRequest(w, "password is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res := s.Accounts.Test(ctx, a, mailauth.Credentials{Username: a.Username, Password: in.Password})
	if !res.IMAPOK || (!res.SMTPOK && !in.AllowSMTPError) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": apiError{Code: "connection_failed", Message: "connection test failed"}, "test": res})
		return
	}
	if err := s.DB.CreateAccount(r.Context(), a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "exists", "this account already exists")
			return
		}
		s.fail(w, r, err)
		return
	}
	enc, err := s.Accounts.EncryptPassword(a.ID, in.Password)
	if err == nil {
		a.PasswordEnc = enc
		err = s.DB.UpdateAccountSettings(r.Context(), a)
	}
	if err != nil {
		_ = s.DB.DeleteAccount(r.Context(), u.ID, a.ID)
		s.fail(w, r, err)
		return
	}
	if !res.SMTPOK {
		_ = s.DB.SetAccountStatus(r.Context(), a.ID, store.StatusPending, "SMTP: "+res.SMTPError)
	}
	s.Engine.AccountAdded(a.ID)
	s.Log.Info("account added", "account_id", a.ID, "provider", a.Provider)
	a, _ = s.DB.Account(r.Context(), a.ID)
	writeJSON(w, http.StatusCreated, toDTO(a))
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountParam(w, r)
	if !ok {
		return
	}
	var in accountInput
	if !readJSON(w, r, &in, 64<<10) {
		return
	}
	if in.Email != "" && !strings.EqualFold(in.Email, a.Email) {
		badRequest(w, "the email address of an account cannot be changed")
		return
	}
	serverChanged := in.IMAP != nil || in.SMTP != nil || in.Password != "" || in.Username != "" || in.TLSCAPEM != nil
	if a.AuthType == providers.AuthOAuth2 && (in.Password != "" || in.Username != "" && in.Username != a.Username) {
		badRequest(w, "OAuth2 accounts have no password")
		return
	}
	in.apply(a)
	if err := accounts.Validate(a); err != nil {
		badRequest(w, err.Error())
		return
	}
	if in.Password != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		res := s.Accounts.Test(ctx, a, mailauth.Credentials{Username: a.Username, Password: in.Password})
		if !res.IMAPOK || (!res.SMTPOK && !in.AllowSMTPError) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": apiError{Code: "connection_failed", Message: "connection test failed"}, "test": res})
			return
		}
		enc, err := s.Accounts.EncryptPassword(a.ID, in.Password)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		a.PasswordEnc = enc
	}
	if err := s.DB.UpdateAccountSettings(r.Context(), a); err != nil {
		s.fail(w, r, err)
		return
	}
	if serverChanged || in.SyncEnabled != nil || in.IdleEnabled != nil || in.SyncIntervalSec != nil {
		if serverChanged {
			_ = s.DB.SetAccountStatus(r.Context(), a.ID, store.StatusPending, "")
		}
		s.Engine.AccountChanged(a.ID)
	}
	a, _ = s.DB.Account(r.Context(), a.ID)
	writeJSON(w, http.StatusOK, toDTO(a))
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountParam(w, r)
	if !ok {
		return
	}
	s.Engine.AccountRemoved(a.ID)
	if a.AuthType == providers.AuthOAuth2 {
		if err := s.OAuth.Revoke(r.Context(), a); err != nil {
			s.Log.Warn("token revocation failed during account deletion", "account_id", a.ID, "err", err)
		}
	}
	if err := s.DB.DeleteAccount(r.Context(), a.UserID, a.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("account deleted", "account_id", a.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountParam(w, r)
	if !ok {
		return
	}
	s.Engine.Trigger(a.ID, true)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) defaultAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountParam(w, r)
	if !ok {
		return
	}
	if err := s.DB.SetDefaultAccount(r.Context(), a.UserID, a.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reconnectAccount clears the error state and retries; OAuth2 accounts
// without valid tokens get an authorisation URL.
func (s *Server) reconnectAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountParam(w, r)
	if !ok {
		return
	}
	if a.AuthType == providers.AuthOAuth2 && a.OAuthRefreshEnc == "" {
		authURL, err := s.OAuth.Start(r.Context(), a.UserID, sessionFrom(r).ID, a.Provider, a.ID, a.Email)
		if err != nil {
			s.oauthStartError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"authUrl": authURL})
		return
	}
	_ = s.DB.SetAccountStatus(r.Context(), a.ID, store.StatusPending, "")
	s.Engine.AccountChanged(a.ID)
	writeJSON(w, http.StatusOK, map[string]string{})
}

func (s *Server) revokeAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountParam(w, r)
	if !ok {
		return
	}
	if a.AuthType != providers.AuthOAuth2 {
		badRequest(w, "not an OAuth2 account")
		return
	}
	s.Engine.AccountRemoved(a.ID)
	err := s.OAuth.Revoke(r.Context(), a)
	_ = s.DB.SetAccountStatus(r.Context(), a.ID, store.StatusAuthError, "authorisation revoked by the user")
	s.Engine.AccountAdded(a.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"warning": "local tokens deleted, but the provider could not be reached: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{})
}

// ---- OAuth2 -------------------------------------------------------------

func (s *Server) oauthStartError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, oauth.ErrNotConfigured) {
		writeError(w, http.StatusBadRequest, "oauth_not_configured", "OAuth2 is not configured for this provider: set its client ID and secret (see docs/OAUTH.md)")
		return
	}
	s.fail(w, r, err)
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	var req struct {
		AccountID int64  `json:"accountId"`
		LoginHint string `json:"loginHint"`
	}
	if !readJSON(w, r, &req, 4<<10) {
		return
	}
	u := userFrom(r)
	if req.AccountID != 0 {
		a, err := s.DB.AccountForUser(r.Context(), u.ID, req.AccountID)
		if err != nil || a.Provider != provider {
			badRequest(w, "invalid account")
			return
		}
	} else if list, err := s.DB.ListAccounts(r.Context(), u.ID); err == nil && len(list) >= s.Cfg.MaxAccounts {
		writeError(w, http.StatusConflict, "limit", "maximum number of accounts reached")
		return
	}
	authURL, err := s.OAuth.Start(r.Context(), u.ID, sessionFrom(r).ID, provider, req.AccountID, req.LoginHint)
	if err != nil {
		s.oauthStartError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authUrl": authURL})
}

// oauthCallback completes the flow and redirects to the web client. Results
// are passed in the URL fragment so they never reach server logs.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	redirect := func(params url.Values) {
		http.Redirect(w, r, s.Cfg.PublicURL.String()+"/#/oauth-result?"+params.Encode(), http.StatusSeeOther)
	}
	fail := func(code, detail string) {
		s.Log.Warn("oauth callback failed", "provider", provider, "error_code", code)
		redirect(url.Values{"status": {"error"}, "code": {code}, "detail": {detail}})
	}
	id, err := s.OAuth.Callback(r.Context(), sessionFrom(r).ID, provider, r.URL.Query())
	if err != nil {
		var ce *oauth.CallbackError
		if errors.As(err, &ce) {
			fail(ce.Code, ce.Detail)
		} else {
			fail("internal", "")
		}
		return
	}
	u := userFrom(r)
	if id.UserID != u.ID {
		fail("invalid_state", "")
		return
	}
	ctx := r.Context()
	p := providers.Get(provider)

	// Re-authentication of an existing account (explicit, or same mailbox).
	var existing *store.Account
	if id.AccountID != 0 {
		existing, err = s.DB.AccountForUser(ctx, u.ID, id.AccountID)
		if err != nil {
			fail("invalid_state", "account not found")
			return
		}
		if !strings.EqualFold(existing.Email, id.Email) {
			fail("account_mismatch", "signed in as "+id.Email+" instead of "+existing.Email)
			return
		}
	} else if list, err := s.DB.ListAccounts(ctx, u.ID); err == nil {
		for _, a := range list {
			if a.Provider == provider && strings.EqualFold(a.Email, id.Email) {
				existing = a
			}
		}
	}
	if existing != nil {
		if existing.AuthType != providers.AuthOAuth2 {
			fail("exists", "this mailbox is already configured with a password")
			return
		}
		s.Engine.AccountRemoved(existing.ID)
		if err := s.OAuth.SaveTokens(ctx, existing.ID, id.Access, id.Refresh, id.Expiry, id.Scope); err != nil {
			fail("internal", "")
			return
		}
		_ = s.DB.SetAccountStatus(ctx, existing.ID, store.StatusPending, "")
		s.Engine.AccountAdded(existing.ID)
		s.Log.Info("oauth account re-authenticated", "account_id", existing.ID, "provider", provider)
		redirect(url.Values{"status": {"ok"}, "account": {itoa(existing.ID)}, "reauth": {"1"}})
		return
	}

	a := &store.Account{
		UserID: u.ID, Provider: provider, Email: id.Email, DisplayName: id.Email, SenderName: id.Name,
		IMAPHost: p.IMAP.Host, IMAPPort: p.IMAP.Port, IMAPSecurity: p.IMAP.Security,
		SMTPHost: p.SMTP.Host, SMTPPort: p.SMTP.Port, SMTPSecurity: p.SMTP.Security,
		AuthType: providers.AuthOAuth2, Username: id.Email, OAuthTenant: id.Tenant,
		SyncEnabled: true, IdleEnabled: true, SMTPSavesSent: p.SMTPSavesSent,
	}
	if provider == "microsoft" {
		a.SMTPHost = providers.MicrosoftSMTPHost(id.Tenant)
		a.OAuthTenant = s.Cfg.Microsoft.Tenant
	}
	if err := s.DB.CreateAccount(ctx, a); err != nil {
		fail("internal", "")
		return
	}
	if err := s.OAuth.SaveTokens(ctx, a.ID, id.Access, id.Refresh, id.Expiry, id.Scope); err != nil {
		_ = s.DB.DeleteAccount(ctx, u.ID, a.ID)
		fail("internal", "")
		return
	}
	// Verify that IMAP and SMTP accept the token before keeping the account.
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res := s.Accounts.Test(tctx, a, mailauth.Credentials{Username: a.Username, AccessToken: id.Access})
	if !res.IMAPOK {
		_ = s.OAuth.Revoke(ctx, a)
		_ = s.DB.DeleteAccount(ctx, u.ID, a.ID)
		fail("imap_failed", res.IMAPError)
		return
	}
	if !res.SMTPOK {
		_ = s.DB.SetAccountStatus(ctx, a.ID, store.StatusPending, "SMTP: "+res.SMTPError)
	}
	s.Engine.AccountAdded(a.ID)
	s.Log.Info("oauth account added", "account_id", a.ID, "provider", provider)
	params := url.Values{"status": {"ok"}, "account": {itoa(a.ID)}}
	if !res.SMTPOK {
		params.Set("smtpError", res.SMTPError)
	}
	redirect(params)
}
