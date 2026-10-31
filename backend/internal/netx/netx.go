// Package netx provides hardened network primitives shared by the IMAP and
// SMTP adapters: TLS configuration with certificate verification, dial-time
// destination filtering and per-operation I/O deadlines.
package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

var ErrForbiddenDestination = errors.New("connection to this network address is not allowed")

// Policy controls outgoing connections to mail servers.
type Policy struct {
	// BlockPrivate refuses loopback and RFC 1918/4193 destinations. Link-local
	// and cloud metadata addresses are always refused.
	BlockPrivate bool
	// InsecureSkipVerify disables certificate verification (development only;
	// configuration refuses it when the public URL is https).
	InsecureSkipVerify bool
	Timeout            time.Duration
}

var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("fd00:ec2::254"),
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
}

// CheckAddr validates a resolved destination.
func (p Policy) CheckAddr(ip netip.Addr) error {
	ip = ip.Unmap()
	for _, m := range metadataAddrs {
		if ip == m {
			return ErrForbiddenDestination
		}
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return ErrForbiddenDestination
	}
	if p.BlockPrivate && (ip.IsLoopback() || ip.IsPrivate()) {
		return ErrForbiddenDestination
	}
	return nil
}

// Dial connects to host:port, checking the actual IP right before connect
// (resistant to DNS rebinding).
func (p Policy) Dial(ctx context.Context, host string, port int) (net.Conn, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	d := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 60 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			return p.CheckAddr(ap.Addr())
		},
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, ErrForbiddenDestination) {
			return nil, ErrForbiddenDestination
		}
		return nil, err
	}
	return conn, nil
}

// TLSConfig returns a client TLS configuration for host. caPEM optionally
// adds trusted certificates (e.g. Proton Mail Bridge self-signed cert) on
// top of the system roots.
func (p Policy) TLSConfig(host, caPEM string) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if caPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, fmt.Errorf("invalid CA certificate (PEM expected)")
		}
		cfg.RootCAs = pool
	}
	if p.InsecureSkipVerify {
		cfg.InsecureSkipVerify = true
	}
	return cfg, nil
}

// DeadlineConn applies a rolling deadline before every Read and Write so a
// stalled server cannot block a goroutine forever.
//
// A timeout is fatal for the connection: crypto/tls reports timeouts as
// temporary errors and parsers built on bufio call Read again, which would
// re-arm the deadline and multiply the effective timeout (observed: 4x).
type DeadlineConn struct {
	net.Conn
	timeout atomic.Int64
	failed  atomic.Pointer[error]
}

func NewDeadlineConn(c net.Conn, timeout time.Duration) *DeadlineConn {
	dc := &DeadlineConn{Conn: c}
	dc.timeout.Store(int64(timeout))
	return dc
}

// SetTimeout changes the rolling timeout (e.g. longer while in IMAP IDLE).
func (c *DeadlineConn) SetTimeout(d time.Duration) { c.timeout.Store(int64(d)) }

func (c *DeadlineConn) Read(b []byte) (int, error) {
	if e := c.failed.Load(); e != nil {
		return 0, *e
	}
	if t := time.Duration(c.timeout.Load()); t > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(t))
	}
	n, err := c.Conn.Read(b)
	c.check(err)
	return n, err
}

func (c *DeadlineConn) Write(b []byte) (int, error) {
	if e := c.failed.Load(); e != nil {
		return 0, *e
	}
	if t := time.Duration(c.timeout.Load()); t > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(t))
	}
	n, err := c.Conn.Write(b)
	c.check(err)
	return n, err
}

func (c *DeadlineConn) check(err error) {
	if err != nil && IsTimeout(err) {
		c.failed.CompareAndSwap(nil, &err)
		_ = c.Conn.Close()
	}
}

// IsTimeout reports network timeouts.
func IsTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// CertificateError reports whether err is a TLS certificate validation error.
func IsCertificateError(err error) bool {
	var ce *tls.CertificateVerificationError
	var ue x509.UnknownAuthorityError
	var he x509.HostnameError
	var ie x509.CertificateInvalidError
	return errors.As(err, &ce) || errors.As(err, &ue) || errors.As(err, &he) || errors.As(err, &ie)
}
