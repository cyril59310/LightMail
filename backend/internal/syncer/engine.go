// Package syncer synchronises IMAP mailboxes into the local cache and
// executes message operations (flags, moves, deletions) on the servers.
//
// Each account has its own worker goroutine, connection pool and optional
// IDLE connection, so a failing account never blocks the others. A global
// semaphore bounds concurrent synchronisations and another bounds the total
// number of IMAP connections.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"lightmail/internal/accounts"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/events"
	"lightmail/internal/imapx"
	"lightmail/internal/mailauth"
	"lightmail/internal/metrics"
	"lightmail/internal/netx"
	"lightmail/internal/oauth"
	"lightmail/internal/providers"
	"lightmail/internal/store"
)

type Engine struct {
	db       *store.DB
	accts    *accounts.Service
	hub      *events.Hub
	notifier *events.Notifier
	blobs    *blobs.Store
	log      *slog.Logger
	cfg      *config.Config
	m        *metrics.Registry

	syncSem chan struct{}
	connSem chan struct{}

	mu      sync.Mutex
	workers map[int64]*worker
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func New(cfg *config.Config, db *store.DB, accts *accounts.Service, hub *events.Hub, n *events.Notifier, bs *blobs.Store, m *metrics.Registry, log *slog.Logger) *Engine {
	return &Engine{
		db: db, accts: accts, hub: hub, notifier: n, blobs: bs, log: log, cfg: cfg, m: m,
		syncSem: make(chan struct{}, max(1, cfg.SyncMaxConcurrent)),
		connSem: make(chan struct{}, max(2, cfg.IMAPMaxConnsTotal)),
		workers: map[int64]*worker{},
	}
}

// Start launches a worker for every account.
func (e *Engine) Start(ctx context.Context) error {
	e.ctx, e.cancel = context.WithCancel(ctx)
	list, err := e.db.ListAccounts(ctx, 0)
	if err != nil {
		return err
	}
	for _, a := range list {
		e.startWorker(a.ID)
	}
	e.wg.Add(1)
	go e.housekeeping()
	return nil
}

// Stop stops all workers and closes connections.
func (e *Engine) Stop() {
	if e.cancel == nil {
		return
	}
	e.cancel()
	e.mu.Lock()
	for _, w := range e.workers {
		w.stop()
	}
	e.mu.Unlock()
	e.wg.Wait()
}

func (e *Engine) startWorker(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.workers[id]; ok || e.ctx == nil {
		return
	}
	w := newWorker(e, id)
	e.workers[id] = w
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		w.run()
	}()
}

// AccountAdded starts synchronising a new account.
func (e *Engine) AccountAdded(id int64) { e.startWorker(id) }

// AccountChanged restarts the worker so new settings/credentials apply.
func (e *Engine) AccountChanged(id int64) {
	e.AccountRemoved(id)
	e.startWorker(id)
}

// AccountRemoved stops the worker of a deleted account.
func (e *Engine) AccountRemoved(id int64) {
	e.mu.Lock()
	w := e.workers[id]
	delete(e.workers, id)
	e.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

// Trigger requests an immediate synchronisation.
func (e *Engine) Trigger(id int64, full bool) {
	e.mu.Lock()
	w := e.workers[id]
	e.mu.Unlock()
	if w != nil {
		w.trigger(request{full: full})
	}
}

func (e *Engine) worker(id int64) (*worker, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w := e.workers[id]
	if w == nil {
		return nil, errors.New("account is not active")
	}
	return w, nil
}

// WithConn runs fn with a pooled connection of the account.
func (e *Engine) WithConn(ctx context.Context, accountID int64, fn func(c *imapx.Client) error) error {
	w, err := e.worker(accountID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, release, err := w.pool.get(ctx)
	if err != nil {
		return err
	}
	err = fn(c)
	release(err == nil || isIMAPResponseError(err))
	return err
}

// isIMAPResponseError: a NO/BAD reply leaves the connection usable.
func isIMAPResponseError(err error) bool {
	var ie *imap.Error
	return errors.As(err, &ie)
}

func (e *Engine) housekeeping() {
	defer e.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
			e.mu.Lock()
			for _, w := range e.workers {
				w.pool.reap()
			}
			e.mu.Unlock()
		}
	}
}

// ---- worker -------------------------------------------------------------

type request struct {
	full      bool
	inboxOnly bool
}

type worker struct {
	e        *Engine
	id       int64
	reqs     chan request
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	pool     *pool
	log      *slog.Logger
	failures int
	lastList time.Time
	idleStop context.CancelFunc
}

func newWorker(e *Engine, id int64) *worker {
	ctx, cancel := context.WithCancel(e.ctx)
	w := &worker{e: e, id: id, reqs: make(chan request, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}), log: e.log.With("account_id", id)}
	w.pool = newPool(e.cfg.IMAPMaxConnsPerAcct, 90*time.Second, w.dial, func() {
		<-e.connSem
		e.m.IMAPConns.Dec()
	})
	return w
}

func (w *worker) dial(ctx context.Context) (*imapx.Client, error) {
	return w.dialWith(ctx, nil)
}

func (w *worker) dialWith(ctx context.Context, h *imapclient.UnilateralDataHandler) (*imapx.Client, error) {
	select {
	case w.e.connSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	a, err := w.e.db.Account(ctx, w.id)
	if err != nil {
		<-w.e.connSem
		return nil, err
	}
	c, err := w.e.accts.DialIMAP(ctx, a, h)
	if err != nil {
		<-w.e.connSem
		return nil, err
	}
	w.e.m.IMAPConns.Inc()
	return c, nil
}

func (w *worker) trigger(r request) {
	select {
	case w.reqs <- r:
	default:
		// a request is already pending; upgrade it if needed
		if r.full || !r.inboxOnly {
			select {
			case <-w.reqs:
			default:
			}
			select {
			case w.reqs <- r:
			default:
			}
		}
	}
}

func (w *worker) stop() {
	w.cancel()
	<-w.done
	w.pool.close()
}

func (w *worker) interval(a *store.Account) time.Duration {
	if a.SyncIntervalSec > 0 {
		return time.Duration(a.SyncIntervalSec) * time.Second
	}
	return w.e.cfg.SyncInterval
}

func (w *worker) run() {
	defer close(w.done)
	defer w.stopIdle()
	next := request{full: true}
	for {
		a, err := w.e.db.Account(w.ctx, w.id)
		if err != nil {
			return // account deleted or shutting down
		}
		wait := w.interval(a)
		if !a.SyncEnabled {
			_ = w.e.db.SetAccountStatus(w.ctx, w.id, store.StatusPaused, "")
			w.publishAccount()
		} else if a.Status == store.StatusAuthError && !next.full {
			// do not hammer the provider with rejected credentials
		} else {
			res, err := w.syncOnce(next)
			if err != nil && w.ctx.Err() == nil {
				wait = w.handleError(a, err)
			} else if err == nil {
				w.failures = 0
				if res.moreBackfill {
					wait = 15 * time.Second
				}
				if a.IdleEnabled && w.e.cfg.SyncIdle && res.idleCapable {
					w.startIdle()
				}
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-w.ctx.Done():
			timer.Stop()
			return
		case next = <-w.reqs:
			timer.Stop()
		case <-timer.C:
			next = request{}
		}
	}
}

// handleError records the failure and returns the delay before retrying.
func (w *worker) handleError(a *store.Account, err error) time.Duration {
	w.failures++
	msg := accounts.Explain(err)
	w.e.m.SyncRuns.With("error").Inc()
	switch {
	case mailauth.IsAuthError(err) || errors.Is(err, oauth.ErrReauth):
		w.log.Warn("authentication failed, automatic retries suspended", "err", msg)
		_ = w.e.db.SetAccountStatus(w.ctx, w.id, store.StatusAuthError, msg)
		kind, title := events.KindAuthError, "Échec d'authentification : "+a.Email
		if a.AuthType == providers.AuthOAuth2 {
			kind, title = events.KindReauth, "Reconnexion nécessaire : "+a.Email
		}
		w.e.notifier.Notify(w.ctx, a.UserID, a.ID, kind, title, msg)
		w.publishAccount()
		return 24 * time.Hour // until the user reconnects (trigger)
	}
	backoff := time.Duration(30<<min(w.failures-1, 5)) * time.Second // 30s .. 16m
	if isThrottled(err) {
		backoff = max(backoff, 10*time.Minute)
	}
	backoff = min(backoff, 15*time.Minute)
	backoff += time.Duration(rand.Int64N(int64(backoff / 5)))
	w.log.Warn("sync failed", "err", msg, "retry_in", backoff.Round(time.Second).String(), "failures", w.failures)
	_ = w.e.db.SetAccountStatus(w.ctx, w.id, store.StatusError, msg)
	if w.failures == 3 {
		w.e.notifier.Notify(w.ctx, a.UserID, a.ID, events.KindSyncError, "Synchronisation impossible : "+a.Email, msg)
	}
	w.publishAccount()
	return backoff
}

func isThrottled(err error) bool {
	var ie *imap.Error
	if errors.As(err, &ie) {
		if ie.Code == imap.ResponseCodeLimit {
			return true
		}
		t := strings.ToLower(ie.Text)
		return strings.Contains(t, "too many") || strings.Contains(t, "throttl") || strings.Contains(t, "rate limit")
	}
	return false
}

func (w *worker) publishAccount() {
	if a, err := w.e.db.Account(w.ctx, w.id); err == nil {
		w.e.hub.Publish(a.UserID, events.Event{Type: events.TypeAccount, Data: map[string]any{"id": a.ID, "status": a.Status, "lastError": a.LastError, "lastSyncOkAt": a.LastSyncOKAt}})
	}
}

type syncResult struct {
	idleCapable  bool
	moreBackfill bool
}

// syncOnce runs one synchronisation of the account.
func (w *worker) syncOnce(req request) (syncResult, error) {
	var res syncResult
	select {
	case w.e.syncSem <- struct{}{}:
	case <-w.ctx.Done():
		return res, w.ctx.Err()
	}
	defer func() { <-w.e.syncSem }()
	start := time.Now()
	a, err := w.e.db.Account(w.ctx, w.id)
	if err != nil {
		return res, err
	}
	_ = w.e.db.MarkAccountSyncAttempt(w.ctx, w.id)
	if a.Status != store.StatusOK {
		_ = w.e.db.SetAccountStatus(w.ctx, w.id, store.StatusSyncing, "")
		w.publishAccount()
	}
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Minute)
	defer cancel()
	w.log.Debug("sync: acquiring connection", "full", req.full, "inbox_only", req.inboxOnly)
	c, release, err := w.pool.get(ctx)
	if err != nil {
		return res, err
	}
	w.log.Debug("sync: connection acquired", "elapsed_ms", time.Since(start).Milliseconds())
	healthy := false
	defer func() { release(healthy) }()
	res.idleCapable = c.Caps.Has(imap.CapIdle)

	if req.full || time.Since(w.lastList) > time.Hour || !req.inboxOnly && w.lastList.IsZero() {
		if err := w.syncFolderList(ctx, a, c); err != nil {
			return res, fmt.Errorf("list folders: %w", err)
		}
		w.lastList = time.Now()
	}
	folders, err := w.e.db.AccountFolders(ctx, a.ID)
	if err != nil {
		return res, err
	}
	fs := &folderSync{w: w, a: a, c: c, ctx: ctx, full: req.full}
	var folderErrs []error
	for _, f := range folders {
		if !f.Selectable || !f.SyncEnabled || (req.inboxOnly && f.Role != store.RoleInbox) {
			continue
		}
		if err := fs.sync(f); err != nil {
			if c.IsClosed() || netx.IsTimeout(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return res, err
			}
			w.log.Warn("folder sync failed", "folder", f.Name, "err", imapx.Describe(err))
			folderErrs = append(folderErrs, fmt.Errorf("%s: %w", f.Name, err))
		}
	}
	// Backfill older messages within the time budget.
	deadline := time.Now().Add(w.e.cfg.SyncBackfillBudget)
	if !req.inboxOnly {
		for _, f := range folders {
			if !f.Selectable || !f.SyncEnabled || f.BackfillDone {
				continue
			}
			for !f.BackfillDone && time.Now().Before(deadline) {
				if err := fs.backfill(f); err != nil {
					if c.IsClosed() || netx.IsTimeout(err) {
						return res, err
					}
					w.log.Warn("backfill failed", "folder", f.Name, "err", imapx.Describe(err))
					break
				}
			}
			if !f.BackfillDone {
				res.moreBackfill = true
			}
		}
	}
	healthy = true
	w.e.m.SyncDuration.Since(start)
	if fs.inserted >= 1000 {
		// Large imports change the data distribution: refresh planner stats.
		_ = w.e.db.Optimize(w.ctx)
	}
	if len(folderErrs) > 0 && len(folderErrs) == countSyncable(folders) {
		return res, errors.Join(folderErrs...)
	}
	w.e.m.SyncRuns.With("ok").Inc()
	_ = w.e.db.MarkAccountSynced(w.ctx, w.id)
	w.publishAccount()
	w.log.Debug("sync done", "duration_ms", time.Since(start).Milliseconds(), "inbox_only", req.inboxOnly)
	return res, nil
}

func countSyncable(fs []*store.Folder) int {
	n := 0
	for _, f := range fs {
		if f.Selectable && f.SyncEnabled {
			n++
		}
	}
	return n
}

// syncFolderList mirrors the server mailbox list.
func (w *worker) syncFolderList(ctx context.Context, a *store.Account, c *imapx.Client) error {
	boxes, err := c.ListMailboxes()
	if err != nil {
		return err
	}
	if len(boxes) == 0 {
		return errors.New("server returned no mailbox")
	}
	prov := providers.Get(a.Provider)
	var keep []int64
	changed := false
	existing, _ := w.e.db.AccountFolders(ctx, a.ID)
	known := map[string]bool{}
	for _, f := range existing {
		known[f.Name] = true
	}
	for _, b := range boxes {
		role := imapx.RoleFor(b)
		syncEnabled := true
		if prov != nil {
			for _, r := range prov.SkipRoles {
				if r == role {
					syncEnabled = false
				}
			}
		}
		delim := ""
		if b.Delim != 0 {
			delim = string(b.Delim)
		}
		attrs := make([]string, 0, len(b.Attrs))
		for _, at := range b.Attrs {
			attrs = append(attrs, string(at))
		}
		f, err := w.e.db.UpsertFolder(ctx, &store.Folder{
			AccountID: a.ID, Name: b.Mailbox, Delimiter: delim, Role: role, Attrs: strings.Join(attrs, " "),
			Selectable: imapx.IsSelectable(b), Subscribed: true, SyncEnabled: syncEnabled,
		})
		if err != nil {
			return err
		}
		if !known[b.Mailbox] {
			changed = true
		}
		keep = append(keep, f.ID)
	}
	if len(keep) != len(existing) {
		changed = true
	}
	if err := w.e.db.DeleteFoldersNotIn(ctx, a.ID, keep); err != nil {
		return err
	}
	if changed {
		w.e.hub.Publish(a.UserID, events.Event{Type: events.TypeFolders, Data: map[string]any{"accountId": a.ID}})
	}
	return nil
}
