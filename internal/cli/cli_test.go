package cli

import (
	"bytes"
	"net"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoArgsWithoutServerPrintsUsage(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(Options{
		Stdin:        strings.NewReader(""),
		Stdout:       &stdout,
		Stderr:       &stderr,
		DataDir:      dir,
		GuestSocket:  dir + "/no-guest.sock",
		ServerSocket: dir + "/no-server.sock",
	})
	if ExitCode(err) != 2 {
		t.Fatalf("exit %d, err %v", ExitCode(err), err)
	}
	if !strings.Contains(stderr.String(), "box serve") || !strings.Contains(stderr.String(), "box join") || !strings.Contains(stderr.String(), "box agent") {
		t.Fatalf("usage missing: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "box node") {
		t.Fatalf("node still in usage: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestNodeIsGone(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	err := Run(Options{
		Args:         []string{"node"},
		Stdout:       &bytes.Buffer{},
		Stderr:       &stderr,
		DataDir:      dir,
		GuestSocket:  filepath.Join(dir, "no-guest.sock"),
		ServerSocket: filepath.Join(dir, "no-server.sock"),
	})
	if ExitCode(err) != 2 {
		t.Fatalf("exit %d", ExitCode(err))
	}
	if strings.Contains(stderr.String(), "node join") {
		t.Fatalf("usage %q", stderr.String())
	}
}

func TestServeIgnoresAgentSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var stderr bytes.Buffer
	err = Run(Options{
		Args:         []string{"serve"},
		Stdout:       &bytes.Buffer{},
		Stderr:       &stderr,
		DataDir:      filepath.Join(dir, "data"),
		ServerSocket: filepath.Join(dir, "data", "box.sock"),
		GuestSocket:  sock,
	})
	if ExitCode(err) == 0 {
		t.Fatal("expected failure")
	}
	if strings.Contains(stderr.String(), "not available") {
		t.Fatalf("refused serve: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "domain") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestGuestCommandsNeedAgentSocket(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"domain"}, {"portal", "ls"}} {
		var stderr bytes.Buffer
		err := Run(Options{
			Args:         args,
			Stdout:       &bytes.Buffer{},
			Stderr:       &stderr,
			DataDir:      dir,
			GuestSocket:  filepath.Join(dir, "missing.sock"),
			ServerSocket: filepath.Join(dir, "no-server.sock"),
		})
		if ExitCode(err) == 0 {
			t.Fatal(args)
		}
		if !strings.Contains(stderr.String(), "these commands run on a computer") {
			t.Fatalf("%v: %q", args, stderr.String())
		}
	}
}

func TestJoinRejectsReservedName(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	err := Run(Options{
		Args:         []string{"join", "127.0.0.1:9", "--name", "box"},
		Stdout:       &bytes.Buffer{},
		Stderr:       &stderr,
		DataDir:      dir,
		GuestSocket:  filepath.Join(dir, "no-guest.sock"),
		ServerSocket: filepath.Join(dir, "no-server.sock"),
	})
	if ExitCode(err) != 1 {
		t.Fatalf("exit %d %q", ExitCode(err), stderr.String())
	}
	if !strings.Contains(stderr.String(), "reserved") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestCheckLoginUser(t *testing.T) {
	if err := checkLoginUser(""); err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkLoginUser(current.Username); err != nil {
		t.Fatal(err)
	}
	err = checkLoginUser("no-such-box-user-zz")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing user: %v", err)
	}
	for _, name := range []string{"root", "nobody", "daemon"} {
		u, lookupErr := user.Lookup(name)
		if lookupErr != nil || u.Uid == current.Uid {
			continue
		}
		err = checkLoginUser(name)
		if err == nil || !strings.Contains(err.Error(), "not the current account") {
			t.Fatalf("%s: %v", name, err)
		}
		return
	}
	t.Fatal("no other local account to reject")
}

func TestSanitizeComputerName(t *testing.T) {
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{host: "My.Host+1", want: "my-host-1", ok: true},
		{host: "web_box", want: "web-box", ok: true},
		{host: "ok", want: "ok", ok: true},
		{host: "box", ok: false},
		{host: "BOX", ok: false},
		{host: "pair", ok: false},
		{host: "join", ok: false},
		{host: "...", ok: false},
		{host: "", ok: false},
	}
	for _, tc := range cases {
		got, err := computerNameFromHost(tc.host)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Fatalf("%q -> %q %v, want %q", tc.host, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%q should fail, got %q", tc.host, got)
		}
	}
	long := strings.Repeat("a", 80)
	got, err := computerNameFromHost(long)
	if err != nil || got != strings.Repeat("a", 63) {
		t.Fatalf("long -> %q %v", got, err)
	}
}
