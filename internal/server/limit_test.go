package server_test

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/ratelimit"
	"golang.org/x/crypto/ssh"
)

// attempt offers one unbound key and runs the session it opens. It returns
// what the session printed: the password prompt while pairing is live, or
// nothing once the address is refused.
func attempt(t *testing.T, addr string) (string, error) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "box",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = addr
	sess, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput("")
	return string(out), err
}

// TestUnknownKeysAreRateLimited drives the real SSH entry. Every key here is
// new, so each attempt is a guess. While a pairing password is live an
// unknown key still opens a session, which stops at the password prompt and
// is counted. After the burst, the address is refused before any session.
func TestUnknownKeysAreRateLimited(t *testing.T) {
	dir := t.TempDir()
	sshAddr, httpAddr, quicAddr := freeTCP(t), freeTCP(t), freeUDP(t)
	start(t, dir, "box.example.com", sshAddr, httpAddr, quicAddr)

	for i := 0; i < ratelimit.Burst; i++ {
		out, _ := attempt(t, sshAddr)
		if !strings.Contains(out, "password:") {
			t.Fatalf("attempt %d was refused before the burst: %q", i+1, out)
		}
	}
	// The failure that reaches the limit earns the ban, so that attempt
	// still runs its session. The next one is refused inside the callback.
	if out, _ := attempt(t, sshAddr); !strings.Contains(out, "password:") {
		t.Fatalf("the banning attempt was refused early: %q", out)
	}
	for i := 0; i < 2; i++ {
		if out, err := attempt(t, sshAddr); err == nil || strings.Contains(out, "password:") {
			t.Fatalf("banned attempt %d opened a session: %q %v", i+1, out, err)
		}
	}
}
