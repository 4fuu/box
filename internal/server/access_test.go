package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
)

func TestEventAPIAndPrivatePortal(t *testing.T) {
	dir := t.TempDir()
	srv, _ := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	defer srv.Close()
	sock := filepath.Join(dir, "box.sock")

	var created control.TokenView
	if err := localCall(t, sock, "token_add", map[string]string{"comment": "door", "for": "1h"}, &created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.Comment != "door" {
		t.Fatalf("%+v", created)
	}
	var again []control.TokenView
	if err := localCall(t, sock, "token_ls", nil, &again); err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Token != created.Token {
		t.Fatalf("token not listed again: %+v", again)
	}

	eventHost := "event.box.example.com"
	if status, _ := do(t, srv.HTTPAddr(), http.MethodPost, eventHost, "/api/events", "", nil); status != 401 {
		t.Fatalf("event without token %d", status)
	}
	// A client-supplied from is ignored: the token's comment names the device.
	body := []byte(`{"topic":"door","body":"open","from":"sensor-1"}`)
	status, raw := do(t, srv.HTTPAddr(), http.MethodPost, eventHost, "/api/events", created.Token, body)
	if status != 201 || !strings.Contains(raw, `"id"`) {
		t.Fatalf("publish %d %s", status, raw)
	}
	status, raw = do(t, srv.HTTPAddr(), http.MethodGet, eventHost, "/api/events?since=0&topic=door", created.Token, nil)
	if status != 200 || !strings.Contains(raw, `"from":"door"`) || !strings.Contains(raw, "open") {
		t.Fatalf("poll %d %s", status, raw)
	}
	if strings.Contains(raw, "sensor-1") {
		t.Fatalf("client from was trusted: %s", raw)
	}
	if status, _ := do(t, srv.HTTPAddr(), http.MethodGet, eventHost, "/", created.Token, nil); status != 404 {
		t.Fatalf("event host extra path %d", status)
	}
	if status := getHost(t, srv.HTTPAddr(), "nope.box.example.com"); status != 421 {
		t.Fatalf("unknown %d", status)
	}

	st, err := store.Open(filepath.Join(dir, "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	compToken := "computer-token-for-access-test"
	if err := st.CreateComputer("home", secret.Hash(compToken), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent, err := tunnel.Dial(ctx, srv.QUICAddr(), srv.QUICFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err := agent.Hello(ctx, tunnel.Identity{Name: "home", Token: compToken, User: "alice", AgentVersion: "t"}); err != nil {
		t.Fatal(err)
	}
	agent.Handle(func(string, json.RawMessage) (any, error) { return nil, nil })
	go acceptAgent(ctx, agent, nil, nil, nil)

	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "event", Port: 1}, nil); err == nil {
		t.Fatal("event label was claimed")
	}
	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "auth", Port: 1}, nil); err == nil {
		t.Fatal("auth label was claimed")
	}
	var add tunnel.PortalAddResponse
	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "lock", Port: 9, Private: true}, &add); err != nil {
		t.Fatal(err)
	}
	var pub tunnel.PortalAddResponse
	if err := agent.Call(ctx, tunnel.OpPortalAdd, tunnel.PortalAddRequest{Label: "web", Port: 9}, &pub); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if status, _ := doAccept(t, srv.HTTPAddr(), "web.box.example.com", "", ""); status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public portal did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	status, loc := doAccept(t, srv.HTTPAddr(), "lock.box.example.com", "text/html", "")
	if status != 302 || !strings.Contains(loc, "auth.box.example.com") || !strings.Contains(loc, "next=") {
		t.Fatalf("gate redirect %d %s", status, loc)
	}
	status, _ = doAccept(t, srv.HTTPAddr(), "lock.box.example.com", "", "")
	if status != 401 {
		t.Fatalf("api without token %d", status)
	}
	status, raw = doAccept(t, srv.HTTPAddr(), "lock.box.example.com", "", created.Token)
	if status != 200 || raw != "pong" {
		t.Fatalf("private with header %d %q", status, raw)
	}
	hreq, err := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	hreq.Host = "lock.box.example.com"
	hreq.Header.Set("X-Box-Token", created.Token)
	hresp, err := (&http.Client{Timeout: 5 * time.Second}).Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	hresp.Body.Close()
	if hresp.Header.Get("X-Saw-Gate") != "no" {
		t.Fatalf("portal saw the token header: %s", hresp.Header.Get("X-Saw-Gate"))
	}

	form := "token=" + created.Token + "&next=http%3A%2F%2Flock.box.example.com%2F"
	req, err := http.NewRequest(http.MethodPost, "http://"+srv.HTTPAddr()+"/", strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "auth.box.example.com"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{CheckRedirect: stopRedirect, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("login %d", resp.StatusCode)
	}
	var gate *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "box_token" {
			gate = c
		}
	}
	if gate == nil || gate.Value != created.Token || !gate.HttpOnly || gate.Domain != "box.example.com" {
		t.Fatalf("cookie %+v", gate)
	}

	creq, err := http.NewRequest(http.MethodGet, "http://"+srv.HTTPAddr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	creq.Host = "lock.box.example.com"
	creq.Header.Set("Accept", "text/html")
	creq.AddCookie(gate)
	cresp, err := (&http.Client{Timeout: 5 * time.Second}).Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	defer cresp.Body.Close()
	cb, _ := io.ReadAll(cresp.Body)
	if cresp.StatusCode != 200 || string(cb) != "pong" || cresp.Header.Get("X-Saw-Gate") != "no" {
		t.Fatalf("cookie proxy %d %q saw %s", cresp.StatusCode, cb, cresp.Header.Get("X-Saw-Gate"))
	}

	var published tunnel.EventResult
	if err := agent.Call(ctx, tunnel.OpEventPub, tunnel.EventPublish{Topic: "door", Body: []byte("from-agent")}, &published); err != nil {
		t.Fatal(err)
	}
	if published.ID == 0 {
		t.Fatalf("publish id %d", published.ID)
	}
	var events tunnel.EventList
	if err := agent.Call(ctx, tunnel.OpEventGet, tunnel.EventQuery{Since: 0}, &events); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events.Events {
		if e.ID == published.ID && e.From == "home" && string(e.Body) == "from-agent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent event missing or wrong: %+v", events.Events)
	}
}

func do(t *testing.T, addr, method, host, path, token string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if token != "" {
		req.Header.Set("X-Box-Token", token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func doAccept(t *testing.T, addr, host, accept, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("X-Box-Token", token)
	}
	resp, err := (&http.Client{CheckRedirect: stopRedirect, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 302 {
		return resp.StatusCode, resp.Header.Get("Location")
	}
	return resp.StatusCode, string(b)
}

func stopRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func TestAuthPageRejectsOpenRedirect(t *testing.T) {
	dir := t.TempDir()
	srv, _ := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	defer srv.Close()
	var created control.TokenView
	if err := localCall(t, filepath.Join(dir, "box.sock"), "token_add", map[string]string{"for": "1h"}, &created); err != nil {
		t.Fatal(err)
	}
	form := "token=" + created.Token + "&next=https://evil.example/phish"
	req, err := http.NewRequest(http.MethodPost, "http://"+srv.HTTPAddr()+"/", strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "auth.box.example.com"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{CheckRedirect: stopRedirect, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "signed in\n" {
		t.Fatalf("open redirect %d %q loc %s", resp.StatusCode, b, resp.Header.Get("Location"))
	}
}
