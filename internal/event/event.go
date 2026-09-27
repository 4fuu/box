// Package event is the server's in-memory event log.
// A computer, an SSH client, or the event HTTP API appends a line.
// The log is not written to disk. When it is full, the oldest line is dropped.
package event

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// MaxEvents is the ring size. Older lines are dropped.
	MaxEvents = 256
	// MaxBody is the largest event text, in bytes.
	MaxBody = 4096
)

var (
	topicRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	fromRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// Item is one published line. ID increases and is never reused.
type Item struct {
	ID    int64     `json:"id"`
	Topic string    `json:"topic"`
	Body  string    `json:"body"`
	From  string    `json:"from"`
	Time  time.Time `json:"time"`
}

// Bus is the ring. It is safe for concurrent use.
type Bus struct {
	mu   sync.Mutex
	next int64
	buf  []Item
	now  func() time.Time
}

// New returns an empty log.
func New() *Bus {
	return &Bus{now: func() time.Time { return time.Now().UTC() }}
}

// SetNow replaces the clock. Tests use it.
func (b *Bus) SetNow(fn func() time.Time) {
	if fn == nil {
		return
	}
	b.mu.Lock()
	b.now = fn
	b.mu.Unlock()
}

// Publish appends one line and returns it with its id.
func (b *Bus) Publish(from, topic, body string) (Item, error) {
	topic = strings.TrimSpace(topic)
	body = strings.TrimSpace(body)
	from = strings.TrimSpace(from)
	if !topicRe.MatchString(topic) {
		return Item{}, errors.New("invalid topic")
	}
	if body == "" || len(body) > MaxBody || strings.ContainsAny(body, "\r\n\t") {
		return Item{}, errors.New("invalid event")
	}
	if !fromRe.MatchString(from) {
		return Item{}, errors.New("invalid event")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	item := Item{ID: b.next, Topic: topic, Body: body, From: from, Time: b.now().UTC()}
	if len(b.buf) == MaxEvents {
		copy(b.buf, b.buf[1:])
		b.buf = b.buf[:len(b.buf)-1]
	}
	b.buf = append(b.buf, item)
	return item, nil
}

// Since returns lines with id greater than since, oldest first.
// An empty topic returns every topic.
func (b *Bus) Since(since int64, topic string) []Item {
	topic = strings.TrimSpace(topic)
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Item{}
	for _, item := range b.buf {
		if item.ID <= since {
			continue
		}
		if topic != "" && item.Topic != topic {
			continue
		}
		out = append(out, item)
	}
	return out
}
