// Package cli is the box command line: serve, node, the guest, and the localhost client.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/guest"
	"github.com/4fuu/box/internal/node"
	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/runtime"
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
		o.GuestSocket = paths.GuestSocket
	}
	if o.ServerSocket == "" {
		o.ServerSocket = filepath.Join(o.DataDir, "box.sock")
	}
	if o.Context == nil {
		o.Context = context.Background()
	}
	if inGuest(o.GuestSocket) {
		return runGuest(o)
	}
	return runHost(o)
}

func inGuest(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

func runGuest(o Options) error {
	if len(o.Args) == 0 {
		fmt.Fprintln(o.Stderr, "usage: box domain | box portal check|add|ls|rm")
		return errUsage
	}
	if o.Args[0] == "serve" || o.Args[0] == "node" {
		fmt.Fprintf(o.Stderr, "box %s is not available in a computer\n", o.Args[0])
		return errFail
	}
	if err := guest.Run(o.GuestSocket, o.Args, o.Stdout, o.Stderr); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return nil
}

func runHost(o Options) error {
	if len(o.Args) == 0 {
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	switch o.Args[0] {
	case "serve":
		return serve(o)
	case "node":
		return runNode(o)
	case "domain", "portal":
		fmt.Fprintln(o.Stderr, "domain is read inside a computer")
		return errFail
	case "pair":
		return localPair(o, "pair", "client")
	case "key":
		return localKey(o)
	case "env":
		return localEnv(o)
	case "status":
		return localStatus(o)
	default:
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
}

const hostUsage = `usage:
  box serve --domain <domain>
  box pair
  box node pair
  box node
  box node join --server <host:port> --code <code> --name <name>
  box key ls|rm|copy
  box env ls|set|rm
  box status
`

func serve(o Options) error {
	domain := ""
	args := o.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--domain" && i+1 < len(args) {
			i++
			domain = args[i]
			continue
		}
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	srv, err := server.Start(o.Context, server.Config{
		Domain: domain, DataDir: o.DataDir, Stdout: o.Stdout,
		SocketPath: o.ServerSocket,
	})
	if err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	<-o.Context.Done()
	return srv.Close()
}

func runNode(o Options) error {
	args := o.Args[1:]
	if len(args) == 0 {
		return runNodeServe(o)
	}
	switch args[0] {
	case "pair":
		return localPair(o, "node_pair", "node")
	case "join":
		return nodeJoin(o, args[1:])
	default:
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
}

func nodeJoin(o Options, args []string) error {
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") || i+1 >= len(args) {
			fmt.Fprintln(o.Stderr, "usage: box node join --server <host:port> --code <code> --name <name>")
			return errUsage
		}
		flags[strings.TrimPrefix(args[i], "--")] = args[i+1]
		i++
	}
	if flags["server"] == "" || flags["code"] == "" || flags["name"] == "" {
		fmt.Fprintln(o.Stderr, "usage: box node join --server <host:port> --code <code> --name <name>")
		return errUsage
	}
	host, _, err := net.SplitHostPort(flags["server"])
	if err != nil {
		host = flags["server"]
	}
	apiBase := "http://" + host + "/box/node/v1"
	if err := node.Join(o.Context, apiBase, flags["server"], flags["code"], flags["name"], o.DataDir); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return runNodeServe(o)
}

func runNodeServe(o Options) error {
	host := nodeAPIHost(o.DataDir)
	ctrl, err := node.Open(o.DataDir, host)
	if err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	ctrl.Runtime = &runtime.Podman{}
	if err := ctrl.Serve(o.Context); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	return nil
}

func nodeAPIHost(dataDir string) string {
	raw, err := os.ReadFile(filepath.Join(dataDir, "node.json"))
	if err != nil {
		return ""
	}
	var f struct {
		Server string `json:"server"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return ""
	}
	host, _, err := net.SplitHostPort(f.Server)
	if err != nil {
		host = f.Server
	}
	return "http://" + host + "/box/node/v1"
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
		var resp struct {
			Public string `json:"public"`
		}
		if err := localCall(o, "key_copy", nil, &resp); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		_, err := io.WriteString(o.Stdout, resp.Public)
		return err
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

func localStatus(o Options) error {
	var st control.Status
	if err := localCall(o, "status", nil, &st); err != nil {
		fmt.Fprintln(o.Stderr, err.Error())
		return errFail
	}
	text, err := control.FormatStatus(st, false)
	if err != nil {
		return err
	}
	_, err = io.WriteString(o.Stdout, text)
	return err
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
