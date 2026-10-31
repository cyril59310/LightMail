// Package mailauth defines credentials used by the IMAP and SMTP adapters
// and the SASL mechanisms they need.
package mailauth

import (
	"errors"
	"fmt"

	"github.com/emersion/go-sasl"
)

// Credentials authenticate against a mail server: either a password or an
// OAuth2 access token (XOAUTH2).
type Credentials struct {
	Username    string
	Password    string
	AccessToken string
}

func (c Credentials) IsOAuth() bool { return c.AccessToken != "" }

// Endpoint is a mail server address.
type Endpoint struct {
	Host     string
	Port     int
	Security string // tls | starttls
	CAPEM    string // optional extra trusted CA
}

func (e Endpoint) String() string { return fmt.Sprintf("%s:%d (%s)", e.Host, e.Port, e.Security) }

// AuthError means the server rejected the credentials. It must not be
// retried automatically (risk of account lockout at the provider).
type AuthError struct {
	Protocol string
	Message  string
}

func (e *AuthError) Error() string {
	return e.Protocol + " authentication failed: " + e.Message
}

func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// xoauth2 implements the XOAUTH2 SASL mechanism used by Google and Microsoft.
// https://developers.google.com/gmail/imap/xoauth2-protocol
type xoauth2 struct{ user, token string }

func NewXOAuth2(user, token string) sasl.Client { return &xoauth2{user, token} }

func (a *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + a.user + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

// Next is called with the JSON error document when the token is refused;
// an empty response makes the server send the final failure.
func (a *xoauth2) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}
