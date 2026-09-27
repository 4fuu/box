// Package guest is the CLI on a computer: domain, portal, and event.
// It talks only to the agent socket.
package guest

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/box/internal/rpc"
)

// Run executes domain or portal against the guest socket.
func Run(socket string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: box domain | box portal check|add|ls|rm | box event pub|get")
	}
	switch args[0] {
	case "domain":
		if len(args) != 1 {
			return fmt.Errorf("usage: box domain")
		}
		var out rpc.DomainBody
		if err := call(socket, rpc.OpDomain, nil, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.Domain)
		return nil
	case "portal":
		return portal(socket, args[1:], stdout)
	case "event":
		return eventCmd(socket, args[1:], stdout)
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
		if err := call(socket, rpc.OpPortalCheck, rpc.PortalBody{Label: args[1]}, &out); err != nil {
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
		if err := call(socket, rpc.OpPortalAdd, rpc.PortalBody{Label: args[1], Port: port, Private: private}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.URL)
		return nil
	case "ls":
		var out rpc.PortalList
		if err := call(socket, rpc.OpPortalLs, nil, &out); err != nil {
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
		return call(socket, rpc.OpPortalRm, rpc.PortalBody{Label: args[1]}, nil)
	default:
		return fmt.Errorf("usage: box portal check|add|ls|rm")
	}
}

func eventCmd(socket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: box event pub <topic> <text> | box event get [--since n] [--topic name]")
	}
	switch args[0] {
	case "pub":
		if len(args) < 3 {
			return fmt.Errorf("usage: box event pub <topic> <text>")
		}
		var out rpc.EventItem
		req := rpc.EventPublish{Topic: args[1], Body: strings.Join(args[2:], " ")}
		if err := call(socket, rpc.OpEventPub, req, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.ID)
		return nil
	case "get":
		since, topic, err := eventFlags(args[1:])
		if err != nil {
			return err
		}
		var out rpc.EventList
		if err := call(socket, rpc.OpEventGet, rpc.EventQuery{Since: since, Topic: topic}, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "ID\tTIME\tFROM\tTOPIC\tBODY\n")
		for _, item := range out.Events {
			fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\t%s\n", item.ID, item.Time.UTC().Format(time.RFC3339), item.From, item.Topic, item.Body)
		}
		return nil
	default:
		return fmt.Errorf("usage: box event pub <topic> <text> | box event get [--since n] [--topic name]")
	}
}

func eventFlags(args []string) (int64, string, error) {
	var since int64
	var topic string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 >= len(args) {
				return 0, "", fmt.Errorf("missing value for --since")
			}
			i++
			n, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || n < 0 {
				return 0, "", fmt.Errorf("invalid since")
			}
			since = n
		case "--topic":
			if i+1 >= len(args) {
				return 0, "", fmt.Errorf("missing value for --topic")
			}
			i++
			topic = args[i]
		default:
			return 0, "", fmt.Errorf("usage: box event get [--since n] [--topic name]")
		}
	}
	return since, topic, nil
}

func parsePort(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 || n > 65535 || fmt.Sprintf("%d", n) != strings.TrimSpace(s) {
		return 0, fmt.Errorf("invalid port")
	}
	return n, nil
}

func call(socket, op string, req, resp any) error {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fmt.Errorf("guest socket is not available")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return rpc.Call(conn, op, req, resp)
}
