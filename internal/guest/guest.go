// Package guest is the CLI inside a computer: domain and portal.
// It talks only to the guest socket mounted for this container.
package guest

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/4fuu/box/internal/rpc"
)

// Run executes domain or portal against the guest socket.
func Run(socket string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: box domain | box portal check|add|ls|rm")
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
		if len(args) != 3 {
			return fmt.Errorf("usage: box portal add <label> <port>")
		}
		port, err := parsePort(args[2])
		if err != nil {
			return err
		}
		var out rpc.PortalResult
		if err := call(socket, rpc.OpPortalAdd, rpc.PortalBody{Label: args[1], Port: port}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.URL)
		return nil
	case "ls":
		var out rpc.PortalList
		if err := call(socket, rpc.OpPortalLs, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "HOST\tPORT\n")
		for _, p := range out.Portals {
			fmt.Fprintf(stdout, "%s\t%d\n", p.Label, p.Port)
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
