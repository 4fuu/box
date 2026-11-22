package server_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/tunnel"
)

// waitOffline polls the portal until its computer is gone: a request the
// proxy could not forward ends without a reply, because box answers nothing
// on the computer's behalf.
func waitOffline(t *testing.T, addr, host string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := "no request sent"
	for {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(portalRequest(t, addr, host))
		if err == nil {
			last = resp.Status
			resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				return
			}
		}
		if err != nil {
			last = err.Error()
			if strings.Contains(last, "EOF") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("computer stayed online; last %s", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func portalRequest(t *testing.T, addr, host string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	return req
}

// TestPortalWaitsForReconnect kills a computer's tunnel and brings a new
// one up shortly after. A request that arrives in the gap is held until the
// tunnel is back, then answered by the computer.
func TestPortalWaitsForReconnect(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hostKey, hostLine := newKey(t)
	first := e2eAgentKey(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token, hostKey, hostLine)
	var add tunnel.PortalAddResponse
	if err := first.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 3000}, &add); err != nil {
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
	if err := second.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 3000}, &add); err != nil {
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
// the token header before the request crosses the tunnel, and that the
// computer's own status and headers come back as it sent them.
func TestPortalGateReachesComputer(t *testing.T) {
	srv, _, signer := e2eBind(t)
	token := e2eJoin(t, srv, signer, "home")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := e2eAgent(t, ctx, srv.QUICAddr(), srv.QUICFingerprint(), token)
	var add tunnel.PortalAddResponse
	if err := sess.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 3000}, &add); err != nil {
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
	if got := resp.Header.Get("X-Saw-Gate"); got != "no" {
		t.Fatalf("the token header reached the computer: X-Saw-Gate %q", got)
	}
}
