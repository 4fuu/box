// Package repl is the non-interactive control REPL served over SSH.
// A person with no command gets the TUI. A command, including --json, stays
// plain text and calls control.Service.
package repl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/4fuu/box/internal/control"
	"strconv"

	"golang.org/x/crypto/ssh"
)

// ErrExit is returned by Exec for session-ending commands (exit, quit,
// logout). The SSH one-shot path treats it as success without printing an error.
var ErrExit = errors.New("exit")

// REPL runs one command. Output for a person goes to Out.
// --json selects the script form for lists.
type REPL struct {
	Out    io.Writer
	Err    io.Writer
	Svc    *control.Service
	Bridge func(name string) error
	Pub    ssh.PublicKey
	From   string // remote address, for approve rate limits
}

func New(_ io.Reader, out, errw io.Writer, svc *control.Service) *REPL {
	if errw == nil {
		errw = out
	}
	return &REPL{Out: out, Err: errw, Svc: svc}
}

// crlfWriter turns lone \n into \r\n. A PTY peer runs its terminal in raw
// mode with output processing off, so the server must emit both bytes or
// every line staircases into the previous one.
type crlfWriter struct{ w io.Writer }

// CRLF wraps w so newline-terminated output renders correctly on a raw PTY.
// Sequences that already carry \r\n pass through unchanged.
func CRLF(w io.Writer) io.Writer { return crlfWriter{w} }

func (c crlfWriter) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte("\n")) {
		return c.w.Write(p)
	}
	out := make([]byte, 0, len(p)+16)
	for i, b := range p {
		if b == '\n' && (i == 0 || p[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, b)
	}
	if _, err := c.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Exec runs one already-split command. The error is not written; the caller prints it.
func (r *REPL) Exec(argv []string) error {
	pos, flags, asJSON, err := parseArgv(argv)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New("unknown command")
	}
	name, args := pos[0], pos[1:]
	if name == "key" || name == "env" || name == "token" || name == "event" {
		if len(args) == 0 {
			return fmt.Errorf("%q needs a subcommand — run help %s", name, name)
		}
		name = name + " " + args[0]
		args = args[1:]
	}
	switch name {
	case "ls":
		return r.write(func() (string, error) {
			list, err := r.Svc.ListComputers()
			if err != nil {
				return "", err
			}
			return control.FormatComputers(list, asJSON)
		})
	case "ssh":
		if len(args) != 1 {
			return errors.New("usage: ssh <name>")
		}
		if r.Bridge == nil {
			return errors.New("ssh is available from a bound client")
		}
		return r.Bridge(args[0])
	case "rm":
		return r.cmdRm(args)
	case "rename":
		if len(args) != 2 {
			return errors.New("usage: rename <name> <new>")
		}
		return r.Svc.Rename(args[0], args[1])
	case "stat":
		if len(args) != 1 {
			return errors.New("usage: stat <name>")
		}
		view, err := r.Svc.Stat(context.Background(), args[0])
		if err != nil {
			return err
		}
		return r.print(control.FormatStat(view, asJSON))
	case "pending":
		return r.write(func() (string, error) {
			return control.FormatPending(r.Svc.Pending(), asJSON)
		})
	case "approve":
		if len(args) != 1 {
			return errors.New("usage: approve <code>")
		}
		msg, err := r.Svc.Approve(args[0], r.From)
		if err != nil {
			return err
		}
		fmt.Fprintln(r.Out, msg)
		return nil
	case "pair":
		p, err := r.Svc.PairClient()
		if err != nil {
			return err
		}
		_, err = io.WriteString(r.Out, control.FormatPairing(p, "client"))
		return err
	case "key ls":
		return r.write(func() (string, error) {
			list, err := r.Svc.Keys()
			if err != nil {
				return "", err
			}
			return control.FormatKeys(list, asJSON)
		})
	case "key rm":
		if len(args) != 1 {
			return errors.New("usage: key rm <fingerprint>")
		}
		return r.Svc.RemoveKey(args[0])
	case "env set":
		if len(args) < 2 {
			return errors.New("usage: env set <name> <value>")
		}
		msg, err := r.Svc.SetEnv(args[0], strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		fmt.Fprintln(r.Out, msg)
		return nil
	case "env rm":
		if len(args) != 1 {
			return errors.New("usage: env rm <name>")
		}
		return r.Svc.DeleteEnv(args[0])
	case "env ls":
		names, err := r.Svc.EnvNames()
		if err != nil {
			return err
		}
		return r.print(control.FormatEnv(names, asJSON))
	case "token add":
		ttl, err := control.ParseTTL(flags["for"])
		if err != nil {
			return err
		}
		view, err := r.Svc.AddToken(strings.Join(args, " "), ttl)
		if err != nil {
			return err
		}
		_, err = io.WriteString(r.Out, control.FormatToken(view))
		return err
	case "token ls":
		return r.write(func() (string, error) {
			list, err := r.Svc.Tokens()
			if err != nil {
				return "", err
			}
			return control.FormatTokens(list, asJSON)
		})
	case "token rm":
		if len(args) != 1 {
			return errors.New("usage: token rm <id>")
		}
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || id < 1 {
			return errors.New("usage: token rm <id>")
		}
		return r.Svc.RemoveToken(id)
	case "event pub":
		if len(args) < 2 {
			return errors.New("usage: event pub <topic> <text>")
		}
		item, err := r.Svc.PublishEvent("ssh", args[0], strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		fmt.Fprintln(r.Out, item.ID)
		return nil
	case "event get":
		if len(args) != 0 {
			return errors.New("usage: event get [--since n] [--topic name]")
		}
		since, err := parseSince(flags["since"])
		if err != nil {
			return err
		}
		list, err := r.Svc.ReadEvents(since, flags["topic"])
		if err != nil {
			return err
		}
		return r.print(control.FormatEvents(list, asJSON))
	case "whoami":
		line, err := r.Svc.WhoAmI(r.Pub)
		if err != nil {
			return err
		}
		fmt.Fprintln(r.Out, line)
		return nil
	case "help":
		return r.cmdHelp(pos)
	case "exit", "quit", "logout":
		return ErrExit
	case "clear":
		return nil
	default:
		return fmt.Errorf("unknown command %q — run help", name)
	}
}

func (r *REPL) cmdRm(args []string) error {
	if len(args) != 1 && len(args) != 2 {
		return errors.New("usage: rm <name>")
	}
	if len(args) != 2 {
		return errors.New("rm asks for the name again")
	}
	if args[1] != args[0] {
		return errors.New("name did not match")
	}
	return r.Svc.Remove(args[0])
}

func (r *REPL) write(fn func() (string, error)) error {
	text, err := fn()
	if err != nil {
		return err
	}
	_, err = io.WriteString(r.Out, text)
	return err
}

func (r *REPL) print(text string, err error) error {
	if err != nil {
		return err
	}
	_, err = io.WriteString(r.Out, text)
	return err
}

func parseArgv(argv []string) (pos []string, flags map[string]string, asJSON bool, err error) {
	flags = map[string]string{}
	known := map[string]bool{"since": true, "topic": true, "for": true}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			pos = append(pos, argv[i+1:]...)
			break
		}
		if a == "--json" {
			asJSON = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			if !known[name] {
				return nil, nil, false, fmt.Errorf("unknown flag --%s", name)
			}
			if i+1 >= len(argv) {
				return nil, nil, false, fmt.Errorf("missing value for --%s", name)
			}
			i++
			flags[name] = argv[i]
			continue
		}
		pos = append(pos, a)
	}
	return pos, flags, asJSON, nil
}

// Split splits a REPL line with single and double quotes.
func Split(line string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	flush := func() {
		args = append(args, b.String())
		b.Reset()
	}
	in := false
	for i := 0; i < len(line); i++ {
		c := rune(line[i])
		if quote != 0 {
			if c == quote {
				quote = 0
				continue
			}
			if c == '\\' && quote == '"' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
				continue
			}
			b.WriteByte(line[i])
			in = true
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			in = true
			continue
		}
		if unicode.IsSpace(c) {
			if in {
				flush()
				in = false
			}
			continue
		}
		b.WriteByte(line[i])
		in = true
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if in {
		flush()
	}
	return args, nil
}

type helpEntry struct {
	name  string
	usage string
	short string
	subs  []helpEntry
}

var helpHelp = []helpEntry{
	{
		name: "Computers",
		subs: []helpEntry{
			{name: "ls", usage: "ls", short: "List computers"},
			{name: "ssh", usage: "ssh <name>", short: "Open a shell on a computer"},
			{name: "rm", usage: "rm <name>", short: "Delete a computer; asks for the name again"},
			{name: "rename", usage: "rename <name> <new>", short: "Rename a computer; portals stay claimed"},
			{name: "stat", usage: "stat <name>", short: "Live load from the agent"},
			{name: "pending", usage: "pending", short: "Computers waiting for approval"},
			{name: "approve", usage: "approve <code>", short: "Approve a pending join"},
		},
	},
	{
		name: "Keys & secrets",
		subs: []helpEntry{
			{name: "pair", usage: "pair", short: "Print a one-time password that adds your ssh key"},
			{name: "key ls", usage: "key ls", short: "List paired ssh keys"},
			{name: "key rm", usage: "key rm <fingerprint>", short: "Revoke an ssh key"},
			{name: "env set", usage: "env set <name> <value>", short: "Store a variable; new sessions on every computer receive it"},
			{name: "env rm", usage: "env rm <name>", short: "Remove a variable; existing sessions keep the old value"},
			{name: "env ls", usage: "env ls", short: "List variable names; values are never printed"},
			{name: "whoami", usage: "whoami", short: "Show the key this session authenticated with"},
			{name: "token add", usage: "token add [--for 12h] [comment]", short: "Create an access token. It does not expire unless --for is set"},
			{name: "token ls", usage: "token ls", short: "List access tokens, including the secret"},
			{name: "token rm", usage: "token rm <id>", short: "Revoke an access token"},
		},
	},
	{
		name: "Events",
		subs: []helpEntry{
			{name: "event pub", usage: "event pub <topic> <text>", short: "Publish one event"},
			{name: "event get", usage: "event get [--since n] [--topic name]", short: "Print events newer than an id"},
		},
	},
	{
		name: "Session",
		subs: []helpEntry{
			{name: "clear", usage: "clear", short: "Clear the screen"},
			{name: "exit", usage: "exit", short: "End this session"},
		},
	},
	{
		name: "Help",
		subs: []helpEntry{
			{name: "help", usage: "help [all | <command>]", short: "This help; help key covers every key subcommand"},
		},
	},
}

func (r *REPL) cmdHelp(pos []string) error {
	switch {
	case len(pos) == 1:
		r.printHelpOverview()
		return nil
	case len(pos) == 2 && pos[1] == "all":
		r.printHelpAll()
		return nil
	case len(pos) == 2:
		return r.printHelpFor(pos[1])
	default:
		return errors.New("usage: help [all | <command>]")
	}
}

func (r *REPL) printHelpOverview() {
	fmt.Fprintf(r.Out, "Common commands:\n\n")
	rows := []struct{ group, cmds string }{
		{"Computers", "ls  ssh  rm  rename  stat  pending  approve"},
		{"Keys & secrets", "pair  key†  env†  token†  whoami"},
		{"Events", "event†"},
		{"Session", "clear  exit"},
		{"Help", "help"},
	}
	for _, row := range rows {
		fmt.Fprintf(r.Out, "%-16s%s\n", row.group, row.cmds)
	}
	fmt.Fprintf(r.Out, "\n† marks a command with subcommands.\n")
	fmt.Fprintf(r.Out, "Run help all for a list of all commands, help <command> for more detail.\n")
}

func parseSince(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid since")
	}
	return n, nil
}

func helpLine(w io.Writer, usage, short string) {
	if len(usage) > 28 {
		fmt.Fprintf(w, "  %s\n      %s\n", usage, short)
		return
	}
	fmt.Fprintf(w, "  %-28s%s\n", usage, short)
}

func (r *REPL) printHelpAll() {
	for _, group := range helpHelp {
		fmt.Fprintf(r.Out, "%s:\n", group.name)
		for _, e := range group.subs {
			helpLine(r.Out, e.usage, e.short)
		}
		fmt.Fprintln(r.Out)
	}
}

func (r *REPL) printHelpFor(name string) error {
	name = strings.ToLower(name)
	for _, group := range helpHelp {
		for _, e := range group.subs {
			if e.name == name {
				helpLine(r.Out, e.usage, e.short)
				return nil
			}
			if fields := strings.Fields(e.name); len(fields) > 1 && fields[0] == name {
				fmt.Fprintf(r.Out, "%s:\n", group.name)
				for _, sub := range group.subs {
					if f := strings.Fields(sub.name); len(f) > 1 && f[0] == name {
						helpLine(r.Out, sub.usage, sub.short)
					}
				}
				fmt.Fprintln(r.Out)
				return nil
			}
		}
	}
	return fmt.Errorf("no help for %q", name)
}
