// Package tui is the bubbletea control UI shared by the SSH REPL and the
// localhost dashboard. Both talk to one control service; this package does
// not keep a second copy of that state.
package tui

import (
	"context"
	"io"
	"time"
)

// Computer is one row of the computer table.
type Computer struct {
	Name         string   `json:"name"`
	Online       bool     `json:"online"`
	User         string   `json:"user"`
	Address      string   `json:"address"`
	AgentVersion string   `json:"agent_version"`
	Portals      []string `json:"portals"`
}

// Pending is a computer waiting for approval. The code is not here.
type Pending struct {
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	User      string    `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Portal is one claim.
type Portal struct {
	Label    string `json:"label"`
	Host     string `json:"host"`
	Computer string `json:"computer"`
	Port     int    `json:"port"`
}

// Key is a bound client key. The public key material is not included.
type Key struct {
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment"`
	BoundAt     time.Time `json:"bound_at"`
}

// Snapshot is everything the screens draw. Env holds names, never values.
type Snapshot struct {
	Domain    string     `json:"domain"`
	Computers []Computer `json:"computers"`
	Pending   []Pending  `json:"pending"`
	Portals   []Portal   `json:"portals"`
	Keys      []Key      `json:"keys"`
	Env       []string   `json:"env"`
}

// Pairing is a one-time client password. Callers show it once and drop it.
type Pairing struct {
	Secret  string
	Expires time.Time
}

// Backend is how the screens read and change the server.
// The SSH REPL adapts control.Service. The dashboard adapts the localhost socket.
type Backend interface {
	Snapshot(context.Context) (Snapshot, error)
	Approve(ctx context.Context, code string) (string, error)
	Remove(ctx context.Context, name string) error
	Rename(ctx context.Context, oldName, newName string) error
	RemoveKey(ctx context.Context, fingerprint string) error
	Pair(context.Context) (Pairing, error)
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
