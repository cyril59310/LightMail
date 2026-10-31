package api

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed openapi.yaml
var openapiSpec []byte

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(openapiSpec)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats, _ := s.DB.Stats(ctx)
	ver, _ := s.DB.SchemaVersion(ctx)
	list, _ := s.DB.ListAccounts(ctx, userFrom(r).ID)
	accts := make([]map[string]any, 0, len(list))
	for _, a := range list {
		accts = append(accts, map[string]any{"id": a.ID, "email": a.Email, "status": a.Status, "lastError": a.LastError, "lastSyncAt": a.LastSyncAt, "lastSyncOkAt": a.LastSyncOKAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       Version,
		"goVersion":     runtime.Version(),
		"schemaVersion": ver,
		"database":      stats,
		"runtime":       s.Metrics.Snapshot(),
		"accounts":      accts,
		"sseClients":    s.Hub.Subscribers(),
		"sync": map[string]any{
			"intervalSeconds": int(s.Cfg.SyncInterval.Seconds()), "idle": s.Cfg.SyncIdle,
			"maxConcurrent": s.Cfg.SyncMaxConcurrent, "maxConnsPerAccount": s.Cfg.IMAPMaxConnsPerAcct,
		},
		"keyId": s.KR.PrimaryID(),
	})
}

// metricsHandler serves Prometheus metrics to an authenticated session or a
// scraper presenting LIGHTMAIL_METRICS_TOKEN as a bearer token.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	ok := userFrom(r) != nil
	if !ok && s.Cfg.MetricsToken != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ok = subtle.ConstantTimeCompare([]byte(got), []byte(s.Cfg.MetricsToken)) == 1
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.Metrics.Write(w)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListNotifications(r.Context(), userFrom(r).ID, 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !readJSON(w, r, &req, 64<<10) {
		return
	}
	if err := s.DB.MarkNotificationsRead(r.Context(), userFrom(r).ID, req.IDs); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// eventStream pushes real-time events with Server-Sent Events.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer
	w.WriteHeader(http.StatusOK)
	ch, cancel := s.Hub.Subscribe(userFrom(r).ID)
	defer cancel()
	fmt.Fprint(w, "retry: 5000\n: connected\n\n")
	_ = rc.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		case ev := <-ch:
			b, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// ---- settings ----------------------------------------------------------

var settingRules = map[string]*regexp.Regexp{
	"language":             regexp.MustCompile(`^(fr|en)$`),
	"theme":                regexp.MustCompile(`^(light|dark|system)$`),
	"timezone":             regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+\-]+){0,2}$|^UTC$|^$`),
	"pageSize":             regexp.MustCompile(`^(25|50|100|200)$`),
	"density":              regexp.MustCompile(`^(comfortable|compact)$`),
	"markReadOnOpen":       regexp.MustCompile(`^(true|false)$`),
	"confirmDelete":        regexp.MustCompile(`^(true|false)$`),
	"confirmSendNoSubject": regexp.MustCompile(`^(true|false)$`),
	"defaultFormat":        regexp.MustCompile(`^(html|text)$`),
	"browserNotifications": regexp.MustCompile(`^(true|false)$`),
	"undoSendSeconds":      regexp.MustCompile(`^([0-9]|[12][0-9]|30)$`),
	"unifiedAccounts":      regexp.MustCompile(`^(\[\]|\[\d+(,\d+)*\]|all)$`),
	"readingPane":          regexp.MustCompile(`^(right|bottom|off)$`),
	"threadView":           regexp.MustCompile(`^(true|false)$`),
	"remoteImages":         regexp.MustCompile(`^(ask|always)$`),
}

var notifyKinds = map[string]bool{"sync_error": true, "auth_error": true, "reauth_required": true, "token_expired": true,
	"send_failed": true, "send_ambiguous": true, "quota": true, "new_mail": true}

func validSetting(k, v string) bool {
	if strings.HasPrefix(k, "notify.") {
		return notifyKinds[strings.TrimPrefix(k, "notify.")] && (v == "on" || v == "off")
	}
	if k == "accountLayout" {
		return validAccountLayout(v)
	}
	re, ok := settingRules[k]
	if !ok {
		return false
	}
	if k == "timezone" && v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return false
		}
	}
	return re.MatchString(v)
}

var accountGroupID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// validAccountLayout checks the sidebar organisation: display order of the
// accounts and named categories. Unknown account ids are ignored by the client.
func validAccountLayout(v string) bool {
	var l struct {
		Ungrouped []int64 `json:"ungrouped"`
		Groups    []struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			Accounts []int64 `json:"accounts"`
		} `json:"groups"`
	}
	if !strings.HasPrefix(v, "{") || json.Unmarshal([]byte(v), &l) != nil || len(l.Groups) > 50 {
		return false
	}
	seen := make(map[string]bool, len(l.Groups))
	for _, g := range l.Groups {
		name := strings.TrimSpace(g.Name)
		if !accountGroupID.MatchString(g.ID) || seen[g.ID] || name == "" || utf8.RuneCountInString(name) > 60 {
			return false
		}
		seen[g.ID] = true
	}
	return true
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.DB.Settings(r.Context(), userFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defaults := map[string]string{
		"language": "fr", "theme": "system", "timezone": "", "pageSize": "50", "density": "comfortable",
		"markReadOnOpen": "true", "confirmDelete": "true", "confirmSendNoSubject": "true", "defaultFormat": "html",
		"browserNotifications": "false", "undoSendSeconds": strconv.Itoa(int(s.Cfg.UndoSendDelay.Seconds())), "unifiedAccounts": "all",
		"readingPane": "right", "threadView": "true", "remoteImages": "ask", "accountLayout": "{}",
	}
	for k, v := range st {
		defaults[k] = v
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var kv map[string]string
	if !readJSON(w, r, &kv, 32<<10) {
		return
	}
	if len(kv) > 100 {
		badRequest(w, "too many settings")
		return
	}
	for k, v := range kv {
		if !validSetting(k, v) {
			badRequest(w, "invalid setting "+strconv.Quote(k))
			return
		}
	}
	if err := s.DB.SetSettings(r.Context(), userFrom(r).ID, kv); err != nil {
		s.fail(w, r, err)
		return
	}
	s.getSettings(w, r)
}
