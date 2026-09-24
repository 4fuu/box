// Package repl is the control REPL served over SSH.
package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/size"
	"golang.org/x/crypto/ssh"
)

// REPL runs one command or a prompt loop. Output for a person goes to Out.
// --json selects the script form. key copy writes only the public key.
type REPL struct {
	in          *bufio.Reader
	Out         io.Writer
	Err         io.Writer
	Interactive bool
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

// Loop reads lines until EOF. A command error is printed and the loop continues.
func (r *REPL) Loop() error {
	for {
		if r.Interactive {
			fmt.Fprint(r.Out, "box ▶ ")
		}
		line, err := r.in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line = strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if line != "" {
			argv, splitErr := Split(line)
			if splitErr != nil {
				fmt.Fprintln(r.Err, splitErr.Error())
			} else if runErr := r.Exec(argv); runErr != nil {
				fmt.Fprintln(r.Err, runErr.Error())
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
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
			return fmt.Errorf("unknown command")
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
	default:
		return errors.New("unknown command")
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
	line, err := r.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(strings.TrimRight(line, "\r\n"))
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
