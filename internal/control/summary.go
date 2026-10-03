package control

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/tunnel"
)

// statTimeout bounds one computer's stat reply in a summary. A slow computer
// shows no load for that refresh; it does not hold up the others.
const statTimeout = time.Second

// recentEvents is how many of the newest events a summary carries.
const recentEvents = 5

// Metrics are the server's own counters since start. The server bumps them;
// Summary reads them. A nil *Metrics counts nothing.
type Metrics struct {
	Started time.Time

	consoles       atomic.Int64
	sessions       atomic.Int64
	sessionsTotal  atomic.Int64
	requests       atomic.Int64
	requestsDenied atomic.Int64
	requestsFailed atomic.Int64
}

func NewMetrics() *Metrics { return &Metrics{Started: time.Now()} }

// Console marks a control session open. Call the returned func when it ends.
func (m *Metrics) Console() func() {
	if m == nil {
		return func() {}
	}
	m.consoles.Add(1)
	return func() { m.consoles.Add(-1) }
}

// Session marks a spliced SSH session open. Call the returned func when it ends.
func (m *Metrics) Session() func() {
	if m == nil {
		return func() {}
	}
	m.sessions.Add(1)
	m.sessionsTotal.Add(1)
	return func() { m.sessions.Add(-1) }
}

// Request counts one portal HTTP request.
func (m *Metrics) Request() {
	if m != nil {
		m.requests.Add(1)
	}
}

// Denied counts a private portal request turned away for a missing token.
func (m *Metrics) Denied() {
	if m != nil {
		m.requestsDenied.Add(1)
	}
}

// Failed counts a portal request that could not reach the computer.
func (m *Metrics) Failed() {
	if m != nil {
		m.requestsFailed.Add(1)
	}
}

// Summary is the live state behind the TUI summary screen. It holds no
// secrets: no tokens, no env values, no event bodies beyond the recent few.
type Summary struct {
	Now      time.Time     `json:"now"`
	Started  time.Time     `json:"started"`
	Server   ServerCounts  `json:"server"`
	Machines []MachineLive `json:"machines"`
	// EventsTotal is the id of the newest event: how many were ever published.
	EventsTotal int64        `json:"events_total"`
	EventsHeld  int          `json:"events_held"`
	Events      []event.Item `json:"events"`
}

// ServerCounts are Metrics as numbers.
type ServerCounts struct {
	Consoles       int64 `json:"consoles"`
	Sessions       int64 `json:"sessions"`
	SessionsTotal  int64 `json:"sessions_total"`
	Requests       int64 `json:"requests"`
	RequestsDenied int64 `json:"requests_denied"`
	RequestsFailed int64 `json:"requests_failed"`
}

// MachineLive is one computer's tunnel and load. Tunnel and Stat are empty
// while it is offline; StatErr says why a live computer has no load.
type MachineLive struct {
	Name    string               `json:"name"`
	Online  bool                 `json:"online"`
	Tunnel  tunnel.ConnStats     `json:"tunnel"`
	Stat    *tunnel.StatResponse `json:"stat,omitempty"`
	StatErr string               `json:"stat_err,omitempty"`
}

// Summary gathers server counters, every computer's tunnel counters, and a
// stat from each live agent, asked in parallel with a short timeout.
func (s *Service) Summary(ctx context.Context) (Summary, error) {
	sum := Summary{Now: time.Now(), Machines: []MachineLive{}, Events: []event.Item{}}
	if m := s.Metrics; m != nil {
		sum.Started = m.Started
		sum.Server = ServerCounts{
			Consoles:       m.consoles.Load(),
			Sessions:       m.sessions.Load(),
			SessionsTotal:  m.sessionsTotal.Load(),
			Requests:       m.requests.Load(),
			RequestsDenied: m.requestsDenied.Load(),
			RequestsFailed: m.requestsFailed.Load(),
		}
	}
	list, err := s.Store.ListComputers()
	if err != nil {
		return sum, err
	}
	sum.Machines = make([]MachineLive, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		ml := &sum.Machines[i]
		ml.Name = c.Name
		conn := s.liveGet(c.Name)
		if conn == nil {
			continue
		}
		ml.Online = true
		ml.Tunnel = conn.Stats()
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, statTimeout)
			defer cancel()
			var st tunnel.StatResponse
			if err := conn.Call(cctx, tunnel.OpStat, tunnel.StatRequest{}, &st); err != nil {
				ml.StatErr = err.Error()
				return
			}
			ml.Stat = &st
		}()
	}
	wg.Wait()
	if s.Events != nil {
		held, err := s.Store.EventCount()
		if err != nil {
			return sum, err
		}
		_, latest, err := s.Store.EventWindow()
		if err != nil {
			return sum, err
		}
		sum.EventsHeld = int(held)
		sum.EventsTotal = latest
		recent, err := s.Store.RecentEvents(recentEvents)
		if err != nil {
			return sum, err
		}
		if recent != nil {
			sum.Events = recent
		}
	}
	return sum, nil
}
