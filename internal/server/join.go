package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/4fuu/box/internal/approve"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/store"
	"github.com/charmbracelet/ssh"
)

const joinWait = 10 * time.Minute

// joinWaiter is the parked SSH session. The queue lock is never held while
// this session waits, and Grant does not run under that lock.
//
// grant is the only writer of the computer row. It reports success only after
// the session has accepted the token. A cancel waits for that decision instead
// of racing a non-blocking receive.
type joinWaiter struct {
	mu        sync.Mutex
	started   bool
	cancelled bool
	token     string

	ready chan struct{}
	ack   chan error
	once  sync.Once
}

func newJoinWaiter() *joinWaiter {
	return &joinWaiter{
		ready: make(chan struct{}),
		ack:   make(chan error, 1),
	}
}

func (w *joinWaiter) closeReady() { w.once.Do(func() { close(w.ready) }) }

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Server) grant(p approve.Pending) error {
	s.mu.Lock()
	w := s.waiters[p.Name]
	s.mu.Unlock()
	if w == nil {
		return errors.New("no matching join")
	}
	raw, err := newToken()
	if err != nil {
		return err
	}

	w.mu.Lock()
	w.started = true
	if w.cancelled {
		w.mu.Unlock()
		w.closeReady()
		return errors.New("join expired")
	}
	w.mu.Unlock()

	if err := s.store.CreateComputer(p.Name, secret.Hash(raw), p.User); err != nil {
		if errors.Is(err, store.ErrExists) {
			err = fmt.Errorf("%s already exists", p.Name)
		}
		w.closeReady()
		return err
	}

	w.mu.Lock()
	if w.cancelled {
		w.mu.Unlock()
		_ = s.store.DeleteComputer(p.Name)
		w.closeReady()
		return errors.New("join expired")
	}
	w.token = raw
	w.mu.Unlock()
	w.closeReady()

	// The session acks only after the SSH write. A failure deletes the row
	// before Approve tells the operator the join succeeded.
	if err := <-w.ack; err != nil {
		_ = s.store.DeleteComputer(p.Name)
		return err
	}
	return nil
}

func (h *sshServer) serveJoin(sess ssh.Session) {
	class, name := classifyUser(sess.User())
	if class != classJoin || name == "" {
		writeJoinError(sess, "rejected")
		return
	}
	hash, _ := sess.Context().Value(hashKey).(string)
	if !isHex64(hash) {
		writeJoinError(sess, "rejected")
		return
	}
	w := newJoinWaiter()
	s := h.s
	s.mu.Lock()
	if s.waiters == nil {
		s.waiters = map[string]*joinWaiter{}
	}
	if _, exists := s.waiters[name]; exists {
		s.mu.Unlock()
		writeJoinError(sess, "rejected")
		return
	}
	s.waiters[name] = w
	s.mu.Unlock()

	submitted := false
	defer func() {
		s.mu.Lock()
		if s.waiters[name] == w {
			delete(s.waiters, name)
		}
		s.mu.Unlock()
		if submitted && s.svc.Queue != nil {
			s.svc.Queue.Drop(name)
		}
	}()

	addr := ""
	if sess.RemoteAddr() != nil {
		addr = sess.RemoteAddr().String()
	}
	if s.svc.Queue == nil || s.svc.Queue.Submit(name, addr, "", hash) != nil {
		writeJoinError(sess, "rejected")
		return
	}
	submitted = true

	timer := time.NewTimer(joinWait)
	defer timer.Stop()
	select {
	case <-w.ready:
		h.deliverJoin(sess, w)
	case <-timer.C:
		h.abortJoin(sess, w, "expired")
	case <-sess.Context().Done():
		h.abortJoin(sess, w, "")
	}
}

// deliverJoin writes the token. grant is blocked on ack until this returns.
func (h *sshServer) deliverJoin(sess ssh.Session, w *joinWaiter) {
	w.mu.Lock()
	token := w.token
	w.mu.Unlock()
	if token == "" {
		writeJoinError(sess, "rejected")
		return
	}
	if err := h.s.writeJoinToken(sess, token); err != nil {
		w.ack <- err
		return
	}
	w.ack <- nil
}

// abortJoin tells grant the client will not take the token, and waits until
// grant has either not created a row or deleted it. There is no default.
func (h *sshServer) abortJoin(sess ssh.Session, w *joinWaiter, reason string) {
	w.mu.Lock()
	w.cancelled = true
	started := w.started
	w.mu.Unlock()
	if started {
		<-w.ready
	}
	w.mu.Lock()
	token := w.token
	w.mu.Unlock()
	if token != "" {
		if reason == "" {
			reason = "cancelled"
		}
		w.ack <- errors.New("join " + reason)
	}
	if reason == "" {
		return
	}
	writeJoinError(sess, reason)
}

func (s *Server) writeJoinToken(sess ssh.Session, token string) error {
	if err := json.NewEncoder(sess).Encode(map[string]any{
		"token":       token,
		"quic":        s.quicEndpoint(),
		"fingerprint": s.fingerprint,
		"domain":      s.svc.Domain,
	}); err != nil {
		return err
	}
	_ = sess.Exit(0)
	return nil
}

func writeJoinError(sess ssh.Session, kind string) {
	if kind != "expired" {
		kind = "rejected"
	}
	_ = json.NewEncoder(sess).Encode(map[string]string{"error": kind})
	_ = sess.Exit(0)
}
