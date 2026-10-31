package netx

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCheckAddr(t *testing.T) {
	open := Policy{}
	strict := Policy{BlockPrivate: true}
	cases := []struct {
		ip             string
		open, strictOK bool
	}{
		{"169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"0.0.0.0", false, false},
		{"127.0.0.1", true, false},
		{"10.1.2.3", true, false},
		{"192.168.1.1", true, false},
		{"8.8.8.8", true, true},
		{"::ffff:169.254.169.254", false, false},
	}
	for _, c := range cases {
		ip := netip.MustParseAddr(c.ip)
		if got := open.CheckAddr(ip) == nil; got != c.open {
			t.Errorf("open %s: got %v", c.ip, got)
		}
		if got := strict.CheckAddr(ip) == nil; got != c.strictOK {
			t.Errorf("strict %s: got %v", c.ip, got)
		}
	}
}

func TestDialBlocksAtConnectTime(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	_, err = Policy{BlockPrivate: true}.Dial(context.Background(), "localhost", port)
	if !errors.Is(err, ErrForbiddenDestination) {
		t.Fatalf("expected forbidden, got %v", err)
	}
	c, err := Policy{}.Dial(context.Background(), "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// A self-signed server certificate must be rejected unless its CA is
// explicitly trusted for the account.
func TestTLSVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	port, _ := strconv.Atoi(portStr)
	p := Policy{}

	try := func(caPEM string) error {
		cfg, err := p.TLSConfig("example.com", caPEM)
		if err != nil {
			return err
		}
		raw, err := p.Dial(context.Background(), host, port)
		if err != nil {
			return err
		}
		defer raw.Close()
		return tls.Client(raw, cfg).Handshake()
	}
	err := try("")
	if err == nil || !IsCertificateError(err) {
		t.Fatalf("self-signed certificate accepted: %v", err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	if err := try(ca); err != nil {
		t.Fatalf("trusted CA rejected: %v", err)
	}
	if _, err := p.TLSConfig("x", "garbage"); err == nil {
		t.Fatal("invalid PEM accepted")
	}
}

// A read timeout must not be re-armed by later Read calls.
func TestDeadlineConnTimeoutIsFatal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			defer c.Close()
			time.Sleep(3 * time.Second) // silent server
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDeadlineConn(raw, 200*time.Millisecond)
	start := time.Now()
	buf := make([]byte, 1)
	for i := 0; i < 4; i++ {
		if _, err := dc.Read(buf); !IsTimeout(err) {
			t.Fatalf("read %d: expected timeout, got %v", i, err)
		}
	}
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Fatalf("timeout re-armed: 4 reads took %s", d)
	}
}
