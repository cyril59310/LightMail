// Package api implements the versioned REST API (/api/v1), the
// Server-Sent Events stream and serves the compiled web client.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"lightmail/internal/accounts"
	"lightmail/internal/auth"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/events"
	"lightmail/internal/metrics"
	"lightmail/internal/oauth"
	"lightmail/internal/outbox"
	"lightmail/internal/store"
	"lightmail/internal/syncer"
)

// Version is set at build time (-ldflags "-X lightmail/internal/api.Version=...").
var Version = "dev"

type Server struct {
	Cfg      *config.Config
	DB       *store.DB
	KR       *crypto.Keyring
	Auth     *auth.Manager
	Accounts *accounts.Service
	OAuth    *oauth.Manager
	Engine   *syncer.Engine
	Outbox   *outbox.Service
	Blobs    *blobs.Store
	Hub      *events.Hub
	Metrics  *metrics.Registry
	Log      *slog.Logger
	Static   http.Handler // compiled frontend
}

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
)

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}
func sessionFrom(r *http.Request) *store.Session {
	s, _ := r.Context().Value(ctxSession).(*store.Session)
	return s
}

// Handler builds the HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	pub := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, h) }
	priv := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(h)) }

	// Public endpoints
	pub("GET /api/v1/health", s.health)
	pub("POST /api/v1/auth/login", s.login)
	pub("GET /api/v1/auth/me", s.me)
	pub("GET /api/v1/openapi.yaml", s.openapi)
	pub("GET /api/v1/metrics", s.metricsHandler) // session or bearer token, checked inside

	// Authenticated endpoints
	priv("GET /api/v1/oauth/{provider}/callback", s.oauthCallback)
	priv("POST /api/v1/oauth/{provider}/start", s.oauthStart)
	priv("POST /api/v1/auth/logout", s.logout)
	priv("POST /api/v1/auth/logout-all", s.logoutAll)
	priv("POST /api/v1/auth/password", s.changePassword)
	priv("GET /api/v1/auth/sessions", s.listSessions)

	priv("GET /api/v1/providers", s.listProviders)
	priv("GET /api/v1/providers/detect", s.detectProvider)
	priv("GET /api/v1/accounts", s.listAccounts)
	priv("POST /api/v1/accounts", s.createAccount)
	priv("POST /api/v1/accounts/test", s.testAccount)
	priv("GET /api/v1/accounts/{id}", s.getAccount)
	priv("PATCH /api/v1/accounts/{id}", s.updateAccount)
	priv("DELETE /api/v1/accounts/{id}", s.deleteAccount)
	priv("POST /api/v1/accounts/{id}/sync", s.syncAccount)
	priv("POST /api/v1/accounts/{id}/reconnect", s.reconnectAccount)
	priv("POST /api/v1/accounts/{id}/default", s.defaultAccount)
	priv("POST /api/v1/accounts/{id}/revoke", s.revokeAccount)

	priv("GET /api/v1/folders", s.listFolders)
	priv("POST /api/v1/folders", s.createFolder)
	priv("PATCH /api/v1/folders/{id}", s.updateFolder)
	priv("DELETE /api/v1/folders/{id}", s.deleteFolder)
	priv("GET /api/v1/counters", s.counters)
	priv("GET /api/v1/contacts", s.contacts)

	priv("GET /api/v1/messages", s.listMessages)
	priv("GET /api/v1/search", s.searchMessages)
	priv("POST /api/v1/messages/actions", s.messageActions)
	priv("POST /api/v1/messages/send", s.sendMessage)
	priv("GET /api/v1/messages/{id}", s.getMessage)
	priv("GET /api/v1/messages/{id}/body.html", s.messageHTML)
	priv("GET /api/v1/messages/{id}/raw", s.messageRaw)
	priv("GET /api/v1/messages/{id}/headers", s.messageHeaders)
	priv("GET /api/v1/messages/{id}/attachments/{att}", s.attachment)
	priv("POST /api/v1/messages/{id}/edit-draft", s.draftFromMessage)

	priv("POST /api/v1/uploads", s.upload)
	priv("DELETE /api/v1/uploads/{id}", s.deleteUpload)
	priv("GET /api/v1/drafts", s.listDrafts)
	priv("POST /api/v1/drafts", s.saveDraft)
	priv("GET /api/v1/drafts/{id}", s.getDraft)
	priv("PUT /api/v1/drafts/{id}", s.saveDraft)
	priv("DELETE /api/v1/drafts/{id}", s.deleteDraft)
	priv("GET /api/v1/outbox", s.listOutbox)
	priv("POST /api/v1/outbox/{id}/cancel", s.cancelOutbox)
	priv("POST /api/v1/outbox/{id}/retry", s.retryOutbox)
	priv("DELETE /api/v1/outbox/{id}", s.deleteOutbox)

	priv("GET /api/v1/notifications", s.listNotifications)
	priv("POST /api/v1/notifications/read", s.readNotifications)
	priv("GET /api/v1/events", s.eventStream)
	priv("GET /api/v1/settings", s.getSettings)
	priv("PUT /api/v1/settings", s.putSettings)
	priv("GET /api/v1/status", s.status)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
	if s.Static != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.Static.ServeHTTP(w, r)
		})
	}
	return s.recoverer(s.observe(s.securityHeaders(s.loadSession(s.csrf(mux)))))
}

// ---- middleware -------------------------------------------------------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("panic in handler", "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// observe logs requests (path only: query strings may carry OAuth codes)
// and records latency metrics.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		if r.URL.Path != "/api/v1/events" {
			s.Metrics.HTTPDuration.Observe(d.Seconds())
		}
		s.Metrics.HTTPRequests.With(strconv.Itoa(sw.status/100) + "xx").Inc()
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/v1/health" {
			lvl := slog.LevelDebug
			if sw.status >= 500 {
				lvl = slog.LevelError
			} else if sw.status >= 400 && sw.status != 401 {
				lvl = slog.LevelInfo
			}
			s.Log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status_code", sw.status, "duration_ms", d.Milliseconds(), "bytes", sw.bytes, "ip", s.clientIP(r))
		}
	})
}

const appCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'; worker-src 'self'; manifest-src 'self'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	https := s.Cfg.PublicURL.Scheme == "https"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
		h.Set("Content-Security-Policy", appCSP)
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
			if sess, u, err := s.Auth.Validate(r.Context(), c.Value); err == nil {
				ctx := context.WithValue(r.Context(), ctxUser, u)
				ctx = context.WithValue(ctx, ctxSession, sess)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf protects state-changing requests: the Origin (when sent) must be the
// public origin, cross-site fetches are refused, and authenticated requests
// must carry the per-session token in X-CSRF-Token.
func (s *Server) csrf(next http.Handler) http.Handler {
	origin := s.Cfg.PublicURL.Scheme + "://" + s.Cfg.PublicURL.Host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			writeError(w, http.StatusForbidden, "bad_origin", "cross-origin request refused")
			return
		}
		if sfs := r.Header.Get("Sec-Fetch-Site"); sfs == "cross-site" {
			writeError(w, http.StatusForbidden, "bad_origin", "cross-site request refused")
			return
		}
		if sess := sessionFrom(r); sess != nil {
			if !crypto.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), sess.CSRFToken) {
				writeError(w, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the client address, trusting X-Forwarded-For only from
// configured reverse proxies.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !s.trusted(addr) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		a, err := netip.ParseAddr(p)
		if err != nil {
			break
		}
		if !s.trusted(a) {
			return a.String()
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if a, err := netip.ParseAddr(xr); err == nil {
			return a.String()
		}
	}
	return host
}

func (s *Server) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.Cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ---- helpers ------------------------------------------------------------

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

// fail maps internal errors to responses without leaking details.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "conflicting state")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout", "the mail server did not answer in time")
	default:
		s.Log.Error("request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// readJSON decodes a JSON body strictly (unknown fields refused, size bounded).
func readJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "content_type", "Content-Type must be application/json")
		return false
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "bad_json", "invalid JSON body: "+sanitizeJSONErr(err))
		}
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "bad_json", "unexpected data after JSON body")
		return false
	}
	return true
}

func sanitizeJSONErr(err error) string {
	m := err.Error()
	if len(m) > 200 {
		m = m[:200]
	}
	return m
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func queryID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if id < 0 {
		return 0
	}
	return id
}

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "invalid", msg)
}

var _ io.Writer = (*statusWriter)(nil)
