package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/guest"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/node"
	"github.com/4fuu/box/internal/runtime"
	"github.com/4fuu/box/internal/server"
	"golang.org/x/crypto/ssh"
)

func TestServeRequiresDomain(t *testing.T) {
	_, err := server.Start(context.Background(), server.Config{
		DataDir: t.TempDir(), SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
	})
	if err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Fatal(err)
	}
}

func TestServerControlPlane(t *testing.T) {
	dir := t.TempDir()
	nodeDir := t.TempDir()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer up.Close()

	var rpcAddr string
	var rpcMu sync.Mutex
	var backendAddr string
	var backendPub ssh.PublicKey
	var backendDials int
	var buf bytes.Buffer
	srv, err := server.Start(context.Background(), server.Config{
		Domain:     "box.example.com",
		DataDir:    dir,
		SSHAddr:    "127.0.0.1:0",
		HTTPAddr:   "127.0.0.1:0",
		SocketPath: filepath.Join(dir, "box.sock"),
		FRPVhost:   up.Listener.Addr().String(),
		Stdout:     &buf,
		Dial: func(ctx context.Context, nodeID, proxy string) (net.Conn, error) {
			if proxy != "rpc" {
				return nil, fmt.Errorf("no %s", proxy)
			}
			rpcMu.Lock()
			addr := rpcAddr
			rpcMu.Unlock()
			if addr == "" {
				return nil, fmt.Errorf("rpc not ready")
			}
			return net.Dial("tcp", addr)
		},
		Backend: func(ctx context.Context, computer string) (net.Conn, ssh.PublicKey, error) {
			backendDials++
			if backendAddr == "" || computer != "web" {
				return nil, nil, fmt.Errorf("no backend")
			}
			c, err := net.Dial("tcp", backendAddr)
			if err != nil {
				return nil, nil, err
			}
			return c, backendPub, nil
		},
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
	out, errOut, err := sshRun(t, srv.SSHAddr(), "pair+"+pass, signer, "image add base registry.example/base:latest")
	if err != nil {
		t.Fatalf("image add: %v\n%s\n%s", err, out, errOut)
	}
	if _, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "image default base"); err != nil {
		t.Fatalf("default: %v %s", err, errOut)
	}
	out, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "node pair")
	if err != nil {
		t.Fatalf("node pair: %v %s", err, errOut)
	}
	code := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "code: ") {
			code = strings.TrimPrefix(line, "code: ")
		}
	}
	if code == "" {
		t.Fatalf("no code in %q", out)
	}
	apiBase := "http://" + srv.HTTPAddr() + "/box/node/v1"
	if err := node.Join(context.Background(), apiBase, "127.0.0.1:7000", code, "home", nodeDir); err != nil {
		t.Fatal(err)
	}
	ctrl, err := node.Open(nodeDir, apiBase)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeRuntime{}
	ctrl.Runtime = fake
	ctrl.Capacity = func() (int, int64, int64, error) { return 8, 16 << 30, 200 << 30, nil }
	nodeCtx, cancelNode := context.WithCancel(context.Background())
	t.Cleanup(cancelNode)
	go ctrl.Serve(nodeCtx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if addr := ctrl.RPCAddr(); addr != "" {
			rpcMu.Lock()
			rpcAddr = addr
			rpcMu.Unlock()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rpc did not listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		out, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "node ls")
		if err == nil && strings.Contains(out, "home") && strings.Contains(out, "yes") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node not online: %v\n%s\n%s", err, out, errOut)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if _, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "env set GH_TOKEN ghp_secret"); err != nil {
		t.Fatalf("env set: %v %s", err, errOut)
	}
	out, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "env ls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ghp_secret") || !strings.Contains(out, "GH_TOKEN") {
		t.Fatalf("env ls leaked or missed name: %q", out)
	}
	if _, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "image pull base home"); err != nil {
		t.Fatalf("pull: %v %s", err, errOut)
	}
	if err := ctrl.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, errOut, err = sshRun(t, srv.SSHAddr(), "box", signer, "new web --image base --node home --cpu 2 --memory 2G --disk 20G --json")
	if err != nil {
		t.Fatalf("new: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(out, "ssh web@box.example.com") {
		t.Fatalf("new output %s", out)
	}
	if status := getHost(t, srv.HTTPAddr(), "web.box.example.com"); status != 421 {
		t.Fatalf("portal before claim: %d", status)
	}
	var portals bytes.Buffer
	if err := guest.Run(filepath.Join(nodeDir, "guests", "web.sock"), []string{"portal", "add", "web", "3000"}, &portals, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(portals.String()) != "http://web.box.example.com" {
		t.Fatalf("portal %q", portals.String())
	}
	if body, status := get(t, srv.HTTPAddr(), "web.box.example.com"); status != 200 || body != "web.box.example.com" {
		t.Fatalf("proxy %d %q", status, body)
	}
	if status := getHost(t, srv.HTTPAddr(), "nope.box.example.com"); status != 421 {
		t.Fatalf("unknown host %d", status)
	}
	if len(fake.specs) != 1 || fake.specs[0].Env["GH_TOKEN"] != "ghp_secret" {
		t.Fatalf("env not injected: %+v", fake.specs)
	}
	walkNoSecret(t, nodeDir, "ghp_secret")
	copied, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "key copy")
	if err != nil {
		t.Fatalf("key copy: %v %s", err, errOut)
	}
	pub, err := os.ReadFile(filepath.Join(dir, "github_ed25519.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if copied != string(pub) {
		t.Fatalf("key copy %q pub %q", copied, pub)
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	backendAddr, backendPub = startBackend(t, authorized)
	out, errOut, err = sshRun(t, srv.SSHAddr(), "web", signer, "echo hi")
	if err != nil {
		t.Fatalf("splice: %v\n%s\n%s", err, out, errOut)
	}
	if strings.TrimSpace(out) != "ran echo hi" {
		t.Fatalf("splice output %q", out)
	}
	if backendDials != 1 {
		t.Fatalf("dials %d", backendDials)
	}
	stranger, _, err := keys.GenerateSigner("stranger")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = sshRun(t, srv.SSHAddr(), "web", stranger, "echo hi"); err == nil {
		t.Fatal("unknown key reached a computer")
	}
	if backendDials != 1 {
		t.Fatal("unknown key opened a backend connection")
	}

	// A restart does not print another password.
	srv.Close()
	var buf2 bytes.Buffer
	srv2, err := server.Start(context.Background(), server.Config{
		DataDir: dir, SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0",
		SocketPath: filepath.Join(dir, "box.sock"), Stdout: &buf2,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv2.Close()
	if strings.Contains(buf2.String(), "one-time password") {
		t.Fatalf("second start printed a password: %s", buf2.String())
	}
}

func startBackend(t *testing.T, authorized ssh.PublicKey) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "box" || !sameKey(key, authorized) {
				return nil, fmt.Errorf("rejected")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveBackend(conn, cfg)
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func serveBackend(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Value string }
				ssh.Unmarshal(req.Payload, &payload)
				req.Reply(true, nil)
				fmt.Fprintf(channel, "ran %s\n", payload.Value)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

func sameKey(a, b ssh.PublicKey) bool {
	ab, bb := a.Marshal(), b.Marshal()
	if len(ab) != len(bb) {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
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
	resp, err := http.DefaultClient.Do(req)
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

type fakeRuntime struct {
	mu    sync.Mutex
	specs []runtime.Spec
}

func (f *fakeRuntime) PrepareHome(context.Context, string, string) error { return nil }
func (f *fakeRuntime) VolumeCreate(context.Context, string) error        { return nil }
func (f *fakeRuntime) VolumeRemove(context.Context, string) error        { return nil }
func (f *fakeRuntime) CopyVolume(context.Context, string, string, string) error {
	return nil
}
func (f *fakeRuntime) Run(_ context.Context, spec runtime.Spec) (runtime.Inspected, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	return runtime.Inspected{BridgeIP: "10.88.0.2", SSHPort: 2200, Running: true}, nil
}
func (f *fakeRuntime) RemoveContainer(context.Context, string) error { return nil }
func (f *fakeRuntime) RenameContainer(context.Context, string, string) error {
	return nil
}
func (f *fakeRuntime) UpdateResources(context.Context, string, *float64, *int64) error {
	return nil
}
func (f *fakeRuntime) Inspect(context.Context, string) (runtime.Inspected, error) {
	return runtime.Inspected{BridgeIP: "10.88.0.2", SSHPort: 2200, Running: true}, nil
}
func (f *fakeRuntime) Pull(context.Context, string) error { return nil }
func (f *fakeRuntime) ImageExists(context.Context, string) (bool, error) {
	return true, nil
}
func (f *fakeRuntime) Stats(context.Context, string) (int64, int64, bool) { return 0, 0, false }

// TestPairOverPTY walks the interactive pairing path the way stock OpenSSH
// uses it: a PTY is allocated, so the client sends raw bytes and Enter
// arrives as \r. The password prompt must accept it, bind the key, and drop
// into the REPL, whose lines must also terminate on \r.
func TestPairOverPTY(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	srv, err := server.Start(context.Background(), server.Config{
		Domain:     "box.example.com",
		DataDir:    dir,
		SSHAddr:    "127.0.0.1:0",
		HTTPAddr:   "127.0.0.1:0",
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
	got := readUntil(t, br, "box ▶")
	if !strings.Contains(got, "Welcome to box") {
		t.Fatalf("no banner before prompt, got %q", got)
	}
	fmt.Fprint(in, "help\r")
	got = readUntil(t, br, "† marks a command with subcommands")
	if !strings.Contains(got, "Common commands:") || !strings.Contains(got, "\r\n") {
		t.Fatalf("help output missing text or CRLF, got %q", got)
	}
	fmt.Fprint(in, "bogus\r")
	readUntil(t, br, `unknown command "bogus"`)
	fmt.Fprint(in, "ls\r")
	readUntil(t, br, "NAME")
	fmt.Fprint(in, "\x04")
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// The key is bound now: the plain exec path works without any password.
	if _, errOut, err := sshRun(t, srv.SSHAddr(), "box", signer, "ls"); err != nil {
		t.Fatalf("exec after pair: %v %s", err, errOut)
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
