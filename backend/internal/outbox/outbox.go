// Package outbox implements the persistent send queue.
//
// A message is first written to the blob store and queued with a short
// "undo" delay. Delivery is attempted by a bounded number of workers.
// Retries only happen when the server cannot have accepted the message
// (temporary reply or failure before the end of DATA). When the outcome is
// unknown (connection lost after the final dot, or process crash while
// sending), the item becomes "ambiguous": the Sent folder is searched for
// its Message-ID, and if it cannot be found the user decides whether to
// send it again.
package outbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/accounts"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/events"
	"lightmail/internal/mailauth"
	"lightmail/internal/metrics"
	"lightmail/internal/mime"
	"lightmail/internal/providers"
	"lightmail/internal/smtpx"
	"lightmail/internal/store"
	"lightmail/internal/syncer"
)

const maxAttempts = 6

type Service struct {
	db       *store.DB
	accts    *accounts.Service
	eng      *syncer.Engine
	blobs    *blobs.Store
	hub      *events.Hub
	notifier *events.Notifier
	log      *slog.Logger
	cfg      *config.Config
	m        *metrics.Registry
	sem      chan struct{}
	wake     chan struct{}
	wg       sync.WaitGroup
	// UndoDelay can be overridden (tests).
	UndoDelay time.Duration
	// AmbiguousCheckDelay is the wait before searching the Sent folder.
	AmbiguousCheckDelay time.Duration
}

func New(cfg *config.Config, db *store.DB, accts *accounts.Service, eng *syncer.Engine, bs *blobs.Store, hub *events.Hub, n *events.Notifier, m *metrics.Registry, log *slog.Logger) *Service {
	return &Service{
		db: db, accts: accts, eng: eng, blobs: bs, hub: hub, notifier: n, log: log, cfg: cfg, m: m,
		sem: make(chan struct{}, max(1, cfg.SMTPMaxConcurrent)), wake: make(chan struct{}, 1),
		UndoDelay: cfg.UndoSendDelay, AmbiguousCheckDelay: 20 * time.Second,
	}
}

// Meta links an outgoing message to local state.
type Meta struct {
	Mode       string // new | reply | replyall | forward
	RelatedMsg int64
	DraftID    int64
	SendAt     time.Time // zero = now + undo delay
}

var ErrTooLarge = errors.New("message exceeds the maximum size")

// Enqueue builds the message and queues it.
func (s *Service) Enqueue(ctx context.Context, userID int64, a *store.Account, out *mime.Outgoing, meta Meta) (*store.OutboxItem, error) {
	if err := out.Validate(); err != nil {
		return nil, err
	}
	if out.MessageID == "" {
		out.MessageID = mime.NewMessageID(a.Email)
	}
	var buf bytes.Buffer
	if err := out.Build(&buf); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > s.cfg.MaxUploadBytes {
		return nil, fmt.Errorf("%w (%d MB)", ErrTooLarge, s.cfg.MaxUploadBytes>>20)
	}
	sha, size, err := s.blobs.Put(&buf, 0)
	if err != nil {
		return nil, err
	}
	_ = s.db.TouchBlob(ctx, sha, size)
	at := meta.SendAt
	if at.IsZero() {
		at = time.Now().Add(s.UndoDelay)
	}
	item := &store.OutboxItem{
		UserID: userID, AccountID: a.ID, Status: store.OutboxScheduled, MessageIDHdr: "<" + out.MessageID + ">",
		Subject: out.Subject, EnvelopeFrom: out.From.Addr, Rcpts: out.Recipients(), RawSHA256: sha,
		RelatedMsg: meta.RelatedMsg, Mode: meta.Mode, DraftID: meta.DraftID, NextAttemptAt: at.Unix(),
	}
	if err := s.db.CreateOutbox(ctx, item); err != nil {
		return nil, err
	}
	s.publish(item)
	s.Wake()
	return item, nil
}

// Cancel aborts a scheduled send during the undo window.
func (s *Service) Cancel(ctx context.Context, userID, id int64) error {
	if err := s.db.TransitionOutbox(ctx, userID, id, []string{store.OutboxScheduled, store.OutboxRetry}, store.OutboxCanceled, 0); err != nil {
		return err
	}
	if it, err := s.db.OutboxItem(ctx, id); err == nil {
		s.publish(it)
	}
	return nil
}

// Retry re-queues a failed or ambiguous message at the user's request.
func (s *Service) Retry(ctx context.Context, userID, id int64) error {
	if err := s.db.TransitionOutbox(ctx, userID, id, []string{store.OutboxFailed, store.OutboxAmbiguous}, store.OutboxScheduled, time.Now().Unix()); err != nil {
		return err
	}
	s.Wake()
	return nil
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run processes the queue until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	if n, err := s.db.RecoverInterruptedSends(ctx); err == nil && n > 0 {
		s.log.Warn("messages interrupted while sending marked ambiguous", "count", n)
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	copyTick := time.NewTicker(2 * time.Minute)
	defer copyTick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-t.C:
		case <-s.wake:
		case <-copyTick.C:
			s.retrySentCopies(ctx)
			continue
		}
		free := cap(s.sem) - len(s.sem)
		if free <= 0 {
			continue
		}
		items, err := s.db.ClaimDueOutbox(ctx, free)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("outbox claim", "err", err)
			}
			continue
		}
		for _, it := range items {
			s.sem <- struct{}{}
			s.wg.Add(1)
			go func(it *store.OutboxItem) {
				defer func() { <-s.sem; s.wg.Done() }()
				// Sending must complete even during shutdown.
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
				defer cancel()
				s.send(sctx, it)
			}(it)
		}
	}
}

func (s *Service) send(ctx context.Context, it *store.OutboxItem) {
	log := s.log.With("outbox_id", it.ID, "account_id", it.AccountID)
	s.publish(it)
	a, err := s.db.Account(ctx, it.AccountID)
	if err != nil {
		s.fail(ctx, it, "account no longer exists", false)
		return
	}
	f, err := s.blobs.Open(it.RawSHA256)
	if err != nil {
		s.fail(ctx, it, "message content is missing", false)
		return
	}
	defer f.Close()
	c, err := s.accts.DialSMTP(ctx, a)
	if err != nil {
		s.m.SMTPErrors.Inc()
		s.handleFailure(ctx, log, it, a, err)
		return
	}
	defer c.Close()
	if max := c.MaxSize(); max > 0 {
		if st, err := f.Stat(); err == nil && st.Size() > int64(max) {
			s.fail(ctx, it, fmt.Sprintf("message size %d MB exceeds the server limit of %d MB", st.Size()>>20, max>>20), true)
			return
		}
	}
	err = c.Send(it.EnvelopeFrom, it.Rcpts, f)
	if err != nil {
		s.m.SMTPErrors.Inc()
		s.handleFailure(ctx, log, it, a, err)
		return
	}
	c.Quit()
	s.m.SMTPSent.Inc()
	log.Info("message sent", "recipients", len(it.Rcpts))
	s.markSent(ctx, it, a)
}

func (s *Service) handleFailure(ctx context.Context, log *slog.Logger, it *store.OutboxItem, a *store.Account, err error) {
	msg := accounts.Explain(err)
	switch smtpx.OutcomeOf(err) {
	case smtpx.Ambiguous:
		log.Warn("send outcome unknown", "err", msg)
		it.Status, it.LastError = store.OutboxAmbiguous, "connection lost after the message was transmitted: "+msg
		_ = s.db.UpdateOutbox(ctx, it)
		s.publish(it)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.resolveAmbiguous(context.WithoutCancel(ctx), it, a)
		}()
	case smtpx.Temporary:
		if it.Attempts >= maxAttempts {
			s.fail(ctx, it, msg, true)
			return
		}
		delay := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour}[min(it.Attempts-1, 4)]
		log.Warn("temporary send failure, will retry", "err", msg, "retry_in", delay.String())
		it.Status, it.LastError, it.NextAttemptAt = store.OutboxRetry, msg, time.Now().Add(delay).Unix()
		_ = s.db.UpdateOutbox(ctx, it)
		s.publish(it)
	default:
		if mailauth.IsAuthError(err) {
			_ = s.db.SetAccountStatus(ctx, a.ID, store.StatusAuthError, msg)
		}
		s.fail(ctx, it, msg, true)
	}
}

func (s *Service) fail(ctx context.Context, it *store.OutboxItem, msg string, notify bool) {
	it.Status, it.LastError = store.OutboxFailed, msg
	_ = s.db.UpdateOutbox(ctx, it)
	s.publish(it)
	if notify {
		s.notifier.Notify(ctx, it.UserID, it.AccountID, events.KindSendFailed, "Échec d'envoi : "+it.Subject, msg)
	}
}

// resolveAmbiguous looks for the message in the Sent folder: providers that
// store submitted mail there let us confirm delivery without resending.
func (s *Service) resolveAmbiguous(ctx context.Context, it *store.OutboxItem, a *store.Account) {
	time.Sleep(s.AmbiguousCheckDelay)
	found, err := s.eng.FolderHasMessageID(ctx, a.ID, store.RoleSent, it.MessageIDHdr)
	if err == nil && found {
		s.log.Info("ambiguous send confirmed by Sent folder", "outbox_id", it.ID)
		it.CopiedToSent = true
		s.markSent(ctx, it, a)
		return
	}
	s.notifier.Notify(ctx, it.UserID, it.AccountID, events.KindSendAmbiguous, "Envoi incertain : "+it.Subject,
		"La connexion a été interrompue après la transmission du message. Il a peut-être été envoyé. Vérifiez vos messages envoyés avant de le renvoyer.")
}

func (s *Service) markSent(ctx context.Context, it *store.OutboxItem, a *store.Account) {
	it.Status, it.SentAt, it.LastError = store.OutboxSent, time.Now().Unix(), ""
	if !it.CopiedToSent {
		it.CopiedToSent = s.copyToSent(ctx, it, a)
	}
	_ = s.db.UpdateOutbox(ctx, it)
	s.publish(it)
	if it.RelatedMsg != 0 {
		if m, err := s.db.MessageW(ctx, it.RelatedMsg); err == nil {
			act := syncer.ActAnswered
			if it.Mode == "forward" {
				act = syncer.ActForwarded
			}
			s.eng.Apply(ctx, it.UserID, []*store.Message{m}, act, 0)
		}
	}
	if it.DraftID != 0 {
		if d, err := s.db.DraftW(ctx, it.DraftID); err == nil {
			if d.ServerFolder != 0 && d.ServerUID != 0 {
				_ = s.eng.DeleteUID(ctx, d.ServerFolder, d.ServerUID)
			}
			_ = s.db.DeleteDraft(ctx, d.UserID, d.ID)
		}
	}
	s.eng.Trigger(a.ID, false)
}

// copyToSent appends the message to the Sent folder unless the provider
// does it itself or a copy is already there.
func (s *Service) copyToSent(ctx context.Context, it *store.OutboxItem, a *store.Account) bool {
	if p := providers.Get(a.Provider); (p != nil && p.SMTPSavesSent) || a.SMTPSavesSent {
		return true
	}
	if found, err := s.eng.FolderHasMessageID(ctx, a.ID, store.RoleSent, it.MessageIDHdr); err == nil && found {
		return true
	}
	f, err := s.blobs.Open(it.RawSHA256)
	if err != nil {
		return false
	}
	raw, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return false
	}
	if _, _, err := s.eng.AppendTo(ctx, a.ID, store.RoleSent, raw, []imap.Flag{imap.FlagSeen}); err != nil {
		s.log.Warn("copy to Sent failed, will retry", "outbox_id", it.ID, "err", err)
		if strings.Contains(strings.ToUpper(err.Error()), "OVERQUOTA") {
			s.notifier.Notify(ctx, it.UserID, a.ID, events.KindQuota, "Quota dépassé : "+a.Email, "Le message a été envoyé mais n'a pas pu être copié dans Envoyés : la boîte est pleine.")
		}
		return false
	}
	return true
}

func (s *Service) retrySentCopies(ctx context.Context) {
	items, err := s.db.PendingSentCopies(ctx)
	if err != nil {
		return
	}
	for _, it := range items {
		a, err := s.db.Account(ctx, it.AccountID)
		if err != nil {
			continue
		}
		if s.copyToSent(ctx, it, a) {
			it.CopiedToSent = true
			_ = s.db.UpdateOutbox(ctx, it)
		}
	}
}

func (s *Service) publish(it *store.OutboxItem) {
	s.hub.Publish(it.UserID, events.Event{Type: events.TypeOutbox, Data: it})
}

// ---- drafts ----------------------------------------------------------

// SyncDrafts uploads changed drafts to the server Drafts folder,
// replacing the previous server copy. Runs periodically.
func (s *Service) SyncDrafts(ctx context.Context, build func(context.Context, *store.Draft) (*mime.Outgoing, *store.Account, error)) {
	drafts, err := s.db.UnsyncedDrafts(ctx, time.Now().Add(-5*time.Second).Unix())
	if err != nil {
		return
	}
	for _, d := range drafts {
		out, a, err := build(ctx, d)
		if err != nil {
			continue
		}
		if out.MessageID == "" {
			out.MessageID = mime.NewMessageID(a.Email)
		}
		var buf bytes.Buffer
		if err := out.Build(&buf); err != nil {
			continue
		}
		f, uid, err := s.eng.AppendTo(ctx, a.ID, store.RoleDrafts, buf.Bytes(), []imap.Flag{imap.FlagDraft, imap.FlagSeen})
		if err != nil {
			s.log.Debug("draft upload failed", "draft_id", d.ID, "err", err)
			continue
		}
		if d.ServerFolder != 0 && d.ServerUID != 0 && (d.ServerFolder != f.ID || d.ServerUID != uid) {
			_ = s.eng.DeleteUID(ctx, d.ServerFolder, d.ServerUID)
		}
		_ = s.db.SetDraftServerCopy(ctx, d.ID, f.ID, uid, d.UpdatedAt)
		s.eng.Trigger(a.ID, false)
	}
}
