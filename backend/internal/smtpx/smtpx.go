// Package smtpx is the SMTP submission adapter.
//
// Send classifies failures precisely because retrying blindly can create
// duplicates:
//   - a permanent SMTP reply (5xx) fails the message;
//   - a temporary reply (4xx) or a network error before the end of DATA
//     is retryable: the server must discard an incomplete transaction;
//   - a network error after the final "." was written (no reply read) is
//     ambiguous: the server may have accepted the message.
package smtpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"lightmail/internal/mailauth"
	"lightmail/internal/netx"
)

type Outcome int

const (
	Sent Outcome = iota
	Permanent
	Temporary
	Ambiguous
)

func (o Outcome) String() string {
	return [...]string{"sent", "permanent", "temporary", "ambiguous"}[o]
}

// SendError describes a failed submission.
type SendError struct {
	Outcome Outcome
	Stage   string
	Err     error
}

func (e *SendError) Error() string { return fmt.Sprintf("smtp %s (%s): %v", e.Stage, e.Outcome, e.Err) }
func (e *SendError) Unwrap() error { return e.Err }

type Options struct {
	Policy   netx.Policy
	Endpoint mailauth.Endpoint
	Creds    mailauth.Credentials
	// LocalName is sent in EHLO; defaults to "localhost" to avoid leaking
	// the container hostname.
	LocalName string
}

type Client struct {
	c *smtp.Client
}

// Dial connects with implicit TLS or mandatory STARTTLS and authenticates.
func Dial(ctx context.Context, o Options) (*Client, error) {
	ep := o.Endpoint
	if ep.Security != "tls" && ep.Security != "starttls" {
		return nil, fmt.Errorf("smtp: unsupported security mode %q (plaintext is not allowed)", ep.Security)
	}
	tlsCfg, err := o.Policy.TLSConfig(ep.Host, ep.CAPEM)
	if err != nil {
		return nil, err
	}
	raw, err := o.Policy.Dial(ctx, ep.Host, ep.Port)
	if err != nil {
		return nil, &SendError{Temporary, "connect", err}
	}
	timeout := o.Policy.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	dc := netx.NewDeadlineConn(raw, timeout)
	var c *smtp.Client
	if ep.Security == "tls" {
		tc := tls.Client(dc, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, &SendError{tlsOutcome(err), "tls", err}
		}
		c = smtp.NewClient(tc)
	} else {
		c, err = smtp.NewClientStartTLS(dc, tlsCfg)
		if err != nil {
			raw.Close()
			return nil, &SendError{tlsOutcome(err), "starttls", err}
		}
	}
	c.CommandTimeout = timeout
	c.SubmissionTimeout = 5 * time.Minute
	name := o.LocalName
	if name == "" {
		name = "localhost"
	}
	if err := c.Hello(name); err != nil {
		c.Close()
		return nil, classify("ehlo", err)
	}
	var auth sasl.Client
	if o.Creds.IsOAuth() {
		auth = mailauth.NewXOAuth2(o.Creds.Username, o.Creds.AccessToken)
	} else if c.SupportsAuth("PLAIN") {
		auth = sasl.NewPlainClient("", o.Creds.Username, o.Creds.Password)
	} else if c.SupportsAuth("LOGIN") {
		auth = sasl.NewLoginClient(o.Creds.Username, o.Creds.Password)
	} else {
		c.Close()
		return nil, &SendError{Permanent, "auth", errors.New("server offers no supported authentication mechanism (PLAIN, LOGIN, XOAUTH2)")}
	}
	if err := c.Auth(auth); err != nil {
		c.Close()
		var se *smtp.SMTPError
		if errors.As(err, &se) && (se.Code == 535 || se.Code == 534 || se.Code == 530 || se.Code == 454 && strings.Contains(strings.ToLower(se.Message), "auth")) {
			return nil, &mailauth.AuthError{Protocol: "SMTP", Message: fmt.Sprintf("%d %s", se.Code, se.Message)}
		}
		return nil, classify("auth", err)
	}
	return &Client{c: c}, nil
}

func tlsOutcome(err error) Outcome {
	if netx.IsCertificateError(err) {
		return Permanent
	}
	return Temporary
}

// Send submits raw to rcpts. The client must not be reused after an error.
func (cl *Client) Send(from string, rcpts []string, raw io.Reader) error {
	c := cl.c
	if err := c.Mail(from, nil); err != nil {
		return classify("mail from", err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r, nil); err != nil {
			return classify("rcpt to "+r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return classify("data", err)
	}
	if _, err := io.Copy(w, raw); err != nil {
		// The final "." was not sent: the transaction is incomplete and the
		// server must not deliver it.
		return &SendError{Temporary, "data body", err}
	}
	if _, err := w.CloseWithResponse(); err != nil {
		var se *smtp.SMTPError
		if errors.As(err, &se) {
			return classify("end of data", err)
		}
		return &SendError{Ambiguous, "end of data", err}
	}
	return nil
}

// Quit ends the session; errors after a successful DATA are irrelevant.
func (cl *Client) Quit() {
	_ = cl.c.Quit()
	_ = cl.c.Close()
}

func (cl *Client) Close() { _ = cl.c.Close() }

// MaxSize returns the SIZE limit advertised by the server (0 if none).
func (cl *Client) MaxSize() int {
	n, ok := cl.c.MaxMessageSize()
	if !ok {
		return 0
	}
	return n
}

func classify(stage string, err error) error {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		if se.Code >= 500 {
			return &SendError{Permanent, stage, err}
		}
		return &SendError{Temporary, stage, err}
	}
	return &SendError{Temporary, stage, err}
}

// OutcomeOf returns the outcome category of an error returned by Dial/Send.
func OutcomeOf(err error) Outcome {
	if err == nil {
		return Sent
	}
	var se *SendError
	if errors.As(err, &se) {
		return se.Outcome
	}
	if mailauth.IsAuthError(err) {
		return Permanent
	}
	return Temporary
}
