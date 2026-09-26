package tunnel

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

var errBadHeader = errors.New("bad header")

var _ net.Conn = (*streamConn)(nil)

// streamConn is a QUIC stream as a net.Conn. Deadlines are the stream's.
type streamConn struct {
	str    *quic.Stream
	r      io.Reader
	local  net.Addr
	remote net.Addr
}

func newStreamConn(st *quic.Stream, r io.Reader, local, remote net.Addr) net.Conn {
	if r == nil {
		r = st
	}
	return &streamConn{str: st, r: r, local: local, remote: remote}
}

func (c *streamConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *streamConn) Write(p []byte) (int, error) { return c.str.Write(p) }

func (c *streamConn) Close() error {
	c.str.CancelRead(0)
	return c.str.Close()
}

// CloseWrite finishes the send side and leaves the response readable.
func (c *streamConn) CloseWrite() error { return c.str.Close() }

func (c *streamConn) LocalAddr() net.Addr                { return c.local }
func (c *streamConn) RemoteAddr() net.Addr               { return c.remote }
func (c *streamConn) SetDeadline(t time.Time) error      { return c.str.SetDeadline(t) }
func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.str.SetReadDeadline(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.str.SetWriteDeadline(t) }

func writeHeaderCtx(ctx context.Context, st *quic.Stream, kind string, port int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var mu sync.Mutex
	writing := true
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if writing {
			_ = st.SetWriteDeadline(time.Now())
		}
	})
	err := writeHeader(st, kind, port)
	mu.Lock()
	writing = false
	_ = st.SetWriteDeadline(time.Time{})
	mu.Unlock()
	stop()
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func writeHeader(w io.Writer, kind string, port int) error {
	var line string
	switch kind {
	case KindSSH:
		line = "ssh\n"
	case KindPortal:
		if port < 0 || port > 65535 {
			return errors.New("bad port")
		}
		line = "portal " + strconv.Itoa(port) + "\n"
	default:
		return errBadHeader
	}
	n, err := io.WriteString(w, line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return nil
}

func readHeader(r *bufio.Reader) (string, int, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) || errors.Is(err, io.EOF) {
			return "", 0, errBadHeader
		}
		return "", 0, err
	}
	s := strings.TrimSuffix(string(line), "\n")
	if s == KindSSH {
		return KindSSH, 0, nil
	}
	portStr, ok := strings.CutPrefix(s, "portal ")
	if !ok || portStr == "" {
		return "", 0, errBadHeader
	}
	for _, c := range portStr {
		if c < '0' || c > '9' {
			return "", 0, errBadHeader
		}
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p > 65535 {
		return "", 0, errBadHeader
	}
	return KindPortal, p, nil
}

// acceptHeader reads one stream header. A bad header closes st.
// The returned reader includes any bytes buffered past the header line.
func acceptHeader(ctx context.Context, st *quic.Stream) (string, int, io.Reader, error) {
	br := bufio.NewReaderSize(st, 64)
	type result struct {
		kind string
		port int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		k, p, err := readHeader(br)
		ch <- result{k, p, err}
	}()
	select {
	case <-ctx.Done():
		st.CancelRead(0)
		<-ch
		_ = st.Close()
		return "", 0, nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			st.CancelRead(0)
			_ = st.Close()
			return "", 0, nil, r.err
		}
		return r.kind, r.port, br, nil
	}
}
