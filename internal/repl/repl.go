// Package repl is the control REPL served over SSH.
package repl

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/size"
	"golang.org/x/crypto/ssh"
)

// ErrExit is returned by Exec for session-ending commands (exit, quit,
// logout). Loop returns nil on it and the SSH one-shot path treats it as
// success without printing an error.
var ErrExit = errors.New("exit")

// errInterrupt reports that ^C was pressed while editing a line or answering
// a prompt. Loop drops the line and prints a fresh prompt instead of an
// error message.
var errInterrupt = errors.New("line interrupted")

// REPL runs one command or a prompt loop. Output for a person goes to Out.
// --json selects the script form. key copy writes only the public key.
type REPL struct {
	in          *bufio.Reader
	Out         io.Writer
	Err         io.Writer
	Interactive bool
	Raw         bool // the peer has a PTY: read lines with local line discipline
	Color       bool // the peer has a PTY with a real terminal: emit ANSI colors
	Width       int  // the peer's terminal width in columns; 0 means unknown
	Svc         *control.Service
	Bridge      func(name string) error
	Pub         ssh.PublicKey
}

func New(in io.Reader, out, errw io.Writer, svc *control.Service) *REPL {
	if errw == nil {
		errw = out
	}
	return &REPL{in: bufio.NewReader(in), Out: out, Err: errw, Svc: svc}
}

// prompt returns the REPL prompt. The colored form wraps the whole prompt in
// one escape pair so a plain-text substring match still finds "box ▶ ".
func (r *REPL) prompt() string {
	if r.Color {
		return "\x1b[1;32mbox ▶ \x1b[0m"
	}
	return "box ▶ "
}

// errText renders an error message, in red when the peer supports color.
func (r *REPL) errText(msg string) string {
	if r.Color {
		return "\x1b[31m" + msg + "\x1b[0m"
	}
	return msg
}

// Banner prints the greeting shown once when an interactive session starts.
func (r *REPL) Banner() {
	welcome := "Welcome to box."
	if r.Svc != nil && r.Svc.Domain != "" {
		welcome = "Welcome to box — " + r.Svc.Domain + "."
	}
	help := "help"
	if r.Color {
		welcome = "\x1b[1m" + welcome + "\x1b[0m"
		help = "\x1b[1m" + help + "\x1b[0m"
	}
	fmt.Fprintf(r.Out, "%s\n\nRun %s for the command list.\n\n", welcome, help)
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

// Loop reads lines until EOF. A command error is printed and the loop continues.
func (r *REPL) Loop() error {
	// One editor for the whole session so history persists across lines.
	ed := &Editor{in: r.in, out: r.Out, raw: r.Raw, echo: r.Raw, width: r.Width}
	if r.Interactive {
		ed.prompt = r.prompt()
		ed.complete = r.completeLine
	}
	for {
		if r.Interactive {
			fmt.Fprint(r.Out, r.prompt())
		}
		line, err := ed.ReadLine()
		if err != nil && !errors.Is(err, io.EOF) {
			if errors.Is(err, errInterrupt) {
				continue // ^C: drop the line, print a fresh prompt
			}
			return err
		}
		if line != "" {
			argv, splitErr := Split(line)
			if splitErr != nil {
				fmt.Fprintln(r.Err, r.errText(splitErr.Error()))
			} else if runErr := r.Exec(argv); runErr != nil {
				switch {
				case errors.Is(runErr, ErrExit):
					return nil
				case errors.Is(runErr, errInterrupt):
					// ^C at a prompt inside the command cancels it quietly.
				default:
					fmt.Fprintln(r.Err, r.errText(runErr.Error()))
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// ReadLine reads one line of input for callers without session state (a
// one-off question, a pairing password). SSH servers get no terminal line
// discipline for free: a client with a PTY runs its local terminal in raw
// mode, so ReadLine does the echoing itself, returns io.EOF on ^D on an
// empty line, and returns errInterrupt on ^C while echoing. When raw is
// false the client's terminal handles all of it and ReadLine only splits
// on \n. Loop owns an Editor directly.
func ReadLine(in *bufio.Reader, out io.Writer, raw, echo bool) (string, error) {
	return (&Editor{in: in, out: out, raw: raw, echo: echo}).ReadLine()
}

// completeLine returns Tab-completion candidates for the word at the rune
// index cur in line. It completes command and subcommand names from help,
// and live object names (computers, nodes, images, env keys) where a
// command takes one.
func (r *REPL) completeLine(line string, cur int) []string {
	runes := []rune(line)
	if cur > len(runes) {
		cur = len(runes)
	}
	text := string(runes[:cur])
	fields := strings.Fields(text)
	wordIdx := len(fields) // a trailing space starts a fresh word
	prefix := ""
	if !strings.HasSuffix(text, " ") && len(fields) > 0 {
		wordIdx = len(fields) - 1
		prefix = fields[len(fields)-1]
	}
	if strings.HasPrefix(prefix, "-") {
		if wordIdx == 0 {
			return nil // a leading dash is not a command
		}
		return filterPrefix(flagsFor(fields[0]), prefix)
	}
	if wordIdx == 0 {
		return filterPrefix(commandNames(), prefix)
	}
	switch fields[0] {
	case "node", "image", "key", "env":
		if wordIdx == 1 {
			return filterPrefix(subcommandNames(fields[0]), prefix)
		}
		return filterPrefix(r.objectNames(fields[0]+" "+fields[1], wordIdx-2), prefix)
	case "help":
		if wordIdx == 1 {
			// Complete help topics: every command except help itself, plus "all".
			names := []string{"all"}
			for _, n := range commandNames() {
				if n != "help" {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			return filterPrefix(names, prefix)
		}
		return nil
	default:
		return filterPrefix(r.objectNames(fields[0], wordIdx-1), prefix)
	}
}

// flagsFor lists the flags a top-level command accepts, sorted. parseArgv
// accepts the size and placement flags on every command; only new and
// resize act on all of them.
func flagsFor(command string) []string {
	flags := []string{"--json"}
	switch command {
	case "new":
		flags = append(flags, "--image", "--node", "--cpu", "--memory", "--disk")
	case "resize":
		flags = append(flags, "--cpu", "--memory", "--disk")
	}
	sort.Strings(flags)
	return flags
}

// commandNames lists every top-level command name, sorted.
func commandNames() []string {
	var names []string
	seen := map[string]bool{}
	for _, group := range helpHelp {
		for _, e := range group.subs {
			name := strings.Fields(e.name)[0]
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// subcommandNames lists the subcommands of a group command, sorted.
func subcommandNames(group string) []string {
	var names []string
	for _, g := range helpHelp {
		for _, e := range g.subs {
			if f := strings.Fields(e.name); len(f) == 2 && f[0] == group {
				names = append(names, f[1])
			}
		}
	}
	sort.Strings(names)
	return names
}

// objectNames returns live object names for the argument position argIdx of
// cmd ("ssh", "node tag", ...), or nil when the command takes no name there
// or the store is unavailable.
func (r *REPL) objectNames(cmd string, argIdx int) []string {
	if r.Svc == nil || argIdx < 0 {
		return nil
	}
	var names []string
	var err error
	switch cmd {
	case "ssh", "rm", "restart", "stat", "resize", "rename":
		if argIdx == 0 {
			var views []control.ComputerView
			if views, err = r.Svc.ListComputers(); err == nil {
				for _, v := range views {
					names = append(names, v.Name)
				}
			}
		}
	case "node rm", "node tag":
		if argIdx == 0 {
			var views []control.NodeView
			if views, err = r.Svc.ListNodes(); err == nil {
				for _, v := range views {
					names = append(names, v.Name)
				}
			}
		}
	case "image rm", "image default":
		if argIdx == 0 {
			var views []control.ImageView
			if views, err = r.Svc.ListImages(); err == nil {
				for _, v := range views {
					names = append(names, v.Name)
				}
			}
		}
	case "image pull":
		if argIdx == 1 {
			return r.objectNames("node rm", 0)
		}
		return r.objectNames("image rm", 0)
	case "env rm":
		if argIdx == 0 {
			names, err = r.Svc.EnvNames()
		}
	}
	if err != nil {
		return nil
	}
	return names
}

// filterPrefix keeps the names that start with prefix.
func filterPrefix(names []string, prefix string) []string {
	if prefix == "" {
		return names
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
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
	if name == "node" || name == "image" || name == "key" || name == "env" {
		if len(args) == 0 {
			return fmt.Errorf("%q needs a subcommand — run help %s", name, name)
		}
		name = name + " " + args[0]
		args = args[1:]
	}
	switch name {
	case "new":
		return r.cmdNew(args, flags, asJSON)
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
	case "restart":
		if len(args) != 1 {
			return errors.New("usage: restart <name>")
		}
		return r.Svc.Restart(r.ctx(), args[0])
	case "rename":
		if len(args) != 2 {
			return errors.New("usage: rename <name> <new>")
		}
		return r.Svc.Rename(r.ctx(), args[0], args[1])
	case "resize":
		return r.cmdResize(args, flags)
	case "stat":
		if len(args) != 1 {
			return errors.New("usage: stat <name>")
		}
		view, err := r.Svc.Stat(r.ctx(), args[0])
		if err != nil {
			return err
		}
		return r.print(control.FormatStat(view, asJSON))
	case "node ls":
		return r.write(func() (string, error) {
			list, err := r.Svc.ListNodes()
			if err != nil {
				return "", err
			}
			return control.FormatNodes(list, asJSON)
		})
	case "node pair":
		p, err := r.Svc.PairNode()
		if err != nil {
			return err
		}
		_, err = io.WriteString(r.Out, control.FormatPairing(p, "node"))
		return err
	case "node rm":
		if len(args) != 1 {
			return errors.New("usage: node rm <name>")
		}
		return r.Svc.RemoveNode(args[0])
	case "node tag":
		if len(args) != 2 || !strings.Contains(args[1], "=") {
			return errors.New("usage: node tag <name> k=v")
		}
		k, v, _ := strings.Cut(args[1], "=")
		return r.Svc.TagNode(args[0], k, v)
	case "image ls":
		return r.write(func() (string, error) {
			list, err := r.Svc.ListImages()
			if err != nil {
				return "", err
			}
			return control.FormatImages(list, asJSON)
		})
	case "image add":
		if len(args) != 2 {
			return errors.New("usage: image add <name> <ref>")
		}
		return r.Svc.AddImage(args[0], args[1])
	case "image pull":
		node := ""
		if len(args) == 2 {
			node = args[1]
		} else if len(args) != 1 {
			return errors.New("usage: image pull <name> [node]")
		}
		return r.Svc.Pull(r.ctx(), args[0], node)
	case "image rm":
		if len(args) != 1 {
			return errors.New("usage: image rm <name>")
		}
		return r.Svc.RemoveImage(args[0])
	case "image default":
		if len(args) != 1 {
			return errors.New("usage: image default <name>")
		}
		return r.Svc.SetDefaultImage(args[0])
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
	case "key copy":
		line, err := r.Svc.KeyCopy()
		if err != nil {
			return err
		}
		_, err = io.WriteString(r.Out, line)
		return err
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
	case "whoami":
		line, err := r.Svc.WhoAmI(r.Pub)
		if err != nil {
			return err
		}
		fmt.Fprintln(r.Out, line)
		return nil
	case "defaults":
		d, err := r.Svc.Defaults()
		if err != nil {
			return err
		}
		return r.print(control.FormatDefaults(d, asJSON))
	case "help":
		return r.cmdHelp(pos)
	case "exit", "quit", "logout":
		return ErrExit
	case "clear":
		if r.Raw {
			// Home, clear screen and scrollback. Only with a PTY: a pipe
			// would just receive escape junk.
			io.WriteString(r.Out, "\x1b[H\x1b[2J\x1b[3J")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q — run help", name)
	}
}

func (r *REPL) cmdNew(args []string, flags map[string]string, asJSON bool) error {
	def, err := r.Svc.Defaults()
	if err != nil {
		return err
	}
	var name string
	if len(args) > 0 {
		name = args[0]
	} else if r.Interactive {
		name, err = r.ask("name", "")
		if err != nil {
			return err
		}
	}
	if name == "" {
		return errors.New("name is required")
	}
	image := flags["image"]
	if image == "" {
		image, err = r.ask("image", def.Image)
		if err != nil {
			return err
		}
	}
	node := flags["node"]
	if node == "" {
		node, err = r.ask("node", def.Node)
		if err != nil {
			return err
		}
	}
	cpu := def.CPU
	if raw := flags["cpu"]; raw != "" {
		cpu, err = size.ParseCPU(raw)
		if err != nil {
			return err
		}
	} else if r.Interactive {
		raw, err = r.ask("cpu", size.FormatCPU(def.CPU))
		if err != nil {
			return err
		}
		if raw != "" {
			cpu, err = size.ParseCPU(raw)
			if err != nil {
				return err
			}
		}
	}
	memory := def.Memory
	if raw := flags["memory"]; raw != "" {
		memory, err = size.ParseBytes(raw)
		if err != nil {
			return err
		}
	} else if r.Interactive {
		raw, err = r.ask("memory", size.FormatBytes(def.Memory))
		if err != nil {
			return err
		}
		if raw != "" {
			memory, err = size.ParseBytes(raw)
			if err != nil {
				return err
			}
		}
	}
	disk := def.Disk
	if raw := flags["disk"]; raw != "" {
		disk, err = size.ParseBytes(raw)
		if err != nil {
			return err
		}
	} else if r.Interactive {
		raw, err = r.ask("disk", size.FormatBytes(def.Disk))
		if err != nil {
			return err
		}
		if raw != "" {
			disk, err = size.ParseBytes(raw)
			if err != nil {
				return err
			}
		}
	}
	created, err := r.Svc.Create(r.ctx(), control.CreateInput{
		Name: name, Image: image, Node: node, CPU: cpu, Memory: memory, Disk: disk,
	})
	if err != nil {
		return err
	}
	return r.print(control.FormatCreated(created, asJSON))
}

func (r *REPL) cmdRm(args []string) error {
	if len(args) != 1 && len(args) != 2 {
		return errors.New("usage: rm <name>")
	}
	name := args[0]
	confirm := ""
	if len(args) == 2 {
		confirm = args[1]
	} else if r.Interactive {
		var err error
		confirm, err = r.ask("name", "")
		if err != nil {
			return err
		}
	} else {
		return errors.New("rm asks for the name again")
	}
	if confirm != name {
		return errors.New("name did not match")
	}
	return r.Svc.Delete(r.ctx(), name)
}

func (r *REPL) cmdResize(args []string, flags map[string]string) error {
	if len(args) != 1 {
		return errors.New("usage: resize <name>")
	}
	in := control.ResizeInput{Name: args[0]}
	if raw := flags["cpu"]; raw != "" {
		v, err := size.ParseCPU(raw)
		if err != nil {
			return err
		}
		in.CPU = &v
	}
	if raw := flags["memory"]; raw != "" {
		v, err := size.ParseBytes(raw)
		if err != nil {
			return err
		}
		in.Memory = &v
	}
	if raw := flags["disk"]; raw != "" {
		v, err := size.ParseBytes(raw)
		if err != nil {
			return err
		}
		in.Disk = &v
	}
	if in.CPU == nil && in.Memory == nil && in.Disk == nil {
		if !r.Interactive {
			return errors.New("nothing to change")
		}
		cur, err := r.Svc.Stat(r.ctx(), args[0])
		if err != nil {
			return err
		}
		if raw, err := r.ask("cpu", size.FormatCPU(cur.CPU)); err != nil {
			return err
		} else if raw != "" && raw != size.FormatCPU(cur.CPU) {
			v, err := size.ParseCPU(raw)
			if err != nil {
				return err
			}
			in.CPU = &v
		}
		if raw, err := r.ask("memory", size.FormatBytes(cur.Memory)); err != nil {
			return err
		} else if raw != "" && raw != size.FormatBytes(cur.Memory) {
			v, err := size.ParseBytes(raw)
			if err != nil {
				return err
			}
			in.Memory = &v
		}
		if raw, err := r.ask("disk", size.FormatBytes(cur.Disk)); err != nil {
			return err
		} else if raw != "" && raw != size.FormatBytes(cur.Disk) {
			v, err := size.ParseBytes(raw)
			if err != nil {
				return err
			}
			in.Disk = &v
		}
	}
	return r.Svc.Resize(r.ctx(), in)
}

func (r *REPL) ask(prompt, def string) (string, error) {
	if !r.Interactive {
		return def, nil
	}
	if def != "" {
		fmt.Fprintf(r.Out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(r.Out, "%s: ", prompt)
	}
	line, err := ReadLine(r.in, r.Out, r.Raw, r.Raw)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (r *REPL) ctx() context.Context { return context.Background() }

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
	known := map[string]bool{"image": true, "node": true, "cpu": true, "memory": true, "disk": true}
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
			{name: "new", usage: "new [name] [--image ref] [--node n] [--cpu 2] [--memory 2G] [--disk 20G]", short: "Create a computer; asks for anything missing"},
			{name: "ssh", usage: "ssh <name>", short: "Open a shell inside a computer"},
			{name: "ls", usage: "ls", short: "List computers"},
			{name: "rm", usage: "rm <name>", short: "Destroy a computer; asks for the name again"},
			{name: "restart", usage: "restart <name>", short: "Restart a computer"},
			{name: "resize", usage: "resize <name> [--cpu 2] [--memory 4G] [--disk 40G]", short: "Change cpu, memory or disk; asks against current values"},
			{name: "rename", usage: "rename <name> <new-name>", short: "Rename a computer"},
			{name: "stat", usage: "stat <name>", short: "Show detail for one computer"},
		},
	},
	{
		name: "Nodes & images",
		subs: []helpEntry{
			{name: "node ls", usage: "node ls", short: "List deploy nodes"},
			{name: "node pair", usage: "node pair", short: "Print the one-time code that joins a deploy node"},
			{name: "node rm", usage: "node rm <name>", short: "Remove a deploy node"},
			{name: "node tag", usage: "node tag <name> k=v ...", short: "Tag a node; new targets tags by default"},
			{name: "image ls", usage: "image ls", short: "List computer images"},
			{name: "image add", usage: "image add <name> <ref>", short: "Register an image reference"},
			{name: "image pull", usage: "image pull <name> [node]", short: "Pull an image onto one node or every matching node"},
			{name: "image rm", usage: "image rm <name>", short: "Remove an image"},
			{name: "image default", usage: "image default <name>", short: "Make an image the default for new"},
		},
	},
	{
		name: "Keys & secrets",
		subs: []helpEntry{
			{name: "pair", usage: "pair", short: "Print a one-time password that adds your ssh key"},
			{name: "key ls", usage: "key ls", short: "List paired ssh keys"},
			{name: "key rm", usage: "key rm <fingerprint>", short: "Revoke an ssh key"},
			{name: "key copy", usage: "key copy", short: "Print the server's GitHub public key and nothing else"},
			{name: "env set", usage: "env set <name> <value>", short: "Store a secret, injected into every computer"},
			{name: "env rm", usage: "env rm <name>", short: "Remove a secret"},
			{name: "env ls", usage: "env ls", short: "List secret names; values are never printed"},
			{name: "whoami", usage: "whoami", short: "Show the key this session authenticated with"},
			{name: "defaults", usage: "defaults", short: "Show the default node, image and size that new uses"},
		},
	},
	{
		name: "Session",
		subs: []helpEntry{
			{name: "clear", usage: "clear", short: "Clear the screen"},
			{name: "exit", usage: "exit", short: "End this session; ^D on an empty line works too"},
		},
	},
	{
		name: "Help",
		subs: []helpEntry{
			{name: "help", usage: "help [all | <command>]", short: "This help; help node covers every node subcommand"},
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
	bold, reset := "", ""
	if r.Color {
		bold, reset = "\x1b[1m", "\x1b[0m"
	}
	fmt.Fprintf(r.Out, "%sCommon commands:%s\n\n", bold, reset)
	rows := []struct{ group, cmds string }{
		{"Computers", "new  ssh  ls  rm  restart  resize  rename  stat"},
		{"Nodes & images", "node†  image†"},
		{"Keys & secrets", "pair  key†  env†  whoami  defaults"},
		{"Session", "clear  exit"},
		{"Help", "help"},
	}
	for _, row := range rows {
		// markDagger after the width formatting: escapes must not shift columns.
		fmt.Fprintf(r.Out, "%s", r.markDagger(fmt.Sprintf("%s%-16s%s%s\n", bold, row.group, reset, row.cmds)))
	}
	dagger := r.markDagger("†")
	fmt.Fprintf(r.Out, "\n%s marks a command with subcommands.\n", dagger)
	dim := reset
	if r.Color {
		dim = "\x1b[2m"
	}
	fmt.Fprintf(r.Out, "%sRun help all for a list of all commands, help <command> for more detail.%s\n", dim, reset)
}

// markDagger colors the † subcommand marker blue when the peer supports color.
func (r *REPL) markDagger(s string) string {
	if r.Color {
		return strings.ReplaceAll(s, "†", "\x1b[1;34m†\x1b[0m")
	}
	return s
}

func helpLine(w io.Writer, usage, short string) {
	if len(usage) > 28 {
		fmt.Fprintf(w, "  %s\n      %s\n", usage, short)
		return
	}
	fmt.Fprintf(w, "  %-28s%s\n", usage, short)
}

func (r *REPL) printHelpAll() {
	bold, reset := "", ""
	if r.Color {
		bold, reset = "\x1b[1m", "\x1b[0m"
	}
	for _, group := range helpHelp {
		fmt.Fprintf(r.Out, "%s%s:%s\n", bold, group.name, reset)
		for _, e := range group.subs {
			helpLine(r.Out, e.usage, e.short)
		}
		fmt.Fprintln(r.Out)
	}
}

func (r *REPL) printHelpFor(name string) error {
	name = strings.ToLower(name)
	bold, reset := "", ""
	if r.Color {
		bold, reset = "\x1b[1m", "\x1b[0m"
	}
	for _, group := range helpHelp {
		for _, e := range group.subs {
			entry := e
			if entry.name == name {
				helpLine(r.Out, entry.usage, entry.short)
				return nil
			}
			if fields := strings.Fields(entry.name); len(fields) > 1 && fields[0] == name {
				fmt.Fprintf(r.Out, "%s%s:%s\n", bold, group.name, reset)
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
