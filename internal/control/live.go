package control

import (
	"sync"

	"github.com/4fuu/box/internal/tunnel"
)

// Live is the set of computers whose QUIC connection is up.
// The map key is the current name, which rename can change.
type Live struct {
	mu    sync.Mutex
	conns map[string]*tunnel.Conn
	names map[*tunnel.Conn]string
}

func NewLive() *Live {
	return &Live{
		conns: map[string]*tunnel.Conn{},
		names: map[*tunnel.Conn]string{},
	}
}

// Do runs fn while holding the live map. fn may call the locked helpers.
// It must not call back into Live methods that take the lock, and it must not
// block on a tunnel call: those wait on the computer.
func (l *Live) Do(fn func() error) error {
	if l == nil {
		return fn()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return fn()
}

// AttachIf registers c when ok reports the computer still exists.
// ok runs under the lock so a delete cannot land between the check and the insert.
// The previous connection, if any, is returned for the caller to close.
func (l *Live) AttachIf(name string, c *tunnel.Conn, ok func() bool) (old *tunnel.Conn, attached bool) {
	if l == nil || c == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if ok != nil && !ok() {
		return nil, false
	}
	if prev := l.conns[name]; prev != nil && prev != c {
		old = prev
		delete(l.names, prev)
	}
	l.conns[name] = c
	l.names[c] = name
	return old, true
}

func (l *Live) Get(name string) *tunnel.Conn {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.conns[name]
}

func (l *Live) CurrentName(c *tunnel.Conn) string {
	if l == nil || c == nil {
		if c == nil {
			return ""
		}
		return c.Name()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if name, ok := l.names[c]; ok {
		return name
	}
	return c.Name()
}

func (l *Live) All() []*tunnel.Conn {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*tunnel.Conn, 0, len(l.conns))
	for _, c := range l.conns {
		out = append(out, c)
	}
	return out
}

// Forget drops c if it is still the connection registered for its name.
func (l *Live) Forget(c *tunnel.Conn) {
	if l == nil || c == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetLocked(c)
}

func (l *Live) forgetLocked(c *tunnel.Conn) {
	name, ok := l.names[c]
	if !ok {
		return
	}
	if l.conns[name] == c {
		delete(l.conns, name)
	}
	delete(l.names, c)
}

// dropLocked removes name and returns its connection. Caller holds the lock via Do.
func (l *Live) dropLocked(name string) *tunnel.Conn {
	c := l.conns[name]
	if c == nil {
		return nil
	}
	delete(l.conns, name)
	delete(l.names, c)
	return c
}

func (l *Live) renameLocked(oldName, newName string) {
	c := l.conns[oldName]
	if c == nil {
		return
	}
	delete(l.conns, oldName)
	if prev := l.conns[newName]; prev != nil && prev != c {
		delete(l.names, prev)
	}
	l.conns[newName] = c
	l.names[c] = newName
}
