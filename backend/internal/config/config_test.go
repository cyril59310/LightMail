package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadRequiresMasterKey(t *testing.T) {
	_, err := load(envMap(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "MASTER_KEY") {
		t.Fatalf("expected master key error, got %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(envMap(map[string]string{"LIGHTMAIL_MASTER_KEY": testKey}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.SyncInterval != 5*time.Minute || c.CookieSecure {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if got := c.OAuthRedirectURL("google"); got != "http://localhost:8080/api/v1/oauth/google/callback" {
		t.Fatalf("redirect url = %s", got)
	}
}

func TestSecretFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := load(envMap(map[string]string{
		"LIGHTMAIL_MASTER_KEY_FILE": p,
		"LIGHTMAIL_PUBLIC_URL":      "https://mail.example.com/",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MasterKey) != 32 || !c.CookieSecure || c.PublicURL.String() != "https://mail.example.com" {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestInsecureTLSRefusedInProduction(t *testing.T) {
	_, err := load(envMap(map[string]string{
		"LIGHTMAIL_MASTER_KEY":             testKey,
		"LIGHTMAIL_PUBLIC_URL":             "https://mail.example.com",
		"LIGHTMAIL_DEV_ALLOW_INSECURE_TLS": "true",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRejectsBadValues(t *testing.T) {
	for _, m := range []map[string]string{
		{"LIGHTMAIL_MASTER_KEY": "short"},
		{"LIGHTMAIL_MASTER_KEY": testKey, "LIGHTMAIL_PUBLIC_URL": "ftp://x"},
		{"LIGHTMAIL_MASTER_KEY": testKey, "LIGHTMAIL_PUBLIC_URL": "https://x/sub"},
		{"LIGHTMAIL_MASTER_KEY": testKey, "LIGHTMAIL_SYNC_INTERVAL": "soon"},
		{"LIGHTMAIL_MASTER_KEY": testKey, "LIGHTMAIL_DATABASE_URL": "postgres://x"},
	} {
		if _, err := load(envMap(m)); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}
