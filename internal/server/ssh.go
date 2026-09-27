package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/repl"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tui"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"
)

type ctxKey struct{ name string }

var (
	routeKey   = &ctxKey{"route"}
	hashKey    = &ctxKey{"join-hash"}
	backendKey = &ctxKey{"backend"}
)

type sshServer struct {
	s   *Server
	srv *ssh.Server
	// dialHook replaces dial in tests. Production leaves it nil.
	dialHook func(context.Context, string) (*gossh.Client, error)
}

func newSSH(s *Server, hostKey string) (*sshServer, error) {
	h := &sshServer{s: s}
	srv, err := wish.NewServer(
		wish.WithAddress(s.cfg.SSHAddr),
		wish.WithHostKeyPath(hostKey),
		wish.WithPublicKeyAuth(h.publicKey),
		wish.WithPasswordAuth(h.password),
	)
	if err != nil {
		return nil, err
	}
	srv.Handler = h.session
	srv.ChannelHandlers = map[string]ssh.ChannelHandler{
		"session":      ssh.DefaultSessionHandler,
		"direct-tcpip": h.directTCP,
	}
	srv.SubsystemHandlers = map[string]ssh.SubsystemHandler{
		"sftp": h.subsystem,
	}
	srv.RequestHandlers = map[string]ssh.RequestHandler{
		"tcpip-forward":        h.tcpipForward,
		"cancel-tcpip-forward": h.cancelForward,
	}
	h.srv = srv
	return h, nil
}

func (h *sshServer) serve(ln net.Listener) { _ = h.srv.Serve(ln) }

func (h *sshServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = h.srv.Shutdown(ctx)
}

func (h *sshServer) publicKey(ctx ssh.Context, key ssh.PublicKey) bool {
	user := ctx.User()
	class, _ := classifyUser(user)
	// pair+ is decided here only so the client can sign. golang.org/x/crypto/ssh
	// calls this for the unsigned publickey query, before the signature exists.
	if class == classPair {
		if key == nil {
			return false
		}
		accept, route := publicKeyDecision(class, false, false, false)
		if !accept {
			return false
		}
		ctx.SetValue(routeKey, route)
		return true
	}
	bound := false
	if key != nil {
		ok, err := h.s.svc.HasKey(key)
		if err != nil {
			return false
		}
		bound = ok
	}
	live := false
	if class == classREPL && !bound {
		ok, err := h.s.store.HasLivePairing()
		if err != nil {
			return false
		}
		live = ok
	}
	computer := class == classOther && h.s.svc.IsComputer(user)
	accept, route := publicKeyDecision(class, bound, live, computer)
	if !accept {
		return false
	}
	ctx.SetValue(routeKey, route)
	return true
}

func (h *sshServer) password(ctx ssh.Context, password string) bool {
	user := ctx.User()
	class, _ := classifyUser(user)
	tokenOK := false
	if class == classOther {
		ok, err := h.s.store.TokenMatches(user, password)
		if err != nil || !ok {
			return false
		}
		tokenOK = true
	}
	accept, route := passwordDecision(class, isHex64(password), tokenOK)
	if !accept {
		return false
	}
	ctx.SetValue(routeKey, route)
	if route == routeJoin {
		ctx.SetValue(hashKey, password)
	}
	return true
}

func (h *sshServer) session(sess ssh.Session) {
	switch sess.Context().Value(routeKey) {
	case routeREPL:
		h.serveREPL(sess)
	case routeJoin:
		h.serveJoin(sess)
	case routeBoot:
		h.serveBootstrap(sess)
	case routeBind:
		if !h.readPassword(sess) {
			return
		}
		h.serveREPL(sess)
	case routeSplice:
		_ = h.bridge(sess, sess.User(), true)
	case routePair:
		if !h.finishPair(sess) {
			return
		}
		h.serveREPL(sess)
	default:
		fmt.Fprintln(sess.Stderr(), "unknown computer")
		_ = sess.Exit(1)
	}
}

// finishPair consumes the one-time password and binds the key that just
// authenticated. The signature has already been checked; this is the same
// moment as the interactive password prompt.
func (h *sshServer) finishPair(sess ssh.Session) bool {
	_, rest := classifyUser(sess.User())
	_, _, pty := sess.Pty()
	errOut := io.Writer(sess.Stderr())
	if pty {
		errOut = repl.CRLF(sess)
	}
	if err := h.s.svc.ConsumeClient(rest); err != nil {
		fmt.Fprintln(errOut, err.Error())
		_ = sess.Exit(1)
		return false
	}
	if sess.PublicKey() == nil {
		fmt.Fprintln(errOut, "no key")
		_ = sess.Exit(1)
		return false
	}
	if err := h.s.svc.Bind(sess.PublicKey(), ""); err != nil {
		fmt.Fprintln(errOut, err.Error())
		_ = sess.Exit(1)
		return false
	}
	h.s.svc.PushKeys(sess.Context())
	return true
}

func (h *sshServer) serveBootstrap(sess ssh.Session) {
	if _, err := h.s.store.Computer(sess.User()); err != nil {
		_ = sess.Exit(1)
		return
	}
	_ = json.NewEncoder(sess).Encode(map[string]any{
		"quic":        h.s.quicEndpoint(),
		"fingerprint": h.s.fingerprint,
		"domain":      h.s.svc.Domain,
	})
	_ = sess.Exit(0)
}

func (h *sshServer) serveREPL(sess ssh.Session) {
	ptyReq, winch, pty := sess.Pty()
	if len(sess.Command()) == 0 {
		h.serveTUI(sess, ptyReq, winch, pty)
		return
	}
	out, errOut := io.Writer(sess), io.Writer(sess.Stderr())
	if pty {
		out = repl.CRLF(out)
		errOut = out
	}
	r := repl.New(sess, out, errOut, h.s.svc)
	r.Pub = sess.PublicKey()
	if sess.RemoteAddr() != nil {
		r.From = sess.RemoteAddr().String()
	}
	r.Bridge = func(name string) error { return h.bridge(sess, name, false) }
	if err := r.Exec(sess.Command()); err != nil {
		if errors.Is(err, repl.ErrExit) {
			return
		}
		fmt.Fprintln(errOut, err.Error())
		_ = sess.Exit(1)
	}
}

// serveTUI is the interactive control session. A shell opened from it uses
// the same bridge as `ssh <name>`; the TUI does not speak SSH itself.
func (h *sshServer) serveTUI(sess ssh.Session, ptyReq ssh.Pty, winch <-chan ssh.Window, pty bool) {
	from := ""
	if sess.RemoteAddr() != nil {
		from = sess.RemoteAddr().String()
	}
	id := ""
	if key := sess.PublicKey(); key != nil {
		id, _ = h.s.svc.WhoAmI(key)
	}
	var mu sync.Mutex
	var onWin func(ssh.Window)
	// latest starts as the PTY request size. charmbracelet/ssh has already
	// queued that size on winch. The reader records it before calling a
	// listener, so a nil listener does not drop the seed. Publishing a
	// listener and copying latest share one lock, so a resize cannot land
	// in the gap and disappear.
	latest := ptyReq.Window
	sizeWith := func(fn func(ssh.Window)) ssh.Window {
		mu.Lock()
		onWin = fn
		cur := latest
		mu.Unlock()
		return cur
	}
	setWinch := func(fn func(ssh.Window)) { sizeWith(fn) }
	if pty && winch != nil {
		go func() {
			for w := range winch {
				mu.Lock()
				latest = w
				fn := onWin
				mu.Unlock()
				if fn != nil {
					fn(w)
				}
			}
		}()
	}
	in := newSessionInput(sess)
	notice := ""
	ctx := sess.Context()
	for {
		reader := in.attach()
		resize := make(chan tui.Size, 4)
		cur := sizeWith(func(w ssh.Window) {
			select {
			case resize <- tui.Size{Width: w.Width, Height: w.Height}:
			default:
			}
		})
		cfg := tui.Config{
			Context: ctx, In: reader, Out: sess,
			Backend:    tui.ServiceBackend{Svc: h.s.svc, From: from},
			AllowShell: true, Identity: id, Notice: notice,
			Width: cur.Width, Height: cur.Height,
			Resize: resize,
		}
		if pty {
			cfg.Term = ptyReq.Term
			cfg.Environ = append(append([]string{}, sess.Environ()...), "TERM="+ptyReq.Term)
		}
		name, err := tui.Run(cfg)
		setWinch(nil)
		notice = ""
		if err != nil {
			in.detach()
			errOut := io.Writer(sess.Stderr())
			if pty {
				errOut = repl.CRLF(sess)
			}
			fmt.Fprintln(errOut, err.Error())
			_ = sess.Exit(1)
			return
		}
		if name == "" {
			in.detach()
			return
		}
		if err := h.bridgeIO(sess, name, false, in.attach(), setWinch, true); err != nil {
			notice = err.Error()
		}
	}
}

func (h *sshServer) readPassword(sess ssh.Session) bool {
	_, _, pty := sess.Pty()
	out, errOut := io.Writer(sess), io.Writer(sess.Stderr())
	if pty {
		out = repl.CRLF(out)
		errOut = out
	}
	if len(sess.Command()) > 0 {
		fmt.Fprintln(errOut, "use pair+password to bind a key")
		_ = sess.Exit(1)
		return false
	}
	fmt.Fprint(out, "password: ")
	line, err := repl.ReadSecret(bufio.NewReader(sess), out, pty)
	if err != nil {
		_ = sess.Exit(1)
		return false
	}
	if err := h.s.svc.ConsumeClient(line); err != nil {
		fmt.Fprintln(errOut, err.Error())
		_ = sess.Exit(1)
		return false
	}
	if sess.PublicKey() == nil {
		fmt.Fprintln(errOut, "no key")
		_ = sess.Exit(1)
		return false
	}
	if err := h.s.svc.Bind(sess.PublicKey(), ""); err != nil {
		fmt.Fprintln(errOut, err.Error())
		_ = sess.Exit(1)
		return false
	}
	h.s.svc.PushKeys(sess.Context())
	return true
}

type backendHold struct {
	once   sync.Once
	client *gossh.Client
	err    error
}

func (h *sshServer) clientFor(ctx ssh.Context, computer string) (*gossh.Client, error) {
	ctx.Lock()
	v := ctx.Value(backendKey)
	if v == nil {
		v = &backendHold{}
		ctx.SetValue(backendKey, v)
	}
	ctx.Unlock()
	hold := v.(*backendHold)
	hold.once.Do(func() {
		hold.client, hold.err = h.dial(ctx, computer)
		if hold.err == nil {
			go func() {
				<-ctx.Done()
				hold.client.Close()
			}()
		}
	})
	return hold.client, hold.err
}

func (h *sshServer) dial(ctx context.Context, computer string) (*gossh.Client, error) {
	if !h.s.svc.IsComputer(computer) {
		return nil, fmt.Errorf("computer %s not found", computer)
	}
	live := h.s.svc.Live.Get(computer)
	if live == nil {
		return nil, errors.New("computer is offline")
	}
	rec, err := h.s.store.Computer(computer)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("computer %s not found", computer)
		}
		return nil, err
	}
	user := rec.LoginUser
	if user == "" {
		user = live.Identity().User
	}
	if user == "" {
		return nil, errors.New("computer has no login user")
	}
	hostLine := live.Identity().HostKey
	if hostLine == "" {
		hostLine = rec.HostKey
	}
	want, err := parseHostKey(hostLine)
	if err != nil {
		return nil, errors.New("host key mismatch")
	}
	raw, err := live.OpenSSH(ctx)
	if err != nil {
		return nil, errors.New("computer is offline")
	}
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	cfg := &gossh.ClientConfig{
		User: user,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(h.s.splice)},
		HostKeyCallback: func(_ string, _ net.Addr, got gossh.PublicKey) error {
			if got == nil || !keysEqual(want, got) {
				return errors.New("host key mismatch")
			}
			return nil
		},
	}
	cc, chans, reqs, err := gossh.NewClientConn(raw, computer, cfg)
	if err != nil {
		raw.Close()
		if strings.Contains(err.Error(), "host key") {
			return nil, errors.New("host key mismatch")
		}
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return gossh.NewClient(cc, chans, reqs), nil
}

func parseHostKey(line string) (gossh.PublicKey, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, errors.New("missing host key")
	}
	pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, err
	}
	return pub, nil
}

func (h *sshServer) bridge(sess ssh.Session, computer string, closeSession bool) error {
	return h.bridgeIO(sess, computer, closeSession, nil, nil, false)
}

// dialNamed dials computer on every call. A failure is not remembered.
// Tests replace dialHook; production calls dial.
func (h *sshServer) dialNamed(ctx context.Context, computer string) (*gossh.Client, error) {
	if h.dialHook != nil {
		return h.dialHook(ctx, computer)
	}
	return h.dial(ctx, computer)
}

// bridgeIO splices to the computer. stdin nil reads the session. setWinch, when
// set, receives window changes because the TUI already owns the pty channel.
// fresh is the TUI path: dial the named computer now, and do not reuse the
// splice session's cached client. The splice route passes fresh false.
func (h *sshServer) bridgeIO(sess ssh.Session, computer string, closeSession bool, stdin io.Reader, setWinch func(func(ssh.Window)), fresh bool) error {
	if stdin == nil {
		stdin = sess
	}
	errOut := io.Writer(sess.Stderr())
	if _, _, pty := sess.Pty(); pty {
		errOut = repl.CRLF(sess)
	}
	var client *gossh.Client
	var err error
	if fresh {
		client, err = h.dialNamed(sess.Context(), computer)
	} else {
		client, err = h.clientFor(sess.Context(), computer)
	}
	if err != nil {
		fmt.Fprintln(errOut, err.Error())
		if closeSession {
			_ = sess.Exit(1)
		}
		return err
	}
	if fresh {
		// A pager or sleep ignores stdin EOF, so Wait never returns when the
		// control session is already gone. Close on that cancel, and again
		// when bridgeIO returns. A second Close is an error, not a panic.
		// The splice route does not use this path; clientFor still owns that cache.
		go func() {
			<-sess.Context().Done()
			client.Close()
		}()
		defer client.Close()
	}
	bs, err := client.NewSession()
	if err != nil {
		fmt.Fprintln(errOut, "computer is offline")
		if closeSession {
			_ = sess.Exit(1)
		}
		return err
	}
	defer bs.Close()
	if pty, winch, ok := sess.Pty(); ok {
		_ = bs.RequestPty(pty.Term, pty.Window.Height, pty.Window.Width, gossh.TerminalModes{})
		if setWinch != nil {
			setWinch(func(w ssh.Window) {
				_ = bs.WindowChange(w.Height, w.Width)
			})
			defer setWinch(nil)
		} else {
			go func() {
				for win := range winch {
					_ = bs.WindowChange(win.Height, win.Width)
				}
			}()
		}
	}
	bs.Stdin = stdin
	stdout, err := bs.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := bs.StderrPipe()
	if err != nil {
		return err
	}
	go io.Copy(sess, stdout)
	go io.Copy(sess.Stderr(), stderr)
	raw := ""
	if computer == sess.User() {
		raw = sess.RawCommand()
	}
	if raw == "" {
		err = bs.Shell()
	} else {
		err = bs.Start(raw)
	}
	if err != nil {
		fmt.Fprintln(errOut, err.Error())
		if closeSession {
			_ = sess.Exit(1)
		}
		return err
	}
	err = bs.Wait()
	code := exitCode(err)
	if closeSession {
		_ = sess.Exit(code)
	}
	if code != 0 {
		return fmt.Errorf("exit %d", code)
	}
	return nil
}

func spliceRoute(ctx ssh.Context) bool {
	return ctx.Value(routeKey) == routeSplice
}

func (h *sshServer) subsystem(sess ssh.Session) {
	if !spliceRoute(sess.Context()) {
		_ = sess.Exit(1)
		return
	}
	client, err := h.clientFor(sess.Context(), sess.User())
	if err != nil {
		_ = sess.Exit(1)
		return
	}
	bs, err := client.NewSession()
	if err != nil {
		_ = sess.Exit(1)
		return
	}
	defer bs.Close()
	stdin, err := bs.StdinPipe()
	if err != nil {
		_ = sess.Exit(1)
		return
	}
	stdout, err := bs.StdoutPipe()
	if err != nil {
		_ = sess.Exit(1)
		return
	}
	go io.Copy(stdin, sess)
	go io.Copy(sess, stdout)
	if err := bs.RequestSubsystem(sess.Subsystem()); err != nil {
		_ = sess.Exit(1)
		return
	}
	_ = sess.Exit(exitCode(bs.Wait()))
}

func (h *sshServer) directTCP(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
	if !spliceRoute(ctx) {
		newChan.Reject(gossh.Prohibited, "port forwarding is disabled")
		return
	}
	var d struct {
		DestAddr   string
		DestPort   uint32
		OriginAddr string
		OriginPort uint32
	}
	if err := gossh.Unmarshal(newChan.ExtraData(), &d); err != nil {
		newChan.Reject(gossh.ConnectionFailed, "port forwarding is disabled")
		return
	}
	client, err := h.clientFor(ctx, ctx.User())
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, "computer is offline")
		return
	}
	dest := net.JoinHostPort(d.DestAddr, fmt.Sprintf("%d", d.DestPort))
	backend, err := client.Dial("tcp", dest)
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, "unreachable")
		return
	}
	ch, reqs, err := newChan.Accept()
	if err != nil {
		backend.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	go func() { defer ch.Close(); defer backend.Close(); io.Copy(ch, backend) }()
	go func() { defer ch.Close(); defer backend.Close(); io.Copy(backend, ch) }()
}

func (h *sshServer) tcpipForward(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	if !spliceRoute(ctx) {
		return false, nil
	}
	var payload struct {
		BindAddr string
		BindPort uint32
	}
	if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
		return false, nil
	}
	client, err := h.clientFor(ctx, ctx.User())
	if err != nil {
		return false, nil
	}
	addr := net.JoinHostPort(payload.BindAddr, fmt.Sprintf("%d", payload.BindPort))
	ln, err := client.Listen("tcp", addr)
	if err != nil {
		return false, nil
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	sshConn, _ := ctx.Value(ssh.ContextKeyConn).(*gossh.ServerConn)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if sshConn == nil {
				c.Close()
				continue
			}
			originHost, originPortStr, _ := net.SplitHostPort(c.RemoteAddr().String())
			var originPort int
			fmt.Sscanf(originPortStr, "%d", &originPort)
			chPayload := gossh.Marshal(struct {
				DestAddr   string
				DestPort   uint32
				OriginAddr string
				OriginPort uint32
			}{payload.BindAddr, uint32(port), originHost, uint32(originPort)})
			ch, reqs, err := sshConn.OpenChannel("forwarded-tcpip", chPayload)
			if err != nil {
				c.Close()
				continue
			}
			go gossh.DiscardRequests(reqs)
			go func() { defer ch.Close(); defer c.Close(); io.Copy(ch, c) }()
			go func() { defer ch.Close(); defer c.Close(); io.Copy(c, ch) }()
		}
	}()
	return true, gossh.Marshal(struct{ BindPort uint32 }{uint32(port)})
}

func (h *sshServer) cancelForward(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	return true, nil
}

func keysEqual(a, b gossh.PublicKey) bool {
	ab, bb := a.Marshal(), b.Marshal()
	if len(ab) != len(bb) {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *gossh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus()
	}
	return 1
}
