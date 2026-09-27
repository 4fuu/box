package server_test

// End-to-end scenarios beyond the happy path in serve_test.go: a slow
// network between server and computer, interruptions on either side, and
// the TUI over a real PTY in several states. Everything runs in process
// against the real listeners: QUIC over a delaying UDP proxy, SSH over
// loopback TCP, the TUI over a real PTY.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/server"
	"github.com/4fuu/box/internal/tunnel"
	gossh "golang.org/x/crypto/ssh"
)

// e2eBind starts a server and binds one client key through the one-time
// password. It returns the server, its data dir, and the bound signer.
func e2eBind(t *testing.T) (*server.Server, string, gossh.Signer) {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	srv, err := server.Start(context.Background(), server.Config{
		Domain: "box.example.com", DataDir: dir,
		SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
		SocketPath: filepath.Join(dir, "box.sock"), Stdout: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "pair+"+passwordOf(t, buf.String()), signer, "whoami"); err != nil {
		t.Fatalf("pair: %v %s", err, errOut)
	}
	return srv, dir, signer
}

// e2eJoin approves a join for name through the REPL and returns the
// computer token.
func e2eJoin(t *testing.T, srv *server.Server, signer gossh.Signer, name string) string {
	t.Helper()
	code, err := secret.ApprovalCode()
	if err != nil {
		t.Fatal(err)
	}
	type joined struct {
		out string
		err error
	}
	joinCh := make(chan joined, 1)
	go func() {
		out, _, err := sshCredentialRun(t, srv.SSHAddr(), "join+"+name, secret.Hash(code), "x")
		joinCh <- joined{out, err}
	}()
	waitPending(t, srv.SSHAddr(), signer, name)
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "approve "+code); err != nil {
		t.Fatalf("approve: %v %s", err, errOut)
	}
	var got joined
	select {
	case got = <-joinCh:
	case <-time.After(10 * time.Second):
		t.Fatal("join did not finish")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	var reply struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got.out), &reply); err != nil {
		t.Fatalf("join json %q: %v", got.out, err)
	}
	if reply.Error != "" || reply.Token == "" {
		t.Fatalf("join reply %+v", reply)
	}
	return reply.Token
}

// e2eAgent dials the tunnel, says hello with its own host key, serves a
// slow stat handler, and accepts streams with the shell-capable fake
// below. The host key line it registers is the one its sshd serves, so
// splices pass the server's fingerprint check.
func e2eAgent(t *testing.T, ctx context.Context, addr, fingerprint, token string, extraHosts ...gossh.Signer) *tunnel.Session {
	t.Helper()
	hostKey, hostLine := newKey(t)
	sess, err := tunnel.Dial(ctx, addr, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Hello(ctx, tunnel.Identity{
		Name: "home", Token: token, User: "alice", HostKey: hostLine, AgentVersion: "e2e",
	}); err != nil {
		t.Fatal(err)
	}
	sess.Handle(func(op string, _ json.RawMessage) (any, error) {
		if op == tunnel.OpStat {
			time.Sleep(300 * time.Millisecond) // a slow computer
			return tunnel.StatResponse{CPU: 0.5, Memory: 12, Disk: 34, Uptime: 56}, nil
		}
		return nil, nil
	})
	go acceptAgentShell(ctx, sess, hostKey, extraHosts...)
	return sess
}

// acceptAgentShell is acceptAgent with a fake sshd that also answers a
// shell request, so interactive splice sessions have a life to lose.
func acceptAgentShell(ctx context.Context, sess *tunnel.Session, host gossh.Signer, extraHosts ...gossh.Signer) {
	for {
		kind, _, conn, err := sess.Accept(ctx)
		if err != nil {
			return
		}
		switch kind {
		case tunnel.KindPortal:
			go httpPong(conn)
		case tunnel.KindSSH:
			go serveShellSSH(conn, host, extraHosts...)
		default:
			conn.Close()
		}
	}
}

// serveShellSSH pretends to be the computer's sshd. It records env requests
// per session and answers "showenv <name>" with the recorded value, so a
// test can prove the server injected an env variable into the session.
func serveShellSSH(conn net.Conn, host gossh.Signer, extraHosts ...gossh.Signer) {
	defer conn.Close()
	cfg := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, _ gossh.PublicKey) (*gossh.Permissions, error) {
			return &gossh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(host)
	for _, s := range extraHosts {
		cfg.AddHostKey(s)
	}
	sc, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go gossh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(gossh.UnknownChannelType, "no")
			continue
		}
		ch, reqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			env := map[string]string{}
			for req := range reqs {
				switch req.Type {
				case "pty-req":
					req.Reply(true, nil)
				case "env":
					var e struct{ Name, Value string }
					if err := gossh.Unmarshal(req.Payload, &e); err == nil && e.Name != "" {
						env[e.Name] = e.Value
					}
					req.Reply(true, nil)
				case "exec":
					var payload struct{ Command string }
					_ = gossh.Unmarshal(req.Payload, &payload)
					req.Reply(true, nil)
					if name, ok := strings.CutPrefix(payload.Command, "showenv "); ok {
						_, _ = io.WriteString(ch, env[name]+"\n")
					} else {
						_, _ = io.WriteString(ch, "hi\n")
					}
					_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
					return
				case "shell":
					req.Reply(true, nil)
					_, _ = io.WriteString(ch, "hi\n")
					// Hold the session open until either side drops it.
					_, _ = io.Copy(io.Discard, ch)
					return
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

// delayUDP proxies UDP packets to backend, delaying every packet by delay
// in each direction. A QUIC session through it sees a slow network; QUIC
// tolerates the mild reordering time.AfterFunc introduces.
func delayUDP(t *testing.T, backend string, delay time.Duration) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	backAddr, err := net.ResolveUDPAddr("udp", backend)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	peers := map[string]net.PacketConn{}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := append([]byte(nil), buf[:n]...)
			mu.Lock()
			peer := peers[from.String()]
			mu.Unlock()
			if peer == nil {
				peer, err = net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					continue
				}
				mu.Lock()
				peers[from.String()] = peer
				mu.Unlock()
				go func(to net.Addr, c net.PacketConn) {
					buf := make([]byte, 64<<10)
					for {
						n, _, err := c.ReadFrom(buf)
						if err != nil {
							return
						}
						pkt := append([]byte(nil), buf[:n]...)
						time.AfterFunc(delay, func() { _, _ = pc.WriteTo(pkt, to) })
					}
				}(from, peer)
			}
			c := peer
			time.AfterFunc(delay, func() { _, _ = c.WriteTo(pkt, backAddr) })
		}
	}()
	return pc.LocalAddr().String()
}

// waitOnline polls the localhost snapshot until the computer's online
// flag reaches want.
func waitOnline(t *testing.T, sock, name string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var snap control.Snapshot
		if err := localCall(t, sock, "snapshot", nil, &snap); err != nil {
			t.Fatal(err)
		}
		for _, c := range snap.Computers {
			if c.Name == name && c.Online == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("computer %s did not reach online=%v", name, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ptyStream pumps a PTY output pipe into a channel so tests can both
// wait for a marker and collect everything seen over a duration without
// blocking on a read that may never come (pipe reads carry no deadline).
type ptyStream struct {
	ch  chan string
	buf string
}

func newPTYStream(r io.Reader) *ptyStream {
	s := &ptyStream{ch: make(chan string, 64)}
	go func() {
		defer close(s.ch)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				s.ch <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

// until returns everything up to and including the next want, keeping any
// trailing bytes buffered for later reads.
func (s *ptyStream) until(t *testing.T, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if i := strings.Index(s.buf, want); i >= 0 {
			out := s.buf[:i+len(want)]
			s.buf = s.buf[i+len(want):]
			return out
		}
		select {
		case chunk, ok := <-s.ch:
			if !ok {
				t.Fatalf("pty closed while waiting for %q, have %q", want, s.buf)
			}
			s.buf += chunk
		case <-deadline:
			t.Fatalf("timed out waiting for %q, got %q", want, s.buf)
		}
	}
}

// forDuration drains the buffered remainder and everything that arrives
// within d.
func (s *ptyStream) forDuration(t *testing.T, d time.Duration) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(s.buf)
	s.buf = ""
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case chunk, ok := <-s.ch:
			if !ok {
				return sb.String()
			}
			sb.WriteString(chunk)
		case <-timer.C:
			return sb.String()
		}
	}
}

// TestTunnelLatency runs the whole computer path through a UDP proxy that
// delays every packet in both directions: join, tunnel hello, a portal
// fetch, a slow stat call, and a splice session all still work.
func TestTunnelLatency(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxy := delayUDP(t, srv.QUICAddr(), 40*time.Millisecond)
	agent := e2eAgent(t, ctx, proxy, srv.QUICFingerprint(), token)
	defer agent.Close()

	var add tunnel.PortalAddResponse
	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 3000}, &add); err != nil {
		t.Fatal(err)
	}
	if body, status := get(t, srv.HTTPAddr(), "web.box.example.com"); status != 200 || body != "pong" {
		t.Fatalf("portal through delay: %d %q", status, body)
	}
	statOut, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "stat home")
	if err != nil {
		t.Fatalf("stat through delay: %v %s", err, errOut)
	}
	if !strings.Contains(statOut, "0.5") || !strings.Contains(statOut, "56") {
		t.Fatalf("stat %q", statOut)
	}
	lsOut, _, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls --json")
	if err != nil || !strings.Contains(lsOut, `"online":true`) {
		t.Fatalf("ls through delay: %v %s", err, lsOut)
	}
	spliceOut, errOut, err := sshRun(t, srv.SSHAddr(), "home", signer, "echo")
	if err != nil || spliceOut != "hi\n" {
		t.Fatalf("splice through delay: %v %s %q", err, errOut, spliceOut)
	}
}

// TestSpliceHostKeyAlgorithm pins the regression behind issue #1: a real
// sshd serves several host keys, and x/crypto's default preference puts
// ECDSA ahead of ed25519, so sshd presented its ECDSA key while the server
// pinned the registered ed25519 key and every splice failed with "host key
// mismatch". The fake sshd here serves both keys while the agent registers
// only the ed25519 line; the splice must still pass.
func TestSpliceHostKeyAlgorithm(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecSigner, err := gossh.NewSignerFromKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	agent := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token, ecSigner)
	defer agent.Close()

	if out, errOut, err := sshRun(t, srv.SSHAddr(), "home", signer, "echo"); err != nil || out != "hi\n" {
		t.Fatalf("splice: %v %s %q", err, errOut, out)
	}
}

// TestSpliceSessionEnv pins issue #2's server side: env set on the server
// reaches the splice session through per-session env requests, and nothing
// about the value is pushed to the computer over the control stream.
func TestSpliceSessionEnv(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	defer agent.Close()

	var opsMu sync.Mutex
	ops := map[string]bool{}
	agent.Handle(func(op string, _ json.RawMessage) (any, error) {
		if op == tunnel.OpStat {
			return tunnel.StatResponse{CPU: 0.5, Memory: 12, Disk: 34, Uptime: 56}, nil
		}
		opsMu.Lock()
		ops[op] = true
		opsMu.Unlock()
		return nil, nil
	})

	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "env set E2E_TOKEN tok-42"); err != nil {
		t.Fatalf("env set: %v %s", err, errOut)
	}
	out, errOut, err := sshRun(t, srv.SSHAddr(), "home", signer, "showenv E2E_TOKEN")
	if err != nil || out != "tok-42\n" {
		t.Fatalf("showenv: %v %s %q", err, errOut, out)
	}
	out, _, err = sshRun(t, srv.SSHAddr(), "home", signer, "showenv NEVER_SET")
	if err != nil || out != "\n" {
		t.Fatalf("unset var leaked something: %v %q", err, out)
	}
	opsMu.Lock()
	envPushed := ops["env"]
	opsMu.Unlock()
	if envPushed {
		t.Fatal("server pushed env over the control stream")
	}
}

// TestAgentInterruption drops the tunnel, checks the offline state and
// error surfaces, reconnects with the same token, kills a live splice
// session mid-stream, and finally restarts the server under the agent.
func TestAgentInterruption(t *testing.T) {
	srv, dir, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	sock := filepath.Join(dir, "box.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	waitOnline(t, sock, "home", true)
	if out, _, err := sshRun(t, srv.SSHAddr(), "home", signer, "echo"); err != nil || out != "hi\n" {
		t.Fatalf("splice before drop: %v %q", err, out)
	}

	// The agent dies. The panel must show offline and splices must say so.
	_ = agent.Close()
	waitOnline(t, sock, "home", false)
	_, errOut, err := sshRun(t, srv.SSHAddr(), "home", signer, "echo")
	if err == nil || !strings.Contains(errOut, "computer is offline") {
		t.Fatalf("splice while offline: %v %s", err, errOut)
	}

	// The agent comes back with the same token and the same host key.
	agent2 := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	defer agent2.Close()
	waitOnline(t, sock, "home", true)

	// A live interactive splice loses the tunnel mid-session and must end,
	// not hang.
	cfg := &gossh.ClientConfig{
		User:            "home",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := gossh.Dial("tcp", srv.SSHAddr(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	readUntil(t, bufio.NewReader(stdout), "hi")
	_ = agent2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Wait() }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("splice session survived the tunnel dying")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("splice session hung after the tunnel died")
	}

	// The server restarts under the agent. The token, the bound key, and
	// the listeners all survive; a fresh dial works again.
	srv.Close()
	srv2, err := server.Start(context.Background(), server.Config{
		Domain: "box.example.com", DataDir: dir,
		SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
		SocketPath: sock, Stdout: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv2.Close() })
	agent3 := e2eAgent(t, ctx, srv2.QUICAddr(), srv2.QUICFingerprint(), token)
	defer agent3.Close()
	waitOnline(t, sock, "home", true)
	if out, _, err := sshRun(t, srv2.SSHAddr(), "home", signer, "echo"); err != nil || out != "hi\n" {
		t.Fatalf("splice after restart: %v %q", err, out)
	}
	if _, _, err := sshRun(t, srv2.SSHAddr(), "box", signer, "ls"); err != nil {
		t.Fatalf("bound key after restart: %v", err)
	}
}

// TestTUIStatesOverPTY drives the TUI through its states over a real PTY:
// approve a join from the pending screen, watch the computer come online,
// see the status notice expire back into the key hints, see the offline
// notice when the tunnel drops, and enter a splice session from the row.
func TestTUIStatesOverPTY(t *testing.T) {
	srv, _, signer := e2eBind(t)
	code, err := secret.ApprovalCode()
	if err != nil {
		t.Fatal(err)
	}
	type joined struct {
		out string
		err error
	}
	joinCh := make(chan joined, 1)
	go func() {
		out, _, err := sshCredentialRun(t, srv.SSHAddr(), "join+home", secret.Hash(code), "x")
		joinCh <- joined{out, err}
	}()

	client, err := gossh.Dial("tcp", srv.SSHAddr(), &gossh.ClientConfig{
		User:            "box",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 28, 100, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	in, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	s := newPTYStream(out)

	s.until(t, "computers", 10*time.Second)
	// The pending screen lists the waiting join; approve it from the form.
	fmt.Fprint(in, "2")
	s.until(t, "home", 10*time.Second)
	fmt.Fprint(in, "\r")
	s.until(t, "code:", 10*time.Second)
	fmt.Fprintf(in, "%s\r", code)
	// The approve notice can be replaced by the "home joined" snapshot
	// diff within one paint, so the durable signals are the empty pending
	// list and the join session finishing.
	s.until(t, "no pending approvals", 10*time.Second)

	var got joined
	select {
	case got = <-joinCh:
	case <-time.After(10 * time.Second):
		t.Fatal("join did not finish")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	var reply struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got.out), &reply); err != nil || reply.Error != "" || reply.Token == "" {
		t.Fatalf("join reply %q: %v", got.out, err)
	}

	// The agent dials; the computers screen shows the online row.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), reply.Token)
	defer agent.Close()
	fmt.Fprint(in, "1")
	s.until(t, "yes", 10*time.Second)

	// Drain the frame tail (it still carries the last notice), wait out
	// the TTL, then force a repaint with a key and read the bar: the
	// notices must be gone and the key hints back.
	s.forDuration(t, time.Second)
	time.Sleep(6 * time.Second)
	fmt.Fprint(in, "?")
	window := s.forDuration(t, 3*time.Second)
	if strings.Contains(window, "home joined") || strings.Contains(window, "home online") {
		t.Fatalf("notice did not expire: %q", window)
	}
	if !strings.Contains(window, "1-6 screens") {
		t.Fatalf("hints did not return: %q", window)
	}

	// The tunnel drops; the panel notices.
	_ = agent.Close()
	fmt.Fprint(in, "1")
	s.until(t, "home offline", 10*time.Second)

	// Back online, then splice into the computer straight from the row.
	agent2 := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), reply.Token)
	defer agent2.Close()
	s.until(t, "yes", 10*time.Second)
	fmt.Fprint(in, "\r")
	s.until(t, "hi", 10*time.Second)
	_ = client.Close()
}
