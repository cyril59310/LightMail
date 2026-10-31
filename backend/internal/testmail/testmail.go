// Package testmail provides real IMAP and SMTP servers (in-memory backends,
// TLS with a generated CA) for integration tests and local benchmarks. It
// is never used by the production server.
package testmail

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Cert is a self-signed CA + leaf certificate for 127.0.0.1/localhost.
type Cert struct {
	Server *tls.Config
	CAPEM  string
}

// NewCert generates a CA and a leaf certificate valid for localhost,
// 127.0.0.1 and the extra host names given.
func NewCert(hosts ...string) (*Cert, error) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "LightMail Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		DNSNames: append([]string{"localhost"}, hosts...), IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return &Cert{
		Server: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12},
		CAPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
	}, nil
}

// ---- IMAP -------------------------------------------------------------

type IMAPServer struct {
	Host     string
	Port     int
	Mem      *imapmemserver.Server
	Users    map[string]*imapmemserver.User
	tokens   sync.Map // access token -> [2]string{user, password}
	srv      *imapserver.Server
	ln       net.Listener
	Conns    atomic.Int64 // currently open connections
	Total    atomic.Int64 // connections accepted
	Down     atomic.Bool  // refuse new connections
	BadLogin atomic.Bool  // reject every login
}

// StartIMAP starts an IMAPS server (implicit TLS) on 127.0.0.1.
func StartIMAP(cert *Cert, addr string) (*IMAPServer, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	s := &IMAPServer{Mem: imapmemserver.New(), Users: map[string]*imapmemserver.User{}}
	s.srv = imapserver.New(&imapserver.Options{
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &imapSession{Session: s.Mem.NewSession(), srv: s}, nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapMove: {}, imap.CapUIDPlus: {}, imap.CapSpecialUse: {},
			imap.CapListExtended: {}, imap.CapESearch: {}, imap.CapLiteralPlus: {},
		},
		Logger: log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	ta := ln.Addr().(*net.TCPAddr)
	s.Host, s.Port = "127.0.0.1", ta.Port
	go s.srv.Serve(&countingListener{Listener: ln, s: s, tls: cert.Server})
	return s, nil
}

func (s *IMAPServer) Close() { s.srv.Close() }

// AddUser creates a user with standard mailboxes.
func (s *IMAPServer) AddUser(username, password string, mailboxes ...string) *imapmemserver.User {
	u := imapmemserver.NewUser(username, password)
	s.Mem.AddUser(u)
	s.Users[username] = u
	if len(mailboxes) == 0 {
		mailboxes = []string{"INBOX", "Sent", "Drafts", "Trash", "Archive"}
	}
	for _, m := range mailboxes {
		_ = u.Create(m, nil)
	}
	return u
}

// AddToken lets XOAUTH2 authenticate with token as user.
func (s *IMAPServer) AddToken(token, username, password string) {
	s.tokens.Store(token, [2]string{username, password})
}
func (s *IMAPServer) RevokeToken(token string) { s.tokens.Delete(token) }

// Deliver appends a raw message to a mailbox of user.
func (s *IMAPServer) Deliver(username, mailbox string, raw []byte, flags ...imap.Flag) error {
	u := s.Users[username]
	if u == nil {
		return errors.New("unknown user")
	}
	_, err := u.Append(mailbox, bytes.NewReader(raw), &imap.AppendOptions{Flags: flags, Time: time.Now()})
	return err
}

type countingListener struct {
	net.Listener
	s   *IMAPServer
	tls *tls.Config
}

func (l *countingListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.s.Down.Load() {
			c.Close()
			continue
		}
		l.s.Conns.Add(1)
		l.s.Total.Add(1)
		return tls.Server(&countingConn{Conn: c, s: l.s}, l.tls), nil
	}
}

type countingConn struct {
	net.Conn
	s    *IMAPServer
	once sync.Once
}

func (c *countingConn) Close() error {
	c.once.Do(func() { c.s.Conns.Add(-1) })
	return c.Conn.Close()
}

type imapSession struct {
	imapserver.Session
	srv *IMAPServer
}

func (s *imapSession) Login(username, password string) error {
	if s.srv.BadLogin.Load() {
		return imapserver.ErrAuthFailed
	}
	return s.Session.Login(username, password)
}

func (s *imapSession) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	return s.Session.(imapserver.SessionMove).Move(w, numSet, dest)
}

func (s *imapSession) AuthenticateMechanisms() []string { return []string{"PLAIN", "XOAUTH2"} }

func (s *imapSession) Authenticate(mech string) (sasl.Server, error) {
	switch mech {
	case "PLAIN":
		return sasl.NewPlainServer(func(identity, username, password string) error {
			return s.Login(username, password)
		}), nil
	case "XOAUTH2":
		return &xoauth2Server{check: func(user, token string) error {
			v, ok := s.srv.tokens.Load(token)
			if !ok || v.([2]string)[0] != user {
				return imapserver.ErrAuthFailed
			}
			return s.Login(v.([2]string)[0], v.([2]string)[1])
		}}, nil
	}
	return nil, errors.New("unsupported mechanism")
}

type xoauth2Server struct {
	check func(user, token string) error
	step  int
}

func (x *xoauth2Server) Next(resp []byte) ([]byte, bool, error) {
	if x.step > 0 {
		return nil, true, imapserver.ErrAuthFailed
	}
	x.step++
	user, token, err := parseXOAuth2(resp)
	if err != nil {
		return nil, true, err
	}
	if err := x.check(user, token); err != nil {
		return nil, true, err
	}
	return nil, true, nil
}

func parseXOAuth2(b []byte) (user, token string, err error) {
	for _, f := range strings.Split(string(b), "\x01") {
		switch {
		case strings.HasPrefix(f, "user="):
			user = strings.TrimPrefix(f, "user=")
		case strings.HasPrefix(f, "auth=Bearer "):
			token = strings.TrimPrefix(f, "auth=Bearer ")
		}
	}
	if user == "" || token == "" {
		return "", "", errors.New("malformed XOAUTH2 response")
	}
	return user, token, nil
}

// ---- SMTP -------------------------------------------------------------

// SMTPMode injects failures.
type SMTPMode int32

const (
	SMTPOK            SMTPMode = iota
	SMTPTempFailRcpt           // 451 on RCPT
	SMTPPermFailRcpt           // 550 on RCPT
	SMTPDropAfterData          // read the whole message then close the connection
)

type Received struct {
	From  string
	Rcpts []string
	Data  []byte
	User  string
}

type SMTPServer struct {
	Host     string
	Port     int
	srv      *smtp.Server
	mu       sync.Mutex
	Messages []Received
	Mode     atomic.Int32
	users    map[string]string
	tokens   sync.Map
}

// StartSMTP starts a submission server with implicit TLS.
func StartSMTP(cert *Cert, users map[string]string) (*SMTPServer, error) {
	return StartSMTPAddr(cert, users, "127.0.0.1:0")
}

// StartSMTPAddr starts the submission server on a given address.
func StartSMTPAddr(cert *Cert, users map[string]string, addr string) (*SMTPServer, error) {
	s := &SMTPServer{users: users}
	be := smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) { return &smtpSession{srv: s, conn: c}, nil })
	s.srv = smtp.NewServer(be)
	s.srv.Domain = "localhost"
	s.srv.ReadTimeout = 10 * time.Second
	s.srv.WriteTimeout = 10 * time.Second
	s.srv.MaxMessageBytes = 50 << 20
	s.srv.ErrorLog = log.New(io.Discard, "", 0)
	ln, err := tls.Listen("tcp", addr, cert.Server)
	if err != nil {
		return nil, err
	}
	s.Host, s.Port = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
	go s.srv.Serve(ln)
	return s, nil
}

func (s *SMTPServer) Close()                          { s.srv.Close() }
func (s *SMTPServer) AddToken(token, username string) { s.tokens.Store(token, username) }

func (s *SMTPServer) Received() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.Messages...)
}

type smtpSession struct {
	srv   *SMTPServer
	conn  *smtp.Conn
	user  string
	from  string
	rcpts []string
}

func (s *smtpSession) AuthMechanisms() []string { return []string{"PLAIN", "XOAUTH2"} }

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case "PLAIN":
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if p, ok := s.srv.users[username]; !ok || p != password {
				return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "Authentication credentials invalid"}
			}
			s.user = username
			return nil
		}), nil
	case "XOAUTH2":
		return &xoauth2Server{check: func(user, token string) error {
			v, ok := s.srv.tokens.Load(token)
			if !ok || v.(string) != user {
				return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "Authentication unsuccessful"}
			}
			s.user = user
			return nil
		}}, nil
	}
	return nil, smtp.ErrAuthUnsupported
}

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	if s.user == "" {
		return smtp.ErrAuthRequired
	}
	s.from = from
	return nil
}

func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	switch SMTPMode(s.srv.Mode.Load()) {
	case SMTPTempFailRcpt:
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
	case SMTPPermFailRcpt:
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such user"}
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if SMTPMode(s.srv.Mode.Load()) == SMTPDropAfterData {
		// Simulate a connection lost after the server accepted the message.
		s.srv.mu.Lock()
		s.srv.Messages = append(s.srv.Messages, Received{s.from, s.rcpts, b, s.user})
		s.srv.mu.Unlock()
		s.conn.Conn().Close()
		return fmt.Errorf("connection dropped")
	}
	s.srv.mu.Lock()
	s.srv.Messages = append(s.srv.Messages, Received{s.from, s.rcpts, b, s.user})
	s.srv.mu.Unlock()
	return nil
}

func (s *smtpSession) Reset()        { s.from, s.rcpts = "", nil }
func (s *smtpSession) Logout() error { return nil }

// ---- message generator ------------------------------------------------

// Message builds a simple RFC 5322 message.
func Message(from, to, subject, body string, date time.Time, extraHeaders ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%d.%s>\r\n", from, to, subject, date.Format(time.RFC1123Z), date.UnixNano(), strings.ReplaceAll(from, "@", ".")+"@test")
	for _, h := range extraHeaders {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}

// MessageWithAttachment builds a multipart message with one attachment.
func MessageWithAttachment(from, to, subject, body, filename string, content []byte, date time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <att.%d@test>\r\nMIME-Version: 1.0\r\n", from, to, subject, date.Format(time.RFC1123Z), date.UnixNano())
	b.WriteString("Content-Type: multipart/mixed; boundary=\"XYZ\"\r\n\r\n--XYZ\r\nContent-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(body + "\r\n--XYZ\r\n")
	fmt.Fprintf(&b, "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", filename)
	enc := make([]byte, 0, len(content)*2)
	enc = appendBase64Lines(enc, content)
	b.Write(enc)
	b.WriteString("\r\n--XYZ--\r\n")
	return []byte(b.String())
}

func appendBase64Lines(dst, src []byte) []byte {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	line := 0
	for i := 0; i < len(src); i += 3 {
		var v uint32
		n := min(3, len(src)-i)
		for j := 0; j < 3; j++ {
			v <<= 8
			if j < n {
				v |= uint32(src[i+j])
			}
		}
		out := []byte{enc[v>>18&63], enc[v>>12&63], enc[v>>6&63], enc[v&63]}
		if n < 3 {
			out[3] = '='
		}
		if n < 2 {
			out[2] = '='
		}
		dst = append(dst, out...)
		line += 4
		if line >= 76 {
			dst = append(dst, '\r', '\n')
			line = 0
		}
	}
	return dst
}
