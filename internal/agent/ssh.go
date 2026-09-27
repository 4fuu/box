package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/4fuu/box/internal/keys"
	"golang.org/x/crypto/ssh"
)

// sshExchange opens a machine session as user with a fresh key pair —
// machine keys are never bound on the server — sends the one credential
// line, and returns the server's first reply line.
func sshExchange(ctx context.Context, addr, user, credential string) ([]byte, error) {
	client, err := dialSSH(ctx, addr, user)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return readSSHLine(ctx, client, credential)
}

func dialSSH(ctx context.Context, addr, user string) (*ssh.Client, error) {
	signer, _, err := keys.GenerateSigner("box-machine")
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Bound the handshake only. Join then waits on the caller's context.
	stop := context.AfterFunc(hctx, func() { _ = conn.Close() })
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The operator typed this host. The QUIC pin arrives on this
		// session, so there is no host key to check yet.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if !stop() {
		if err == nil {
			_ = c.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("ssh handshake timed out")
	}
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// readSSHLine sends the credential line and reads the server's first reply.
// The write runs in its own goroutine: the server may exit the session
// before reading, and a failed write must not mask the reply.
func readSSHLine(ctx context.Context, client *ssh.Client, credential string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	sess.Stderr = io.Discard
	if err := sess.Shell(); err != nil {
		return nil, err
	}
	go func() {
		_, _ = io.WriteString(stdin, credential+"\n")
		_ = stdin.Close()
	}()
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		br := bufio.NewReader(io.LimitReader(stdout, 1<<20))
		line, err := br.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		_ = client.Close()
		<-ch
		return nil, ctx.Err()
	case r := <-ch:
		line := bytes.TrimSpace(r.line)
		if len(line) == 0 {
			if r.err != nil {
				return nil, r.err
			}
			return nil, errors.New("empty ssh reply")
		}
		return line, nil
	}
}
