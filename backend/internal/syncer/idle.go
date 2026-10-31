package syncer

import (
	"time"

	"context"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// idleLoop keeps a dedicated connection in IMAP IDLE on the inbox and
// triggers an inbox synchronisation when the server reports changes. It
// reconnects with exponential backoff and stops if IDLE is unsupported;
// periodic polling continues in every case.
func (w *worker) idleLoop(ctx context.Context) {
	backoff := 10 * time.Second
	for ctx.Err() == nil {
		err := w.idleSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == errNoIdle {
			w.log.Info("server does not support IDLE, using polling only")
			return
		}
		if err != nil {
			w.log.Debug("IDLE session ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Minute)
	}
}

type idleErr string

func (e idleErr) Error() string { return string(e) }

const errNoIdle = idleErr("IDLE not supported")

func (w *worker) idleSession(ctx context.Context) error {
	inbox, err := w.e.db.FolderByRole(ctx, w.id, "inbox")
	if err != nil {
		return err
	}
	changed := make(chan struct{}, 1)
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	h := &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { signal() },
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				signal()
			}
		},
		Fetch: func(*imapclient.FetchMessageData) { signal() },
	}
	dctx, cancel := context.WithTimeout(ctx, time.Minute)
	c, err := w.dialWith(dctx, h)
	cancel()
	if err != nil {
		return err
	}
	w.e.m.IdleConns.Inc()
	defer func() {
		w.e.m.IdleConns.Dec()
		c.Logout()
		<-w.e.connSem
		w.e.m.IMAPConns.Dec()
	}()
	if !c.Caps.Has(imap.CapIdle) {
		return errNoIdle
	}
	if _, err := c.SelectMailbox(inbox.Name, false); err != nil {
		return err
	}
	// Drain notifications caused by SELECT itself.
	select {
	case <-changed:
	default:
	}
	for ctx.Err() == nil {
		c.SetTimeout(30 * time.Minute)
		cmd, err := c.Idle()
		if err != nil {
			return err
		}
		restart := time.NewTimer(25 * time.Minute) // RFC 2177: re-issue before 29 min
		var fire bool
		select {
		case <-ctx.Done():
		case <-changed:
			fire = true
		case <-restart.C:
		case <-c.Closed():
			restart.Stop()
			return idleErr("connection closed")
		}
		restart.Stop()
		c.ResetTimeout()
		if err := cmd.Close(); err != nil {
			return err
		}
		if err := cmd.Wait(); err != nil {
			return err
		}
		if fire {
			// Debounce bursts (e.g. several messages delivered at once).
			time.Sleep(1500 * time.Millisecond)
			select {
			case <-changed:
			default:
			}
			w.trigger(request{inboxOnly: true})
		}
	}
	return nil
}

func (w *worker) startIdle() {
	if w.idleStop != nil {
		return
	}
	ctx, cancel := context.WithCancel(w.ctx)
	w.idleStop = cancel
	go w.idleLoop(ctx)
}

func (w *worker) stopIdle() {
	if w.idleStop != nil {
		w.idleStop()
		w.idleStop = nil
	}
}
