// Package app wires every component together.
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lightmail/internal/accounts"
	"lightmail/internal/api"
	"lightmail/internal/auth"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/events"
	"lightmail/internal/metrics"
	"lightmail/internal/netx"
	"lightmail/internal/oauth"
	"lightmail/internal/outbox"
	"lightmail/internal/store"
	"lightmail/internal/syncer"
	"lightmail/internal/web"
)

type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	DB      *store.DB
	KR      *crypto.Keyring
	Blobs   *blobs.Store
	Auth    *auth.Manager
	OAuth   *oauth.Manager
	Engine  *syncer.Engine
	Outbox  *outbox.Service
	API     *api.Server
	Metrics *metrics.Registry
	Handler http.Handler

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New opens the database and builds every service (nothing runs yet).
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	kr, err := crypto.NewKeyring(cfg.MasterKey, cfg.OldKeys...)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, cfg.DBPath, 4)
	if err != nil {
		return nil, err
	}
	if err := db.CheckKey(ctx, kr); err != nil {
		db.Close()
		return nil, err
	}
	bs, err := blobs.New(filepath.Join(cfg.DataDir, "blobs"))
	if err != nil {
		db.Close()
		return nil, err
	}
	_ = bs.CleanTmp()
	m := metrics.New()
	hub := events.NewHub()
	notifier := events.NewNotifier(db, hub, log, cfg.WebhookURL)
	om := oauth.New(cfg, db, kr, log)
	policy := netx.Policy{BlockPrivate: cfg.BlockPrivateNetworks, InsecureSkipVerify: cfg.AllowInsecureTLS, Timeout: cfg.NetTimeout}
	if cfg.AllowInsecureTLS {
		log.Warn("TLS certificate verification is DISABLED (LIGHTMAIL_DEV_ALLOW_INSECURE_TLS): never use this in production")
	}
	as := &accounts.Service{DB: db, KR: kr, OAuth: om, Policy: policy, Metrics: m}
	eng := syncer.New(cfg, db, as, hub, notifier, bs, m, log)
	om.OnReauth = func(ctx context.Context, a *store.Account, reason string) {
		_ = db.SetAccountStatus(ctx, a.ID, store.StatusAuthError, "OAuth2 authorisation expired or revoked ("+reason+"): reconnect the account")
		notifier.Notify(ctx, a.UserID, a.ID, events.KindReauth, "Reconnexion nécessaire : "+a.Email, "L'autorisation OAuth2 a expiré ou a été révoquée.")
	}
	ob := outbox.New(cfg, db, as, eng, bs, hub, notifier, m, log)
	am := auth.NewManager(db, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	am.Keyring = kr
	srv := &api.Server{Cfg: cfg, DB: db, KR: kr, Auth: am, Accounts: as, OAuth: om, Engine: eng, Outbox: ob, Blobs: bs, Hub: hub, Metrics: m, Log: log, Static: web.Handler(cfg.AppName)}
	m.Extra = func(w io.Writer) { writeDBMetrics(w, db) }
	return &App{Cfg: cfg, Log: log, DB: db, KR: kr, Blobs: bs, Auth: am, OAuth: om, Engine: eng, Outbox: ob, API: srv, Metrics: m, Handler: srv.Handler()}, nil
}

// Start launches background services.
func (a *App) Start(ctx context.Context) error {
	if created, err := auth.EnsureAdmin(ctx, a.DB, a.Cfg.AdminUser, a.Cfg.AdminPassword); err != nil {
		return fmt.Errorf("bootstrap admin user: %w", err)
	} else if created {
		a.Log.Info("admin user created from LIGHTMAIL_ADMIN_PASSWORD", "username", a.Cfg.AdminUser)
	}
	if n, _ := a.DB.CountUsers(ctx); n == 0 {
		a.Log.Warn("no user exists: set LIGHTMAIL_ADMIN_PASSWORD or run `lightmail user create <name>`")
	}
	ctx, a.cancel = context.WithCancel(ctx)
	if err := a.Engine.Start(ctx); err != nil {
		return err
	}
	a.wg.Add(2)
	go func() { defer a.wg.Done(); a.Outbox.Run(ctx) }()
	go func() { defer a.wg.Done(); a.housekeeping(ctx) }()
	return nil
}

// Stop shuts services down; in-flight sends are allowed to finish.
func (a *App) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	a.Engine.Stop()
	a.wg.Wait()
	a.DB.Close()
}

func (a *App) housekeeping(ctx context.Context) {
	drafts := time.NewTicker(20 * time.Second)
	defer drafts.Stop()
	purge := time.NewTicker(10 * time.Minute)
	defer purge.Stop()
	a.cleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-drafts.C:
			a.Outbox.SyncDrafts(ctx, a.API.BuildDraft)
		case <-purge.C:
			a.cleanup(ctx)
		}
	}
}

// cleanup purges expired sessions, stale uploads, and evicts the
// attachment cache beyond its size/age budget.
func (a *App) cleanup(ctx context.Context) {
	_ = a.DB.PurgeExpired(ctx, a.Auth.IdleCutoff())
	_ = a.DB.Optimize(ctx)
	a.Auth.GC()
	_ = a.DB.PurgeUploads(ctx, time.Now().Add(-7*24*time.Hour).Unix())
	if n, err := a.DB.PruneBodies(ctx, time.Now().Add(-a.Cfg.CacheTTL).Unix()); err == nil && n > 0 {
		a.Log.Info("pruned cached message bodies", "count", n)
	}
	evict, err := a.DB.BlobsToEvict(ctx, a.Cfg.CacheMaxBytes, time.Now().Add(-a.Cfg.CacheTTL).Unix())
	if err == nil && len(evict) > 0 {
		for _, sha := range evict {
			_ = a.Blobs.Delete(sha)
		}
		_ = a.DB.DeleteBlobs(ctx, evict)
		a.Log.Info("evicted cached files", "count", len(evict))
	}
	// Orphan files (e.g. crash between write and DB insert).
	known, err := a.DB.AllBlobSHAs(ctx)
	if err == nil {
		_ = a.Blobs.Walk(func(sha string, size int64) error {
			if !known[sha] {
				_ = a.Blobs.Delete(sha)
			}
			return nil
		})
	}
}
