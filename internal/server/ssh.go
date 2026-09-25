package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/repl"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"
)

type ctxKey struct{ name string }

var (
	needPasswordKey = &ctxKey{"need-password"}
	backendKey      = &ctxKey{"backend"}
)

type sshServer struct {
	s   *Server
	srv *ssh.Server
}

func newSSH(s *Server, hostKey string) (*sshServer, error) {
	h := &sshServer{s: s}
	srv, err := wish.NewServer(
		wish.WithAddress(s.cfg.SSHAddr),
		wish.WithHostKeyPath(hostKey),
		wish.WithPublicKeyAuth(h.publicKey),
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
	bound, err := h.s.svc.HasKey(key)
	if err != nil {
		return false
	}
	if strings.HasPrefix(user, "pair+") {
		if err := h.s.svc.ConsumeClient(strings.TrimPrefix(user, "pair+")); err != nil {
			return false
		}
		if err := h.s.svc.Bind(key, ""); err != nil {
			return false
		}
		h.s.svc.PushKeys(ctx)
		return true
	}
	if h.s.svc.IsComputer(user) {
		return bound
	}
	if bound {
		return true
	}
	live, err := h.s.store.HasLivePairing("client")
	if err != nil || !live {
		return false
	}
	ctx.SetValue(needPasswordKey, true)
	return true
}

func (h *sshServer) session(sess ssh.Session) {
	if sess.Context().Value(needPasswordKey) == true {
		if !h.readPassword(sess) {
			return
		}
	}
	user := sess.User()
	if h.s.svc.IsComputer(user) {
		_ = h.bridge(sess, user, true)
		return
	}
	ptyReq, _, pty := sess.Pty()
	out, errOut := io.Writer(sess), io.Writer(sess.Stderr())
	if pty {
		// A PTY peer runs its terminal raw with output processing off:
		// server text must carry \r\n, and stderr merges into the one screen.
		out = repl.CRLF(out)
		errOut = out
	}
	r := repl.New(sess, out, errOut, h.s.svc)
	r.Interactive = len(sess.Command()) == 0
	r.Pub = sess.PublicKey()
	r.Bridge = func(name string) error { return h.bridge(sess, name, false) }
	if len(sess.Command()) == 0 {
		r.Raw = pty // a PTY peer sends \r and gets no echo: read with a line discipline
		r.Color = pty && ptyReq.Term != "dumb"
		r.Width = ptyReq.Window.Width
		r.Banner()
		_ = r.Loop()
		return
	}
	if err := r.Exec(sess.Command()); err != nil {
		if errors.Is(err, repl.ErrExit) {
			return
		}
		fmt.Fprintln(errOut, err.Error())
		_ = sess.Exit(1)
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
	line, err := repl.ReadLine(bufio.NewReader(sess), out, pty, false)
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
	var conn net.Conn
	var pub gossh.PublicKey
	var err error
	if h.s.cfg.Backend != nil {
		conn, pub, err = h.s.cfg.Backend(ctx, computer)
	} else {
		conn, pub, err = h.defaultDial(ctx, computer)
	}
	if err != nil {
		return nil, err
	}
	want := pub
	cfg := &gossh.ClientConfig{
		User: paths.LoginUser,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(h.s.github)},
		HostKeyCallback: func(_ string, _ net.Addr, got gossh.PublicKey) error {
			if want == nil || got == nil || !keysEqual(want, got) {
				return errors.New("host key mismatch")
			}
			return nil
		},
	}
	cc, chans, reqs, err := gossh.NewClientConn(conn, computer, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return gossh.NewClient(cc, chans, reqs), nil
}

func (h *sshServer) defaultDial(ctx context.Context, computer string) (net.Conn, gossh.PublicKey, error) {
	if h.s.cfg.Dial == nil {
		return nil, nil, fmt.Errorf("node is unreachable")
	}
	id, err := h.s.svc.NodeID(computer)
	if err != nil {
		return nil, nil, err
	}
	line, err := h.s.svc.HostPublicKey(ctx, computer)
	if err != nil {
		return nil, nil, err
	}
	pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, nil, errors.New("host key mismatch")
	}
	conn, err := h.s.cfg.Dial(ctx, id, "ssh/"+computer)
	if err != nil {
		return nil, nil, fmt.Errorf("node is unreachable")
	}
	return conn, pub, nil
}

func (h *sshServer) bridge(sess ssh.Session, computer string, closeSession bool) error {
	// Server-generated errors need \r\n for a PTY peer and merge into its one
	// screen; container I/O passes through as-is.
	errOut := io.Writer(sess.Stderr())
	if _, _, pty := sess.Pty(); pty {
		errOut = repl.CRLF(sess)
	}
	client, err := h.clientFor(sess.Context(), computer)
	if err != nil {
		fmt.Fprintln(errOut, err.Error())
		if closeSession {
			_ = sess.Exit(1)
		}
		return err
	}
	bs, err := client.NewSession()
	if err != nil {
		fmt.Fprintln(errOut, "unreachable")
		if closeSession {
			_ = sess.Exit(1)
		}
		return err
	}
	defer bs.Close()
	if pty, winch, ok := sess.Pty(); ok {
		_ = bs.RequestPty(pty.Term, pty.Window.Height, pty.Window.Width, gossh.TerminalModes{})
		go func() {
			for win := range winch {
				_ = bs.WindowChange(win.Height, win.Width)
			}
		}()
	}
	bs.Stdin = sess
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

func (h *sshServer) subsystem(sess ssh.Session) {
	if !h.s.svc.IsComputer(sess.User()) {
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
	if !h.s.svc.IsComputer(ctx.User()) {
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
		newChan.Reject(gossh.ConnectionFailed, "unreachable")
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
	if !h.s.svc.IsComputer(ctx.User()) {
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
