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
	return pause(ctx, attempt, time.Second)
}

// pause waits the reconnect delay for attempt, grown from base. Tests use a
// base of a few milliseconds so they can watch the delay grow.
func pause(ctx context.Context, attempt int, base time.Duration) error {
	timer := time.NewTimer(tunnel.BackoffFor(attempt, rand.Float64(), base))
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
		up, err := a.once(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrRejected) {
			a.stopListen()
			_ = wipeState(stateDir)
			return ErrRejected
		}
		if up {
			attempt = 0
		}
		if err := waitBackoff(ctx, attempt); err != nil {
			return err
		}
		attempt++
	}
}

// once bootstraps, dials, and serves until the tunnel drops. up reports
// whether the tunnel stayed up long enough to count as a reconnect, not a
// flapping server: resetting the backoff for one that drops at once would
// turn a broken server into a tight loop of SSH exchanges.
func (a *agent) once(ctx context.Context) (up bool, err error) {
	sess, res, err := a.connect(ctx)
	if err != nil {
		return false, err
	}
	defer sess.Close()
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
	up = true
	start := time.Now()
	if err := a.noteHello(res); err != nil {
		return up, err
	}

	for {
		kind, port, conn, err := sess.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return up, ctx.Err()
			}
			return up && time.Since(start) >= tunnel.Lived, err
		}
		go proxyStream(ctx, kind, port, conn)
	}
}

func (a *agent) connect(ctx context.Context) (*tunnel.Session, tunnel.HelloResult, error) {
	a.mu.Lock()
	comp := a.comp
	a.mu.Unlock()
	if len(comp.Nodes) > 0 {
		sess, res, err := a.connectNodes(ctx, comp)
		if err == nil {
			return sess, res, nil
		}
		if ctx.Err() != nil {
			return nil, tunnel.HelloResult{}, ctx.Err()
		}
		// A pinned endpoint may have moved. SSH is the channel that carries
		// the new list, and it also reports a revoked token.
		if berr := a.bootstrap(ctx); berr != nil {
			if errors.Is(berr, ErrRejected) {
				return nil, tunnel.HelloResult{}, berr
			}
			return nil, tunnel.HelloResult{}, err
		}
		a.mu.Lock()
		comp = a.comp
		a.mu.Unlock()
		if len(comp.Nodes) > 0 {
			return a.connectNodes(ctx, comp)
		}
		return a.dialOne(ctx, comp)
	}
	if err := a.bootstrap(ctx); err != nil {
		return nil, tunnel.HelloResult{}, err
	}
	a.mu.Lock()
	comp = a.comp
	a.mu.Unlock()
	return a.dialOne(ctx, comp)
}

func (a *agent) dialOne(ctx context.Context, comp computer) (*tunnel.Session, tunnel.HelloResult, error) {
	sess, err := tunnel.Dial(ctx, comp.QUIC, comp.Fingerprint)
	if err != nil {
		return nil, tunnel.HelloResult{}, err
	}
	res, err := a.hello(ctx, sess, comp, 20*time.Second)
	if err != nil {
		_ = sess.Close()
		if tokenRefused(err) {
			err = ErrRejected
		}
		return nil, tunnel.HelloResult{}, err
	}
	return sess, res, nil
}

const nodeTimeout = 5 * time.Second

func (a *agent) connectNodes(ctx context.Context, comp computer) (*tunnel.Session, tunnel.HelloResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dialCtx, stopDial := context.WithTimeout(ctx, nodeTimeout)
	defer stopDial()
	candidates := make(chan *tunnel.Session)
	for _, n := range comp.Nodes {
		go func() {
			sess, _ := tunnel.Dial(dialCtx, n.QUIC, n.Fingerprint)
			select {
			case candidates <- sess:
			case <-ctx.Done():
				if sess != nil {
					_ = sess.Close()
				}
			}
		}()
	}
	// Race handshakes, not hellos: only the chosen node should attach the computer.
	for range comp.Nodes {
		select {
		case <-ctx.Done():
			return nil, tunnel.HelloResult{}, ctx.Err()
		case sess := <-candidates:
			if sess == nil {
				continue
			}
			res, err := a.hello(ctx, sess, comp, nodeTimeout)
			if err == nil {
				return sess, res, nil
			}
			_ = sess.Close()
		}
	}
	// With independent node state, a refusal does not authorize wiping the computer.
	return nil, tunnel.HelloResult{}, errors.New("no configured node accepted the computer")
}

func (a *agent) hello(ctx context.Context, sess *tunnel.Session, comp computer, timeout time.Duration) (tunnel.HelloResult, error) {
	sess.Handle(a.onControl)
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return sess.Hello(hctx, tunnel.Identity{
		Name:         comp.Name,
		Token:        comp.Token,
		User:         comp.User,
		HostKey:      hostKey(),
		AgentVersion: agentVersion,
	})
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
		// A single-node server sends no list. Keep the pinned one rather than
		// forgetting a peer during a rolling upgrade.
		if len(res.Nodes) > 0 {
			c.Nodes = res.Nodes
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
	if raw, ok := probe["nodes"]; ok {
		var list []tunnel.NodeEndpoint
		if err := json.Unmarshal(raw, &list); err != nil {
			return err
		}
		if err := validNodes(list); err != nil {
			return err
		}
		c.Nodes = list
	}
	if join && (c.QUIC == "" || c.Fingerprint == "") {
		return errors.New("incomplete join reply")
	}
	return nil
}

func validNodes(list []tunnel.NodeEndpoint) error {
	for _, n := range list {
		host, port, err := net.SplitHostPort(n.QUIC)
		if err != nil || host == "" || port == "" || strings.TrimSpace(n.Fingerprint) == "" {
			return errors.New("invalid node list")
		}
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
