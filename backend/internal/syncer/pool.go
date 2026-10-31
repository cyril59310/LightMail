package syncer

import (
	"context"
	"sync"
	"time"

	"lightmail/internal/imapx"
)

// pool keeps a few authenticated IMAP connections per account for the sync
// loop and on-demand operations, closing idle ones after idleTTL.
type pool struct {
	dial    func(ctx context.Context) (*imapx.Client, error)
	onClose func()
	slots   chan struct{}
	mu      sync.Mutex
	idle    []pooled
	idleTTL time.Duration
	closed  bool
}

type pooled struct {
	c    *imapx.Client
	used time.Time
}

func newPool(max int, idleTTL time.Duration, dial func(ctx context.Context) (*imapx.Client, error), onClose func()) *pool {
	if max < 1 {
		max = 1
	}
	return &pool{dial: dial, onClose: onClose, slots: make(chan struct{}, max), idleTTL: idleTTL}
}

// get returns a connection; release must be called with healthy=false when
// the connection may be in an unknown state.
func (p *pool) get(ctx context.Context) (*imapx.Client, func(healthy bool), error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	var c *imapx.Client
	p.mu.Lock()
	for len(p.idle) > 0 {
		last := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		if !last.c.IsClosed() {
			c = last.c
			break
		}
		p.onClose()
	}
	p.mu.Unlock()
	if c == nil {
		var err error
		c, err = p.dial(ctx)
		if err != nil {
			<-p.slots
			return nil, nil, err
		}
	}
	released := false
	return c, func(healthy bool) {
		if released {
			return
		}
		released = true
		p.mu.Lock()
		if healthy && !p.closed && !c.IsClosed() {
			p.idle = append(p.idle, pooled{c, time.Now()})
			p.mu.Unlock()
		} else {
			p.mu.Unlock()
			go func() { c.Logout(); p.onClose() }()
		}
		<-p.slots
	}, nil
}

// reap closes connections idle for longer than idleTTL.
func (p *pool) reap() {
	p.mu.Lock()
	var keep []pooled
	var drop []pooled
	for _, x := range p.idle {
		if time.Since(x.used) > p.idleTTL || x.c.IsClosed() {
			drop = append(drop, x)
		} else {
			keep = append(keep, x)
		}
	}
	p.idle = keep
	p.mu.Unlock()
	for _, x := range drop {
		x.c.Logout()
		p.onClose()
	}
}

func (p *pool) close() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, x := range idle {
		x.c.Logout()
		p.onClose()
	}
}
