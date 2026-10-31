package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug, "json")
	log.Info("login attempt",
		"username", "alice",
		"password", "hunter2-pass",
		"refresh_token", "1//refresh-SECRET",
		"err", errors.New("token endpoint said: access_token=ya29.SECRETVALUE&x=1"),
		"detail", "Authorization: Bearer eyJSECRET.jwt",
		"secret", Secret("plain-secret"),
	)
	log.Info(fmt.Sprintf("auth=Bearer %s", "xoauthSECRET"))
	out := buf.String()
	for _, leaked := range []string{"hunter2-pass", "1//refresh-SECRET", "ya29.SECRETVALUE", "eyJSECRET", "plain-secret", "xoauthSECRET"} {
		if strings.Contains(out, leaked) {
			t.Errorf("secret %q leaked in logs: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("non-sensitive value missing: %s", out)
	}
}

func TestSecretType(t *testing.T) {
	s := Secret("x")
	if fmt.Sprint(s) != redacted || fmt.Sprintf("%#v", s) != redacted || s.Reveal() != "x" {
		t.Fatal("Secret formatting leaks value")
	}
}
