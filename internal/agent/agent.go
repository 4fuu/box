// Package agent runs on a computer as a normal user. Join parks an approval
// code over SSH. Run keeps the QUIC tunnel, authorized keys, and guest socket.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/tunnel"
)

// ErrRejected means the server refused the join or revoked the token.
// Run deletes the state directory before returning it.
var ErrRejected = errors.New("rejected")

const agentVersion = "dev"

// waitBackoff sleeps before another bootstrap. Tests replace it so a
// reconnect does not wait out tunnel.Backoff.
var waitBackoff = func(ctx context.Context, attempt int) error {
	timer := time.NewTimer(tunnel.Backoff(attempt, rand.Float64()))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type agent struct {
	dir string
	ctx context.Context

	mu   sync.Mutex
	comp computer
	keys []string
	sess *tunnel.Session
	ln   net.Listener
}

// Join prints a one-time approval code, sends its hash as the first line
// of a join+<name> SSH session, and stores the server's reply in stateDir.
func Join(ctx context.Context, serverAddr, name, userName, stateDir string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if userName == "" {
		u, err := user.Current()
		if err != nil {
			return err
		}
		userName = u.Username
	}
	addr, err := withPort(serverAddr)
	if err != nil {
		return err
	}
	code, err := secret.ApprovalCode()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "approval code: %s\n", code)
	fmt.Fprintf(out, "enter this code at the server to approve %q\n", name)
	fmt.Fprintln(out, "waiting\u2026")

	line, err := sshExchange(ctx, addr, "join+"+name, secret.Hash(code))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	a := &agent{dir: stateDir, comp: computer{Name: name, User: userName, SSH: addr}}
	comp, err := a.applyReply(line, true)
	if err != nil {
		return err
	}
	if comp.Domain != "" {
		fmt.Fprintf(out, "approved. tunnel up as %s.%s\n", name, comp.Domain)
	}
	ensureSSHD(out)
	return nil
}

// Run reads computer.json and keeps a tunnel until ctx is done.
// A refused token deletes stateDir and returns ErrRejected.
func Run(ctx context.Context, stateDir string) error {
	comp, err := readComputer(stateDir)
	if err != nil {
		return err
	}
	a := &agent{dir: stateDir, ctx: ctx, comp: comp}
	if err := a.loadAndRewrite(); err != nil {
		return err
	}
	// Idempotent: adds AcceptEnv when sshd's config still lacks it, prints
	// the lines to add when the file is not writable, silent when done.
	ensureSSHD(os.Stdout)
	_ = installSkill(stateDir)
	if err := a.listen(); err != nil {
		return err
	}
	defer a.stopListen()

	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := a.once(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrRejected) {
			a.stopListen()
			_ = wipeState(stateDir)
			return ErrRejected
		}
		if err := waitBackoff(ctx, attempt); err != nil {
			return err
		}
		attempt++
	}
}

func (a *agent) once(ctx context.Context) error {
	if err := a.bootstrap(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	comp := a.comp
	a.mu.Unlock()
	sess, err := tunnel.Dial(ctx, comp.QUIC, comp.Fingerprint)
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Handle(a.onControl)

	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	res, err := sess.Hello(hctx, tunnel.Identity{
		Name:         comp.Name,
		Token:        comp.Token,
		User:         comp.User,
		HostKey:      hostKey(),
		AgentVersion: agentVersion,
	})
	cancel()
	if err != nil {
		_ = sess.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tokenRefused(err) {
			return fmt.Errorf("%w", ErrRejected)
		}
		return err
	}
	a.mu.Lock()
	a.sess = sess
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.sess == sess {
			a.sess = nil
		}
		a.mu.Unlock()
	}()
	if err := a.noteHello(res); err != nil {
		return err
	}

	for {
		kind, port, conn, err := sess.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go proxyStream(ctx, kind, port, conn)
	}
}

func (a *agent) bootstrap(ctx context.Context) error {
	a.mu.Lock()
	comp := a.comp
	a.mu.Unlock()
	addr, err := comp.sshAddress()
	if err != nil {
		return err
	}
	bctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	line, err := sshExchange(bctx, addr, "boot+"+comp.Name, comp.Token)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	_, err = a.applyReply(line, false)
	return err
}

func (a *agent) noteHello(res tunnel.HelloResult) error {
	return a.save(func(c *computer) {
		if res.Domain != "" {
			c.Domain = res.Domain
		}
	})
}

func (a *agent) save(fn func(*computer)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(&a.comp)
	return writeComputer(a.dir, a.comp)
}

func (a *agent) applyReply(line []byte, join bool) (computer, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(line, &probe); err != nil {
		return computer{}, err
	}
	if raw, ok := probe["error"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return computer{}, err
		}
		if s != "" {
			if s == "rejected" || (join && s == "expired") {
				return computer{}, fmt.Errorf("%s: %w", s, ErrRejected)
			}
			return computer{}, errors.New(s)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := applyPresent(&a.comp, probe, join); err != nil {
		return computer{}, err
	}
	if err := writeComputer(a.dir, a.comp); err != nil {
		return computer{}, err
	}
	return a.comp, nil
}

func applyPresent(c *computer, probe map[string]json.RawMessage, join bool) error {
	if raw, ok := probe["token"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if s != "" {
			c.Token = s
		}
	}
	if join && c.Token == "" {
		return errors.New("incomplete join reply")
	}
	if raw, ok := probe["quic"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if s != "" {
			c.QUIC = s
		}
	}
	if raw, ok := probe["fingerprint"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if s != "" {
			c.Fingerprint = s
		}
	}
	if raw, ok := probe["domain"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		c.Domain = s
	}
	if join && (c.QUIC == "" || c.Fingerprint == "") {
		return errors.New("incomplete join reply")
	}
	return nil
}

func tokenRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRejected) {
		return true
	}
	msg := err.Error()
	return msg == "rejected" || strings.HasPrefix(msg, "rejected:") || strings.Contains(msg, "rejected token")
}
