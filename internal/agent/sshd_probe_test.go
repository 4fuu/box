package agent

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/guest"
	"github.com/4fuu/box/internal/keys"
)

// writeTestHostKey writes a valid sshd host public key and points the agent
// at it. The key never leaves the test's temp dir.
func writeTestHostKey(t *testing.T) string {
	t.Helper()
	_, line, err := keys.GenerateSigner("box-test-host")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ssh_host_ed25519_key.pub")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setSSHHost(t, path)
	return strings.TrimSpace(line)
}

// startBannerListener stands in for sshd: it writes one line and closes.
func startBannerListener(t *testing.T, banner string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Write([]byte(banner))
			}(c)
		}
	}()
	return ln
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestProbeSSHD(t *testing.T) {
	writeTestHostKey(t)
	ln := startBannerListener(t, "SSH-2.0-OpenSSH_9.9 box-test\r\n")
	setSSHDial(t, ln.Addr().String())
	if err := probeSSHD(); err != nil {
		t.Fatal(err)
	}
}

// RFC 4253 lets the server send other lines before its identification.
func TestProbeSSHDAllowsPrelude(t *testing.T) {
	writeTestHostKey(t)
	ln := startBannerListener(t, "not the banner\r\nSSH-2.0-OpenSSH_9.9 box-test\r\n")
	setSSHDial(t, ln.Addr().String())
	if err := probeSSHD(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeSSHDRejectsMissingOrBadHostKey(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		setSSHHost(t, filepath.Join(t.TempDir(), "missing.pub"))
		setSSHDial(t, closedAddr(t))
		err := probeSSHD()
		if err == nil || !strings.Contains(err.Error(), "host key") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("not a key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "host.pub")
		if err := os.WriteFile(path, []byte("this is not a public key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		setSSHHost(t, path)
		setSSHDial(t, closedAddr(t))
		err := probeSSHD()
		if err == nil || !strings.Contains(err.Error(), "not usable") {
			t.Fatalf("err %v", err)
		}
	})
}

func TestProbeSSHDRejectsClosedPortAndNonSSH(t *testing.T) {
	writeTestHostKey(t)
	t.Run("closed", func(t *testing.T) {
		setSSHDial(t, closedAddr(t))
		err := probeSSHD()
		if err == nil || !strings.Contains(err.Error(), "not reachable") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("not ssh", func(t *testing.T) {
		ln := startBannerListener(t, "HTTP/1.1 200 OK\r\n\r\n")
		setSSHDial(t, ln.Addr().String())
		err := probeSSHD()
		if err == nil || !strings.Contains(err.Error(), "no SSH identification line") {
			t.Fatalf("err %v", err)
		}
	})
}

// enableSSHD succeeds without a tunnel: the probe stands and the next
// reconnect carries the key.
func TestEnableSSHDWithoutTunnel(t *testing.T) {
	writeTestHostKey(t)
	ln := startBannerListener(t, "SSH-2.0-OpenSSH_9.9 box-test\r\n")
	setSSHDial(t, ln.Addr().String())
	a := &agent{dir: t.TempDir()}
	res := a.enableSSHD()
	if !res.Enabled || res.Reconnecting || res.Error != "" {
		t.Fatalf("%+v", res)
	}
	if res.Output == "" {
		t.Fatal("ensureSSHD printed nothing")
	}
}

func TestEnableSSHDLeavesSSHDisabledOnProbeFailure(t *testing.T) {
	writeTestHostKey(t)
	setSSHDial(t, closedAddr(t))
	a := &agent{dir: t.TempDir()}
	res := a.enableSSHD()
	if res.Enabled || res.Reconnecting || res.Error == "" {
		t.Fatalf("%+v", res)
	}
	checkGuestProbeDisabled(t, a)
}

// checkGuestProbeDisabled runs the guest command against a live agent socket
// with no tunnel and checks the error names the disabled state.
func checkGuestProbeDisabled(t *testing.T, a *agent) {
	t.Helper()
	if err := a.listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopListen)
	var stdout, stderr bytes.Buffer
	err := guest.Run(context.Background(), filepath.Join(a.dir, "agent.sock"), []string{"sshd", "enable"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("enable succeeded: %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "ssh is still disabled") || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err %v", err)
	}
}
