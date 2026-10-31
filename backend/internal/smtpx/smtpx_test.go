package smtpx

import (
	"context"
	"strings"
	"testing"
	"time"

	"lightmail/internal/mailauth"
	"lightmail/internal/netx"
	"lightmail/internal/testmail"
)

func setup(t *testing.T) (*testmail.SMTPServer, Options) {
	t.Helper()
	cert, err := testmail.NewCert()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := testmail.StartSMTP(cert, map[string]string{"alice@test": "pw-alice"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv, Options{
		Policy:   netx.Policy{Timeout: 5 * time.Second},
		Endpoint: mailauth.Endpoint{Host: srv.Host, Port: srv.Port, Security: "tls", CAPEM: cert.CAPEM},
		Creds:    mailauth.Credentials{Username: "alice@test", Password: "pw-alice"},
	}
}

func send(t *testing.T, o Options, rcpts ...string) error {
	t.Helper()
	c, err := Dial(context.Background(), o)
	if err != nil {
		return err
	}
	defer c.Close()
	err = c.Send("alice@test", rcpts, strings.NewReader("Subject: hi\r\n\r\nbody\r\n"))
	if err == nil {
		c.Quit()
	}
	return err
}

func TestSendOK(t *testing.T) {
	srv, o := setup(t)
	if err := send(t, o, "bob@test", "hidden@test"); err != nil {
		t.Fatal(err)
	}
	got := srv.Received()
	if len(got) != 1 || len(got[0].Rcpts) != 2 || got[0].User != "alice@test" {
		t.Fatalf("received: %+v", got)
	}
}

func TestAuthFailure(t *testing.T) {
	_, o := setup(t)
	o.Creds.Password = "bad"
	err := send(t, o, "bob@test")
	if !mailauth.IsAuthError(err) || OutcomeOf(err) != Permanent {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestUntrustedCertificateIsPermanent(t *testing.T) {
	_, o := setup(t)
	o.Endpoint.CAPEM = ""
	err := send(t, o, "bob@test")
	if OutcomeOf(err) != Permanent || !netx.IsCertificateError(err) {
		t.Fatalf("got %v", err)
	}
}

func TestOutcomes(t *testing.T) {
	srv, o := setup(t)
	cases := []struct {
		mode testmail.SMTPMode
		want Outcome
	}{
		{testmail.SMTPTempFailRcpt, Temporary},
		{testmail.SMTPPermFailRcpt, Permanent},
		{testmail.SMTPDropAfterData, Ambiguous},
	}
	for _, c := range cases {
		srv.Mode.Store(int32(c.mode))
		err := send(t, o, "bob@test")
		if got := OutcomeOf(err); got != c.want {
			t.Errorf("mode %d: outcome %v (%v), want %v", c.mode, got, err, c.want)
		}
	}
}

func TestXOAuth2(t *testing.T) {
	srv, o := setup(t)
	srv.AddToken("tok", "alice@test")
	o.Creds = mailauth.Credentials{Username: "alice@test", AccessToken: "tok"}
	if err := send(t, o, "bob@test"); err != nil {
		t.Fatal(err)
	}
	o.Creds.AccessToken = "expired"
	if err := send(t, o, "bob@test"); !mailauth.IsAuthError(err) {
		t.Fatalf("expected auth error, got %v", err)
	}
}
