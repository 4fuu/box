package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/server"
	"github.com/4fuu/box/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

func TestServeRequiresDomain(t *testing.T) {
	_, err := server.Start(context.Background(), server.Config{
		DataDir: t.TempDir(), SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
	})
	if err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Fatal(err)
	}
}

func TestAddrPersistence(t *testing.T) {
	dir := t.TempDir()
	ssh1, http1, quic1 := freeTCP(t), freeTCP(t), freeUDP(t)
	srv, _ := start(t, dir, "box.example.com", ssh1, http1, quic1)
	if srv.SSHAddr() != ssh1 || srv.HTTPAddr() != http1 || srv.QUICAddr() != quic1 {
		t.Fatalf("bound %s %s %s, want %s %s %s", srv.SSHAddr(), srv.HTTPAddr(), srv.QUICAddr(), ssh1, http1, quic1)
	}
	fp := srv.QUICFingerprint()
	if fp == "" {
		t.Fatal("no fingerprint")
	}
	for _, name := range []string{"quic.pem", "splice_ed25519"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", name, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "github_ed25519")); !os.IsNotExist(err) {
		t.Fatal("github key should not be generated")
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}

	ssh2, http2, quic2 := freeTCP(t), freeTCP(t), freeUDP(t)
	again, _ := start(t, dir, "other.example", ssh2, http2, quic2)
	if again.SSHAddr() != ssh1 || again.HTTPAddr() != http1 || again.QUICAddr() != quic1 {
		t.Fatalf("second start used flags: %s %s %s", again.SSHAddr(), again.HTTPAddr(), again.QUICAddr())
	}
	if again.QUICFingerprint() != fp {
		t.Fatal("quic certificate was regenerated")
	}
	var st struct {
		Domain string `json:"domain"`
	}
	if err := localCall(t, filepath.Join(dir, "box.sock"), "status", nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.Domain != "box.example.com" {
		t.Fatalf("domain changed to %s", st.Domain)
	}
}

func TestUnknownHost421(t *testing.T) {
	dir := t.TempDir()
	srv, _ := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if status := getHost(t, srv.HTTPAddr(), "nope.box.example.com"); status != 421 {
		t.Fatalf("status %d", status)
	}
	if status := getHost(t, srv.HTTPAddr(), "box.example.com"); status != 421 {
		t.Fatalf("apex %d", status)
	}
}

func TestJoinRejectsBadPasswordAndReservedName(t *testing.T) {
	dir := t.TempDir()
	srv, greet := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if _, _, err := sshPasswordRun(t, srv.SSHAddr(), "join+home", "not-a-hash", "x"); err == nil {
		t.Fatal("non-hex join password was accepted")
	}
	out, _, err := sshPasswordRun(t, srv.SSHAddr(), "join+box", secret.Hash("abcdef"), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"error":"rejected"`) {
		t.Fatalf("reserved join: %s", out)
	}
	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	// Bind the key, then a key auth to join+ must not park a session.
	pass := passwordOf(t, greet)
	if _, _, err := sshRun(t, srv.SSHAddr(), "pair+"+pass, signer, "ls"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sshRun(t, srv.SSHAddr(), "join+home", signer, "x"); err == nil {
		t.Fatal("bound key opened join+")
	}
}

func TestComputersTunnel(t *testing.T) {
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
	pass := passwordOf(t, buf.String())
	rawDB, err := os.ReadFile(filepath.Join(dir, "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawDB), pass) {
		t.Fatal("one-time password was stored")
	}

	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "pair+"+pass, signer, "whoami"); err != nil {
		t.Fatalf("pair: %v %s", err, errOut)
	}
	if _, _, err := sshPasswordRun(t, srv.SSHAddr(), "home", "not-a-token", "x"); err == nil {
		t.Fatal("unknown computer accepted a password")
	}

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
		out, _, err := sshPasswordRun(t, srv.SSHAddr(), "join+home", secret.Hash(code), "x")
		joinCh <- joined{out, err}
	}()
	waitPending(t, srv.SSHAddr(), signer, "home")
	approved, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "approve "+code)
	if err != nil {
		t.Fatalf("approve: %v %s", err, errOut)
	}
	if !strings.Contains(approved, "approved home") {
		t.Fatalf("approve output %q", approved)
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
		Token       string `json:"token"`
		QUIC        string `json:"quic"`
		Fingerprint string `json:"fingerprint"`
		Domain      string `json:"domain"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got.out), &reply); err != nil {
		t.Fatalf("join json %q: %v", got.out, err)
	}
	if reply.Error != "" || reply.Token == "" || reply.Domain != "box.example.com" || reply.Fingerprint != srv.QUICFingerprint() {
		t.Fatalf("%+v", reply)
	}
	if reply.QUIC != "box.example.com:"+portOf(srv.QUICAddr()) {
		t.Fatalf("quic %q", reply.QUIC)
	}
	walkNoSecret(t, dir, reply.Token)
	walkNoSecret(t, dir, code)

	bootOut, _, err := sshPasswordRun(t, srv.SSHAddr(), "home", reply.Token, "boot")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bootOut, reply.Token) || !strings.Contains(bootOut, `"quic"`) || strings.Contains(bootOut, `"token"`) {
		t.Fatalf("bootstrap %s", bootOut)
	}
	if _, _, err := sshPasswordRun(t, srv.SSHAddr(), "home", "not-the-token", "boot"); err == nil {
		t.Fatal("bad token opened a session")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	badSess, err := tunnel.Dial(ctx, srv.QUICAddr(), srv.QUICFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badSess.Hello(ctx, tunnel.Identity{Name: "home", Token: "not-the-token", User: "alice"}); err == nil {
		t.Fatal("bad token hello succeeded")
	}
	_ = badSess.Close()

	hostKey, hostLine := newKey(t)
	_, otherLine := newKey(t)
	agent, err := tunnel.Dial(ctx, srv.QUICAddr(), reply.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err := agent.Hello(ctx, tunnel.Identity{
		Name: "home", Token: reply.Token, User: "alice", HostKey: hostLine, AgentVersion: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	agent.Handle(func(op string, _ json.RawMessage) (any, error) {
		if op == tunnel.OpStat {
			return tunnel.StatResponse{CPU: 0.25, Memory: 11, Disk: 22, Uptime: 33}, nil
		}
		return nil, nil
	})
	authed := make(chan ssh.PublicKey, 1)
	var sshStreams atomic.Int32
	go acceptAgent(ctx, agent, hostKey, authed, &sshStreams)

	var add tunnel.PortalAddResponse
	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 3000}, &add); err != nil {
		t.Fatal(err)
	}
	if add.URL != "http://web.box.example.com" || add.Port != 3000 {
		t.Fatalf("portal %+v", add)
	}
	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web.other", Port: 1}, nil); err == nil {
		t.Fatal("dot label was claimed")
	}
	body, status := get(t, srv.HTTPAddr(), "web.box.example.com")
	if status != 200 || body != "pong" {
		t.Fatalf("proxy %d %q", status, body)
	}
	if status := getHost(t, srv.HTTPAddr(), "nope.box.example.com"); status != 421 {
		t.Fatalf("unknown %d", status)
	}
	statOut, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "stat home")
	if err != nil {
		t.Fatalf("stat: %v %s", err, errOut)
	}
	if !strings.Contains(statOut, "0.25") || !strings.Contains(statOut, "33") {
		t.Fatalf("stat %q", statOut)
	}
	lsOut, _, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls --json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lsOut, `"online":true`) || !strings.Contains(lsOut, "t1") || !strings.Contains(lsOut, "web") {
		t.Fatalf("ls %s", lsOut)
	}

	spliceOut, errOut, err := sshRun(t, srv.SSHAddr(), "home", signer, "echo")
	if err != nil {
		t.Fatalf("splice: %v %s", err, errOut)
	}
	if spliceOut != "hi\n" {
		t.Fatalf("splice output %q", spliceOut)
	}
	select {
	case key := <-authed:
		want := publicFrom(t, filepath.Join(dir, "splice_ed25519.pub"))
		if !sameKey(key, want) {
			t.Fatal("splice did not authenticate with the splice key")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ssh server did not see a key")
	}
	keysOut, _, err := sshRun(t, srv.SSHAddr(), "box", signer, "key ls")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(filepath.Join(dir, "splice_ed25519.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(keysOut, strings.TrimSpace(string(pub))) {
		t.Fatal("key ls listed the splice key")
	}
	streams := sshStreams.Load()
	if err := tokenMustNotSplice(t, srv.SSHAddr(), reply.Token); err != nil {
		t.Fatal(err)
	}
	if got := sshStreams.Load(); got != streams {
		t.Fatalf("token opened %d ssh streams", got-streams)
	}

	_ = agent.Close()
	bad, err := tunnel.Dial(ctx, srv.QUICAddr(), reply.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err := bad.Hello(ctx, tunnel.Identity{
		Name: "home", Token: reply.Token, User: "alice", HostKey: otherLine, AgentVersion: "t2",
	}); err != nil {
		t.Fatal(err)
	}
	bad.Handle(func(string, json.RawMessage) (any, error) { return nil, nil })
	go acceptAgent(ctx, bad, hostKey, nil, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls")
		if err == nil && strings.Contains(out, "t2") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement tunnel not visible: %v %s", err, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, errOut, err = sshRun(t, srv.SSHAddr(), "home", signer, "echo")
	if err == nil || !strings.Contains(errOut, "host key mismatch") {
		t.Fatalf("mismatch: %v %s", err, errOut)
	}

	_ = bad.Close()
	deadline = time.Now().Add(5 * time.Second)
	for {
		if status := getHost(t, srv.HTTPAddr(), "web.box.example.com"); status == 502 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("online portal after disconnect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "rm home"); err == nil || !strings.Contains(errOut, "asks for the name") {
		t.Fatalf("rm without confirm: %v %s", err, errOut)
	}
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "rm home home"); err != nil {
		t.Fatalf("rm: %v %s", err, errOut)
	}
	if _, _, err := sshPasswordRun(t, srv.SSHAddr(), "home", reply.Token, "boot"); err == nil {
		t.Fatal("revoked token still authenticated")
	}
	if err := localCall(t, filepath.Join(dir, "box.sock"), "bind", map[string]string{"key": "x"}, nil); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("socket bind: %v", err)
	}
}

func TestCancelledJoinLeavesNoComputer(t *testing.T) {
	dir := t.TempDir()
	srv, greet := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	pass := passwordOf(t, greet)
	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sshRun(t, srv.SSHAddr(), "pair+"+pass, signer, "ls"); err != nil {
		t.Fatal(err)
	}
	code, err := secret.ApprovalCode()
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", srv.SSHAddr(), &ssh.ClientConfig{
		User:            "join+parked",
		Auth:            []ssh.AuthMethod{ssh.Password(secret.Hash(code))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run("x") }()
	waitPending(t, srv.SSHAddr(), signer, "parked")
	client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("join session did not exit")
	}
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "approve "+code); err == nil {
		t.Fatalf("approve after cancel succeeded: %s", errOut)
	}
	out, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls")
	if err != nil {
		t.Fatalf("ls: %v %s", err, errOut)
	}
	if strings.Contains(out, "parked") {
		t.Fatalf("cancelled join left a computer:\n%s", out)
	}
}

type refuseSign struct{ ssh.Signer }

func (refuseSign) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return nil, errors.New("refuse to sign")
}

func TestUnsignedPublicKeyDoesNotBurnPairing(t *testing.T) {
	dir := t.TempDir()
	srv, greet := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	pass := passwordOf(t, greet)
	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sshRun(t, srv.SSHAddr(), "pair+"+pass, refuseSign{signer}, "ls"); err == nil {
		t.Fatal("refused signature authenticated")
	}
	// The query must not have bound the key or consumed the password.
	if _, _, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls"); err == nil {
		t.Fatal("unsigned query bound the key")
	}
	if _, _, err := sshRun(t, srv.SSHAddr(), "pair+"+pass, signer, "ls"); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSnapshotOmitsSecrets(t *testing.T) {
	dir := t.TempDir()
	start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	sock := filepath.Join(dir, "box.sock")
	const secretValue = "super-secret-value"
	var msg struct {
		Message string `json:"message"`
	}
	if err := localCall(t, sock, "env_set", map[string]string{"name": "TOKEN", "value": secretValue}, &msg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg.Message, secretValue) {
		t.Fatal(msg.Message)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := rpc.Write(conn, rpc.Message{Op: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	got, err := rpc.Read(conn)
	if err != nil || !got.OK {
		t.Fatalf("%v %s", err, got.Error)
	}
	body := string(got.Body)
	if strings.Contains(body, secretValue) {
		t.Fatal(body)
	}
	for _, want := range []string{`"env"`, "TOKEN", `"computers"`, `"pending"`, `"portals"`, `"keys"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("snapshot missing %q: %s", want, body)
		}
	}
}

func TestPairOverPTY(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	srv, err := server.Start(context.Background(), server.Config{
		Domain:     "box.example.com",
		DataDir:    dir,
		SSHAddr:    "127.0.0.1:0",
		HTTPAddr:   "127.0.0.1:0",
		QUICAddr:   "127.0.0.1:0",
		SocketPath: filepath.Join(dir, "box.sock"),
		Stdout:     &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	pass := passwordOf(t, buf.String())

	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", srv.SSHAddr(), &ssh.ClientConfig{
		User:            "box",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
	if err := sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
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
	br := bufio.NewReader(out)

	readUntil(t, br, "password: ")
	fmt.Fprintf(in, "%s\r", pass)
	got := readUntil(t, br, "PORTALS")
	if !strings.Contains(got, "NAME") || !strings.Contains(got, "ONLINE") || !strings.Contains(got, "\r\n") {
		t.Fatalf("tui missing the computer table, got %q", got)
	}
	fmt.Fprint(in, "q")
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls"); err != nil {
		t.Fatalf("exec after pair: %v %s", err, errOut)
	}
}

func start(t *testing.T, dir, domain, sshAddr, httpAddr, quicAddr string) (*server.Server, string) {
	t.Helper()
	var buf bytes.Buffer
	srv, err := server.Start(context.Background(), server.Config{
		Domain: domain, DataDir: dir,
		SSHAddr: sshAddr, HTTPAddr: httpAddr, QUICAddr: quicAddr,
		SocketPath: filepath.Join(dir, "box.sock"), Stdout: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, buf.String()
}

func waitPending(t *testing.T, addr string, signer ssh.Signer, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _, err := sshRun(t, addr, "box", signer, "pending")
		if err == nil && strings.Contains(out, name) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending missing %s: %v %s", name, err, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func acceptAgent(ctx context.Context, sess *tunnel.Session, host ssh.Signer, authed chan ssh.PublicKey, sshStreams *atomic.Int32) {
	for {
		kind, _, conn, err := sess.Accept(ctx)
		if err != nil {
			return
		}
		switch kind {
		case tunnel.KindPortal:
			go httpPong(conn)
		case tunnel.KindSSH:
			if sshStreams != nil {
				sshStreams.Add(1)
			}
			go serveAgentSSH(conn, host, authed)
		default:
			conn.Close()
		}
	}
}

// tokenMustNotSplice fails if a computer token can open SFTP or forwarding.
// Those channels are a bound-key splice; the token is only the bootstrap.
func tokenMustNotSplice(t *testing.T, addr, token string) error {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "home",
		Auth:            []ssh.AuthMethod{ssh.Password(token)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	// wish accepts the subsystem request before the handler runs. Wait for
	// that handler to finish, then the caller checks that no ssh stream opened.
	if err := sess.RequestSubsystem("sftp"); err == nil {
		_ = sess.Wait()
	}
	if _, err := client.Dial("tcp", "127.0.0.1:9"); err == nil {
		return errors.New("token opened direct-tcpip")
	}
	if ln, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		ln.Close()
		return errors.New("token opened tcpip-forward")
	}
	return nil
}

func httpPong(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	body := "pong"
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func serveAgentSSH(conn net.Conn, host ssh.Signer, authed chan ssh.PublicKey) {
	defer conn.Close()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if authed != nil {
				select {
				case authed <- key:
				default:
				}
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(host)
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, reqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range reqs {
				if req.Type == "exec" {
					req.Reply(true, nil)
					_, _ = io.WriteString(ch, "hi\n")
					_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
					return
				}
				req.Reply(false, nil)
			}
		}()
	}
}

func newKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return signer, line
}

func publicFrom(t *testing.T, path string) ssh.PublicKey {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func sameKey(a, b ssh.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	return bytes.Equal(a.Marshal(), b.Marshal())
}

func freeTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func freeUDP(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	return addr
}

func portOf(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return p
}

func localCall(t *testing.T, sock, op string, req, resp any) error {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return rpc.Call(conn, op, req, resp)
}

func passwordOf(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "one-time password: ") {
			return strings.TrimPrefix(line, "one-time password: ")
		}
	}
	t.Fatalf("no password in %q", out)
	return ""
}

func sshRun(t *testing.T, addr, user string, signer ssh.Signer, cmd string) (string, string, error) {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", "", err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	err = sess.Run(cmd)
	return stdout.String(), stderr.String(), err
}

func sshPasswordRun(t *testing.T, addr, user, password, cmd string) (string, string, error) {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", "", err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	err = sess.Run(cmd)
	return stdout.String(), stderr.String(), err
}

func getHost(t *testing.T, addr, host string) int {
	t.Helper()
	_, status := get(t, addr, host)
	return status
}

func get(t *testing.T, addr, host string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

func walkNoSecret(t *testing.T, dir, secret string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSocket != 0 {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("secret written to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readUntil(t *testing.T, br *bufio.Reader, want string) string {
	t.Helper()
	type result struct {
		got string
		err error
	}
	ch := make(chan result, 1)
	var mu sync.Mutex
	var seen []byte
	go func() {
		for {
			mu.Lock()
			done := strings.Contains(string(seen), want)
			mu.Unlock()
			if done {
				break
			}
			b, err := br.ReadByte()
			mu.Lock()
			if err != nil {
				got := string(seen)
				mu.Unlock()
				ch <- result{got, err}
				return
			}
			seen = append(seen, b)
			mu.Unlock()
		}
		mu.Lock()
		got := string(seen)
		mu.Unlock()
		ch <- result{got, nil}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read until %q: got %q: %v", want, res.got, res.err)
		}
		return res.got
	case <-time.After(10 * time.Second):
		mu.Lock()
		got := string(seen)
		mu.Unlock()
		t.Fatalf("timed out reading for %q, got %q", want, got)
		return got
	}
}
