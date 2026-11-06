package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/auth"
	"lightmail/internal/config"
	"lightmail/internal/logging"
	"lightmail/internal/testmail"
)

const (
	adminPassword = "admin-Secret-Pass-42"
	mailPassword  = "mailbox-Secret-PW-99"
)

// syncBuffer is a concurrency-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type e2e struct {
	t    *testing.T
	app  *App
	srv  *httptest.Server
	logs *syncBuffer
	imap *testmail.IMAPServer
	smtp *testmail.SMTPServer
	cert *testmail.Cert
}

func startE2E(t *testing.T) *e2e {
	t.Helper()
	cert, _ := testmail.NewCert()
	is, err := testmail.StartIMAP(cert, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(is.Close)
	ss, err := testmail.StartSMTP(cert, map[string]string{"alice@test": mailPassword})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ss.Close)
	is.AddUser("alice@test", mailPassword)

	srv := httptest.NewUnstartedServer(nil)
	pub, _ := url.Parse("http://" + srv.Listener.Addr().String())
	dir := t.TempDir()
	key, _ := config.ParseKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	cfg := &config.Config{
		AppName: "LightMail", PublicURL: pub, DataDir: dir, DBPath: filepath.Join(dir, "lightmail.db"), MasterKey: key,
		AdminUser: "admin", AdminPassword: adminPassword, SessionIdleTTL: time.Hour, SessionAbsoluteTTL: 24 * time.Hour,
		SyncInterval: time.Hour, SyncMaxConcurrent: 2, IMAPMaxConnsPerAcct: 2, IMAPMaxConnsTotal: 20, SyncChunkSize: 100,
		SyncBackfillBudget: 10 * time.Second, SyncFlagRefreshPeriod: time.Hour, NetTimeout: 10 * time.Second,
		SMTPMaxConcurrent: 2, UndoSendDelay: 0, MaxAttachmentBytes: 1 << 20, MaxUploadBytes: 5 << 20, MaxFetchBytes: 10 << 20,
		CacheMaxBytes: 100 << 20, CacheTTL: 24 * time.Hour, MaxAccounts: 5,
	}
	logs := &syncBuffer{}
	a, err := New(context.Background(), cfg, logging.New(logs, slog.LevelDebug, "json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = a.Handler
	srv.Start()
	t.Cleanup(func() { srv.Close(); cancel(); a.Stop() })
	return &e2e{t: t, app: a, srv: srv, logs: logs, imap: is, smtp: ss, cert: cert}
}

// client is a browser-like HTTP client with cookies and the CSRF token.
type client struct {
	e    *e2e
	http *http.Client
	csrf string
}

func (e *e2e) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) do(method, path string, body any, hdr ...string) (*http.Response, []byte) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	req.Header.Set("Origin", c.e.srv.URL)
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func (c *client) login(user, pass string) *http.Response {
	resp, b := c.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass})
	if resp.StatusCode == 200 {
		var r struct{ CSRFToken string }
		json.Unmarshal(b, &r)
		c.csrf = r.CSRFToken
	}
	return resp
}

func decode[T any](t *testing.T, b []byte) T {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func TestAllPrivateRoutesRequireAuth(t *testing.T) {
	e := startE2E(t)
	c := e.client()
	routes := [][2]string{
		{"GET", "/api/v1/accounts"}, {"POST", "/api/v1/accounts"}, {"POST", "/api/v1/accounts/test"}, {"GET", "/api/v1/accounts/1"},
		{"PATCH", "/api/v1/accounts/1"}, {"DELETE", "/api/v1/accounts/1"}, {"POST", "/api/v1/accounts/1/sync"},
		{"POST", "/api/v1/accounts/1/reconnect"}, {"POST", "/api/v1/accounts/1/revoke"}, {"POST", "/api/v1/accounts/1/default"},
		{"GET", "/api/v1/providers"}, {"GET", "/api/v1/folders"}, {"POST", "/api/v1/folders"}, {"DELETE", "/api/v1/folders/1"},
		{"GET", "/api/v1/messages?view=unified"}, {"GET", "/api/v1/messages/1"}, {"GET", "/api/v1/messages/1/body.html"},
		{"GET", "/api/v1/messages/1/raw"}, {"GET", "/api/v1/messages/1/attachments/1"}, {"POST", "/api/v1/messages/actions"},
		{"POST", "/api/v1/messages/send"}, {"GET", "/api/v1/search?q=x"}, {"POST", "/api/v1/uploads"}, {"GET", "/api/v1/drafts"},
		{"POST", "/api/v1/drafts"}, {"GET", "/api/v1/outbox"}, {"GET", "/api/v1/notifications"}, {"GET", "/api/v1/events"},
		{"GET", "/api/v1/settings"}, {"PUT", "/api/v1/settings"}, {"GET", "/api/v1/status"}, {"GET", "/api/v1/metrics"},
		{"POST", "/api/v1/oauth/google/start"}, {"GET", "/api/v1/oauth/google/callback?code=x&state=y"},
		{"POST", "/api/v1/auth/logout-all"}, {"POST", "/api/v1/auth/password"}, {"GET", "/api/v1/counters"}, {"GET", "/api/v1/contacts?q=ab"},
	}
	for _, r := range routes {
		resp, _ := c.do(r[0], r[1], map[string]string{})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401", r[0], r[1], resp.StatusCode)
		}
	}
	// Public endpoints
	if resp, _ := c.do("GET", "/api/v1/health", nil); resp.StatusCode != 200 {
		t.Error("health should be public")
	}
}

func TestLoginSessionAndCSRF(t *testing.T) {
	e := startE2E(t)
	c := e.client()
	if resp := c.login("admin", "wrong-password"); resp.StatusCode != 401 {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}
	resp := c.login("admin", adminPassword)
	if resp.StatusCode != 200 || c.csrf == "" {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	// Security headers
	for _, path := range []string{"/", "/api/v1/settings"} {
		resp, _ = c.do("GET", path, nil)
		h := resp.Header
		if !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s: missing security headers %v", path, h)
		}
	}
	// CSRF: missing token, wrong token, foreign origin, cross-site fetch
	token := c.csrf
	c.csrf = ""
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "dark"}); resp.StatusCode != 403 {
		t.Fatalf("missing CSRF token accepted: %d", resp.StatusCode)
	}
	c.csrf = "forged"
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "dark"}); resp.StatusCode != 403 {
		t.Fatalf("forged CSRF token accepted: %d", resp.StatusCode)
	}
	c.csrf = token
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "dark"}, "Origin", "https://evil.example"); resp.StatusCode != 403 {
		t.Fatalf("foreign origin accepted: %d", resp.StatusCode)
	}
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "dark"}, "Sec-Fetch-Site", "cross-site"); resp.StatusCode != 403 {
		t.Fatalf("cross-site accepted: %d", resp.StatusCode)
	}
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "dark"}, "Content-Type", "text/plain"); resp.StatusCode != 415 {
		t.Fatalf("non-JSON body accepted: %d", resp.StatusCode)
	}
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "dark"}); resp.StatusCode != 200 {
		t.Fatalf("valid request refused: %d", resp.StatusCode)
	}
	// Strict validation of settings
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"theme": "<script>"}); resp.StatusCode != 400 {
		t.Fatal("invalid setting accepted")
	}
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"unknownKey": "1"}); resp.StatusCode != 400 {
		t.Fatal("unknown setting accepted")
	}
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"accountLayout": `{"ungrouped":[2],"groups":[{"id":"g1","name":"Travail","accounts":[1],"collapsed":true}],"collapsedAccounts":[2]}`}); resp.StatusCode != 200 {
		t.Fatalf("valid account layout refused: %d", resp.StatusCode)
	}
	for _, bad := range []string{`null`, `[]`, `{"groups":[{"id":"g1","name":"  ","accounts":[]}]}`, `{"groups":[{"id":"a b","name":"x"}]}`, `{"groups":[{"id":"g","name":"x"},{"id":"g","name":"y"}]}`, `{"ungrouped":"x"}`, `{"groups":[{"id":"g","name":"x","collapsed":"yes"}]}`, `{"collapsedAccounts":["1"]}`} {
		if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"accountLayout": bad}); resp.StatusCode != 400 {
			t.Fatalf("invalid account layout accepted: %s", bad)
		}
	}
	ep := `"imap":{"host":"imap.x.fr","port":993,"security":"tls"},"smtp":{"host":"smtp.x.fr","port":587,"security":"starttls"}`
	if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"serverPresets": `[{"id":"s1","name":"X",` + ep + `}]`}); resp.StatusCode != 200 {
		t.Fatalf("valid server presets refused: %d", resp.StatusCode)
	}
	for _, bad := range []string{`null`, `{}`, `[{"id":"s1","name":" ",` + ep + `}]`, `[{"id":"s1","name":"X"}]`,
		`[{"id":"s1","name":"X",` + ep + `},{"id":"s1","name":"Y",` + ep + `}]`,
		`[{"id":"s1","name":"X","imap":{"host":"a b","port":993,"security":"tls"},"smtp":{"host":"s","port":465,"security":"tls"}}]`,
		`[{"id":"s1","name":"X","imap":{"host":"i","port":0,"security":"tls"},"smtp":{"host":"s","port":465,"security":"tls"}}]`,
		`[{"id":"s1","name":"X","imap":{"host":"i","port":993,"security":"none"},"smtp":{"host":"s","port":465,"security":"tls"}}]`} {
		if resp, _ := c.do("PUT", "/api/v1/settings", map[string]string{"serverPresets": bad}); resp.StatusCode != 400 {
			t.Fatalf("invalid server presets accepted: %s", bad)
		}
	}
	// Logout invalidates the session server-side.
	c.do("POST", "/api/v1/auth/logout", nil)
	if resp, _ := c.do("GET", "/api/v1/settings", nil); resp.StatusCode != 401 {
		t.Fatal("session still valid after logout")
	}
}

func TestLoginBruteForce(t *testing.T) {
	e := startE2E(t)
	c := e.client()
	var last int
	for i := 0; i < 8; i++ {
		last = c.login("admin", fmt.Sprintf("wrong-%d-password", i)).StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
	if resp := c.login("admin", adminPassword); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("lockout bypassed with correct password: %d", resp.StatusCode)
	}
}

func TestOAuthCallbackForgedState(t *testing.T) {
	e := startE2E(t)
	c := e.client()
	c.login("admin", adminPassword)
	resp, _ := c.do("GET", "/api/v1/oauth/google/callback?code=abc&state=forged", nil)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "status=error") || !strings.Contains(loc, "invalid_state") {
		t.Fatalf("forged state: %d %s", resp.StatusCode, loc)
	}
	resp, b := c.do("POST", "/api/v1/oauth/google/start", map[string]any{})
	if resp.StatusCode != 400 || !strings.Contains(string(b), "oauth_not_configured") {
		t.Fatalf("unconfigured provider: %d %s", resp.StatusCode, b)
	}
}

func (e *e2e) addAccount(c *client) map[string]any {
	resp, b := c.do("POST", "/api/v1/accounts", map[string]any{
		"provider": "custom", "email": "alice@test", "username": "alice@test", "password": mailPassword,
		"imap":     map[string]any{"host": e.imap.Host, "port": e.imap.Port, "security": "tls"},
		"smtp":     map[string]any{"host": e.smtp.Host, "port": e.smtp.Port, "security": "tls"},
		"tlsCaPem": e.cert.CAPEM,
	})
	if resp.StatusCode != 201 {
		e.t.Fatalf("create account: %d %s", resp.StatusCode, b)
	}
	return decode[map[string]any](e.t, b)
}

func (e *e2e) waitInbox(c *client, n int) []map[string]any {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, b := c.do("GET", "/api/v1/messages?view=unified", nil)
		r := decode[struct{ Items []map[string]any }](e.t, b)
		if len(r.Items) >= n {
			return r.Items
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("inbox never reached %d messages", n)
	return nil
}

const maliciousHTML = `<html><head><script>alert('head')</script></head><body>
<p onclick="alert(1)">Bonjour <b>Alice</b></p>
<img src="https://tracker.example/pixel.gif">
<a href="javascript:alert(2)">clique</a>
<iframe src="https://evil.example"></iframe>
<svg onload="alert(3)"></svg>
</body></html>`

func TestMailFlowOverHTTP(t *testing.T) {
	e := startE2E(t)
	c := e.client()
	c.login("admin", adminPassword)

	now := time.Now()
	e.imap.Deliver("alice@test", "INBOX", testmail.Message("bob@test", "alice@test", "Facture mars", "Voici la facture du mois", now.Add(-time.Hour)))
	e.imap.Deliver("alice@test", "INBOX", testmail.MessageWithAttachment("eve@test", "alice@test", "Piège", maliciousHTML, "../../evil.html", []byte("<script>alert(document.cookie)</script>"), now))

	acct := e.addAccount(c)
	if _, ok := acct["password"]; ok || strings.Contains(fmt.Sprint(acct), mailPassword) {
		t.Fatal("account DTO leaks the password")
	}
	items := e.waitInbox(c, 2)
	evil := items[0]
	if evil["subject"] != "Piège" || evil["hasAttachments"] != true {
		t.Fatalf("unexpected first message: %v", evil)
	}
	id := int64(evil["id"].(float64))

	// Message detail with on-demand body
	_, b := c.do("GET", fmt.Sprintf("/api/v1/messages/%d", id), nil)
	detail := decode[struct {
		HasHTML       bool `json:"hasHtml"`
		RemoteContent int  `json:"remoteContent"`
		Attachments   []struct {
			ID       int64
			Filename string
		}
	}](t, b)
	if !detail.HasHTML || detail.RemoteContent != 1 || len(detail.Attachments) != 1 {
		t.Fatalf("detail: %s", b)
	}
	if detail.Attachments[0].Filename != "evil.html" {
		t.Fatalf("attachment filename not sanitised: %q", detail.Attachments[0].Filename)
	}

	// Sanitised HTML document served with a locked-down CSP
	resp, body := c.do("GET", fmt.Sprintf("/api/v1/messages/%d/body.html", id), nil)
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'none'") || !strings.Contains(csp, "sandbox") || strings.Contains(csp, "https:") {
		t.Fatalf("body CSP: %s", csp)
	}
	low := strings.ToLower(string(body))
	for _, bad := range []string{"<script", "onclick", "javascript:", "<iframe", "onload", "tracker.example"} {
		if strings.Contains(low, bad) {
			t.Fatalf("sanitised body contains %q: %s", bad, body)
		}
	}
	if !strings.Contains(string(body), "Bonjour <b>Alice</b>") {
		t.Fatalf("legit content lost: %s", body)
	}
	resp, body = c.do("GET", fmt.Sprintf("/api/v1/messages/%d/body.html?remote=1", id), nil)
	if !strings.Contains(string(body), "tracker.example") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "img-src 'self' data: https:") {
		t.Fatal("remote images not shown after explicit consent")
	}

	// Attachment can never render as HTML in our origin
	resp, body = c.do("GET", fmt.Sprintf("/api/v1/messages/%d/attachments/%d?inline=1", id, detail.Attachments[0].ID), nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || string(body) != "<script>alert(document.cookie)</script>" {
		t.Fatalf("attachment: %d %v %q", resp.StatusCode, resp.Header, body)
	}

	// Local search (metadata + indexed bodies)
	_, b = c.do("GET", "/api/v1/search?q="+url.QueryEscape("facture"), nil)
	sr := decode[struct{ Items []map[string]any }](t, b)
	if len(sr.Items) != 1 || sr.Items[0]["subject"] != "Facture mars" {
		t.Fatalf("search: %s", b)
	}
	_, b = c.do("GET", "/api/v1/search?q="+url.QueryEscape("from:eve has:attachment"), nil)
	if sr = decode[struct{ Items []map[string]any }](t, b); len(sr.Items) != 1 {
		t.Fatalf("operator search: %s", b)
	}

	// Upload (filename traversal and size limit)
	up := c.upload("../../../etc/passwd", []byte("pièce jointe"))
	if up["filename"] != "passwd" {
		t.Fatalf("upload filename: %v", up)
	}
	if resp := c.uploadRaw("big.bin", bytes.Repeat([]byte("x"), 2<<20)); resp.StatusCode != 413 {
		t.Fatalf("oversized upload: %d", resp.StatusCode)
	}

	// Reply with attachment
	other := items[1]
	resp, b = c.do("POST", "/api/v1/messages/send", map[string]any{
		"accountId": acct["id"], "mode": "reply", "relatedMessageId": other["id"],
		"to": []map[string]string{{"addr": "bob@test"}}, "bcc": []map[string]string{{"addr": "boss@test"}},
		"subject": "Re: Facture mars", "isHtml": true, "bodyHtml": "<p>Merci</p>", "uploads": []string{up["id"].(string)},
	})
	if resp.StatusCode != 202 {
		t.Fatalf("send: %d %s", resp.StatusCode, b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(e.smtp.Received()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	rec := e.smtp.Received()
	if len(rec) != 1 {
		t.Fatal("message not delivered")
	}
	raw := string(rec[0].Data)
	if !strings.Contains(raw, "In-Reply-To: <") || strings.Contains(raw, "boss@test") || len(rec[0].Rcpts) != 2 || !strings.Contains(raw, "passwd") {
		t.Fatalf("delivered message: rcpts=%v\n%s", rec[0].Rcpts, raw)
	}

	// Actions
	resp, b = c.do("POST", "/api/v1/messages/actions", map[string]any{"ids": []any{evil["id"]}, "action": "trash"})
	if resp.StatusCode != 200 {
		t.Fatalf("trash: %d %s", resp.StatusCode, b)
	}
	resp, _ = c.do("POST", "/api/v1/messages/actions", map[string]any{"ids": []any{evil["id"]}, "action": "explode"})
	if resp.StatusCode != 400 {
		t.Fatal("invalid action accepted")
	}

	// Another user cannot see or act on these messages.
	if _, err := e.app.DB.CreateUser(context.Background(), "mallory", mustHash(t), true); err != nil {
		t.Fatal(err)
	}
	m := e.client()
	if resp := m.login("mallory", "mallory-password-1"); resp.StatusCode != 200 {
		t.Fatal("second user login")
	}
	for _, p := range []string{fmt.Sprintf("/api/v1/messages/%d", id), fmt.Sprintf("/api/v1/messages/%d/body.html", id),
		fmt.Sprintf("/api/v1/messages/%d/attachments/%d", id, detail.Attachments[0].ID), fmt.Sprintf("/api/v1/accounts/%v", acct["id"])} {
		if resp, _ := m.do("GET", p, nil); resp.StatusCode != 404 {
			t.Errorf("cross-user access to %s: %d", p, resp.StatusCode)
		}
	}
	if _, b := m.do("GET", "/api/v1/messages?view=unified", nil); strings.Contains(string(b), "Facture") {
		t.Error("cross-user leak in unified inbox")
	}
	if resp, _ := m.do("POST", "/api/v1/messages/actions", map[string]any{"ids": []any{other["id"]}, "action": "trash"}); resp.StatusCode != 404 {
		t.Errorf("cross-user action: %d", resp.StatusCode)
	}

	// Secrets never appear in logs.
	logs := e.logs.String()
	for _, secret := range []string{mailPassword, adminPassword, c.csrf, "mallory-password-1"} {
		if strings.Contains(logs, secret) {
			t.Errorf("secret %q found in logs", secret)
		}
	}
	for _, ck := range c.http.Jar.Cookies(mustURL(e.srv.URL)) {
		if strings.Contains(logs, ck.Value) {
			t.Error("session cookie value found in logs")
		}
	}
}

func mustHash(t *testing.T) string {
	h, err := auth.HashPassword("mallory-password-1")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func (c *client) uploadRaw(name string, data []byte) *http.Response {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.e.srv.URL+"/api/v1/uploads", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	req.Header.Set("Origin", c.e.srv.URL)
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return resp
}

func (c *client) upload(name string, data []byte) map[string]any {
	resp := c.uploadRaw(name, data)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		c.e.t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	return decode[map[string]any](c.e.t, b)
}

func TestCreateAndDeleteFolders(t *testing.T) {
	e := startE2E(t)
	c := e.client()
	c.login("admin", adminPassword)
	acct := e.addAccount(c)
	accountID := int64(acct["id"].(float64))
	// Let the first sync mirror the mailbox list before changing it.
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, b := c.do("GET", fmt.Sprintf("/api/v1/accounts/%d", accountID), nil)
		if decode[map[string]any](t, b)["lastSyncOkAt"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("account never synced")
		}
		time.Sleep(100 * time.Millisecond)
	}

	type folder struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Delimiter string `json:"delimiter"`
		Role      string `json:"role"`
	}
	resp, b := c.do("POST", "/api/v1/folders", map[string]any{"accountId": accountID, "name": "Projets"})
	if resp.StatusCode != 201 {
		t.Fatalf("create folder: %d %s", resp.StatusCode, b)
	}
	parent := decode[folder](t, b)
	if parent.Name != "Projets" || parent.Role != "" {
		t.Fatalf("unexpected folder: %s", b)
	}

	resp, b = c.do("POST", "/api/v1/folders", map[string]any{"accountId": accountID, "parentId": parent.ID, "name": "Client A"})
	if resp.StatusCode != 201 {
		t.Fatalf("create subfolder: %d %s", resp.StatusCode, b)
	}
	child := decode[folder](t, b)
	if child.Name != "Projets"+parent.Delimiter+"Client A" {
		t.Fatalf("subfolder name: %q", child.Name)
	}
	if _, err := e.imap.Users["alice@test"].Status(child.Name, &imap.StatusOptions{}); err != nil {
		t.Fatalf("subfolder missing on the IMAP server: %v", err)
	}

	// The leaf name cannot smuggle a hierarchy level.
	resp, _ = c.do("POST", "/api/v1/folders", map[string]any{"accountId": accountID, "parentId": parent.ID, "name": "a" + parent.Delimiter + "b"})
	if resp.StatusCode != 400 {
		t.Fatalf("delimiter in leaf name accepted: %d", resp.StatusCode)
	}

	resp, b = c.do("DELETE", fmt.Sprintf("/api/v1/folders/%d", child.ID), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete subfolder: %d %s", resp.StatusCode, b)
	}
	_, b = c.do("GET", "/api/v1/folders", nil)
	list := decode[struct{ Items []folder }](t, b)
	for _, f := range list.Items {
		if f.ID == child.ID {
			t.Fatal("deleted folder still listed")
		}
	}
}
