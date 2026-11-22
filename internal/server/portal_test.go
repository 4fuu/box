package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4fuu/box/internal/tunnel"
)

// portalRequest builds a request for the portal at host.
func portalRequest(t *testing.T, addr, host string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	return req
}

// waitOffline polls the portal until its computer is gone. Box answers
// nothing for a computer it cannot reach, so the request ends without a
// reply; a status of any kind would be one box made up.
func waitOffline(t *testing.T, addr, host string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := "no request sent"
	for {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(portalRequest(t, addr, host))
		if err == nil {
			last = resp.Status
			resp.Body.Close()
			t.Fatalf("offline portal answered %s", resp.Status)
		}
		last = err.Error()
		if strings.Contains(last, "EOF") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("computer stayed online; last %s", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fakeUpstream is a computer that serves HTTP/1.1 on one port with
// keep-alive on. It counts the connections it accepted, so a test can see
// whether the proxy reused one.
type fakeUpstream struct {
	ln      net.Listener
	accepts atomic.Int32
	mu      sync.Mutex
	open    []net.Conn
}

// startUpstream serves "pong" on loopback with keep-alive on.
func startUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	return countingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pong")
	}))
}

// port is the loopback port the computer serves.
func (u *fakeUpstream) port() int {
	return u.ln.Addr().(*net.TCPAddr).Port
}

// stillOpen is how many accepted connections have not been closed.
func (u *fakeUpstream) stillOpen() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.open)
}

type countingListener struct {
	net.Listener
	u *fakeUpstream
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.u.accepts.Add(1)
	l.u.mu.Lock()
	l.u.open = append(l.u.open, c)
	l.u.mu.Unlock()
	return &trackedConn{Conn: c, u: l.u}, nil
}

type trackedConn struct {
	net.Conn
	u    *fakeUpstream
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.u.mu.Lock()
		for i, open := range c.u.open {
			if open == c.Conn {
				c.u.open = append(c.u.open[:i], c.u.open[i+1:]...)
				break
			}
		}
		c.u.mu.Unlock()
	})
	return c.Conn.Close()
}

// countingServer is an HTTP server on loopback that counts the
// connections it accepts, so a test can see whether the proxy reused one.
func countingServer(t *testing.T, handler http.Handler) *fakeUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &fakeUpstream{ln: ln}
	// No idle timeout: the computer keeps the proxy's connection until the
	// proxy closes it.
	srv := &http.Server{Handler: handler}
	go srv.Serve(&countingListener{Listener: ln, u: u})
	t.Cleanup(func() {
		_ = srv.Close()
		u.mu.Lock()
		defer u.mu.Unlock()
		for _, c := range u.open {
			c.Close()
		}
	})
	return u
}

// serveUpstream is a fake computer's stream handler: every portal stream
// goes to the upstream, which keeps the connection until the proxy closes
// it.
func serveUpstream(t *testing.T, ctx context.Context, sess *tunnel.Session, up *fakeUpstream) {
	t.Helper()
	go func() {
		for {
			kind, _, conn, err := sess.Accept(ctx)
			if err != nil {
				return
			}
			if kind != tunnel.KindPortal {
				conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				back, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", up.port()))
				if err != nil {
					return
				}
				defer back.Close()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(back, conn)
					close(done)
				}()
				_, _ = io.Copy(conn, back)
				_ = conn.(interface{ CloseWrite() error }).CloseWrite()
				<-done
			}()
		}
	}()
}

// TestPortalReusesConnection keeps a computer that serves keep-alive and
// asks for five pages one after another. They must share one tunnel stream:
// the forwarder is per host and outlives a request.
func TestPortalReusesConnection(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := startUpstream(t)
	sess := bareAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	serveUpstream(t, ctx, sess, up)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: up.port()}, &add); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	for i := 0; i < 5; i++ {
		resp, err := client.Do(portalRequest(t, srv.HTTPAddr(), "web.box.example.com"))
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "pong" {
			t.Fatalf("request %d: %d %q", i+1, resp.StatusCode, body)
		}
	}
	if n := up.accepts.Load(); n != 1 {
		t.Fatalf("five requests opened %d connections to the computer, want 1", n)
	}
	if n := up.stillOpen(); n != 1 {
		t.Fatalf("%d connections left open after five requests, want 1", n)
	}
}

func TestPortalDoesNotReplayAfterResponseLoss(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32
	up := countingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/action" && attempts.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack upstream: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		fmt.Fprint(w, "pong")
	}))
	sess := bareAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	serveUpstream(t, ctx, sess, up)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: up.port()}, &add); err != nil {
		t.Fatal(err)
	}

	const host = "web.box.example.com"
	if body, status := get(t, srv.HTTPAddr(), host); status != 200 || body != "pong" {
		t.Fatalf("warm-up: %d %q", status, body)
	}
	// Fresh downstream connections prevent client-side retries from masking a box retry.
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req := portalRequest(t, srv.HTTPAddr(), host)
	req.URL.Path = "/action"
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("box answered %s after upstream dropped the response; upstream saw %d attempts", resp.Status, attempts.Load())
	}
	if !strings.Contains(err.Error(), "EOF") || attempts.Load() != 1 {
		t.Fatalf("want EOF after one attempt, got %v after %d attempts", err, attempts.Load())
	}

	retry := portalRequest(t, srv.HTTPAddr(), host)
	retry.URL.Path = "/action"
	resp, err = client.Do(retry)
	if err != nil {
		t.Fatalf("explicit retry: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(body) != "pong" || attempts.Load() != 2 {
		t.Fatalf("explicit retry: %d %q, %v; %d attempts total", resp.StatusCode, body, err, attempts.Load())
	}
}

// TestPortalWaitsForReconnect kills a computer's tunnel and brings a new
// one up shortly after. A request that arrives in the gap is held until the
// tunnel is back, then answered by the computer.
func TestPortalWaitsForReconnect(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := startUpstream(t)
	hostKey, hostLine := newKey(t)
	first := e2eAgentKey(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token, hostKey, hostLine)
	serveUpstream(t, ctx, first, up)
	var add tunnel.PortalAddResponse
	if err := first.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: up.port()}, &add); err != nil {
		t.Fatal(err)
	}
	if body, _ := get(t, srv.HTTPAddr(), "web.box.example.com"); body != "pong" {
		t.Fatalf("before the gap: %q", body)
	}

	_ = first.Close()
	waitOffline(t, srv.HTTPAddr(), "web.box.example.com")

	type result struct {
		body   string
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(portalRequest(t, srv.HTTPAddr(), "web.box.example.com"))
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		done <- result{body: string(body), status: resp.StatusCode}
	}()

	time.Sleep(500 * time.Millisecond)
	second := e2eAgentKey(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token, hostKey, hostLine)
	defer second.Close()
	serveUpstream(t, ctx, second, up)
	if err := second.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: up.port()}, &add); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.status != 200 || got.body != "pong" {
		t.Fatalf("request across the reconnect: %d %q %v", got.status, got.body, got.err)
	}
}

// TestPortalOfflineAborts is the other side of the wait: a computer that
// stays away gets no invented status. The response is aborted, which ends
// the client's request without a reply.
func TestPortalOfflineAborts(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 3000}, &add); err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	waitOffline(t, srv.HTTPAddr(), "web.box.example.com")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(portalRequest(t, srv.HTTPAddr(), "web.box.example.com"))
	if err == nil {
		resp.Body.Close()
		t.Fatalf("offline portal answered %d", resp.StatusCode)
	}
	if waited := time.Since(start); waited < 2*time.Second {
		t.Fatalf("offline portal gave up after %s, before the reconnect wait", waited)
	}
}

// TestPortalGateReachesComputer checks that the access gate still strips
// the token header before the request crosses the tunnel.
func TestPortalGateReachesComputer(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var saw atomic.Bool
	ln := countingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Box-Token") != "" || r.Header.Get("Authorization") != "" {
			saw.Store(true)
		}
		fmt.Fprint(w, "pong")
	}))
	sess := bareAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	serveUpstream(t, ctx, sess, ln)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: ln.port()}, &add); err != nil {
		t.Fatal(err)
	}
	req := portalRequest(t, srv.HTTPAddr(), "web.box.example.com")
	req.Header.Set("X-Box-Token", "not-a-token")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "pong" {
		t.Fatalf("portal: %d %q", resp.StatusCode, body)
	}
	if saw.Load() {
		t.Fatal("the token header reached the computer")
	}
}

// bareAgent is a fake computer that says hello but does not accept streams
// itself, so the test's own handler is the only one serving portals. e2eAgent
// answers portals with httpPong on its own, which would race a test handler.
func bareAgent(t *testing.T, ctx context.Context, addr, fingerprint, token string) *tunnel.Session {
	t.Helper()
	_, hostLine := newKey(t)
	sess, err := tunnel.Dial(ctx, addr, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Hello(ctx, tunnel.Identity{
		Name: "home", Token: token, User: "alice", HostKey: hostLine, AgentVersion: "e2e",
	}); err != nil {
		t.Fatal(err)
	}
	sess.Handle(func(string, json.RawMessage) (any, error) { return nil, nil })
	return sess
}

// serveByPort is a fake computer that forwards each portal stream to the
// loopback port the stream names, so a test can serve different ports.
func serveByPort(t *testing.T, ctx context.Context, sess *tunnel.Session) {
	t.Helper()
	go func() {
		for {
			kind, port, conn, err := sess.Accept(ctx)
			if err != nil {
				return
			}
			if kind != tunnel.KindPortal {
				conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				back, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if err != nil {
					return
				}
				defer back.Close()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(back, conn)
					close(done)
				}()
				_, _ = io.Copy(conn, back)
				<-done
			}()
		}
	}()
}

// TestPortalPortChange reclaims a portal label on a new port. The forwarder
// is cached per host, so it must pick up the new port rather than keep
// dialing the one it was built with.
func TestPortalPortChange(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upA := countingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "A") }))
	upB := countingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "B") }))
	sess := bareAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	serveByPort(t, ctx, sess)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: upA.port()}, &add); err != nil {
		t.Fatal(err)
	}
	if body, _ := get(t, srv.HTTPAddr(), "web.box.example.com"); body != "A" {
		t.Fatalf("before the port change: %q", body)
	}
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: upB.port()}, &add); err != nil {
		t.Fatal(err)
	}
	if body, _ := get(t, srv.HTTPAddr(), "web.box.example.com"); body != "B" {
		t.Fatalf("after the port change: %q, want B (the forwarder kept the old port)", body)
	}
}

func TestPortalSurvivesComputerRename(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := countingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		fmt.Fprint(w, "pong")
	}))
	sess := bareAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	serveUpstream(t, ctx, sess, up)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: up.port()}, &add); err != nil {
		t.Fatal(err)
	}
	const host = "web.box.example.com"
	if body, status := get(t, srv.HTTPAddr(), host); status != 200 || body != "pong" {
		t.Fatalf("before rename: %d %q", status, body)
	}
	if _, stderr, err := sshRun(t, srv.SSHAddr(), "box", signer, "rename home house"); err != nil {
		t.Fatalf("rename: %v %s", err, stderr)
	}
	if body, status := get(t, srv.HTTPAddr(), host); status != 200 || body != "pong" {
		t.Fatalf("after rename: %d %q", status, body)
	}
}
