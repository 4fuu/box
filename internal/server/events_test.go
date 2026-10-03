package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/event"
)

// eventServer is one running server plus its first access token.
func eventServer(t *testing.T) (addr, token, sock string) {
	t.Helper()
	dir := t.TempDir()
	srv, _ := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	t.Cleanup(func() { srv.Close() })
	sock = filepath.Join(dir, "box.sock")
	var created control.TokenView
	if err := localCall(t, sock, "token_add",
		map[string]string{"comment": "door-sensor"}, &created); err != nil {
		t.Fatal(err)
	}
	return srv.HTTPAddr(), created.Token, sock
}

func reqCtx(t *testing.T, method, addr, path, token string, body []byte) (*http.Request, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Host = "event.box.example.com"
	if token != "" {
		req.Header.Set("X-Box-Token", token)
	}
	return req, cancel
}

func doReq(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEventRawPostAndDedup(t *testing.T) {
	addr, token, _ := eventServer(t)
	req, _ := reqCtx(t, http.MethodPost, addr, "/api/events/kitchen/door", token, []byte("open"))
	req.Header.Set("Idempotency-Key", "door-1")
	status, raw := doReq(t, req)
	if status != 201 || !strings.Contains(raw, `"id":1`) {
		t.Fatalf("raw publish %d %s", status, raw)
	}
	// The same key from the same token returns the original, 200, flagged.
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events/kitchen/door", token, []byte("open again"))
	req.Header.Set("Idempotency-Key", "door-1")
	status, raw = doReq(t, req)
	if status != 200 || !strings.Contains(raw, `"id":1`) || !strings.Contains(raw, `"duplicate":true`) {
		t.Fatalf("dedup %d %s", status, raw)
	}
	// The key can ride the query instead.
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events/kitchen/door?key=door-2", token, []byte("shut"))
	status, _ = doReq(t, req)
	if status != 201 {
		t.Fatalf("key query %d", status)
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?since=0&topic=kitchen/door", token, nil)
	status, raw = doReq(t, req)
	if status != 200 || !strings.Contains(raw, `"oldest":1`) || !strings.Contains(raw, `"latest":2`) {
		t.Fatalf("read %d %s", status, raw)
	}
	var res event.Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 || res.More {
		t.Fatalf("events %+v more=%v", res.Events, res.More)
	}
	// from is the token comment; token_id is not exposed.
	if res.Events[0].From != "door-sensor" || strings.Contains(raw, "token_id") {
		t.Fatalf("from wrong: %s", raw)
	}
}

func TestEventFromFallsBackToTokenID(t *testing.T) {
	addr, token, sock := eventServer(t)
	// A comment that is not a label falls back to token-<id>.
	var second control.TokenView
	if err := localCall(t, sock, "token_add", map[string]string{"comment": "my sensor"}, &second); err != nil {
		t.Fatal(err)
	}
	req, _ := reqCtx(t, http.MethodPost, addr, "/api/events/door", second.Token, []byte("y"))
	if status, raw := doReq(t, req); status != 201 {
		t.Fatalf("publish with fallback token %d %s", status, raw)
	}
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events/door", token, []byte("x"))
	if status, _ := doReq(t, req); status != 201 {
		t.Fatal("publish failed")
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?since=0", token, nil)
	_, raw := doReq(t, req)
	if !strings.Contains(raw, `"from":"token-2"`) || !strings.Contains(raw, `"from":"door-sensor"`) {
		t.Fatalf("from labels wrong: %s", raw)
	}
}

func TestEventBodyRoundTrip(t *testing.T) {
	addr, token, _ := eventServer(t)
	// Invalid UTF-8 goes in raw and comes back base64.
	raw := []byte{0xff, 0x00, 'a'}
	req, _ := reqCtx(t, http.MethodPost, addr, "/api/events/binary", token, raw)
	if status, _ := doReq(t, req); status != 201 {
		t.Fatal("binary publish failed")
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?since=0", token, nil)
	_, body := doReq(t, req)
	var res struct {
		Events []struct {
			BodyB64 string `json:"body_b64"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 1 || res.Events[0].BodyB64 == "" {
		t.Fatalf("binary body not flagged base64: %s", body)
	}
	got, err := base64.StdEncoding.DecodeString(res.Events[0].BodyB64)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("binary body round trip %x: %v", got, err)
	}
	// JSON in, base64 field out.
	b64 := base64.StdEncoding.EncodeToString(raw)
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events", token,
		[]byte(fmt.Sprintf(`{"topic":"binary","body_b64":%q}`, b64)))
	if status, _ := doReq(t, req); status != 201 {
		t.Fatal("body_b64 publish failed")
	}
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events", token,
		[]byte(`{"topic":"binary","body":"a","body_b64":"YQ=="}`))
	if status, body := doReq(t, req); status != 400 || !strings.Contains(body, "one body") {
		t.Fatalf("two bodies %d %s", status, body)
	}
}

func TestEventRejects(t *testing.T) {
	addr, token, _ := eventServer(t)
	req, _ := reqCtx(t, http.MethodPost, addr, "/api/events", token, []byte(`{"topic":"has space","body":"x"}`))
	if status, body := doReq(t, req); status != 400 || !strings.Contains(body, "invalid topic") {
		t.Fatalf("bad topic %d %s", status, body)
	}
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events", token, []byte(`{"topic":"t","body":"x","key":"has space"}`))
	if status, body := doReq(t, req); status != 400 || !strings.Contains(body, "invalid key") {
		t.Fatalf("bad key %d %s", status, body)
	}
	req, _ = reqCtx(t, http.MethodPost, addr, "/api/events/ok", token, []byte(strings.Repeat("x", event.MaxBody+1)))
	if status, body := doReq(t, req); status != 413 {
		t.Fatalf("oversize body %d %s", status, body)
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?limit=0", token, nil)
	if status, _ := doReq(t, req); status != 400 {
		t.Fatalf("limit 0 %d", status)
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?wait=26", token, nil)
	if status, _ := doReq(t, req); status != 400 {
		t.Fatalf("wait 26 %d", status)
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?since=-1", token, nil)
	if status, _ := doReq(t, req); status != 400 {
		t.Fatalf("since -1 %d", status)
	}
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?topic=x/+y", token, nil)
	if status, _ := doReq(t, req); status != 400 {
		t.Fatalf("bad filter %d", status)
	}
}

func TestEventLongPoll(t *testing.T) {
	addr, token, _ := eventServer(t)
	// A publish wakes the waiting read.
	done := make(chan string, 1)
	req, cancel := reqCtx(t, http.MethodGet, addr, "/api/events?wait=5&topic=door", token, nil)
	go func() {
		status, raw := doReq(t, req)
		done <- fmt.Sprintf("%d %s", status, raw)
	}()
	time.Sleep(300 * time.Millisecond)
	preq, _ := reqCtx(t, http.MethodPost, addr, "/api/events/door", token, []byte("open"))
	if status, _ := doReq(t, preq); status != 201 {
		t.Fatal("publish failed")
	}
	select {
	case got := <-done:
		if !strings.Contains(got, "open") {
			t.Fatalf("wake result %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publish did not wake the long poll")
	}
	cancel()

	// No publish: the wait runs out and the answer is empty but framed.
	req, _ = reqCtx(t, http.MethodGet, addr, "/api/events?wait=1&topic=none", token, nil)
	start := time.Now()
	status, raw := doReq(t, req)
	if status != 200 || !strings.Contains(raw, `"events":[]`) || !strings.Contains(raw, `"oldest":`) {
		t.Fatalf("timeout answer %d %s", status, raw)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("returned before the wait")
	}

	// A client that leaves ends the wait server-side.
	ctx, cancel2 := context.WithCancel(context.Background())
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/events?wait=25", nil)
	req.Host = "event.box.example.com"
	req.Header.Set("X-Box-Token", token)
	go func() {
		cl := &http.Client{}
		resp, err := cl.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	cancel2()
}

func TestEventShutdownEndsWaits(t *testing.T) {
	dir := t.TempDir()
	srv, _ := start(t, dir, "box.example.com", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	var created control.TokenView
	if err := localCall(t, filepath.Join(dir, "box.sock"), "token_add",
		map[string]string{"comment": "t"}, &created); err != nil {
		t.Fatal(err)
	}
	addr := srv.HTTPAddr()
	done := make(chan struct{})
	req, _ := reqCtx(t, http.MethodGet, addr, "/api/events?wait=25", created.Token, nil)
	go func() {
		defer close(done)
		cl := &http.Client{}
		if resp, err := cl.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	srv.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown left the long poll hanging")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("shutdown took %v", time.Since(start))
	}
}

func TestEventLimitAndMoreHTTP(t *testing.T) {
	addr, token, _ := eventServer(t)
	for i := 0; i < 3; i++ {
		req, _ := reqCtx(t, http.MethodPost, addr, "/api/events/t", token, []byte("x"))
		doReq(t, req)
	}
	req, _ := reqCtx(t, http.MethodGet, addr, "/api/events?limit=2", token, nil)
	_, raw := doReq(t, req)
	if !strings.Contains(raw, `"more":true`) {
		t.Fatalf("more missing: %s", raw)
	}
	var res event.Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 || res.Events[1].ID != 2 {
		t.Fatalf("page one %+v", res.Events)
	}
	req, _ = reqCtx(t, http.MethodGet, addr, fmt.Sprintf("/api/events?limit=2&since=%d", res.Events[1].ID), token, nil)
	_, raw = doReq(t, req)
	if !strings.Contains(raw, `"more":false`) {
		t.Fatalf("page two %s", raw)
	}
}
