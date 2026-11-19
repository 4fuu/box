package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/tunnel"
)

// TestSSHDEnableFromGuestSocket is the whole path: the guest command reaches
// the agent, the probe passes, the agent drops its tunnel, and the next hello
// carries the host key the server stores for splices.
func TestSSHDEnableFromGuestSocket(t *testing.T) {
	setNoWait(t)
	dir := t.TempDir()
	setHome(t, dir)
	// Before OpenSSH Server is installed there is no host key: the first
	// hello must carry none, like a real pre-install join.
	setSSHHost(t, filepath.Join(t.TempDir(), "ssh_host_ed25519_key.pub"))
	setSSHDial(t, startBannerListener(t, "SSH-2.0-OpenSSH_9.9 box-test\r\n").Addr().String())

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
		Name: "home", Token: token, QUIC: srv.Addr(), Fingerprint: fp,
		Domain: "box.example.com", User: "alice", SSH: sshAddr,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = Run(ctx, dir)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	conn := acceptConn(t, srv)
	t.Cleanup(func() { _ = conn.Close() })
	conn.Handle(func(op string, _ json.RawMessage) (any, error) {
		if op == rpc.OpDomain {
			return rpc.DomainBody{Domain: "box.example.com"}, nil
		}
		return nil, errors.New("unsupported")
	})

	sock := filepath.Join(dir, "agent.sock")
	// The domain call proves the agent has a session before enable closes it.
	if got := guestRetry(t, sock, "domain"); got != "box.example.com\n" {
		t.Fatalf("domain %q", got)
	}

	// Installing sshd produces the host key the probe reads.
	wantHost := writeTestHostKey(t)
	next := acceptOneAsync(srv)
	got := guestRetry(t, sock, "sshd", "enable")
	if !strings.Contains(got, "ssh enabled; reconnecting the tunnel") {
		t.Fatalf("enable output %q", got)
	}
	second := <-next
	if second.err != nil {
		t.Fatal(second.err)
	}
	t.Cleanup(func() { _ = second.c.Close() })
	if second.c.Identity().HostKey != wantHost {
		t.Fatalf("reconnect host key %q, want %q", second.c.Identity().HostKey, wantHost)
	}
	if second.c.Identity().Name != "home" {
		t.Fatalf("reconnect name %q", second.c.Identity().Name)
	}

	idMu.Lock()
	n := len(ids)
	first := ids[0]
	last := ids[n-1]
	idMu.Unlock()
	if first.HostKey != "" {
		t.Fatalf("first hello had host key %q", first.HostKey)
	}
	if last.HostKey != wantHost {
		t.Fatalf("last hello host key %q", last.HostKey)
	}
}
