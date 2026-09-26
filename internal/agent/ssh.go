package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func sshExchange(ctx context.Context, addr, user, password string) ([]byte, error) {
	client, err := dialSSH(ctx, addr, user, password)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return readSSHLine(ctx, client)
}

func dialSSH(ctx context.Context, addr, user, password string) (*ssh.Client, error) {
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
		Auth: []ssh.AuthMethod{ssh.Password(password)},
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

func readSSHLine(ctx context.Context, client *ssh.Client) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	sess.Stderr = io.Discard
	if err := sess.Shell(); err != nil {
		return nil, err
	}
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

func authFailed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}
