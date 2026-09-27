// Package cli is the box command line: serve, join, the agent, and the localhost client.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/4fuu/box/internal/agent"
	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/guest"
	"github.com/4fuu/box/internal/ident"
	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/server"
)

// Options configures one invocation.
type Options struct {
	Args         []string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	DataDir      string
	GuestSocket  string
	ServerSocket string
	Context      context.Context
}

// Run dispatches one invocation.
func Run(o Options) error {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.DataDir == "" {
		o.DataDir = paths.DataDir
	}
	if o.GuestSocket == "" {
		if dir, err := stateDir(); err == nil {
			o.GuestSocket = filepath.Join(dir, "agent.sock")
		}
	}
	if o.ServerSocket == "" {
		o.ServerSocket = filepath.Join(o.DataDir, "box.sock")
	}
	if o.Context == nil {
		o.Context = context.Background()
	}
	if len(o.Args) == 0 {
		if !socketUp(o.ServerSocket) {
			fmt.Fprint(o.Stderr, hostUsage)
			return errUsage
		}
		return dashboard(o)
	}
	switch o.Args[0] {
	case "serve":
		return serve(o)
	case "join":
		return join(o)
	case "agent":
		return runAgent(o)
	case "domain", "portal":
		return runGuest(o)
	case "pair":
		return localPair(o, "pair", "client")
	case "key":
		return localKey(o)
	case "env":
		return localEnv(o)
	default:
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
}

func runGuest(o Options) error {
	if !socketPresent(o.GuestSocket) {
		fmt.Fprintln(o.Stderr, "these commands run on a computer")
		return errFail
	}
	if err := guest.Run(o.GuestSocket, o.Args, o.Stdout, o.Stderr); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return nil
}

func socketPresent(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

const hostUsage = `usage:
  box
  box serve --domain <domain> [--ssh-addr addr] [--http-addr addr] [--quic-addr addr]
  box join <host> [--name name] [--user name]
  box agent
  box pair
  box key ls|rm
  box env ls|set|rm
  box domain
  box portal check|add|ls|rm
`

func serve(o Options) error {
	domain, sshAddr, httpAddr, quicAddr := "", "", "", ""
	args := o.Args[1:]
	for i := 0; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--domain", "--ssh-addr", "--http-addr", "--quic-addr":
			if i+1 >= len(args) {
				fmt.Fprint(o.Stderr, hostUsage)
				return errUsage
			}
			i++
			switch flag {
			case "--domain":
				domain = args[i]
			case "--ssh-addr":
				sshAddr = args[i]
			case "--http-addr":
				httpAddr = args[i]
			case "--quic-addr":
				quicAddr = args[i]
			}
		default:
			fmt.Fprint(o.Stderr, hostUsage)
			return errUsage
		}
	}
	srv, err := server.Start(o.Context, server.Config{
		Domain: domain, DataDir: o.DataDir, Stdout: o.Stdout,
		SSHAddr: sshAddr, HTTPAddr: httpAddr, QUICAddr: quicAddr,
		SocketPath: o.ServerSocket,
	})
	if err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	<-o.Context.Done()
	return srv.Close()
}

func join(o Options) error {
	host, name, userName, ok := parseJoinArgs(o.Args[1:])
	if !ok {
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	if name == "" {
		hostName, err := os.Hostname()
		if err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		name, err = computerNameFromHost(hostName)
		if err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
	} else if err := ident.Computer(name); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	dir, err := stateDir()
	if err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	if err := agent.Join(o.Context, host, name, userName, dir, o.Stdout); err != nil {
		if errors.Is(err, agent.ErrRejected) {
			fmt.Fprintln(o.Stderr, "rejected")
			return errFail
		}
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return nil
}

func parseJoinArgs(args []string) (host, name, userName string, ok bool) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name", "--user":
			if i+1 >= len(args) {
				return "", "", "", false
			}
			if args[i] == "--name" {
				name = args[i+1]
			} else {
				userName = args[i+1]
			}
			i++
		default:
			if strings.HasPrefix(args[i], "-") || host != "" {
				return "", "", "", false
			}
			host = args[i]
		}
	}
	return host, name, userName, host != ""
}

func computerNameFromHost(host string) (string, error) {
	name := sanitizeComputerName(host)
	if err := ident.Computer(name); err != nil {
		return "", fmt.Errorf("hostname %q is not a legal computer name; pass --name", host)
	}
	return name, nil
}

// sanitizeComputerName makes a hostname usable as a computer name.
// '+' and '.' are not legal, and neither are the reserved names.
func sanitizeComputerName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	var b strings.Builder
	hyphen := true
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func runAgent(o Options) error {
	if len(o.Args) != 1 {
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	dir, err := stateDir()
	if err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	err = agent.Run(o.Context, dir)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if errors.Is(err, agent.ErrRejected) {
		fmt.Fprintln(o.Stderr, "rejected")
		return errFail
	}
	if err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return nil
}

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("home directory is not set")
	}
	return filepath.Join(home, ".box"), nil
}

func localPair(o Options, op, kind string) error {
	var resp struct {
		Secret  string    `json:"secret"`
		Expires time.Time `json:"expires"`
		Kind    string    `json:"kind"`
	}
	if err := localCall(o, op, nil, &resp); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	text := control.FormatPairing(control.Pairing{Secret: resp.Secret, Expires: resp.Expires}, kind)
	_, err := io.WriteString(o.Stdout, text)
	return err
}

func localKey(o Options) error {
	if len(o.Args) < 2 {
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	switch o.Args[1] {
	case "ls":
		var keys []control.KeyView
		if err := localCall(o, "key_ls", nil, &keys); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		text, err := control.FormatKeys(keys, false)
		if err != nil {
			return err
		}
		_, err = io.WriteString(o.Stdout, text)
		return err
	case "rm":
		if len(o.Args) != 3 {
			fmt.Fprintln(o.Stderr, "usage: box key rm <fingerprint>")
			return errUsage
		}
		if err := localCall(o, "key_rm", map[string]string{"match": o.Args[2]}, nil); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		return nil
	case "copy":
		fmt.Fprintln(o.Stderr, "key copy is gone")
		return errFail
	default:
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
}

func localEnv(o Options) error {
	if len(o.Args) < 2 {
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	switch o.Args[1] {
	case "ls":
		var resp struct {
			Names []string `json:"names"`
		}
		if err := localCall(o, "env_ls", nil, &resp); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		text, err := control.FormatEnv(resp.Names, false)
		if err != nil {
			return err
		}
		_, err = io.WriteString(o.Stdout, text)
		return err
	case "set":
		if len(o.Args) < 4 {
			fmt.Fprintln(o.Stderr, "usage: box env set <name> <value>")
			return errUsage
		}
		var resp struct {
			Message string `json:"message"`
		}
		req := map[string]string{"name": o.Args[2], "value": strings.Join(o.Args[3:], " ")}
		if err := localCall(o, "env_set", req, &resp); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		fmt.Fprintln(o.Stdout, resp.Message)
		return nil
	case "rm":
		if len(o.Args) != 3 {
			fmt.Fprintln(o.Stderr, "usage: box env rm <name>")
			return errUsage
		}
		if err := localCall(o, "env_rm", map[string]string{"name": o.Args[2]}, nil); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		return nil
	default:
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
}

func localCall(o Options, op string, req, resp any) error {
	conn, err := net.DialTimeout("unix", o.ServerSocket, 3*time.Second)
	if err != nil {
		return errors.New("server is not running")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return rpc.Call(conn, op, req, resp)
}

type exitKind int

const (
	errUsage exitKind = 2
	errFail  exitKind = 1
)

func (e exitKind) Error() string { return "" }

// ExitCode reports 2 for usage and 1 for a failed command.
func ExitCode(err error) int {
	var k exitKind
	if errors.As(err, &k) {
		return int(k)
	}
	if err == nil {
		return 0
	}
	return 1
}
