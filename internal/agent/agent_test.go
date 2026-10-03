package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/guest"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

func TestJoin(t *testing.T) {
	setSSHDPath(t, filepath.Join(t.TempDir(), "missing-sshd-config"))
	dir := filepath.Join(t.TempDir(), "state")
	const (
		token = "computer-token"
		fp    = "fpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfpfp"
		quic  = "127.0.0.1:443"
	)
	var mu sync.Mutex
	var gotUser, gotPass string
	addr := startSSH(t, func(user, pass string) (string, error) {
		mu.Lock()
		gotUser, gotPass = user, pass
		mu.Unlock()
		raw, err := json.Marshal(map[string]any{
			"token":       token,
			"quic":        quic,
			"fingerprint": fp,
			"domain":      "box.example.com",
		})
		if err != nil {
			return "", err
		}
		return string(raw), nil
	})

	var out bytes.Buffer
	if err := Join(context.Background(), addr, "home", "", dir, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	code := approvalCode(t, text)
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
	for _, line := range []string{
		"approval code: " + code,
		`enter this code at the server to approve "home"`,
		"waiting\u2026",
		"approved. tunnel up as home.box.example.com",
		"sshd_config is not writable. Add these lines, then reload sshd:",
		sshdKeysLine,
		"AcceptEnv *",
	} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing %q in %q", line, text)
		}
	}

	mu.Lock()
	userName, pass := gotUser, gotPass
	mu.Unlock()
	if userName != "join+home" {
		t.Fatalf("user %q", userName)
	}
	if pass != secret.Hash(code) || pass == code {
		t.Fatal("session credential was not the code hash")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "computer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), code) {
		t.Fatalf("approval code leaked into %s", raw)
	}
	var got computer
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "home" || got.Token != token || got.QUIC != quic || got.Fingerprint != fp ||
		got.Domain != "box.example.com" || got.User != u.Username || got.SSH != addr {
		t.Fatalf("%+v", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "computer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", di.Mode().Perm())
	}
}

func TestJoinRejected(t *testing.T) {
	for _, reason := range []string{"rejected", "expired"} {
		t.Run(reason, func(t *testing.T) {
			setSSHDPath(t, filepath.Join(t.TempDir(), "missing-sshd-config"))
			dir := filepath.Join(t.TempDir(), "state")
			raw, err := json.Marshal(map[string]string{"error": reason})
			if err != nil {
				t.Fatal(err)
			}
			addr := startSSH(t, func(user, pass string) (string, error) {
				return string(raw), nil
			})
			var out bytes.Buffer
			err = Join(context.Background(), addr, "home", "alice", dir, &out)
			if !errors.Is(err, ErrRejected) {
				t.Fatalf("err %v", err)
			}
			if strings.Contains(out.String(), "approved") {
				t.Fatal(out.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "computer.json")); !os.IsNotExist(err) {
				t.Fatal("wrote computer.json", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("created state dir", err)
			}
		})
	}
}

func TestRun(t *testing.T) {
	setNoWait(t)
	dir := t.TempDir()
	setHome(t, dir)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cert, fp, err := tunnel.NewCertificate()
	if err != nil {
		t.Fatal(err)
	}
	const token = "tok"
	var idMu sync.Mutex
	var ids []tunnel.Identity
	srv, err := tunnel.Listen("127.0.0.1:0", cert, func(id tunnel.Identity) (tunnel.HelloResult, error) {
		idMu.Lock()
		ids = append(ids, id)
		idMu.Unlock()
		if id.Token != token {
			return tunnel.HelloResult{}, errors.New("rejected")
		}
		return tunnel.HelloResult{Domain: "box.example.com"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	sshAddr := startSSH(t, func(user, credential string) (string, error) {
		if user != "boot+home" || credential != token {
			return "", errors.New("denied")
		}
		raw, err := json.Marshal(map[string]any{
			"quic":        srv.Addr(),
			"fingerprint": fp,
			"domain":      "box.example.com",
		})
		if err != nil {
			return "", err
		}
		return string(raw), nil
	})
	if err := writeComputer(dir, computer{
		Name: "home", Token: token, QUIC: "127.0.0.1:1", Fingerprint: "stale",
		Domain: "old.example", User: "alice", SSH: sshAddr,
	}); err != nil {
		t.Fatal(err)
	}

	portalLn := listenLocal(t)
	sshLn := listenLocal(t)
	setSSHDial(t, sshLn.Addr().String())
	echo(t, portalLn)
	echo(t, sshLn)

	ctx, cancel := context.WithCancel(context.Background())
	var runWG sync.WaitGroup
	var runErr error
	runWG.Add(1)
	go func() {
		defer runWG.Done()
		runErr = Run(ctx, dir)
	}()
	t.Cleanup(func() {
		cancel()
		runWG.Wait()
	})

	conn := acceptConn(t, srv)
	t.Cleanup(func() { _ = conn.Close() })

	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer callCancel()
	var stat tunnel.StatResponse
	if err := conn.Call(callCtx, tunnel.OpStat, tunnel.StatRequest{}, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Memory <= 0 || stat.Disk <= 0 || stat.Uptime <= 0 || stat.CPU < 0 {
		t.Fatalf("stat %+v", stat)
	}

	// Env pushes no longer exist; keys land bare. Values never touch the disk.
	if err := conn.Call(callCtx, tunnel.OpKeys, tunnel.KeysRequest{AuthorizedKeys: []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake one",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ two",
		"   ",
		"bad\nline",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	keysRaw, err := os.ReadFile(filepath.Join(dir, ".ssh", "box_authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := "" +
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake one" + "\n" +
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ two" + "\n"
	if string(keysRaw) != wantKeys {
		t.Fatalf("keys:\n%s", keysRaw)
	}
	accessRaw, err := os.ReadFile(filepath.Join(dir, accessFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(accessRaw), "env") {
		t.Fatalf("access.json mentions env: %s", accessRaw)
	}
	kfi, err := os.Stat(filepath.Join(dir, ".ssh", "box_authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if kfi.Mode().Perm() != 0o600 {
		t.Fatalf("keys mode %o", kfi.Mode().Perm())
	}
	sfi, err := os.Stat(filepath.Join(dir, ".ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if sfi.Mode().Perm() != 0o700 {
		t.Fatalf(".ssh mode %o", sfi.Mode().Perm())
	}

	portalPort := portalLn.Addr().(*net.TCPAddr).Port
	pconn, err := conn.OpenPortal(callCtx, portalPort)
	if err != nil {
		t.Fatal(err)
	}
	expectPong(t, pconn)
	sconn, err := conn.OpenSSH(callCtx)
	if err != nil {
		t.Fatal(err)
	}
	expectPong(t, sconn)

	var seenMu sync.Mutex
	seen := map[string]int{}
	conn.Handle(func(op string, body json.RawMessage) (any, error) {
		seenMu.Lock()
		seen[op]++
		seenMu.Unlock()
		switch op {
		case rpc.OpDomain:
			return rpc.DomainBody{Domain: "from-handler.example"}, nil
		case tunnel.OpPortalCheck:
			var req tunnel.PortalCheckRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			if req.Label != "web" {
				return nil, errors.New("bad label")
			}
			return tunnel.PortalCheckResponse{Free: false, Holder: "other"}, nil
		case tunnel.OpPortalAdd:
			var req tunnel.PortalAddRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			if req.Label != "web" || req.Port != 3000 {
				return nil, errors.New("bad add")
			}
			return tunnel.PortalAddResponse{URL: "http://web.box.example.com", Host: "web.box.example.com", Port: 3000}, nil
		case tunnel.OpPortalLs:
			return tunnel.PortalList{Portals: []tunnel.Portal{{Label: "web", Port: 3000}}}, nil
		case tunnel.OpPortalRm:
			var req tunnel.PortalRmRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			if req.Label != "web" {
				return nil, errors.New("bad rm")
			}
			return nil, nil
		default:
			return nil, errors.New("unsupported")
		}
	})

	sock := filepath.Join(dir, "agent.sock")
	if got := guestRetry(t, sock, "domain"); got != "from-handler.example\n" {
		t.Fatalf("domain %q", got)
	}
	if got := guestOnce(t, sock, "portal", "check", "web"); got != "taken by other\n" {
		t.Fatalf("check %q", got)
	}
	if got := guestOnce(t, sock, "portal", "add", "web", "3000"); got != "http://web.box.example.com\n" {
		t.Fatalf("add %q", got)
	}
	if got := guestOnce(t, sock, "portal", "ls"); got != "HOST\tPORT\tACCESS\nweb\t3000\tpublic\n" {
		t.Fatalf("ls %q", got)
	}
	if got := guestOnce(t, sock, "portal", "rm", "web"); got != "" {
		t.Fatalf("rm %q", got)
	}
	seenMu.Lock()
	gotSeen := mapsClone(seen)
	seenMu.Unlock()
	for _, op := range []string{rpc.OpDomain, tunnel.OpPortalCheck, tunnel.OpPortalAdd, tunnel.OpPortalLs, tunnel.OpPortalRm} {
		if gotSeen[op] < 1 {
			t.Fatalf("op %s not seen: %v", op, gotSeen)
		}
	}

	saved, err := readComputer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.QUIC != srv.Addr() || saved.Fingerprint != fp || saved.Token != token {
		t.Fatalf("%+v", saved)
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("token logged")
	}
	afi, err := os.Stat(filepath.Join(dir, accessFileName))
	if err != nil {
		t.Fatal(err)
	}
	if afi.Mode().Perm() != 0o600 {
		t.Fatalf("access mode %o", afi.Mode().Perm())
	}

	idMu.Lock()
	if len(ids) < 1 {
		idMu.Unlock()
		t.Fatal("no hello")
	}
	first := ids[0]
	idMu.Unlock()
	if first.Name != "home" || first.Token != token || first.User != "alice" || first.AgentVersion != agentVersion {
		t.Fatalf("identity %+v", first)
	}
	if first.HostKey != hostKey() {
		t.Fatalf("host key %q", first.HostKey)
	}
	if _, err := os.Stat("/etc/ssh/ssh_host_ed25519_key.pub"); err == nil && first.HostKey == "" {
		t.Fatal("empty host key")
	}

	skillRaw := mustRead(t, filepath.Join(dir, ".agents", "skills", "box", "SKILL.md"))
	if !strings.Contains(skillRaw, "This computer is a machine") || !strings.Contains(skillRaw, "box domain") ||
		!strings.Contains(skillRaw, "127.0.0.1") || !strings.Contains(skillRaw, "no credentials") ||
		!strings.Contains(skillRaw, "A label has no dot") {
		t.Fatalf("skill %s", skillRaw)
	}
	low := strings.ToLower(skillRaw)
	for _, bad := range []string{"podman", "container", "mise", "frp"} {
		if strings.Contains(low, bad) {
			t.Fatalf("skill mentions %s", bad)
		}
	}

	next := acceptOneAsync(srv)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	second := <-next
	if second.err != nil {
		t.Fatal(second.err)
	}
	t.Cleanup(func() { _ = second.c.Close() })
	idMu.Lock()
	n := len(ids)
	last := ids[n-1]
	idMu.Unlock()
	if n < 2 || last.Name != "home" || last.Token != token || last.AgentVersion != agentVersion {
		t.Fatalf("reconnect n=%d last=%+v", n, last)
	}

	cancel()
	runWG.Wait()
	if !errors.Is(runErr, context.Canceled) {
		t.Fatal(runErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "computer.json")); err != nil {
		t.Fatal("state wiped on cancel", err)
	}
}

func TestRunTokenRejected(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)
	addr := startSSH(t, func(user, credential string) (string, error) {
		return `{"error":"rejected"}`, nil
	})
	if err := writeComputer(dir, computer{
		Name: "home", Token: "tok", QUIC: "127.0.0.1:9", Fingerprint: "aa",
		Domain: "box.example.com", User: "alice", SSH: addr,
	}); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- Run(context.Background(), dir) }()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("state dir remains: %v", err)
	}
}

func TestRunHelloRejected(t *testing.T) {
	setNoWait(t)
	dir := t.TempDir()
	setHome(t, dir)
	cert, fp, err := tunnel.NewCertificate()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := tunnel.Listen("127.0.0.1:0", cert, func(tunnel.Identity) (tunnel.HelloResult, error) {
		return tunnel.HelloResult{}, errors.New("rejected")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _ := srv.Accept(ctx)
		if c != nil {
			_ = c.Close()
		}
	}()
	addr := startSSH(t, func(user, credential string) (string, error) {
		if user != "boot+home" || credential != "tok" {
			return "", errors.New("denied")
		}
		raw, err := json.Marshal(map[string]any{
			"quic":        srv.Addr(),
			"fingerprint": fp,
			"domain":      "box.example.com",
		})
		if err != nil {
			return "", err
		}
		return string(raw), nil
	})
	if err := writeComputer(dir, computer{
		Name: "home", Token: "tok", QUIC: "127.0.0.1:1", Fingerprint: "stale",
		User: "alice", SSH: addr,
	}); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- Run(context.Background(), dir) }()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrRejected) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("state dir remains: %v", err)
	}
}

func TestRunKeepsStateOnNetworkError(t *testing.T) {
	setNoWait(t)
	dir := t.TempDir()
	setHome(t, dir)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeComputer(dir, computer{
		Name: "home", Token: "tok", QUIC: "127.0.0.1:1", Fingerprint: "aa",
		User: "alice", SSH: addr,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = Run(ctx, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "computer.json")); err != nil {
		t.Fatal(err)
	}
}

func TestFormatKeyLine(t *testing.T) {
	if formatKeyLine("ssh-ed25519 AAAA comment") != "ssh-ed25519 AAAA comment" {
		t.Fatal("plain line changed")
	}
	if formatKeyLine("  ssh-ed25519 AAAA  ") != "ssh-ed25519 AAAA" {
		t.Fatal("not trimmed")
	}
	if formatKeyLine("  \n") != "" {
		t.Fatal("blank line")
	}
	if formatKeyLine("bad\nline") != "" {
		t.Fatal("multiline line")
	}
}

// TestAccessPurgesLegacyEnv covers upgrade: an access.json written by an
// older box carries env values on disk. The first boot after upgrade must
// drop them and rewrite the managed keys file without environment= options.
func TestAccessPurgesLegacyEnv(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)
	legacy := `{"authorized_keys": ["ssh-ed25519 AAAA one"], "env": {"GH_TOKEN": "legacy-secret"}}`
	if err := os.WriteFile(filepath.Join(dir, accessFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ssh", "box_authorized_keys"),
		[]byte("environment=\"GH_TOKEN=legacy-secret\" ssh-ed25519 AAAA one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &agent{dir: dir}
	if err := a.loadAndRewrite(); err != nil {
		t.Fatal(err)
	}
	access, err := os.ReadFile(filepath.Join(dir, accessFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(access), "legacy-secret") || strings.Contains(string(access), `"env"`) {
		t.Fatalf("env survived: %s", access)
	}
	got := mustRead(t, filepath.Join(dir, ".ssh", "box_authorized_keys"))
	if got != "ssh-ed25519 AAAA one\n" {
		t.Fatalf("keys file kept options:\n%s", got)
	}
}

func TestAccessKeepsBothSets(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)
	a := &agent{dir: dir}
	keysBody, err := json.Marshal(tunnel.KeysRequest{AuthorizedKeys: []string{"ssh-ed25519 AAAA one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.onControl(tunnel.OpKeys, keysBody); err != nil {
		t.Fatal(err)
	}
	b := &agent{dir: dir}
	if err := b.loadAndRewrite(); err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, filepath.Join(dir, ".ssh", "box_authorized_keys"))
	want := "ssh-ed25519 AAAA one\n"
	if got != want {
		t.Fatalf("after reload:\n%s", got)
	}
	c := &agent{dir: dir}
	if err := c.loadAndRewrite(); err != nil {
		t.Fatal(err)
	}
	keysBody, err = json.Marshal(tunnel.KeysRequest{AuthorizedKeys: []string{"restrict ssh-rsa BBB two"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.onControl(tunnel.OpKeys, keysBody); err != nil {
		t.Fatal(err)
	}
	got = mustRead(t, filepath.Join(dir, ".ssh", "box_authorized_keys"))
	want = "restrict ssh-rsa BBB two\n"
	if got != want {
		t.Fatalf("after keys push:\n%s", got)
	}
	fi, err := os.Stat(filepath.Join(dir, accessFileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("access mode %o", fi.Mode().Perm())
	}
}

func TestPushDoesNotCommitOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".ssh"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &agent{dir: dir, keys: []string{"ssh-ed25519 KEEP"}}
	body, err := json.Marshal(tunnel.KeysRequest{AuthorizedKeys: []string{"ssh-ed25519 AAAA"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.onControl(tunnel.OpKeys, body); err == nil {
		t.Fatal("expected write failure")
	}
	if len(a.keys) != 1 || a.keys[0] != "ssh-ed25519 KEEP" {
		t.Fatalf("memory changed keys=%v", a.keys)
	}
	if _, err := os.Stat(filepath.Join(dir, accessFileName)); !os.IsNotExist(err) {
		t.Fatal("access.json written", err)
	}
}

func TestSSHDConfig(t *testing.T) {
	got, changed := addBoxAuthorizedKeys("#AuthorizedKeysFile .ssh/authorized_keys\n")
	if !changed || !strings.Contains(got, sshdKeysLine) {
		t.Fatalf("commented: %q changed %v", got, changed)
	}
	again, changed := addBoxAuthorizedKeys(got)
	if changed || again != got {
		t.Fatal("duplicated")
	}
	got, changed = addBoxAuthorizedKeys("AuthorizedKeysFile .ssh/authorized_keys .ssh/authorized_keys2\n")
	if !changed {
		t.Fatal("expected change")
	}
	if strings.Count(got, boxAuthorizedKeys) != 1 {
		t.Fatalf("count: %s", got)
	}
	if !strings.Contains(got, ".ssh/authorized_keys2") {
		t.Fatalf("dropped existing file: %s", got)
	}
	got, changed = addBoxAuthorizedKeys("# c\nMatch User x\nAuthorizedKeysFile .ssh/authorized_keys\n")
	if !changed {
		t.Fatal("expected insert")
	}
	if !strings.HasPrefix(got, "# c\n"+sshdKeysLine+"\nMatch User x\n") {
		t.Fatalf("match insert: %q", got)
	}
	eq := "AuthorizedKeysFile=.ssh/authorized_keys\n"
	got, changed = addBoxAuthorizedKeys(eq)
	if !changed || strings.Count(got, "AuthorizedKeysFile") != 1 || !strings.Contains(got, boxAuthorizedKeys) {
		t.Fatalf("equals form: %q changed %v", got, changed)
	}
	already := "AuthorizedKeysFile=.ssh/authorized_keys .ssh/box_authorized_keys\n"
	got, changed = addBoxAuthorizedKeys(already)
	if changed || got != already {
		t.Fatalf("equals already present: %q changed %v", got, changed)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(path, []byte("#AuthorizedKeysFile .ssh/authorized_keys\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, _, err := updateSSHD(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("notes %v", notes)
	}
	notes, warning, err := updateSSHD(path)
	if err != nil || warning != "" || len(notes) != 0 {
		t.Fatalf("second update notes %v warning %q err %v", notes, warning, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), boxAuthorizedKeys) != 1 || strings.Count(string(body), "AcceptEnv *") != 1 {
		t.Fatalf("file %s", body)
	}
	turned, changed, warn := ensureAcceptEnv("AcceptEnv\n")
	if !changed || warn != "" || !strings.Contains(turned, "AcceptEnv *") {
		t.Fatalf("bare: %q changed %v warn %q", turned, changed, warn)
	}
	kept, changed, warn := ensureAcceptEnv("AcceptEnv LANG LC_*\n")
	if changed || kept != "AcceptEnv LANG LC_*\n" || warn == "" {
		t.Fatalf("pattern: %q changed %v warn %q", kept, changed, warn)
	}
	_, changed, warn = ensureAcceptEnv("AcceptEnv LANG\nAcceptEnv *\n")
	if changed || warn != "" {
		t.Fatalf("second line wildcard: changed %v warn %q", changed, warn)
	}
	inserted, changed, warn := ensureAcceptEnv("# c\nMatch User x\nAcceptEnv LANG\n")
	if !changed || warn != "" || !strings.HasPrefix(inserted, "# c\nAcceptEnv *\nMatch") {
		t.Fatalf("match insert: %q changed %v warn %q", inserted, changed, warn)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".sshd-config-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp left behind: %v", matches)
	}
}

func TestReadStat(t *testing.T) {
	s := readStat()
	if s.Memory <= 0 || s.Disk <= 0 || s.Uptime <= 0 || s.CPU < 0 {
		t.Fatalf("%+v", s)
	}
}

func setNoWait(t *testing.T) {
	t.Helper()
	prev := waitBackoff
	waitBackoff = func(ctx context.Context, attempt int) error {
		return ctx.Err()
	}
	t.Cleanup(func() { waitBackoff = prev })
}

func setSSHDial(t *testing.T, addr string) {
	t.Helper()
	prev := sshDialAddr
	sshDialAddr = addr
	t.Cleanup(func() { sshDialAddr = prev })
}

func setSSHHost(t *testing.T, path string) {
	t.Helper()
	prev := sshHostPub
	sshHostPub = path
	t.Cleanup(func() { sshHostPub = prev })
}

func setSSHDPath(t *testing.T, path string) {
	t.Helper()
	prev := sshdConfigPath
	sshdConfigPath = path
	t.Cleanup(func() { sshdConfigPath = prev })
}

func setHome(t *testing.T, dir string) {
	t.Helper()
	prev, ok := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv("HOME", prev)
			return
		}
		_ = os.Unsetenv("HOME")
	})
}

func approvalCode(t *testing.T, out string) string {
	t.Helper()
	const prefix = "approval code: "
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("no code in %q", out)
	return ""
}

// startSSH is a stand-in for the server's SSH endpoint. It accepts any key —
// the agent dials with a fresh key pair — and answers the credential line the
// agent sends as the first line of the session with onAuth's reply.
func startSSH(t *testing.T, onAuth func(user, credential string) (string, error)) string {
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
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)
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
			go serveSSH(conn, cfg, onAuth)
		}
	}()
	return ln.Addr().String()
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig, onAuth func(user, credential string) (string, error)) {
	defer conn.Close()
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "shell" && req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				credCh := make(chan string, 1)
				go func() {
					line, err := bufio.NewReader(channel).ReadString('\n')
					if err != nil {
						credCh <- ""
						return
					}
					credCh <- strings.TrimSpace(line)
				}()
				var credential string
				select {
				case credential = <-credCh:
				case <-time.After(5 * time.Second):
					return
				}
				reply, err := onAuth(sc.User(), credential)
				if err != nil {
					return
				}
				_, _ = io.WriteString(channel, reply+"\n")
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

type accepted struct {
	c   *tunnel.Conn
	err error
}

func acceptConn(t *testing.T, srv *tunnel.Server) *tunnel.Conn {
	t.Helper()
	got := <-acceptOneAsync(srv)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.c == nil {
		t.Fatal("nil conn")
	}
	return got.c
}

func acceptOneAsync(srv *tunnel.Server) <-chan accepted {
	ch := make(chan accepted, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := srv.Accept(ctx)
		ch <- accepted{c, err}
	}()
	return ch
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func echo(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("pong"))
			}(c)
		}
	}()
}

func expectPong(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("got %q", buf)
	}
}

func guestRetry(t *testing.T, sock string, args ...string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		var stdout, stderr bytes.Buffer
		err := guest.Run(context.Background(), sock, args, nil, &stdout, &stderr)
		if err == nil {
			return stdout.String()
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("guest %v: %v", args, last)
	return ""
}

func guestOnce(t *testing.T, sock string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := guest.Run(context.Background(), sock, args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("guest %v: %v (%s)", args, err, stderr.String())
	}
	return stdout.String()
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mapsClone(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
