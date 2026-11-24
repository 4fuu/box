package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/server"
	"github.com/4fuu/box/internal/store"
	"golang.org/x/crypto/ssh"
)

// TestLoopbackComputers is the machine path: a real server, two agents, one
// portal, and an SSH session. It is in package agent so the ssh stream can
// dial a crypto/ssh test server instead of port 22.
func TestLoopbackComputers(t *testing.T) {
	setNoWait(t)
	setSSHDPath(t, filepath.Join(t.TempDir(), "missing-sshd-config"))
	setHome(t, t.TempDir())

	hostSigner, hostLine, err := keys.GenerateSigner("box-test-host")
	if err != nil {
		t.Fatal(err)
	}
	hostPub := filepath.Join(t.TempDir(), "ssh_host_ed25519_key.pub")
	if err := os.WriteFile(hostPub, []byte(hostLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setSSHHost(t, hostPub)
	if hostKey() != strings.TrimSpace(hostLine) {
		t.Fatal("host key hook was not applied")
	}
	setSSHDial(t, startCommandSSH(t, hostSigner))

	dir := t.TempDir()
	var greet bytes.Buffer
	srv, err := server.Start(context.Background(), server.Config{
		Domains: []string{"127.0.0.1", "localhost"}, DataDir: dir,
		SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
		SocketPath: filepath.Join(dir, "box.sock"), Stdout: &greet,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	signer, _, err := keys.GenerateSigner("laptop")
	if err != nil {
		t.Fatal(err)
	}
	pass := oneTimePassword(t, greet.String())
	if _, errOut, err := sshExec(t, srv.SSHAddr(), "pair+"+pass, signer, "whoami"); err != nil {
		t.Fatalf("pair: %v %s", err, errOut)
	}

	homeDir := filepath.Join(t.TempDir(), "home")
	workDir := filepath.Join(t.TempDir(), "work")
	joinApproved(t, srv.SSHAddr(), signer, "home", homeDir)
	joinApproved(t, srv.SSHAddr(), signer, "work", workDir)
	startAgent(t, homeDir)
	startAgent(t, workDir)

	if got := guestRetry(t, filepath.Join(workDir, "agent.sock"), "domain"); got != "127.0.0.1\nlocalhost\n" {
		t.Fatalf("work domain %q", got)
	}

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "from-home")
	}))
	t.Cleanup(hs.Close)
	portalURL, err := url.Parse(hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	added := guestRetry(t, filepath.Join(homeDir, "agent.sock"), "portal", "add", "web", portalURL.Port(), "--domain", "localhost")
	if !strings.Contains(added, "web.localhost") {
		t.Fatalf("portal %q", added)
	}
	body, status := httpGet(t, srv.HTTPAddr(), "web.localhost")
	if status != 200 || body != "from-home" {
		t.Fatalf("portal http %d %q", status, body)
	}

	out, errOut, err := sshExec(t, srv.SSHAddr(), "home", signer, "echo box-e2e")
	if err != nil {
		t.Fatalf("ssh: %v %s", err, errOut)
	}
	if out != "out:echo box-e2e\n" {
		t.Fatalf("stdout %q", out)
	}
}

func TestLoopbackNodeFailover(t *testing.T) {
	setSSHDPath(t, filepath.Join(t.TempDir(), "missing-sshd-config"))
	setHome(t, t.TempDir())
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "from-home")
	}))
	t.Cleanup(portal.Close)
	port := portal.Listener.Addr().(*net.TCPAddr).Port
	const token = "loopback-computer-token"
	startNode := func(computerToken string) *server.Server {
		dir := t.TempDir()
		db, err := store.Open(filepath.Join(dir, "box.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.CreateComputer("home", secret.Hash(computerToken), "alice"); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimPortal("web.127.0.0.1", "home", port, false); err != nil {
			t.Fatal(err)
		}
		srv, err := server.Start(t.Context(), server.Config{
			Domains: []string{"127.0.0.1"}, DataDir: dir,
			SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
			SocketPath: filepath.Join(dir, "box.sock"), Stdout: io.Discard,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		return srv
	}
	a, b := startNode(token), startNode(token)
	// This node can complete TLS but refuses the computer token.
	rejecting := startNode(token + "-different")
	// A slower, first-listed node must not win just because of list order.
	backup := slowQUIC(t, b.QUICAddr())
	dir := t.TempDir()
	state := struct {
		computer
		Nodes []map[string]string `json:"nodes"`
	}{
		computer: computer{Name: "home", User: "alice", Token: token, SSH: a.SSHAddr()},
		Nodes: []map[string]string{
			{"quic": backup, "fingerprint": b.QUICFingerprint()},
			{"quic": rejecting.QUICAddr(), "fingerprint": rejecting.QUICFingerprint()},
			{"quic": a.QUICAddr(), "fingerprint": a.QUICFingerprint()},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "computer.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	startAgent(t, dir)
	if body, status := httpGet(t, a.HTTPAddr(), "web.127.0.0.1"); status != 200 || body != "from-home" {
		t.Fatalf("initial portal via A: %d %q", status, body)
	}
	t.Log("initial portal via fast node A: HTTP 200")
	start := time.Now()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(t.Context(), start.Add(8*time.Second))
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+b.HTTPAddr()+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "web.127.0.0.1"
		if res, err := client.Do(req); err == nil {
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err == nil && res.StatusCode == http.StatusOK && string(body) == "from-home" {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("portal via B did not recover within 8s")
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Logf("portal via B recovered after %s: HTTP 200", time.Since(start).Round(time.Millisecond))
}

func slowQUIC(t *testing.T, addr string) string {
	t.Helper()
	backend, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		var client netip.AddrPort
		for {
			n, from, err := pc.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if from.Port() == uint16(backend.Port) && from.Addr().Unmap() == backend.AddrPort().Addr().Unmap() {
				if client.IsValid() {
					to := client
					pkt := bytes.Clone(buf[:n])
					time.AfterFunc(150*time.Millisecond, func() { _, _ = pc.WriteToUDPAddrPort(pkt, to) })
				}
			} else {
				client = from
				_, _ = pc.WriteToUDP(buf[:n], backend)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func joinApproved(t *testing.T, addr string, signer ssh.Signer, name, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out lockedBuf
	errCh := make(chan error, 1)
	go func() {
		errCh <- Join(ctx, addr, name, "", dir, &out)
	}()
	code := waitPrintedCode(t, &out)
	waitPendingName(t, addr, signer, name)
	approved, errOut, err := sshExec(t, addr, "box", signer, "approve "+code)
	if err != nil {
		t.Fatalf("approve %s: %v %s", name, err, errOut)
	}
	if !strings.Contains(approved, "approved "+name) {
		t.Fatalf("approve %s output %q", name, approved)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("join %s: %v", name, err)
		}
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatalf("join %s did not finish", name)
	}
}

func startAgent(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, dir)
	}()
	t.Cleanup(func() {
		cancel()
		err := <-errCh
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			t.Errorf("agent %s: %v", dir, err)
		}
	})
}

func waitPrintedCode(t *testing.T, out *lockedBuf) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		text := out.String()
		if strings.Contains(text, "approval code: ") {
			return approvalCode(t, text)
		}
		if time.Now().After(deadline) {
			t.Fatal("approval code was not printed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitPendingName(t *testing.T, addr string, signer ssh.Signer, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _, err := sshExec(t, addr, "box", signer, "pending")
		if err == nil && strings.Contains(out, name) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending missing %s", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func oneTimePassword(t *testing.T, out string) string {
	t.Helper()
	const prefix = "one-time password: "
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatal("server did not print a one-time password")
	return ""
}

func sshExec(t *testing.T, addr, user string, signer ssh.Signer, cmd string) (string, string, error) {
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

func httpGet(t *testing.T, addr, host string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

// startCommandSSH is the computer's sshd. It accepts the server's splice key
// and writes the exec command back on stdout. It does not shell out.
func startCommandSSH(t *testing.T, host ssh.Signer) string {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveCommandSSH(conn, cfg)
		}
	}()
	return ln.Addr().String()
}

func serveCommandSSH(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, requests, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				_, _ = fmt.Fprintf(ch, "out:%s\n", payload.Command)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
