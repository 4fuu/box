// Package tui is the bubbletea control UI shared by the SSH REPL and the
// localhost dashboard. Both talk to one control service; this package does
// not keep a second copy of that state.
package tui

import (
	"context"
	"io"

	"github.com/4fuu/box/internal/control"
)

// Backend is how the screens read and change the server.
// The SSH REPL adapts control.Service. The dashboard adapts the localhost socket.
type Backend interface {
	Snapshot(context.Context) (control.Snapshot, error)
	Approve(ctx context.Context, code string) (string, error)
	Remove(ctx context.Context, name string) error
	Rename(ctx context.Context, oldName, newName string) error
	RemoveKey(ctx context.Context, fingerprint string) error
	Pair(context.Context) (control.Pairing, error)
	SetEnv(ctx context.Context, name, value string) (string, error)
	DeleteEnv(ctx context.Context, name string) error
}

// Size is a terminal window.
type Size struct {
	Width  int
	Height int
}

// Config is one run of the TUI.
// Leave In nil to use the process stdin. Set In when the input is an SSH
// session: bubbletea would otherwise open /dev/tty on the server.
// Width and Height are sent as the first window size, before the first paint.
type Config struct {
	Context    context.Context
	In         io.Reader
	Out        io.Writer
	Backend    Backend
	AllowShell bool
	Identity   string
	Notice     string
	Term       string
	Environ    []string
	Width      int
	Height     int
	Resize     <-chan Size
}
