package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/providers"
	"lightmail/internal/store"
)

// fakeProvider is a minimal OAuth2/OIDC authorization server.
type fakeProvider struct {
	srv          *httptest.Server
	challenges   sync.Map // code -> code_challenge
	refreshCalls atomic.Int64
	revoked      atomic.Int64
	rejectGrant  atomic.Bool
	email        string
	refreshDelay time.Duration
}

func idToken(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func newFake(t *testing.T) *fakeProvider {
	f := &fakeProvider{email: "Alice@Gmail.com"}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			code := r.Form.Get("code")
			ch, ok := f.challenges.LoadAndDelete(code)
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != ch.(string) {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600, "token_type": "Bearer",
				"scope":    "https://mail.google.com/ openid email",
				"id_token": idToken(map[string]any{"iss": "https://accounts.google.com", "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(), "email": f.email, "email_verified": true, "name": "Alice"}),
			})
		case "refresh_token":
			f.refreshCalls.Add(1)
			time.Sleep(f.refreshDelay)
			if f.rejectGrant.Load() || r.Form.Get("client_secret") != "client-secret" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "expires_in": 3600})
		}
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) { f.revoked.Add(1) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// authorize simulates the user consenting at the provider: it parses the
// authorization URL and returns the callback query.
func (f *fakeProvider) authorize(t *testing.T, authURL string) url.Values {
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("PKCE missing: %s", authURL)
	}
	if q.Get("access_type") != "offline" || q.Get("redirect_uri") != "https://mail.example.com/api/v1/oauth/google/callback" {
		t.Fatalf("bad auth params: %v", q)
	}
	code := crypto.RandomToken(8)
	f.challenges.Store(code, q.Get("code_challenge"))
	return url.Values{"code": {code}, "state": {q.Get("state")}}
}

type env struct {
	m    *Manager
	db   *store.DB
	f    *fakeProvider
	user *store.User
}

func setup(t *testing.T) *env {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	u, _ := db.CreateUser(context.Background(), "admin", "x", true)
	kr, _ := crypto.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	cfg := &config.Config{PublicURL: mustURL("https://mail.example.com")}
	m := New(cfg, db, kr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := newFake(t)
	m.SetSpec("google", &providers.OAuthSpec{
		AuthURL: f.srv.URL + "/auth", TokenURL: f.srv.URL + "/token", RevokeURL: f.srv.URL + "/revoke",
		Scopes: []string{"https://mail.google.com/", "openid", "email"}, AuthQuery: map[string]string{"access_type": "offline", "prompt": "consent"},
	}, config.OAuthClient{ClientID: "client-id", ClientSecret: "client-secret"})
	return &env{m: m, db: db, f: f, user: u}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestFullFlowAndRefresh(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	authURL, err := e.m.Start(ctx, e.user.ID, "session-A", "google", 0, "alice@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	q := e.f.authorize(t, authURL)
	id, err := e.m.Callback(ctx, "session-A", "google", q)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "alice@gmail.com" || id.Refresh != "refresh-1" || id.Access != "access-1" {
		t.Fatalf("identity: %+v", id)
	}
	// State is single use (replay).
	if _, err := e.m.Callback(ctx, "session-A", "google", q); err == nil {
		t.Fatal("state replay accepted")
	}

	a := &store.Account{UserID: e.user.ID, Provider: "google", Email: id.Email, IMAPHost: "imap.gmail.com", IMAPPort: 993, IMAPSecurity: "tls", SMTPHost: "smtp.gmail.com", SMTPPort: 465, SMTPSecurity: "tls", AuthType: "oauth2", Username: id.Email}
	if err := e.db.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	// Store an already-expired access token to force a refresh.
	if err := e.m.SaveTokens(ctx, a.ID, id.Access, id.Refresh, time.Now().Add(-time.Minute), id.Scope); err != nil {
		t.Fatal(err)
	}
	stored, _ := e.db.Account(ctx, a.ID)
	if strings.Contains(stored.OAuthRefreshEnc, "refresh-1") || strings.Contains(stored.OAuthAccessEnc, "access-1") {
		t.Fatal("tokens stored in clear")
	}

	// Concurrent callers trigger exactly one refresh.
	e.f.refreshDelay = 100 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := e.m.AccessToken(ctx, a.ID)
			if err != nil || tok != "access-2" {
				t.Errorf("token %q %v", tok, err)
			}
		}()
	}
	wg.Wait()
	if n := e.f.refreshCalls.Load(); n != 1 {
		t.Fatalf("expected 1 refresh, got %d", n)
	}
	// Refresh token kept when the provider does not rotate it.
	tok, _ := e.m.AccessToken(ctx, a.ID)
	if tok != "access-2" || e.f.refreshCalls.Load() != 1 {
		t.Fatal("valid token should be reused")
	}

	// Revoked grant -> re-auth required, tokens wiped, hook called.
	var reauth atomic.Bool
	e.m.OnReauth = func(ctx context.Context, acct *store.Account, reason string) { reauth.Store(true) }
	e.f.rejectGrant.Store(true)
	e.m.Invalidate(ctx, a.ID)
	if _, err := e.m.AccessToken(ctx, a.ID); !errors.Is(err, ErrReauth) {
		t.Fatalf("expected reauth, got %v", err)
	}
	if !reauth.Load() {
		t.Fatal("OnReauth not called")
	}
	stored, _ = e.db.Account(ctx, a.ID)
	if stored.OAuthRefreshEnc != "" {
		t.Fatal("tokens should be wiped")
	}
}

func TestCallbackRejectsOtherSession(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	authURL, _ := e.m.Start(ctx, e.user.ID, "session-A", "google", 0, "")
	q := e.f.authorize(t, authURL)
	_, err := e.m.Callback(ctx, "session-B", "google", q)
	var ce *CallbackError
	if !errors.As(err, &ce) || ce.Code != "invalid_state" {
		t.Fatalf("login CSRF not prevented: %v", err)
	}
}

func TestCallbackErrors(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	cases := map[string]func(q url.Values){
		"access_denied":   func(q url.Values) { q.Set("error", "access_denied"); q.Del("code") },
		"invalid_state":   func(q url.Values) { q.Set("state", "forged") },
		"exchange_failed": func(q url.Values) { q.Set("code", "wrong-code") },
	}
	for want, mutate := range cases {
		authURL, _ := e.m.Start(ctx, e.user.ID, "s", "google", 0, "")
		q := e.f.authorize(t, authURL)
		mutate(q)
		_, err := e.m.Callback(ctx, "s", "google", q)
		var ce *CallbackError
		if !errors.As(err, &ce) || ce.Code != want {
			t.Errorf("%s: got %v", want, err)
		}
	}
	// wrong provider in callback URL
	authURL, _ := e.m.Start(ctx, e.user.ID, "s", "google", 0, "")
	q := e.f.authorize(t, authURL)
	if _, err := e.m.Callback(ctx, "s", "microsoft", q); err == nil {
		t.Error("provider mismatch accepted")
	}
}

func TestClaimsValidation(t *testing.T) {
	e := setup(t)
	now := time.Now().Add(time.Hour).Unix()
	ok := idClaims{"iss": "https://accounts.google.com", "aud": "client-id", "exp": float64(now), "email": "a@b.c"}
	if err := e.m.validateClaims("google", "client-id", ok); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]idClaims{
		"aud":   {"iss": "https://accounts.google.com", "aud": "other", "exp": float64(now)},
		"iss":   {"iss": "https://evil.example", "aud": "client-id", "exp": float64(now)},
		"exp":   {"iss": "https://accounts.google.com", "aud": "client-id", "exp": float64(time.Now().Add(-time.Hour).Unix())},
		"unver": {"iss": "https://accounts.google.com", "aud": "client-id", "exp": float64(now), "email_verified": false},
	} {
		if err := e.m.validateClaims("google", "client-id", c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	ms := idClaims{"iss": "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0", "tid": "9188040d-6c67-4c5b-b112-36a304b66dad", "aud": "client-id", "exp": float64(now), "preferred_username": "Bob@Outlook.com"}
	if err := e.m.validateClaims("microsoft", "client-id", ms); err != nil {
		t.Fatal(err)
	}
	email, _, tid := claimsIdentity("microsoft", ms)
	if email != "bob@outlook.com" || providers.MicrosoftSMTPHost(tid) != "smtp-mail.outlook.com" {
		t.Fatalf("ms identity %s %s", email, tid)
	}
}

func TestNotConfigured(t *testing.T) {
	e := setup(t)
	if e.m.Configured("microsoft") {
		t.Fatal("microsoft should not be configured")
	}
	if _, err := e.m.Start(context.Background(), e.user.ID, "s", "microsoft", 0, ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}
}
