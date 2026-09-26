package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func testID() Identity {
	return Identity{
		Name:         "home",
		Token:        "secret",
		User:         "alice",
		HostKey:      "ssh-ed25519 AAAA",
		AgentVersion: "dev",
	}
}

func newTestServer(t *testing.T, token string) (*Server, string) {
	t.Helper()
	cert, fp, err := NewCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if fp != Fingerprint(cert.Certificate[0]) || len(fp) != 64 {
		t.Fatalf("fingerprint %q", fp)
	}
	srv, err := Listen("127.0.0.1:0", cert, func(id Identity) (HelloResult, error) {
		if id.Token != token {
			return HelloResult{}, errors.New("rejected token")
		}
		return HelloResult{Domain: "box.example.com", HTTPPort: 80}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, fp
}

type accepted struct {
	c   *Conn
	err error
}

func acceptAsync(srv *Server) <-chan accepted {
	ch := make(chan accepted, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := srv.Accept(ctx)
		ch <- accepted{c, err}
	}()
	return ch
}

func dial(t *testing.T, addr, fp string) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	s, err := Dial(ctx, addr, fp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func helloOK(t *testing.T, s *Session, id Identity) HelloResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.Hello(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Domain != "box.example.com" || res.HTTPPort != 80 {
		t.Fatalf("hello result %+v", res)
	}
	return res
}

func TestStreamsAndHello(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	helloOK(t, sess, testID())
	got := <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	conn := got.c
	t.Cleanup(func() { _ = conn.Close() })
	if conn.Name() != "home" {
		t.Fatalf("name %q", conn.Name())
	}
	id := conn.Identity()
	if id.User != "alice" || id.HostKey != "ssh-ed25519 AAAA" || id.AgentVersion != "dev" {
		t.Fatalf("identity user=%q host=%q agent=%q", id.User, id.HostKey, id.AgentVersion)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ssh, err := conn.OpenSSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kind, port, peer, err := sess.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindSSH || port != 0 {
		t.Fatalf("ssh stream %s %d", kind, port)
	}
	exchange(t, ssh, peer)

	portal, err := conn.OpenPortal(ctx, 3000)
	if err != nil {
		t.Fatal(err)
	}
	kind, port, peer, err = sess.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindPortal || port != 3000 {
		t.Fatalf("portal stream %s %d", kind, port)
	}
	if peer.LocalAddr() == nil || peer.RemoteAddr() == nil {
		t.Fatal("missing addr")
	}
	exchange(t, portal, peer)
}

func exchange(t *testing.T, a, b interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	SetDeadline(time.Time) error
}) {
	t.Helper()
	defer a.Close()
	defer b.Close()
	deadline := time.Now().Add(5 * time.Second)
	_ = a.SetDeadline(deadline)
	_ = b.SetDeadline(deadline)
	if _, err := a.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(b, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("got %q", buf)
	}
	if _, err := b.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(a, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "pong" {
		t.Fatalf("got %q", buf)
	}
}

func TestBadToken(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := testID()
	id.Token = "nope"
	_, err := sess.Hello(ctx, id)
	if err == nil || err.Error() != "rejected token" {
		t.Fatalf("hello: %v", err)
	}
	got := <-acc
	if got.err == nil || got.err.Error() != "rejected token" {
		t.Fatalf("accept: %v", got.err)
	}
	if err := sess.Call(ctx, OpStat, StatRequest{}, nil); err == nil {
		t.Fatal("call on refused session")
	}
	actx, acancel := context.WithTimeout(context.Background(), time.Second)
	defer acancel()
	if _, _, _, err := sess.Accept(actx); err == nil {
		t.Fatal("accept on refused session")
	}
}

func TestWrongVersion(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := sess.hello(ctx, testID(), 99)
	if err == nil || err.Error() != "unsupported version" {
		t.Fatalf("hello: %v", err)
	}
	got := <-acc
	if got.err == nil || got.err.Error() != "unsupported version" {
		t.Fatalf("accept: %v", got.err)
	}
	if err := sess.Call(ctx, OpStat, StatRequest{}, nil); err == nil {
		t.Fatal("call after refused hello")
	}
}

func TestFingerprint(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrong := strings.Repeat("ab", 32)
	if _, err := Dial(ctx, srv.Addr(), wrong); err == nil {
		t.Fatal("wrong fingerprint dialed")
	}
	// Upper case is the same pin.
	acc := acceptAsync(srv)
	sess, err := Dial(ctx, srv.Addr(), strings.ToUpper(fp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	helloOK(t, sess, testID())
	got := <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() { _ = got.c.Close() })
}

func TestReconnect(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	helloOK(t, sess, testID())
	got := <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := got.c.Close(); err != nil {
		t.Fatal(err)
	}

	acc = acceptAsync(srv)
	sess = dial(t, srv.Addr(), fp)
	helloOK(t, sess, testID())
	got = <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() { _ = got.c.Close() })
}

func TestConcurrentCalls(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	helloOK(t, sess, testID())
	got := <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	conn := got.c
	t.Cleanup(func() { _ = conn.Close() })

	conn.Handle(func(op string, body json.RawMessage) (any, error) {
		if op != OpPortalCheck {
			return nil, errors.New("unexpected " + op)
		}
		var req PortalCheckRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		if req.Label != "web" {
			return nil, errors.New("label")
		}
		return PortalCheckResponse{Free: false, Holder: "other"}, nil
	})
	sess.Handle(func(op string, body json.RawMessage) (any, error) {
		if op != OpStat {
			return nil, errors.New("unexpected " + op)
		}
		return StatResponse{CPU: 1.5, Memory: 10, Disk: 20, Uptime: 30}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var errPortal, errStat error
	var portal PortalCheckResponse
	var stat StatResponse
	wg.Add(2)
	go func() {
		defer wg.Done()
		errPortal = sess.Call(ctx, OpPortalCheck, PortalCheckRequest{Label: "web"}, &portal)
	}()
	go func() {
		defer wg.Done()
		errStat = conn.Call(ctx, OpStat, StatRequest{}, &stat)
	}()
	wg.Wait()
	if errPortal != nil {
		t.Fatal(errPortal)
	}
	if errStat != nil {
		t.Fatal(errStat)
	}
	if portal.Free || portal.Holder != "other" {
		t.Fatalf("portal %+v", portal)
	}
	if stat.CPU != 1.5 || stat.Memory != 10 || stat.Disk != 20 || stat.Uptime != 30 {
		t.Fatalf("stat %+v", stat)
	}
}

func TestBadHeader(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	helloOK(t, sess, testID())
	got := <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	conn := got.c
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := conn.ctrl.qconn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("nope\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sess.Accept(ctx); !errors.Is(err, errBadHeader) {
		t.Fatalf("bad header: %v", err)
	}

	ssh, err := conn.OpenSSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kind, _, peer, err := sess.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindSSH {
		t.Fatalf("kind %s", kind)
	}
	exchange(t, ssh, peer)
}

func TestExtraClientStream(t *testing.T) {
	srv, fp := newTestServer(t, "secret")
	acc := acceptAsync(srv)
	sess := dial(t, srv.Addr(), fp)
	helloOK(t, sess, testID())
	got := <-acc
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() { _ = got.c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := sess.ctrl.qconn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := st.Read(buf); err == nil {
		t.Fatal("extra client stream stayed open")
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(0, 0) != time.Second {
		t.Fatalf("attempt 0: %s", Backoff(0, 0))
	}
	if Backoff(1, 0) != 2*time.Second || Backoff(4, 0) != 16*time.Second {
		t.Fatalf("growth %s %s", Backoff(1, 0), Backoff(4, 0))
	}
	if Backoff(5, 0) != 30*time.Second || Backoff(20, 0) != 30*time.Second {
		t.Fatalf("cap %s %s", Backoff(5, 0), Backoff(20, 0))
	}
	j := Backoff(0, 0.5)
	if j < time.Second || j > 30*time.Second {
		t.Fatalf("jitter attempt 0: %s", j)
	}
	if j == Backoff(0, 0) {
		t.Fatal("jitter did not change the delay")
	}
	if Backoff(3, 0.25) != Backoff(3, 0.25) {
		t.Fatal("jitter is not deterministic")
	}
	if Backoff(20, 0.9) > 30*time.Second || Backoff(4, 0.99) > 30*time.Second {
		t.Fatal("jitter exceeded 30s")
	}
}

func TestBodyShape(t *testing.T) {
	raw, err := json.Marshal(HelloBody{
		Version: 1, Name: "home", Token: "t", User: "alice",
		HostKey: "ssh-ed25519 AAAA", AgentVersion: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":1,"name":"home","token":"t","user":"alice","host_key":"ssh-ed25519 AAAA","agent_version":"dev"}`
	if string(raw) != want {
		t.Fatalf("hello body %s", raw)
	}
	raw, err = json.Marshal(HelloResult{Domain: "box.example.com", HTTPPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"domain":"box.example.com","http_port":80}` {
		t.Fatalf("hello result %s", raw)
	}
	raw, err = json.Marshal(StatResponse{CPU: 1, Memory: 2, Disk: 3, Uptime: 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"cpu":1,"memory":2,"disk":3,"uptime":4}` {
		t.Fatalf("stat %s", raw)
	}
}
