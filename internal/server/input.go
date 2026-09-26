package server

import (
	"bytes"
	"io"
	"sync"
)

// sessionInput is the only reader of an SSH channel while the TUI is up.
// bubbletea cannot cancel a read on that channel, so handing the channel to
// the program and then to a shell would split keystrokes between them.
type sessionInput struct {
	mu  sync.Mutex
	cur *bufferedPipe
}

func newSessionInput(r io.Reader) *sessionInput {
	s := &sessionInput{}
	go s.loop(r)
	return s
}

func (s *sessionInput) loop(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.mu.Lock()
			dst := s.cur
			s.mu.Unlock()
			if dst != nil {
				dst.Write(buf[:n])
			}
		}
		if err != nil {
			s.detach()
			return
		}
	}
}

// attach makes p the reader the TUI or the shell should use, and closes the
// previous one so a blocked Read returns.
func (s *sessionInput) attach() *bufferedPipe {
	p := newBufferedPipe()
	s.mu.Lock()
	old := s.cur
	s.cur = p
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return p
}

func (s *sessionInput) detach() {
	s.mu.Lock()
	old := s.cur
	s.cur = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// bufferedPipe is an in-memory pipe whose Read can be unblocked with Close.
type bufferedPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newBufferedPipe() *bufferedPipe {
	p := &bufferedPipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bufferedPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.buf.Len() == 0 && !p.closed {
		p.cond.Wait()
	}
	if p.buf.Len() == 0 {
		return 0, io.EOF
	}
	return p.buf.Read(b)
}

func (p *bufferedPipe) Write(b []byte) {
	p.mu.Lock()
	if !p.closed && len(b) > 0 {
		p.buf.Write(b)
		p.cond.Broadcast()
	}
	p.mu.Unlock()
}

func (p *bufferedPipe) Close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}
