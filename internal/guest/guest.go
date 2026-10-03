// Package guest is the CLI on a computer: domain, portal, and event.
// It talks only to the agent socket.
package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/rpc"
)

// callTimeout bounds one guest call: 25s of long poll plus slack, under the
// agent's 30s timeout to the server.
const callTimeout = 30 * time.Second

// Run executes domain or portal against the guest socket.
func Run(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: box domain | box portal check|add|ls|rm | box event pub|get")
	}
	switch args[0] {
	case "domain":
		if len(args) != 1 {
			return fmt.Errorf("usage: box domain")
		}
		var out rpc.DomainBody
		if err := call(context.Background(), socket, rpc.OpDomain, nil, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.Domain)
		return nil
	case "portal":
		return portal(socket, args[1:], stdout)
	case "event":
		return eventCmd(ctx, socket, args[1:], stdin, stdout, stderr)
	case "serve", "node":
		return fmt.Errorf("box %s is not available in a computer", args[0])
	default:
		return fmt.Errorf("box %s is not available in a computer", args[0])
	}
}

func portal(socket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: box portal check|add|ls|rm")
	}
	switch args[0] {
	case "check":
		if len(args) != 2 {
			return fmt.Errorf("usage: box portal check <label>")
		}
		var out rpc.PortalResult
		if err := call(context.Background(), socket, rpc.OpPortalCheck, rpc.PortalBody{Label: args[1]}, &out); err != nil {
			return err
		}
		if out.Free {
			fmt.Fprintln(stdout, "free")
			return nil
		}
		if out.Holder != "" {
			fmt.Fprintf(stdout, "taken by %s\n", out.Holder)
			return nil
		}
		fmt.Fprintln(stdout, "taken")
		return nil
	case "add":
		if len(args) != 3 && len(args) != 4 {
			return fmt.Errorf("usage: box portal add <label> <port> [public|private]")
		}
		port, err := parsePort(args[2])
		if err != nil {
			return err
		}
		private := false
		if len(args) == 4 {
			switch args[3] {
			case "public":
			case "private":
				private = true
			default:
				return fmt.Errorf("usage: box portal add <label> <port> [public|private]")
			}
		}
		var out rpc.PortalResult
		if err := call(context.Background(), socket, rpc.OpPortalAdd, rpc.PortalBody{Label: args[1], Port: port, Private: private}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.URL)
		return nil
	case "ls":
		var out rpc.PortalList
		if err := call(context.Background(), socket, rpc.OpPortalLs, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "HOST\tPORT\tACCESS\n")
		for _, p := range out.Portals {
			access := "public"
			if p.Private {
				access = "private"
			}
			fmt.Fprintf(stdout, "%s\t%d\t%s\n", p.Label, p.Port, access)
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: box portal rm <label>")
		}
		return call(context.Background(), socket, rpc.OpPortalRm, rpc.PortalBody{Label: args[1]}, nil)
	default:
		return fmt.Errorf("usage: box portal check|add|ls|rm")
	}
}

const eventUsage = "usage: box event pub <topic> [text...] [--key k] | box event get [--since n] [--topic t]... [--from f]... [--limit n] [--wait s] [--json] [--follow]"

func eventCmd(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", eventUsage)
	}
	switch args[0] {
	case "pub":
		return eventPub(ctx, socket, args[1:], stdin, stdout)
	case "get":
		return eventGet(ctx, socket, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("%s", eventUsage)
	}
}

// eventPub stores text args joined by spaces, or stdin when there is no text
// and stdin is a pipe or file.
func eventPub(ctx context.Context, socket string, args []string, stdin io.Reader, stdout io.Writer) error {
	var key string
	var pos []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--key" {
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for --key")
			}
			i++
			key = args[i]
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: box event pub <topic> [text...] [--key k]")
	}
	body, err := event.ReadBody(pos[1:], stdin, stdinIsTerminal(stdin))
	if err != nil {
		return err
	}
	var out rpc.EventResult
	req := rpc.EventPublish{Topic: pos[0], Body: body, Key: key}
	if err := call(ctx, socket, rpc.OpEventPub, req, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, out.ID)
	return nil
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

// eventGet prints events once, or follows the log until interrupted.
func eventGet(ctx context.Context, socket string, args []string, stdout, stderr io.Writer) error {
	f, err := parseEventFlags(args)
	if err != nil {
		return err
	}
	fetch := func(ctx context.Context, q event.Query) (event.Result, error) {
		var out rpc.EventList
		req := rpc.EventQuery{
			Since: q.Since, Topics: q.Topics, Froms: q.Froms, Limit: q.Limit,
			Wait: int(q.Wait.Seconds()),
		}
		if err := call(ctx, socket, rpc.OpEventGet, req, &out); err != nil {
			return event.Result{}, err
		}
		return event.Result{
			Events: wireItems(out.Events), Oldest: out.Oldest, Latest: out.Latest, More: out.More,
		}, nil
	}
	q := event.Query{Since: f.since, Topics: f.topics, Froms: f.froms, Limit: f.limit}
	if !f.follow {
		q.Wait = time.Duration(f.wait) * time.Second
		res, err := fetch(ctx, q)
		if err != nil {
			return err
		}
		if f.asJSON {
			text, err := control.FormatEvents(res, true)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(stdout, text)
			return err
		}
		text, err := control.FormatEvents(res, false)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(stdout, text)
		return err
	}
	printHeader := true
	return event.Follow(ctx, q, f.since, !f.sinceSet, fetch, func(res event.Result) error {
		if f.asJSON {
			raw, err := json.Marshal(res)
			if err != nil {
				return err
			}
			raw = append(raw, '\n')
			_, err = stdout.Write(raw)
			return err
		}
		if printHeader {
			fmt.Fprintln(stdout, "ID\tTIME\tFROM\tTOPIC\tBODY")
			printHeader = false
		}
		for _, item := range res.Events {
			fmt.Fprintln(stdout, control.FormatEventLine(item))
		}
		return nil
	}, func(since, oldest int64) {
		fmt.Fprintf(stderr, "box: missed events: cursor %d, oldest retained %d\n", since, oldest)
	})
}

func parseEventFlags(args []string) (*eventFlags, error) {
	f := &eventFlags{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			n, err := parseFlagInt(args, &i)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("invalid since")
			}
			f.since = int64(n)
			f.sinceSet = true
		case "--topic":
			t, err := parseFlagString(args, &i)
			if err != nil {
				return nil, err
			}
			f.topics = append(f.topics, t)
		case "--from":
			t, err := parseFlagString(args, &i)
			if err != nil {
				return nil, err
			}
			f.froms = append(f.froms, t)
		case "--limit":
			n, err := parseFlagInt(args, &i)
			if err != nil || n < 1 || n > event.MaxLimit {
				return nil, fmt.Errorf("invalid limit")
			}
			f.limit = n
		case "--wait":
			n, err := parseFlagInt(args, &i)
			if err != nil || n < 0 || n > event.MaxWaitSeconds {
				return nil, fmt.Errorf("invalid wait")
			}
			f.wait = n
		case "--json":
			f.asJSON = true
		case "--follow":
			f.follow = true
		default:
			return nil, fmt.Errorf("%s", eventUsage)
		}
	}
	return f, nil
}

func parseFlagString(args []string, i *int) (string, error) {
	if *i+1 >= len(args) {
		return "", fmt.Errorf("missing value")
	}
	*i++
	return args[*i], nil
}

func parseFlagInt(args []string, i *int) (int, error) {
	if *i+1 >= len(args) {
		return 0, fmt.Errorf("missing value")
	}
	*i++
	return strconv.Atoi(args[*i])
}

func wireItems(list []rpc.EventItem) []event.Item {
	out := make([]event.Item, 0, len(list))
	for _, item := range list {
		out = append(out, event.Item{
			ID: item.ID, Topic: item.Topic, Body: item.Body, From: item.From, Key: item.Key, Time: item.Time,
		})
	}
	return out
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

func parsePort(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 || n > 65535 || fmt.Sprintf("%d", n) != strings.TrimSpace(s) {
		return 0, fmt.Errorf("invalid port")
	}
	return n, nil
}

// call places one request on the guest socket. ctx ends it early: a long
// poll must not sit out its 25 seconds after an interrupt.
func call(ctx context.Context, socket, op string, req, resp any) error {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fmt.Errorf("guest socket is not available")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(callTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	return rpc.Call(conn, op, req, resp)
}
