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
type joinWaiter struct {
	ch   chan joinMsg
	done chan struct{}
	once sync.Once
}

type joinMsg struct {
	token   string
	errText string
}

func (w *joinWaiter) cancel() { w.once.Do(func() { close(w.done) }) }

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
	if err := s.store.CreateComputer(p.Name, secret.Hash(raw), p.User); err != nil {
		if errors.Is(err, store.ErrExists) {
			err = fmt.Errorf("%s already exists", p.Name)
		}
		select {
		case w.ch <- joinMsg{errText: "rejected"}:
		case <-w.done:
		}
		return err
	}
	select {
	case <-w.done:
		_ = s.store.DeleteComputer(p.Name)
		return errors.New("join expired")
	case w.ch <- joinMsg{token: raw}:
		return nil
	}
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
	w := &joinWaiter{ch: make(chan joinMsg, 1), done: make(chan struct{})}
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
		w.cancel()
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
	case msg := <-w.ch:
		s.writeJoin(sess, msg)
	case <-timer.C:
		w.cancel()
		select {
		case msg := <-w.ch:
			s.writeJoin(sess, msg)
		default:
			writeJoinError(sess, "expired")
		}
	case <-sess.Context().Done():
		w.cancel()
		select {
		case msg := <-w.ch:
			if msg.token != "" {
				_ = s.store.DeleteComputer(name)
			}
		default:
		}
	}
}

func (s *Server) writeJoin(sess ssh.Session, msg joinMsg) {
	if msg.errText != "" {
		writeJoinError(sess, msg.errText)
		return
	}
	_ = json.NewEncoder(sess).Encode(map[string]any{
		"token":       msg.token,
		"quic":        s.quicEndpoint(),
		"fingerprint": s.fingerprint,
		"domain":      s.svc.Domain,
		"http_port":   s.httpPort,
	})
	_ = sess.Exit(0)
}

func writeJoinError(sess ssh.Session, kind string) {
	if kind != "expired" {
		kind = "rejected"
	}
	_ = json.NewEncoder(sess).Encode(map[string]string{"error": kind})
	_ = sess.Exit(0)
}
