package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/tui"
)

// socketUp reports whether a server is accepting connections on the localhost
// socket. Usage is printed without a terminal when it is not.
func socketUp(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func dashboard(o Options) error {
	cfg := tui.Config{
		Context:  o.Context,
		Backend:  dashBackend{o: o},
		Identity: "localhost",
	}
	// A substituted reader (tests) must not make bubbletea open /dev/tty.
	// The real process stdin is left unset so a pipe can still attach to the console.
	if o.Stdin != nil && o.Stdin != os.Stdin {
		cfg.In = o.Stdin
	}
	if o.Stdout != nil && o.Stdout != os.Stdout {
		cfg.Out = o.Stdout
	}
	if _, err := tui.Run(cfg); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return nil
}

// dashBackend is the localhost socket in front of the same control service
// the SSH REPL calls. It cannot bind a client key; there is no such op.
type dashBackend struct{ o Options }

func (d dashBackend) Snapshot(context.Context) (control.Snapshot, error) {
	var snap control.Snapshot
	err := localCall(d.o, "snapshot", nil, &snap)
	return snap, err
}

func (d dashBackend) Summary(context.Context) (control.Summary, error) {
	var sum control.Summary
	err := localCall(d.o, "summary", nil, &sum)
	return sum, err
}

func (d dashBackend) Approve(_ context.Context, code string) (string, error) {
	var resp struct {
		Message string `json:"message"`
	}
	err := localCall(d.o, "approve", map[string]string{"code": code}, &resp)
	return resp.Message, err
}

func (d dashBackend) Remove(_ context.Context, name string) error {
	return localCall(d.o, "rm", map[string]string{"name": name}, nil)
}

func (d dashBackend) Rename(_ context.Context, oldName, newName string) error {
	return localCall(d.o, "rename", map[string]string{"old": oldName, "new": newName}, nil)
}

func (d dashBackend) RemoveKey(_ context.Context, fingerprint string) error {
	return localCall(d.o, "key_rm", map[string]string{"match": fingerprint}, nil)
}

func (d dashBackend) Pair(context.Context) (control.Pairing, error) {
	var resp control.Pairing
	if err := localCall(d.o, "pair", nil, &resp); err != nil {
		return control.Pairing{}, err
	}
	return resp, nil
}

func (d dashBackend) SetEnv(_ context.Context, name, value string) (string, error) {
	var resp struct {
		Message string `json:"message"`
	}
	err := localCall(d.o, "env_set", map[string]string{"name": name, "value": value}, &resp)
	return resp.Message, err
}

func (d dashBackend) DeleteEnv(_ context.Context, name string) error {
	return localCall(d.o, "env_rm", map[string]string{"name": name}, nil)
}

func (d dashBackend) AddToken(_ context.Context, comment string) (control.TokenView, error) {
	var view control.TokenView
	err := localCall(d.o, "token_add", map[string]string{"comment": comment}, &view)
	return view, err
}

func (d dashBackend) RemoveToken(_ context.Context, id int64) error {
	return localCall(d.o, "token_rm", map[string]int64{"id": id}, nil)
}
