package auth

import (
	"sync"
	"time"
)

// Limiter tracks failed login attempts per key (IP address or username)
// and imposes an exponential lockout.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
	free    int           // failures allowed before lockout
	base    time.Duration // first lockout duration
	max     time.Duration
	window  time.Duration // failures older than this are forgotten
	now     func() time.Time
}

type limitEntry struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

func NewLimiter(free int, base, max, window time.Duration) *Limiter {
	return &Limiter{entries: map[string]*limitEntry{}, free: free, base: base, max: max, window: window, now: time.Now}
}

// Allow reports whether an attempt may proceed, and if not, for how long
// the key is locked.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return true, 0
	}
	n := l.now()
	if n.Sub(e.lastFailure) > l.window {
		delete(l.entries, key)
		return true, 0
	}
	if n.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(n)
	}
	return true, 0
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.now()
	e := l.entries[key]
	if e == nil || n.Sub(e.lastFailure) > l.window {
		e = &limitEntry{}
		l.entries[key] = e
	}
	e.failures++
	e.lastFailure = n
	if over := e.failures - l.free; over > 0 {
		d := l.base << min(over-1, 16)
		if d > l.max || d <= 0 {
			d = l.max
		}
		e.lockedUntil = n.Add(d)
	}
	if len(l.entries) > 100_000 { // memory bound under distributed attacks
		l.gcLocked(n)
	}
}

// Succeed clears the key.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func (l *Limiter) GC() {
	l.mu.Lock()
	l.gcLocked(l.now())
	l.mu.Unlock()
}

func (l *Limiter) gcLocked(n time.Time) {
	for k, e := range l.entries {
		if n.Sub(e.lastFailure) > l.window && n.After(e.lockedUntil) {
			delete(l.entries, k)
		}
	}
}
