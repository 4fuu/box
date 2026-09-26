// Package approve is the in-memory queue of computers waiting to join.
// The approval code is never stored or logged. Submit takes a secret.Hash;
// Approve hashes the code a person typed.
package approve

import (
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/4fuu/box/internal/secret"
)

const (
	joinTTL       = 10 * time.Minute
	submitWindow  = 10 * time.Minute
	submitLimit   = 5
	approveWindow = time.Minute
	approveLimit  = 5
	maxAttempts   = 5
)

var (
	ErrName     = errors.New("illegal computer name")
	ErrExists   = errors.New("join already pending")
	ErrNotFound = errors.New("no matching join")
	ErrExpired  = errors.New("join expired")
	ErrAttempts = errors.New("too many attempts")
	ErrRate     = errors.New("rate limited")
)

// Pending is one computer waiting for approval. The code hash is not here.
type Pending struct {
	Name      string
	Addr      string
	User      string
	ExpiresAt time.Time
}

type join struct {
	name      string
	addr      string
	user      string
	codeHash  string
	expiresAt time.Time
	attempts  int
}

// Queue is an in-memory pending-join list. It is safe for concurrent use.
type Queue struct {
	mu      sync.Mutex
	now     func() time.Time
	joins   []join
	submits map[string][]time.Time
	fails   map[string][]time.Time
}

func New() *Queue {
	return &Queue{
		now:     time.Now,
		submits: make(map[string][]time.Time),
		fails:   make(map[string][]time.Time),
	}
}

// SetNow replaces the clock. Tests use it; a nil fn keeps the current clock.
func (q *Queue) SetNow(fn func() time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if fn != nil {
		q.now = fn
	}
}

// Submit parks a join. codeHash is already secret.Hash(code).
// ErrExists if that name is already pending. ErrName if the name is illegal.
// ErrRate if this remote address has submitted too many times.
func (q *Queue) Submit(name, addr, user, codeHash string) error {
	if !legalName(name) {
		return ErrName
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	// Retries of a live name are attempts: they spend the same per-address budget.
	if q.recent(q.submits, addr, now, submitWindow) >= submitLimit {
		return ErrRate
	}
	q.note(q.submits, addr, now)
	if i := q.index(name); i >= 0 {
		if now.Before(q.joins[i].expiresAt) {
			return ErrExists
		}
		q.removeAt(i)
	}
	q.joins = append(q.joins, join{
		name:      name,
		addr:      addr,
		user:      user,
		codeHash:  codeHash,
		expiresAt: now.Add(joinTTL),
	})
	return nil
}

// List returns unexpired joins, without the code hash.
func (q *Queue) List() []Pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	out := make([]Pending, 0, len(q.joins))
	for i := range q.joins {
		if now.Before(q.joins[i].expiresAt) {
			out = append(out, q.joins[i].public())
		}
	}
	return out
}

// Approve hashes code and takes the matching live join.
// Five mismatches against the only live join drop it (ErrAttempts).
// A miss against several live joins returns ErrNotFound and drops none.
// ErrExpired if the hash matches an expired join, which is then dropped.
// More than five failed calls for fromAddr inside a minute return ErrRate.
func (q *Queue) Approve(code, fromAddr string) (Pending, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	want := secret.Hash(code)

	liveMatch := -1
	expiredMatch := -1
	liveCount := 0
	sole := -1
	for i := range q.joins {
		eq := hashEq(q.joins[i].codeHash, want)
		if !now.Before(q.joins[i].expiresAt) {
			if eq && expiredMatch < 0 {
				expiredMatch = i
			}
			continue
		}
		liveCount++
		sole = i
		if eq && liveMatch < 0 {
			liveMatch = i
		}
	}
	// A live match is not a failed attempt, so the per-address limit does not apply.
	if liveMatch >= 0 {
		p := q.joins[liveMatch].public()
		q.removeAt(liveMatch)
		return p, nil
	}
	if q.recent(q.fails, fromAddr, now, approveWindow) >= approveLimit {
		return Pending{}, ErrRate
	}
	q.note(q.fails, fromAddr, now)
	if expiredMatch >= 0 {
		q.removeAt(expiredMatch)
		return Pending{}, ErrExpired
	}
	// Several live joins: the miss does not identify which one was guessed.
	if liveCount == 1 {
		q.joins[sole].attempts++
		if q.joins[sole].attempts >= maxAttempts {
			q.removeAt(sole)
			return Pending{}, ErrAttempts
		}
	}
	return Pending{}, ErrNotFound
}

// Drop removes a pending join by name. Missing names are ignored.
func (q *Queue) Drop(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if i := q.index(name); i >= 0 {
		q.removeAt(i)
	}
}

func (j join) public() Pending {
	return Pending{Name: j.name, Addr: j.addr, User: j.user, ExpiresAt: j.expiresAt}
}

func (q *Queue) index(name string) int {
	for i := range q.joins {
		if q.joins[i].name == name {
			return i
		}
	}
	return -1
}

// removeAt clears the last slot so a dropped code hash does not linger in the backing array.
func (q *Queue) removeAt(i int) {
	copy(q.joins[i:], q.joins[i+1:])
	q.joins[len(q.joins)-1] = join{}
	q.joins = q.joins[:len(q.joins)-1]
}

func (q *Queue) recent(bucket map[string][]time.Time, key string, now time.Time, window time.Duration) int {
	kept := within(bucket[key], now, window)
	if len(kept) == 0 {
		delete(bucket, key)
		return 0
	}
	bucket[key] = kept
	return len(kept)
}

func (q *Queue) note(bucket map[string][]time.Time, key string, now time.Time) {
	bucket[key] = append(bucket[key], now)
}

func within(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	n := 0
	for _, at := range ts {
		if at.After(cutoff) {
			ts[n] = at
			n++
		}
	}
	return ts[:n]
}

func hashEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func legalName(name string) bool {
	switch name {
	case "", "box", "pair", "join":
		return false
	}
	if strings.Contains(name, "+") || strings.Contains(name, ".") || strings.Contains(name, "/") {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
