package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/server"
)

// The localhost CLI against a real server: publish over the socket, read
// back, follow until an event lands.
func TestLocalEventRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "box.sock")
	srv, err := server.Start(context.Background(), server.Config{
		Domain: "box.example.com", DataDir: dir, SocketPath: sock,
		SSHAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
		Stdout: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	run := func(args ...string) (string, string, error) {
		t.Helper()
		var out, errb bytes.Buffer
		err := Run(Options{
			Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
			DataDir: dir, ServerSocket: sock, GuestSocket: filepath.Join(dir, "absent.sock"),
			Context: context.Background(),
		})
		return out.String(), errb.String(), err
	}

	out, errb, err := run("event", "pub", "door", "open", "slowly")
	if err != nil || out != "1\n" {
		t.Fatalf("pub out %q err %v %q", out, err, errb)
	}
	// No text args: stdin becomes the body. An empty stdin is an empty body,
	// which is a valid event.
	out, _, err = run("event", "pub", "window")
	if err != nil || out != "2\n" {
		t.Fatalf("stdin body %q %v", out, err)
	}
	out, _, err = run("event", "get", "--since", "0", "--topic", "window")
	if err != nil || !strings.HasSuffix(strings.TrimRight(out, " \n"), "window") {
		t.Fatalf("empty body line %q", out)
	}
	out, _, err = run("event", "get", "--since", "0", "--topic", "door")
	if err != nil || !strings.Contains(out, "door") || !strings.Contains(out, "open slowly") {
		t.Fatalf("get %q %v", out, err)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "server") {
		t.Fatalf("header or from missing: %q", out)
	}
	out, _, err = run("event", "get", "--json", "--limit", "1")
	if err != nil || !strings.Contains(out, `"latest":2`) || !strings.Contains(out, `"from":"server"`) {
		t.Fatalf("json %q %v", out, err)
	}

	// follow prints new events as they arrive, from latest
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		err := Run(Options{
			Args: []string{"event", "get", "--follow"}, Stdin: strings.NewReader(""),
			Stdout: &out, Stderr: io.Discard,
			DataDir: dir, ServerSocket: sock, GuestSocket: filepath.Join(dir, "absent.sock"),
			Context: ctx,
		})
		if err != nil && ctx.Err() == nil {
			t.Error(err)
		}
		done <- out.String()
	}()
	// The follow starts at latest, so only a publish that lands after its
	// first poll counts. Keep publishing until the interrupt.
	go func() {
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, _, _ = run("event", "pub", "door", fmt.Sprintf("shut-%d", i))
			time.Sleep(150 * time.Millisecond)
		}
	}()
	time.Sleep(1500 * time.Millisecond)
	cancel()
	select {
	case out := <-done:
		if !strings.Contains(out, "shut") || strings.Contains(out, "open slowly") {
			t.Fatalf("follow output %q", out)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("follow did not end on cancel")
	}
}
