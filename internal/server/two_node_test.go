package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/server"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
)

// seedCert writes the QUIC certificate a node would create at first start, so
// both nodes' fingerprints are known before either one listens.
func seedCert(t *testing.T, dir string) string {
	t.Helper()
	cert, _, err := tunnel.NewCertificate()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}); err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: key}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quic.pem"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return tunnel.Fingerprint(cert.Certificate[0])
}

type node struct {
	srv    *server.Server
	dir    string
	socket string
	ssh    string
	http   string
	quic   string
	peer   string
}

func newNode(t *testing.T, dir string) *node {
	t.Helper()
	return &node{
		dir: dir, socket: filepath.Join(dir, "box.sock"),
		ssh: freeTCP(t), http: freeTCP(t), quic: freeUDP(t), peer: freeTCP(t),
	}
}

func (n *node) start(t *testing.T, domain string, peer server.PeerConfig) {
	t.Helper()
	peer.Listen = n.peer
	srv, err := server.Start(t.Context(), server.Config{
		Domains: []string{domain}, DataDir: n.dir,
		SSHAddr: n.ssh, HTTPAddr: n.http, QUICAddr: n.quic,
		SocketPath: n.socket, Stdout: io.Discard, Peer: peer,
	})
	if err != nil {
		t.Fatal(err)
	}
	n.srv = srv
	t.Cleanup(func() { _ = srv.Close() })
}

func doHTTP(t *testing.T, addr, host, method, path string, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func waitHTTP(t *testing.T, addr, host string, wantStatus int, wantBody string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastStatus int
	var lastBody string
	for {
		lastStatus, lastBody = doHTTP(t, addr, host, http.MethodGet, "/", "", nil)
		if lastStatus == wantStatus && strings.Contains(lastBody, wantBody) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s via %s: status %d body %q, want %d containing %q", host, addr, lastStatus, lastBody, wantStatus, wantBody)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func dialComputer(t *testing.T, ctx context.Context, n *node, token string) *tunnel.Session {
	t.Helper()
	sess, err := tunnel.Dial(ctx, n.quic, n.srv.QUICFingerprint())
	if err != nil {
		t.Fatalf("dial %s: %v", n.quic, err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	sess.Handle(func(string, json.RawMessage) (any, error) { return nil, nil })
	if _, err := sess.Hello(ctx, tunnel.Identity{Name: "home", Token: token, User: "alice"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return sess
}

// servePortalStreams answers the portal streams a node opens for the test's
// computer by piping each one to the local application.
func servePortalStreams(ctx context.Context, sess *tunnel.Session) {
	go func() {
		for {
			kind, port, conn, err := sess.Accept(ctx)
			if err != nil {
				return
			}
			if kind != tunnel.KindPortal {
				_ = conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				app, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					return
				}
				defer app.Close()
				go func() {
					_, _ = io.Copy(app, conn)
					_ = app.(*net.TCPConn).CloseWrite()
				}()
				_, _ = io.Copy(conn, app)
			}()
		}
	}()
}

// TestTwoNodeConfigAndForwarding runs one computer tunnel against the writer,
// then against the replica, and reads the same portal through both entries.
func TestTwoNodeConfigAndForwarding(t *testing.T) {
	const domain = "127.0.0.1"
	const token = "two-node-computer-token"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "method=%s path=%s body=%s host=%s", r.Method, r.URL.Path, body, r.Host)
	}))
	t.Cleanup(app.Close)
	appPort := app.Listener.Addr().(*net.TCPAddr).Port

	dirA, dirB := t.TempDir(), t.TempDir()
	fpA, fpB := seedCert(t, dirA), seedCert(t, dirB)

	db, err := store.Open(filepath.Join(dirA, "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateComputer("home", secret.Hash(token), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	a, b := newNode(t, dirA), newNode(t, dirB)
	a.start(t, domain, server.PeerConfig{Writer: true, Fingerprint: fpB, Address: b.peer, QUIC: b.quic, Advertise: a.quic})
	b.start(t, domain, server.PeerConfig{Fingerprint: fpA, Address: a.peer, QUIC: a.quic, Advertise: b.quic})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sess := dialComputer(t, ctx, a, token)
	servePortalStreams(ctx, sess)

	var added tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{
		Label: "web", Domain: domain, Port: appPort,
	}, &added); err != nil {
		t.Fatalf("portal add on writer: %v", err)
	}
	// The replica learns the claim from the writer's snapshot, then forwards
	// the request back to the node that holds the tunnel.
	waitHTTP(t, b.http, "web."+domain, http.StatusOK, "method=GET path=/")

	status, body := doHTTP(t, b.http, "web."+domain, http.MethodPost, "/echo?x=1", "hello-peer", map[string]string{"X-Test": "yes"})
	if status != http.StatusOK || !strings.Contains(body, "method=POST") ||
		!strings.Contains(body, "path=/echo") || !strings.Contains(body, "body=hello-peer") ||
		!strings.Contains(body, "host=web."+domain) {
		t.Fatalf("forwarded POST: %d %q", status, body)
	}
	if status, body := doHTTP(t, a.http, "web."+domain, http.MethodGet, "/", "", nil); status != http.StatusOK || !strings.Contains(body, "method=GET") {
		t.Fatalf("local portal: %d %q", status, body)
	}

	// A computer attached to the replica asks for a claim. The replica
	// forwards the write to the writer and the new row comes back in a
	// snapshot, so both entries serve it.
	sessB := dialComputer(t, ctx, b, token)
	servePortalStreams(ctx, sessB)
	var viaReplica tunnel.PortalAddResponse
	if err := sessB.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{
		Label: "via-replica", Domain: domain, Port: appPort,
	}, &viaReplica); err != nil {
		t.Fatalf("portal add via replica: %v", err)
	}
	if viaReplica.Host != "via-replica."+domain {
		t.Fatalf("replica claim = %+v", viaReplica)
	}
	waitHTTP(t, a.http, "via-replica."+domain, http.StatusOK, "method=GET")
	waitHTTP(t, b.http, "via-replica."+domain, http.StatusOK, "method=GET")
	_ = sessB.Close()

	// A private portal uses an access token created on the writer and
	// replicated to the replica.
	var tok struct {
		Token string `json:"token"`
	}
	if err := localCall(t, a.socket, "token_add", map[string]string{"comment": "test"}, &tok); err != nil || tok.Token == "" {
		t.Fatalf("token add: %q %v", tok.Token, err)
	}
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{
		Label: "private", Domain: domain, Port: appPort, Private: true,
	}, &added); err != nil {
		t.Fatalf("private portal add: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, _ := doHTTP(t, b.http, "private."+domain, http.MethodGet, "/", "", nil)
		if status == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("private portal via replica: status %d, want 401", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status, body := doHTTP(t, b.http, "private."+domain, http.MethodGet, "/", "", map[string]string{"X-Box-Token": tok.Token}); status != http.StatusOK || !strings.Contains(body, "method=GET") {
		t.Fatalf("private portal with token: %d %q", status, body)
	}
	// sessB is closed above; wait for the replica to drop that route before
	// reading the private portal through it again.
	deadline = time.Now().Add(10 * time.Second)
	for {
		status, body := doHTTP(t, b.http, "private."+domain, http.MethodGet, "/", "", map[string]string{"X-Box-Token": tok.Token})
		if status == http.StatusOK && strings.Contains(body, "method=GET") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("private portal via replica after route drop: %d %q", status, body)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The writer leaves. The computer reconnects to the replica, which still
	// has the configuration, and configuration writes now fail.
	_ = sess.Close()
	if err := a.srv.Close(); err != nil {
		t.Fatal(err)
	}
	sessAfter := dialComputer(t, ctx, b, token)
	servePortalStreams(ctx, sessAfter)
	if status, body := doHTTP(t, b.http, "web."+domain, http.MethodGet, "/", "", nil); status != http.StatusOK || !strings.Contains(body, "method=GET") {
		t.Fatalf("portal on replica after writer exit: %d %q", status, body)
	}
	err = sessAfter.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{
		Label: "later", Domain: domain, Port: appPort,
	}, &added)
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Fatalf("portal add with writer down: %v", err)
	}
	if err := localCall(t, b.socket, "token_add", map[string]string{"comment": "replica"}, &tok); err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("token add on replica: %v", err)
	}
}

// TestTwoNodeJoinReplyListsBothNodes checks the list a computer is given at
// join and at every reconnect.
func TestTwoNodeJoinReplyListsBothNodes(t *testing.T) {
	const domain = "127.0.0.1"
	dirA, dirB := t.TempDir(), t.TempDir()
	fpA, fpB := seedCert(t, dirA), seedCert(t, dirB)

	db, err := store.Open(filepath.Join(dirA, "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateComputer("home", secret.Hash("token"), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	a, b := newNode(t, dirA), newNode(t, dirB)
	a.start(t, domain, server.PeerConfig{Writer: true, Fingerprint: fpB, Address: b.peer, QUIC: "b.example.com:7443", Advertise: "a.example.com:7443"})
	b.start(t, domain, server.PeerConfig{Fingerprint: fpA, Address: a.peer, QUIC: a.quic, Advertise: b.quic})

	out, _, err := sshCredentialRun(t, a.ssh, "boot+home", "token", "boot")
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Nodes []tunnel.NodeEndpoint `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatalf("reply %q: %v", out, err)
	}
	if len(reply.Nodes) != 2 || reply.Nodes[0].QUIC != "a.example.com:7443" ||
		reply.Nodes[0].Fingerprint != a.srv.QUICFingerprint() ||
		reply.Nodes[1].QUIC != "b.example.com:7443" || reply.Nodes[1].Fingerprint != fpB {
		t.Fatalf("nodes = %+v", reply.Nodes)
	}

	// A computer cannot join at the replica: the registration is a
	// configuration write.
	out, _, err = sshCredentialRun(t, b.ssh, "join+other", strings.Repeat("a", 64), "join")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "configuration writer") {
		t.Fatalf("replica join reply %q", out)
	}
}

// TestTwoNodeForwardUpgrade drives a protocol upgrade through the entry that
// does not hold the tunnel. The 101 and the bytes after it must come back
// unchanged.
func TestTwoNodeForwardUpgrade(t *testing.T) {
	const domain = "127.0.0.1"
	const token = "upgrade-computer-token"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "upgrade only", http.StatusBadRequest)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = brw.Flush()
		buf := make([]byte, 64)
		n, _ := brw.Read(buf)
		_, _ = conn.Write(append([]byte("echo:"), buf[:n]...))
	}))
	t.Cleanup(app.Close)
	appPort := app.Listener.Addr().(*net.TCPAddr).Port

	dirA, dirB := t.TempDir(), t.TempDir()
	fpA, fpB := seedCert(t, dirA), seedCert(t, dirB)
	db, err := store.Open(filepath.Join(dirA, "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateComputer("home", secret.Hash(token), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a, b := newNode(t, dirA), newNode(t, dirB)
	a.start(t, domain, server.PeerConfig{Writer: true, Fingerprint: fpB, Address: b.peer, QUIC: b.quic, Advertise: a.quic})
	b.start(t, domain, server.PeerConfig{Fingerprint: fpA, Address: a.peer, QUIC: a.quic, Advertise: b.quic})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sess := dialComputer(t, ctx, a, token)
	servePortalStreams(ctx, sess)
	var added tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{
		Label: "up", Domain: domain, Port: appPort,
	}, &added); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, _ := doHTTP(t, b.http, "up."+domain, http.MethodGet, "/", "", nil)
		if status == http.StatusBadRequest {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replica did not learn the upgrade portal: status %d", status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	conn, err := net.Dial("tcp", b.http)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET /upgrade HTTP/1.1\r\nHost: up.%s\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n", domain); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", res.StatusCode)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo:" {
		t.Fatalf("upgrade reply %q", got)
	}
}
