package guest_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/guest"
	"github.com/4fuu/box/internal/rpc"
)

// fakeAgent answers event ops on a unix socket the way the real agent does.
// It records the last request it saw.
type fakeAgent struct {
	path    string
	mu      sync.Mutex
	lastPub rpc.EventPublish
	lastGet rpc.EventQuery
	// publishErr makes the next publish fail once.
	publishErr error
}

func startAgent(t *testing.T) *fakeAgent {
	t.Helper()
	dir, err := os.MkdirTemp("", "guest-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fa := &fakeAgent{path: path}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = rpc.Serve(conn, fa.handle)
			}()
		}
	}()
	return fa
}

func (fa *fakeAgent) handle(op string, body json.RawMessage) (any, error) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	switch op {
	case rpc.OpEventPub:
		var req rpc.EventPublish
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		fa.lastPub = req
		if fa.publishErr != nil {
			err := fa.publishErr
			fa.publishErr = nil
			return nil, err
		}
		return rpc.EventResult{ID: 42}, nil
	case rpc.OpEventGet:
		var req rpc.EventQuery
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		fa.lastGet = req
		return rpc.EventList{
			Events: []rpc.EventItem{
				{ID: 1, Topic: "door", Body: []byte("line1\nline2\ttab"), From: "home", Time: time.Unix(0, 0).UTC()},
				{ID: 2, Topic: "bin", Body: []byte{0xff, 0x01}, From: "sensor", Key: "k1", Time: time.Unix(0, 0).UTC()},
			},
			Oldest: 1, Latest: 2,
		}, nil
	default:
		return nil, errors.New("unsupported")
	}
}

func (fa *fakeAgent) pub() rpc.EventPublish {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return fa.lastPub
}

func (fa *fakeAgent) get() rpc.EventQuery {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return fa.lastGet
}

func run(t *testing.T, fa *fakeAgent, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errb strings.Builder
	var in *strings.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	err := guest.Run(context.Background(), fa.path, args, in, &out, &errb)
	return out.String(), errb.String(), err
}

func TestGuestEventPubBodyAndKey(t *testing.T) {
	fa := startAgent(t)
	out, _, err := run(t, fa, "", "event", "pub", "door", "open", "slowly")
	if err != nil || out != "42\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	if got := fa.pub(); got.Topic != "door" || string(got.Body) != "open slowly" || got.Key != "" {
		t.Fatalf("request %+v", got)
	}
	if _, _, err := run(t, fa, "", "event", "pub", "door", "open", "--key", "k-7"); err != nil {
		t.Fatal(err)
	}
	if got := fa.pub(); got.Key != "k-7" {
		t.Fatalf("key %+v", got)
	}
	// No text args: stdin becomes the body, verbatim.
	if _, _, err := run(t, fa, "raw\nbody", "event", "pub", "door"); err != nil {
		t.Fatal(err)
	}
	if got := fa.pub(); string(got.Body) != "raw\nbody" {
		t.Fatalf("stdin body %q", got.Body)
	}
}

func TestGuestEventGetEscaping(t *testing.T) {
	fa := startAgent(t)
	out, errb, err := run(t, fa, "", "event", "get")
	if err != nil || errb != "" {
		t.Fatalf("err %v %q", err, errb)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %d: %q", len(lines), out)
	}
	// tabwriter pads the columns; the fields are what matter.
	if got := strings.Join(strings.Fields(lines[0]), " "); got != "ID TIME FROM TOPIC BODY" {
		t.Fatalf("header %q", lines[0])
	}
	if !strings.Contains(lines[1], "door") || !strings.HasSuffix(lines[1], `line1\nline2\ttab`) {
		t.Fatalf("escaped line %q", lines[1])
	}
	// A binary body is one base64: line, never raw bytes.
	if !strings.Contains(lines[2], "base64:") || strings.Contains(lines[2], "\xff") {
		t.Fatalf("binary line %q", lines[2])
	}
	// --json carries the window and the body rule.
	out, _, err = run(t, fa, "", "event", "get", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Events []struct {
			Body    string `json:"body"`
			BodyB64 string `json:"body_b64"`
			Key     string `json:"key"`
		} `json:"events"`
		Oldest int64 `json:"oldest"`
		Latest int64 `json:"latest"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Oldest != 1 || res.Latest != 2 || len(res.Events) != 2 {
		t.Fatalf("json window %+v", res)
	}
	if res.Events[0].Body != "line1\nline2\ttab" || res.Events[1].BodyB64 == "" || res.Events[1].Key != "k1" {
		t.Fatalf("json bodies %+v", res.Events)
	}
}

func TestGuestEventGetFlags(t *testing.T) {
	fa := startAgent(t)
	if _, _, err := run(t, fa, "", "event", "get", "--since", "5", "--topic", "a", "--topic", "b",
		"--from", "home", "--limit", "7", "--wait", "3"); err != nil {
		t.Fatal(err)
	}
	got := fa.get()
	if got.Since != 5 || len(got.Topics) != 2 || got.Topics[1] != "b" || got.Froms[0] != "home" ||
		got.Limit != 7 || got.Wait != 3 {
		t.Fatalf("query %+v", got)
	}
	for _, bad := range [][]string{
		{"event", "get", "--wait", "26"},
		{"event", "get", "--limit", "0"},
		{"event", "get", "--since", "-1"},
		{"event", "get", "--nope"},
		{"event"},
		{"event", "pub"},
	} {
		if _, _, err := run(t, fa, "", bad...); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
