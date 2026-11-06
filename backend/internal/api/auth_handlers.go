package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"lightmail/internal/auth"
)

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		// Lax (not Strict) so the cookie accompanies the top-level redirect
		// back from the OAuth provider; CSRF is covered by tokens + Origin.
		SameSite: http.SameSiteLaxMode,
	})
}

type meResponse struct {
	Authenticated bool            `json:"authenticated"`
	AppName       string          `json:"appName"`
	Version       string          `json:"version"`
	LogoVersion   int64           `json:"logoVersion,omitempty"`
	SetupRequired bool            `json:"setupRequired,omitempty"`
	User          any             `json:"user,omitempty"`
	CSRFToken     string          `json:"csrfToken,omitempty"`
	Limits        *limitsInfo     `json:"limits,omitempty"`
	OAuth         map[string]bool `json:"oauth,omitempty"`
}

type limitsInfo struct {
	MaxAttachmentBytes int64 `json:"maxAttachmentBytes"`
	MaxMessageBytes    int64 `json:"maxMessageBytes"`
	UndoSendSeconds    int   `json:"undoSendSeconds"`
	MaxAccounts        int   `json:"maxAccounts"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	resp := meResponse{AppName: s.Cfg.AppName, Version: Version, LogoVersion: s.logoVersion(r.Context())}
	u, sess := userFrom(r), sessionFrom(r)
	if u == nil {
		if n, err := s.DB.CountUsers(r.Context()); err == nil && n == 0 {
			resp.SetupRequired = true
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Authenticated = true
	resp.User = map[string]any{"id": u.ID, "username": u.Username, "isAdmin": u.IsAdmin}
	resp.CSRFToken = sess.CSRFToken
	resp.Limits = &limitsInfo{
		MaxAttachmentBytes: s.Cfg.MaxAttachmentBytes, MaxMessageBytes: s.Cfg.MaxUploadBytes,
		UndoSendSeconds: int(s.Cfg.UndoSendDelay.Seconds()), MaxAccounts: s.Cfg.MaxAccounts,
	}
	resp.OAuth = map[string]bool{"google": s.OAuth.Configured("google"), "microsoft": s.OAuth.Configured("microsoft")}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req, 8<<10) {
		return
	}
	if req.Username == "" || req.Password == "" || len(req.Username) > 200 || len(req.Password) > 1024 {
		writeError(w, http.StatusBadRequest, "invalid", "username and password are required")
		return
	}
	sess, token, u, err := s.Auth.Login(r.Context(), req.Username, req.Password, s.clientIP(r), r.UserAgent())
	var le *auth.LockoutError
	switch {
	case errors.As(err, &le):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(le.RetryAfter.Seconds()))))
		s.Log.Warn("login locked out", "ip", s.clientIP(r))
		writeError(w, http.StatusTooManyRequests, "locked", "too many failed attempts, try again later")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.Log.Info("login failed", "ip", s.clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	// Session fixation: any previous session cookie is replaced.
	if old := sessionFrom(r); old != nil {
		_ = s.Auth.Logout(r.Context(), old.ID)
	}
	s.setSessionCookie(w, token, int(time.Until(time.Unix(sess.ExpiresAt, 0)).Seconds()))
	s.Log.Info("login", "user_id", u.ID, "ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": u.ID, "username": u.Username}, "csrfToken": sess.CSRFToken})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess := sessionFrom(r); sess != nil {
		_ = s.Auth.Logout(r.Context(), sess.ID)
	}
	s.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logoutAll(w http.ResponseWriter, r *http.Request) {
	u, sess := userFrom(r), sessionFrom(r)
	if err := s.Auth.LogoutOthers(r.Context(), u.ID, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Auth.Logout(r.Context(), sess.ID)
	s.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req, 8<<10) {
		return
	}
	err := s.Auth.ChangePassword(r.Context(), userFrom(r), req.Current, req.New, sessionFrom(r).ID)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusForbidden, "invalid_credentials", "current password is incorrect")
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
	case err != nil:
		s.fail(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListSessions(r.Context(), userFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cur := sessionFrom(r).ID
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		out = append(out, map[string]any{"createdAt": x.CreatedAt, "lastSeenAt": x.LastSeenAt, "expiresAt": x.ExpiresAt, "userAgent": x.UserAgent, "ip": x.IP, "current": x.ID == cur})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
