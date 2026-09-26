package cli

import (
	"bytes"
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
	if !strings.Contains(stderr.String(), "box serve") {
		t.Fatalf("usage missing: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
}
