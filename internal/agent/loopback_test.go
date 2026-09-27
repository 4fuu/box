package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/server"
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
		Domain: "127.0.0.1", DataDir: dir,
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

	if got := guestRetry(t, filepath.Join(workDir, "agent.sock"), "domain"); got != "127.0.0.1\n" {
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
	added := guestRetry(t, filepath.Join(homeDir, "agent.sock"), "portal", "add", "web", portalURL.Port())
	if !strings.Contains(added, "web.127.0.0.1") {
		t.Fatalf("portal %q", added)
	}
	body, status := httpGet(t, srv.HTTPAddr(), "web.127.0.0.1")
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
