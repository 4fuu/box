package agent

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"

	"github.com/4fuu/box/internal/tunnel"
)

// sshDialAddr is the local sshd. Tests replace it: binding port 22 needs
// privileges this process does not have.
var sshDialAddr = "127.0.0.1:22"

func proxyStream(ctx context.Context, kind string, port int, remote net.Conn) {
	defer remote.Close()
	addr, ok := localAddr(kind, port)
	if !ok {
		return
	}
	var d net.Dialer
	local, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return
	}
	defer local.Close()
	splice(remote, local)
}

func localAddr(kind string, port int) (string, bool) {
	switch kind {
	case tunnel.KindSSH:
		return sshDialAddr, true
	case tunnel.KindPortal:
		if port < 1 || port > 65535 {
			return "", false
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), true
	default:
		return "", false
	}
}

func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		closeWrite(b)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
