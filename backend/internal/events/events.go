// Package events is an in-process publish/subscribe hub used to push
// real-time updates to browsers (Server-Sent Events) and a notifier that
// persists user-facing alerts.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"lightmail/internal/store"
)

// Event types pushed to clients.
const (
	TypeAccount      = "account"      // account status changed
	TypeFolders      = "folders"      // folder list or counters changed
	TypeMessages     = "messages"     // messages added/changed/removed in a folder
	TypeNewMail      = "newmail"      // new messages arrived in an inbox
	TypeOutbox       = "outbox"       // send queue item changed
	TypeNotification = "notification" // persisted notification
)

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type Hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[int64]map[chan Event]struct{}{}} }

// Subscribe returns a channel receiving the user's events.
func (h *Hub) Subscribe(userID int64) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = map[chan Event]struct{}{}
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[userID], ch)
		h.mu.Unlock()
	}
}

// Publish delivers ev without blocking: slow subscribers miss events (the
// client resynchronises on reconnect).
func (h *Hub) Publish(userID int64, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[userID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.subs {
		n += len(s)
	}
	return n
}

// Notification kinds.
const (
	KindSyncError     = "sync_error"
	KindAuthError     = "auth_error"
	KindReauth        = "reauth_required"
	KindTokenExpired  = "token_expired"
	KindSendFailed    = "send_failed"
	KindSendAmbiguous = "send_ambiguous"
	KindQuota         = "quota"
	KindNewMail       = "new_mail"
)

// Notifier stores notifications, pushes them and forwards them to an
// optional webhook. No third-party service is required.
type Notifier struct {
	db      *store.DB
	hub     *Hub
	log     *slog.Logger
	webhook string
	client  *http.Client
}

func NewNotifier(db *store.DB, hub *Hub, log *slog.Logger, webhook string) *Notifier {
	return &Notifier{db: db, hub: hub, log: log, webhook: webhook, client: &http.Client{Timeout: 10 * time.Second}}
}

// Notify records a notification unless the user disabled this kind or an
// identical unread one was emitted recently.
func (n *Notifier) Notify(ctx context.Context, userID, accountID int64, kind, title, body string) {
	if settings, err := n.db.Settings(ctx, userID); err == nil && settings["notify."+kind] == "off" {
		return
	}
	if kind != KindNewMail {
		if dup, err := n.db.RecentNotificationExists(ctx, userID, accountID, kind, time.Now().Add(-6*time.Hour).Unix()); err == nil && dup {
			return
		}
	}
	nt := &store.Notification{UserID: userID, AccountID: accountID, Kind: kind, Title: title, Body: body}
	if kind != KindNewMail {
		if err := n.db.AddNotification(ctx, nt); err != nil {
			n.log.Warn("store notification", "err", err)
			return
		}
	} else {
		nt.CreatedAt = time.Now().Unix()
	}
	n.hub.Publish(userID, Event{Type: TypeNotification, Data: nt})
	if n.webhook != "" {
		go n.sendWebhook(nt)
	}
}

func (n *Notifier) sendWebhook(nt *store.Notification) {
	body, _ := json.Marshal(map[string]any{
		"kind": nt.Kind, "title": nt.Title, "body": nt.Body, "accountId": nt.AccountID, "createdAt": nt.CreatedAt,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhook, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LightMail-Webhook")
	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Warn("webhook delivery failed", "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.log.Warn("webhook delivery rejected", "status_code", resp.StatusCode)
	}
}
