// Package accounts manages mail accounts: secret handling, credential
// resolution (password or OAuth2 token) and connection helpers.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"

	"lightmail/internal/crypto"
	"lightmail/internal/imapx"
	"lightmail/internal/mailauth"
	"lightmail/internal/metrics"
	"lightmail/internal/netx"
	"lightmail/internal/oauth"
	"lightmail/internal/providers"
	"lightmail/internal/smtpx"
	"lightmail/internal/store"
)

type Service struct {
	DB      *store.DB
	KR      *crypto.Keyring
	OAuth   *oauth.Manager
	Policy  netx.Policy
	Metrics *metrics.Registry
}

func passwordCtx(id int64) string { return "account:" + strconv.FormatInt(id, 10) + ":password" }

// EncryptPassword encrypts an account password bound to the account ID.
func (s *Service) EncryptPassword(accountID int64, pw string) (string, error) {
	return s.KR.Encrypt(pw, passwordCtx(accountID))
}

// Credentials resolves the secret needed to log in.
func (s *Service) Credentials(ctx context.Context, a *store.Account) (mailauth.Credentials, error) {
	cr := mailauth.Credentials{Username: a.Username}
	if a.AuthType == providers.AuthOAuth2 {
		tok, err := s.OAuth.AccessToken(ctx, a.ID)
		if err != nil {
			return cr, err
		}
		cr.AccessToken = tok
		return cr, nil
	}
	pw, err := s.KR.Decrypt(a.PasswordEnc, passwordCtx(a.ID))
	if err != nil {
		return cr, fmt.Errorf("cannot decrypt account password: %w", err)
	}
	cr.Password = pw
	return cr, nil
}

func IMAPEndpoint(a *store.Account) mailauth.Endpoint {
	return mailauth.Endpoint{Host: a.IMAPHost, Port: a.IMAPPort, Security: a.IMAPSecurity, CAPEM: a.TLSCAPEM}
}

func SMTPEndpoint(a *store.Account) mailauth.Endpoint {
	return mailauth.Endpoint{Host: a.SMTPHost, Port: a.SMTPPort, Security: a.SMTPSecurity, CAPEM: a.TLSCAPEM}
}

// DialIMAP opens an authenticated IMAP connection. For OAuth2 accounts a
// rejected token is refreshed once before reporting an authentication error.
func (s *Service) DialIMAP(ctx context.Context, a *store.Account, h *imapclient.UnilateralDataHandler) (*imapx.Client, error) {
	for attempt := 0; ; attempt++ {
		cr, err := s.Credentials(ctx, a)
		if err != nil {
			return nil, err
		}
		c, err := imapx.Dial(ctx, imapx.DialOptions{Policy: s.Policy, Endpoint: IMAPEndpoint(a), Creds: cr, Handler: h})
		if err == nil {
			return c, nil
		}
		if s.Metrics != nil {
			s.Metrics.IMAPErrors.Inc()
		}
		if mailauth.IsAuthError(err) && cr.IsOAuth() && attempt == 0 {
			_ = s.OAuth.Invalidate(ctx, a.ID)
			continue
		}
		if mailauth.IsAuthError(err) && cr.IsOAuth() {
			return nil, &mailauth.AuthError{Protocol: "IMAP", Message: err.(*mailauth.AuthError).Message + " — the token is valid but IMAP access was refused: check that IMAP is enabled for this mailbox and allowed by the organisation policy"}
		}
		return nil, err
	}
}

// DialSMTP opens an authenticated SMTP submission connection.
func (s *Service) DialSMTP(ctx context.Context, a *store.Account) (*smtpx.Client, error) {
	for attempt := 0; ; attempt++ {
		cr, err := s.Credentials(ctx, a)
		if err != nil {
			return nil, err
		}
		c, err := smtpx.Dial(ctx, smtpx.Options{Policy: s.Policy, Endpoint: SMTPEndpoint(a), Creds: cr})
		if err == nil {
			return c, nil
		}
		if s.Metrics != nil {
			s.Metrics.SMTPErrors.Inc()
		}
		if mailauth.IsAuthError(err) && cr.IsOAuth() && attempt == 0 {
			_ = s.OAuth.Invalidate(ctx, a.ID)
			continue
		}
		if mailauth.IsAuthError(err) && cr.IsOAuth() && a.Provider == "microsoft" {
			return nil, &mailauth.AuthError{Protocol: "SMTP", Message: err.(*mailauth.AuthError).Message + " — SMTP AUTH may be disabled for this mailbox or tenant (Exchange Online setting SmtpClientAuthenticationDisabled)"}
		}
		return nil, err
	}
}

// TestResult reports the outcome of a connection test.
type TestResult struct {
	IMAPOK    bool   `json:"imapOk"`
	IMAPError string `json:"imapError,omitempty"`
	SMTPOK    bool   `json:"smtpOk"`
	SMTPError string `json:"smtpError,omitempty"`
}

func (r TestResult) OK() bool { return r.IMAPOK && r.SMTPOK }

// Test checks IMAP login and SMTP authentication with explicit credentials.
func (s *Service) Test(ctx context.Context, a *store.Account, cr mailauth.Credentials) TestResult {
	var r TestResult
	c, err := imapx.Dial(ctx, imapx.DialOptions{Policy: s.Policy, Endpoint: IMAPEndpoint(a), Creds: cr})
	if err != nil {
		r.IMAPError = Explain(err)
	} else {
		r.IMAPOK = true
		c.Logout()
	}
	sc, err := smtpx.Dial(ctx, smtpx.Options{Policy: s.Policy, Endpoint: SMTPEndpoint(a), Creds: cr})
	if err != nil {
		r.SMTPError = Explain(err)
	} else {
		r.SMTPOK = true
		sc.Quit()
	}
	return r
}

// Explain turns low-level errors into actionable messages (never secrets).
func Explain(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case mailauth.IsAuthError(err):
		return err.Error()
	case errors.Is(err, oauth.ErrReauth):
		return "authorisation expired or revoked: reconnect the account"
	case errors.Is(err, netx.ErrForbiddenDestination):
		return "connection to this address is blocked by the server policy"
	case netx.IsCertificateError(err):
		return "the server TLS certificate is not trusted (" + trimErr(err) + ")"
	case netx.IsTimeout(err):
		return "connection timed out"
	}
	msg := trimErr(err)
	if strings.Contains(msg, "no such host") {
		return "unknown host name"
	}
	if strings.Contains(msg, "connection refused") {
		return "connection refused by the server (wrong port?)"
	}
	return msg
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// Validate checks user-provided server settings.
func Validate(a *store.Account) error {
	for _, ep := range []struct {
		name, host string
		port       int
		sec        string
	}{{"IMAP", a.IMAPHost, a.IMAPPort, a.IMAPSecurity}, {"SMTP", a.SMTPHost, a.SMTPPort, a.SMTPSecurity}} {
		if ep.host == "" || len(ep.host) > 253 || strings.ContainsAny(ep.host, " /\\@?#\t\r\n") {
			return fmt.Errorf("%s host is invalid", ep.name)
		}
		if ep.port <= 0 || ep.port > 65535 {
			return fmt.Errorf("%s port is invalid", ep.name)
		}
		if ep.sec != providers.SecurityTLS && ep.sec != providers.SecuritySTARTTLS {
			return fmt.Errorf("%s security must be tls or starttls", ep.name)
		}
	}
	if !strings.Contains(a.Email, "@") || len(a.Email) > 320 || strings.ContainsAny(a.Email, " \r\n<>") {
		return errors.New("email address is invalid")
	}
	if a.Username == "" || len(a.Username) > 320 {
		return errors.New("username is required")
	}
	if len(a.SignatureHTML) > 20000 {
		return errors.New("signature is too long")
	}
	if len(a.TLSCAPEM) > 20000 {
		return errors.New("CA certificate is too long")
	}
	return nil
}
