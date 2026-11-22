// Package ratelimit counts authentication failures so guessing slows down.
// An address that keeps failing is refused for a while, and one that keeps
// failing for an hour is refused for the rest of it.
//
// A refused attempt is dropped where it stands: nothing is slept, logged, or
// written back, so a guessing client costs a map lookup and a closed socket.
package ratelimit

import (
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	// Burst is how many failures one address may have inside Window. Every
	// unbound key offered counts, so an agent with many keys, or a home
	// behind one shared address, spends the burst faster than one person
	// would; a small burst bans them for trying to sign in.
	Burst = 20
	// Window is how long Burst failures are counted.
	Window = 5 * time.Minute
	// MaxBan caps a ban. The next failure after it ends bans again, so an
	// address that keeps failing is banned most of the time.
	MaxBan = 30 * time.Minute
	// Quiet clears an address's history after this long without a failure.
	Quiet = time.Hour
	// MaxEntries caps memory when many addresses fail at once. The least
	// recently failed entry is dropped to make room.
	MaxEntries = 10000
)

// Limiter is safe for concurrent use. Its clock can be replaced by tests.
type Limiter struct {
	mu      sync.Mutex
	entries map[netip.Addr]*entry
	now     func() time.Time
	swept   time.Time
}

type entry struct {
	failures int
	first    time.Time // start of the window the failures are counted in
	last     time.Time
	until    time.Time // banned until
	bans     int
}

func New() *Limiter {
	return &Limiter{
		entries: map[netip.Addr]*entry{},
		now:     time.Now,
	}
}

// SetNow replaces the clock. Tests use it.
func (l *Limiter) SetNow(fn func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fn != nil {
		l.now = fn
	}
}

// Banned reports whether an address is refused, and how long that lasts.
func (l *Limiter) Banned(addr netip.Addr) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[addr]
	if e == nil {
		return 0, false
	}
	now := l.now()
	if now.Before(e.until) {
		return e.until.Sub(now), true
	}
	return 0, false
}

// Fail records a refused attempt and returns the ban it earned, if any.
// The ban doubles with each failure past Burst, up to MaxBan, and ends the
// counting window so the next failure starts a new one.
func (l *Limiter) Fail(addr netip.Addr) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	e := l.entries[addr]
	if e == nil {
		if len(l.entries) >= MaxEntries {
			l.evict()
		}
		e = &entry{first: now}
		l.entries[addr] = e
	}
	// A new entry, or one whose last failure banned it, starts a window. An
	// entry from before the window starts a new one with no failures.
	if e.first.IsZero() {
		e.first = now
	} else if now.Sub(e.first) > Window {
		e.failures = 0
		e.first = now
	}
	if e.failures < 1<<20 {
		e.failures++
	}
	e.last = now
	if e.failures <= Burst {
		return 0, false
	}
	e.bans++
	ban := MaxBan
	if shift := e.bans - 1; shift < 10 {
		ban = min(Window<<shift, MaxBan)
	}
	e.until = now.Add(ban)
	e.failures = 0
	e.first = time.Time{}
	return ban, true
}

// Forget clears an address after it authenticated.
func (l *Limiter) Forget(addr netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, addr)
}

// Counts returns the addresses now held.
func (l *Limiter) Counts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// sweep drops addresses quiet for Quiet, and their bans with them. It runs
// once every Quiet at most, so a flood of failures costs a comparison.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < Quiet {
		return
	}
	l.swept = now
	for addr, e := range l.entries {
		if now.Sub(e.last) > Quiet && !now.Before(e.until) {
			delete(l.entries, addr)
		}
	}
}

func (l *Limiter) evict() {
	var oldest netip.Addr
	var at time.Time
	for addr, e := range l.entries {
		if at.IsZero() || e.last.Before(at) {
			oldest, at = addr, e.last
		}
	}
	delete(l.entries, oldest)
}

// Address is the rate limit key for a client: its IP, with an IPv4-mapped
// IPv6 address folded to IPv4 so one host has one key. ok is false for a
// missing or unparsable address, which is never limited.
func Address(remote string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(strings.TrimSpace(remote))
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}
