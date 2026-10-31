// Package imapx is the IMAP adapter: secure connection set-up,
// authentication and the higher-level operations used by the sync engine
// and the API.
package imapx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"lightmail/internal/mailauth"
	"lightmail/internal/netx"
)

// Client is an authenticated IMAP connection.
type Client struct {
	*imapclient.Client
	dc       *netx.DeadlineConn
	timeout  time.Duration
	Caps     imap.CapSet
	selected string
	readOnly bool
}

type DialOptions struct {
	Policy   netx.Policy
	Endpoint mailauth.Endpoint
	Creds    mailauth.Credentials
	Handler  *imapclient.UnilateralDataHandler
	Debug    bool
}

var wordDecoder = &mime.WordDecoder{CharsetReader: charset.Reader}

// Dial connects, negotiates TLS (implicit or mandatory STARTTLS) and
// authenticates.
func Dial(ctx context.Context, o DialOptions) (*Client, error) {
	ep := o.Endpoint
	if ep.Security != "tls" && ep.Security != "starttls" {
		return nil, fmt.Errorf("imap: unsupported security mode %q (plaintext is not allowed)", ep.Security)
	}
	tlsCfg, err := o.Policy.TLSConfig(ep.Host, ep.CAPEM)
	if err != nil {
		return nil, err
	}
	raw, err := o.Policy.Dial(ctx, ep.Host, ep.Port)
	if err != nil {
		return nil, fmt.Errorf("imap: connect %s: %w", ep, err)
	}
	timeout := o.Policy.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	dc := netx.NewDeadlineConn(raw, timeout)
	// Abort the handshake if ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()

	opts := &imapclient.Options{TLSConfig: tlsCfg, UnilateralDataHandler: o.Handler, WordDecoder: wordDecoder}
	var c *imapclient.Client
	if ep.Security == "tls" {
		tc := tls.Client(dc, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("imap: TLS handshake with %s: %w", ep, err)
		}
		c = imapclient.New(tc, opts)
	} else {
		c, err = imapclient.NewStartTLS(dc, opts)
		if err != nil {
			raw.Close()
			return nil, fmt.Errorf("imap: STARTTLS with %s: %w", ep, err)
		}
	}
	if err := c.WaitGreeting(); err != nil {
		c.Close()
		return nil, fmt.Errorf("imap: greeting: %w", err)
	}
	cl := &Client{Client: c, dc: dc, timeout: timeout}
	if err := cl.authenticate(o.Creds); err != nil {
		c.Close()
		return nil, err
	}
	caps, err := c.Capability().Wait()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("imap: capability: %w", err)
	}
	cl.Caps = caps
	if caps.Has(imap.CapCondStore) {
		// ENABLE is not required for CONDSTORE but harmless; ignore errors.
		if caps.Has(imap.CapEnable) {
			_, _ = c.Enable(imap.CapCondStore).Wait()
		}
	}
	return cl, nil
}

func (c *Client) authenticate(cr mailauth.Credentials) error {
	var err error
	if cr.IsOAuth() {
		err = c.Authenticate(mailauth.NewXOAuth2(cr.Username, cr.AccessToken))
	} else {
		if c.Client.Caps().Has(imap.CapLoginDisabled) {
			return &mailauth.AuthError{Protocol: "IMAP", Message: "server disabled LOGIN on this connection"}
		}
		err = c.Login(cr.Username, cr.Password).Wait()
	}
	if err != nil {
		var ie *imap.Error
		if errors.As(err, &ie) {
			msg := strings.TrimSpace(ie.Text)
			if msg == "" {
				msg = string(ie.Code)
			}
			return &mailauth.AuthError{Protocol: "IMAP", Message: msg}
		}
		return fmt.Errorf("imap: authenticate: %w", err)
	}
	return nil
}

// SetTimeout adjusts the I/O timeout (IDLE needs a long one).
func (c *Client) SetTimeout(d time.Duration) { c.dc.SetTimeout(d) }

// ResetTimeout restores the default I/O timeout.
func (c *Client) ResetTimeout() { c.dc.SetTimeout(c.timeout) }

// HasCondStore reports CONDSTORE support.
func (c *Client) HasCondStore() bool { return c.Caps.Has(imap.CapCondStore) }

// SelectIfNeeded selects mailbox unless it is already the selected one.
func (c *Client) SelectIfNeeded(name string) error {
	if c.selected == name {
		return nil
	}
	_, err := c.SelectMailbox(name, false)
	return err
}

// SelectMailbox (re)selects a mailbox and returns fresh state.
func (c *Client) SelectMailbox(name string, condstore bool) (*imap.SelectData, error) {
	data, err := c.Select(name, &imap.SelectOptions{CondStore: condstore && c.HasCondStore()}).Wait()
	if err != nil {
		c.selected = ""
		return nil, err
	}
	c.selected = name
	return data, nil
}

// Selected returns the currently selected mailbox name ("" if none).
func (c *Client) Selected() string { return c.selected }

// Logout closes the session politely, with a short timeout.
func (c *Client) Logout() {
	c.dc.SetTimeout(5 * time.Second)
	_ = c.Client.Logout().Wait()
	_ = c.Close()
}

// IsClosed reports whether the connection has terminated.
func (c *Client) IsClosed() bool {
	select {
	case <-c.Closed():
		return true
	default:
		return false
	}
}
