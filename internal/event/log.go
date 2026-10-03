package event

import (
	"context"
	"sync"
	"time"
)

// DB is the SQLite side of the log. *store.Store implements it.
type DB interface {
	InsertEvent(from string, tokenID *int64, topic string, body []byte, key string) (Item, bool, error)
	QueryEvents(q Query) (Result, error)
	EventWindow() (oldest, latest int64, err error)
}

// Waiter is the publish broadcast. Every wake closes the channel handed out
// so far and hands out a fresh one; a long poll selects on the channel it
// already holds, so one closed channel wakes every waiter once.
type Waiter struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewWaiter returns a Waiter no one has woken yet.
func NewWaiter() *Waiter { return &Waiter{ch: make(chan struct{})} }

// Chan is the channel that closes on the next Wake.
func (w *Waiter) Chan() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

// Wake closes the current channel and starts a new one.
func (w *Waiter) Wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.ch)
	w.ch = make(chan struct{})
}

// Log is the durable event log: validation and the wait loop live here,
// storage in SQLite. It is safe for concurrent use.
type Log struct {
	db   DB
	wake *Waiter
}

// NewLog returns a log backed by db.
func NewLog(db DB) *Log { return &Log{db: db, wake: NewWaiter()} }

// Publish validates and stores one event and returns it with its id.
// tokenID records which access token published, when one did. When key is
// set and the same from published the same key while the original is still
// retained, the original comes back with duplicate set and nothing is stored.
func (l *Log) Publish(from string, tokenID *int64, topic string, body []byte, key string) (Item, bool, error) {
	if !ValidFrom(from) {
		return Item{}, false, ErrInvalidFrom
	}
	if err := ValidTopic(topic); err != nil {
		return Item{}, false, err
	}
	if len(body) > MaxBody {
		return Item{}, false, ErrBodyTooLarge
	}
	if key != "" {
		if err := ValidKey(key); err != nil {
			return Item{}, false, err
		}
	}
	item, dup, err := l.db.InsertEvent(from, tokenID, topic, body, key)
	if err == nil && !dup {
		l.wake.Wake()
	}
	return item, dup, err
}

// Get reads the log. When Wait is set and nothing matches after Since, it
// blocks until a publish wakes it or the wait runs out, then returns —
// possibly empty, always with the retained window. A wake re-queries SQLite;
// the wait budget spans every wake-up. ctx ends the wait early: a client
// gone, a closed stream, or server shutdown.
func (l *Log) Get(ctx context.Context, q Query) (Result, error) {
	q.Topics = append([]string(nil), q.Topics...)
	q.Froms = append([]string(nil), q.Froms...)
	if _, err := ParseFilters(q.Topics); err != nil {
		return Result{}, err
	}
	for _, f := range q.Froms {
		if !ValidFrom(f) {
			return Result{}, ErrInvalidFrom
		}
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
	if q.Wait < 0 {
		q.Wait = 0
	}
	if q.Wait > MaxWait {
		q.Wait = MaxWait
	}
	var deadline time.Time
	if q.Wait > 0 {
		deadline = time.Now().Add(q.Wait)
	}
	for {
		// Take the wake channel before the query: a publish that lands
		// between an empty query and the select must still wake this read.
		ch := l.wake.Chan()
		res, err := l.db.QueryEvents(q)
		if err != nil {
			return res, err
		}
		if len(res.Events) > 0 || q.Wait <= 0 {
			return res, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return res, nil
		}
		timer := time.NewTimer(left)
		select {
		case <-ctx.Done():
			timer.Stop()
			return res, ctx.Err()
		case <-timer.C:
			return res, nil
		case <-ch:
			timer.Stop()
		}
	}
}

// Follow long-polls fetch and hands every result to emit, in order, until
// ctx ends. since is the starting cursor; fromLatest replaces it with the
// newest id first, so the loop begins at "now". gap runs when the log shows
// the caller fell behind retention (since < oldest-1). fetch receives Wait
// already set; emit's error ends the loop.
func Follow(ctx context.Context, q Query, since int64, fromLatest bool, fetch func(context.Context, Query) (Result, error), emit func(Result) error, gap func(since, oldest int64)) error {
	if fromLatest {
		probe := q
		probe.Since = 0
		probe.Limit = 1
		probe.Wait = 0
		res, err := fetch(ctx, probe)
		if err != nil {
			return err
		}
		since = res.Latest
	}
	if q.Wait <= 0 {
		q.Wait = MaxWait
	}
	if q.Wait > MaxWait {
		q.Wait = MaxWait
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		qq := q
		qq.Since = since
		res, err := fetch(ctx, qq)
		if err != nil {
			return err
		}
		if res.Oldest > since+1 && res.Oldest > 0 {
			gap(since, res.Oldest)
		}
		if len(res.Events) > 0 {
			if err := emit(res); err != nil {
				return err
			}
			since = res.Events[len(res.Events)-1].ID
		}
		if res.More {
			continue
		}
	}
}
