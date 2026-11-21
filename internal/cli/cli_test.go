package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNoArgsWithoutServerPrintsHelp(t *testing.T) {
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
	if stderr.String() != hostHelp {
		t.Fatalf("stderr:\n%s\nwant the host help", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestHelpPathsPrintHostHelpToStdout(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		err := Run(Options{
			Args:         args,
			Stdout:       &stdout,
			Stderr:       &stderr,
			DataDir:      dir,
			GuestSocket:  filepath.Join(dir, "no-guest.sock"),
			ServerSocket: filepath.Join(dir, "no-server.sock"),
		})
		if ExitCode(err) != 0 {
			t.Fatalf("%v: exit %d, err %v", args, ExitCode(err), err)
		}
		if stdout.String() != hostHelp {
			t.Fatalf("%v: stdout:\n%s\nwant the host help", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v: stderr %q", args, stderr.String())
		}
	}
	for _, want := range []string{
		"serve", "--domain", "--ssh-addr", "--http-addr", "--quic-addr",
		"--blob-store", "--blob-dir", "--blob-s3-endpoint", "--blob-s3-bucket",
		"--blob-s3-region", "--blob-s3-pathstyle",
		"join", "--name", "--user", "--prepare-sshd", "agent", "pair",
		"key ls", "key rm", "env ls", "env set", "env rm",
		"token add", "--for", "token ls", "token rm",
		"event pub", "event get", "--since", "--topic", "--from", "--limit",
		"--wait", "--json", "--follow", "domain",
		"portal check", "portal add", "portal ls", "portal rm", "public", "private",
		"sshd enable",
	} {
		if !strings.Contains(hostHelp, want) {
			t.Errorf("host help does not mention %q", want)
		}
	}
}

func TestHelpOnComputerPrintsGuestHelp(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		err := Run(Options{
			Args:         args,
			Stdout:       &stdout,
			Stderr:       &stderr,
			DataDir:      dir,
			GuestSocket:  sock,
			ServerSocket: filepath.Join(dir, "no-server.sock"),
		})
		if ExitCode(err) != 0 {
			t.Fatalf("%v: exit %d, err %v", args, ExitCode(err), err)
		}
		got := stdout.String()
		for _, want := range []string{"domain", "portal add", "event get", "--follow", "sshd enable", "help"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%v: guest help missing %q:\n%s", args, want, got)
			}
		}
		if strings.Contains(got, "box serve") {
			t.Fatalf("%v: guest help lists server commands:\n%s", args, got)
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v: stderr %q", args, stderr.String())
		}
	}
}

func TestUnknownCommandPointsAtHelp(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(Options{
		Args:         []string{"bogus"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		DataDir:      dir,
		GuestSocket:  filepath.Join(dir, "no-guest.sock"),
		ServerSocket: filepath.Join(dir, "no-server.sock"),
	})
	if ExitCode(err) != 2 {
		t.Fatalf("exit %d, err %v", ExitCode(err), err)
	}
	if !strings.Contains(stderr.String(), `unknown command "bogus"`) || !strings.Contains(stderr.String(), "box help") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("unknown command dumped the help: %q", stderr.String())
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
	if !strings.Contains(stderr.String(), `unknown command "node"`) || strings.Contains(stderr.String(), "box node") {
		t.Fatalf("stderr %q", stderr.String())
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

func TestServeDataDirFlagMovesStateAndSocket(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var stderr bytes.Buffer
	go func() {
		done <- Run(Options{
			Context: ctx,
			Args: []string{"serve", "--domain", "box.example.com", "--data-dir", data,
				"--ssh-addr", "127.0.0.1:0", "--http-addr", "127.0.0.1:0", "--quic-addr", "127.0.0.1:0"},
			Stdout: &bytes.Buffer{},
			Stderr: &stderr,
		})
	}()
	sock := filepath.Join(data, "box.sock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no socket at %s; stderr %q", sock, stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(data, "box.db")); err != nil {
		t.Fatalf("database not in the flag's directory: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGuestCommandsNeedAgentSocket(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"domain"}, {"portal", "ls"}, {"sshd", "enable"}} {
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
	// A single-account Windows machine has no second account to reject.
	t.Skip("no other local account to reject")
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
