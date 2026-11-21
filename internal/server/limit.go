package server

import (
	"net"
	"net/netip"

	"github.com/4fuu/box/internal/ratelimit"
	"github.com/charmbracelet/ssh"
)

// guesser is one address's view of the limiter. The public key callback and
// the pairing password prompt both use it, so the limits hold across them.
type guesser struct {
	limits *ratelimit.Limiter
	addr   netip.Addr
	known  bool
}

// banned reports whether this address is refused right now.
func (g guesser) banned() bool {
	if !g.known {
		return false
	}
	_, banned := g.limits.Banned(g.addr)
	return banned
}

// failed records a refused attempt. A ban it earns holds every later attempt
// from the address before the key is even checked.
func (g guesser) failed() {
	if !g.known {
		return
	}
	g.limits.Fail(g.addr)
}

// connected clears the address after it authenticated.
func (g guesser) connected() {
	if g.known {
		g.limits.Forget(g.addr)
	}
}

// guesserFor is the limiter's view of a client, keyed by its address.
func (h *sshServer) guesserFor(ctx ssh.Context) guesser {
	remote := ""
	if a := ctx.RemoteAddr(); a != nil {
		remote = a.String()
	}
	addr, ok := ratelimit.Address(remote)
	return guesser{limits: h.s.limits, addr: addr, known: ok}
}

// limitedListener closes connections from banned addresses before the SSH
// handshake, which is where the expensive crypto lives. One banned address
// then costs an accept and a close per attempt.
type limitedListener struct {
	net.Listener
	limits *ratelimit.Limiter
}

func (l limitedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		addr, ok := ratelimit.Address(c.RemoteAddr().String())
		if ok {
			if _, banned := l.limits.Banned(addr); banned {
				c.Close()
				continue
			}
		}
		return c, nil
	}
}
