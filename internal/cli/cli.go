// Package cli is the box command line: serve, join, the agent, and the localhost client.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/box/internal/agent"
	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/event"
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
	case "token":
		return localToken(o)
	case "event":
		return runEvent(o)
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
	if err := guest.Run(o.Context, o.GuestSocket, o.Args, o.Stdin, o.Stdout, o.Stderr); err != nil {
		if o.Context.Err() != nil {
			return nil
		}
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
  box token add|ls|rm
  box event pub|get
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
	if len(o.Args) > 1 && o.Args[1] == "--prepare-sshd" {
		agent.ConfigureSSHD(o.Stdout)
		return nil
	}
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
	if err := checkLoginUser(userName); err != nil {
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

// checkLoginUser rejects a --user that is not this process's account.
// Keys are written under the process home, not another account's.
func checkLoginUser(name string) error {
	if name == "" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return fmt.Errorf("user %s was not found", name)
	}
	current, err := user.Current()
	if err != nil {
		return err
	}
	if u.Uid != current.Uid {
		return fmt.Errorf("user %s is not the current account", name)
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

func runEvent(o Options) error {
	if socketPresent(o.GuestSocket) {
		return runGuest(o)
	}
	return localEvent(o)
}

const localEventUsage = "usage: box event pub <topic> [text...] [--key k] | box event get [--since n] [--topic t]... [--from f]... [--limit n] [--wait s] [--json] [--follow]"

func localEvent(o Options) error {
	if len(o.Args) < 2 {
		fmt.Fprintln(o.Stderr, localEventUsage)
		return errUsage
	}
	switch o.Args[1] {
	case "pub":
		args := o.Args[2:]
		var key string
		var pos []string
		for i := 0; i < len(args); i++ {
			if args[i] == "--key" {
				if i+1 >= len(args) {
					fmt.Fprintln(o.Stderr, "missing value for --key")
					return errUsage
				}
				i++
				key = args[i]
				continue
			}
			pos = append(pos, args[i])
		}
		if len(pos) < 1 {
			fmt.Fprintln(o.Stderr, localEventUsage)
			return errUsage
		}
		body, err := event.ReadBody(pos[1:], o.Stdin, stdinIsTerminal(o.Stdin))
		if err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		var out struct {
			ID        int64 `json:"id"`
			Duplicate bool  `json:"duplicate,omitempty"`
		}
		req := map[string]any{"topic": pos[0], "body": body, "key": key}
		if err := localCall(o, "event_pub", req, &out); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		fmt.Fprintln(o.Stdout, out.ID)
		return nil
	case "get":
		f, err := parseEventFlags(o.Args[2:])
		if err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errUsage
		}
		fetch := func(ctx context.Context, q event.Query) (event.Result, error) {
			var res event.Result
			req := map[string]any{
				"since": q.Since, "topics": q.Topics, "froms": q.Froms,
				"limit": q.Limit, "wait": int(q.Wait.Seconds()),
			}
			if err := localCallCtx(ctx, o, "event_get", req, &res); err != nil {
				return event.Result{}, err
			}
			return res, nil
		}
		q := event.Query{Since: f.since, Topics: f.topics, Froms: f.froms, Limit: f.limit}
		if !f.follow {
			q.Wait = time.Duration(f.wait) * time.Second
			res, err := fetch(o.Context, q)
			if err != nil {
				fmt.Fprintln(o.Stderr, err.Error())
				return errFail
			}
			text, err := control.FormatEvents(res, f.asJSON)
			if err != nil {
				return err
			}
			_, err = io.WriteString(o.Stdout, text)
			return err
		}
		printHeader := true
		err = event.Follow(o.Context, q, f.since, !f.sinceSet, fetch, func(res event.Result) error {
			if f.asJSON {
				raw, err := json.Marshal(res)
				if err != nil {
					return err
				}
				raw = append(raw, '\n')
				_, err = o.Stdout.Write(raw)
				return err
			}
			if printHeader {
				fmt.Fprintln(o.Stdout, "ID\tTIME\tFROM\tTOPIC\tBODY")
				printHeader = false
			}
			for _, item := range res.Events {
				fmt.Fprintln(o.Stdout, control.FormatEventLine(item))
			}
			return nil
		}, func(since, oldest int64) {
			fmt.Fprintf(o.Stderr, "box: missed events: cursor %d, oldest retained %d\n", since, oldest)
		})
		if err != nil && o.Context.Err() != nil {
			return nil
		}
		return err
	default:
		fmt.Fprintln(o.Stderr, localEventUsage)
		return errUsage
	}
}

type eventFlags struct {
	since    int64
	sinceSet bool
	topics   []string
	froms    []string
	limit    int
	wait     int
	asJSON   bool
	follow   bool
}

func parseEventFlags(args []string) (*eventFlags, error) {
	f := &eventFlags{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 >= len(args) {
				return nil, errors.New("missing value for --since")
			}
			i++
			n, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || n < 0 {
				return nil, errors.New("invalid since")
			}
			f.since = n
			f.sinceSet = true
		case "--topic":
			if i+1 >= len(args) {
				return nil, errors.New("missing value for --topic")
			}
			i++
			f.topics = append(f.topics, args[i])
		case "--from":
			if i+1 >= len(args) {
				return nil, errors.New("missing value for --from")
			}
			i++
			f.froms = append(f.froms, args[i])
		case "--limit":
			if i+1 >= len(args) {
				return nil, errors.New("missing value for --limit")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > event.MaxLimit {
				return nil, errors.New("invalid limit")
			}
			f.limit = n
		case "--wait":
			if i+1 >= len(args) {
				return nil, errors.New("missing value for --wait")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 || n > event.MaxWaitSeconds {
				return nil, errors.New("invalid wait")
			}
			f.wait = n
		case "--json":
			f.asJSON = true
		case "--follow":
			f.follow = true
		default:
			return nil, errors.New(localEventUsage)
		}
	}
	return f, nil
}

func stdinIsTerminal(r io.Reader) bool {
	f, ok := r.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func localToken(o Options) error {
	if len(o.Args) < 2 {
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
	switch o.Args[1] {
	case "add":
		comment, dur, err := splitTokenAdd(o.Args[2:])
		if err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errUsage
		}
		var view control.TokenView
		req := map[string]string{"comment": comment, "for": dur}
		if err := localCall(o, "token_add", req, &view); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		_, err = io.WriteString(o.Stdout, control.FormatToken(view))
		return err
	case "ls":
		var list []control.TokenView
		if err := localCall(o, "token_ls", nil, &list); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		text, err := control.FormatTokens(list, false)
		if err != nil {
			return err
		}
		_, err = io.WriteString(o.Stdout, text)
		return err
	case "rm":
		if len(o.Args) != 3 {
			fmt.Fprintln(o.Stderr, "usage: box token rm <id>")
			return errUsage
		}
		id, err := strconv.ParseInt(o.Args[2], 10, 64)
		if err != nil || id < 1 {
			fmt.Fprintln(o.Stderr, "usage: box token rm <id>")
			return errUsage
		}
		if err := localCall(o, "token_rm", map[string]int64{"id": id}, nil); err != nil {
			fmt.Fprintln(o.Stderr, err.Error())
			return errFail
		}
		return nil
	default:
		fmt.Fprint(o.Stderr, hostUsage)
		return errUsage
	}
}

func splitTokenAdd(args []string) (comment, dur string, err error) {
	var words []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--for" {
			if i+1 >= len(args) {
				return "", "", errors.New("usage: box token add [--for 12h] [comment]")
			}
			i++
			dur = args[i]
			continue
		}
		words = append(words, args[i])
	}
	return strings.Join(words, " "), dur, nil
}

func localCall(o Options, op string, req, resp any) error {
	return localCallCtx(context.Background(), o, op, req, resp)
}

// localCallCtx ends one call when ctx does: a long poll must not sit out
// its 25 seconds after an interrupt.
func localCallCtx(ctx context.Context, o Options, op string, req, resp any) error {
	conn, err := net.DialTimeout("unix", o.ServerSocket, 3*time.Second)
	if err != nil {
		return errors.New("server is not running")
	}
	defer conn.Close()
	// 25s of event long poll plus slack.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
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
