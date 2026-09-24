package node

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"

	"github.com/4fuu/box/internal/api"
	"github.com/4fuu/box/internal/rpc"
)

func (c *Controller) restoreGuests() error {
	c.mu.Lock()
	var comps []*comp
	for _, comp := range c.comps {
		comps = append(comps, comp)
	}
	c.mu.Unlock()
	for _, comp := range comps {
		if err := c.listenGuest(comp); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) listenGuest(comp *comp) error {
	if err := os.MkdirAll(filepath.Dir(comp.GuestSocket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(comp.GuestSocket)
	ln, err := net.Listen("unix", comp.GuestSocket)
	if err != nil {
		return err
	}
	if err := os.Chmod(comp.GuestSocket, 0o666); err != nil {
		ln.Close()
		return err
	}
	c.mu.Lock()
	c.guests[comp.Name] = ln
	c.mu.Unlock()
	go c.acceptGuest(ln, comp)
	return nil
}

func (c *Controller) acceptGuest(ln net.Listener, comp *comp) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_ = rpc.Serve(conn, func(op string, body json.RawMessage) (any, error) {
				return c.guest(comp, op, body)
			})
		}()
	}
}

func (c *Controller) closeGuest(name string) {
	c.mu.Lock()
	ln := c.guests[name]
	delete(c.guests, name)
	c.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

func (c *Controller) guest(comp *comp, op string, body json.RawMessage) (any, error) {
	switch op {
	case rpc.OpDomain:
		if c.Domain() == "" {
			var resp api.DomainResponse
			_ = c.post(context.Background(), "/domain", map[string]string{}, &resp)
			if resp.Domain != "" {
				c.mu.Lock()
				c.domain = resp.Domain
				c.mu.Unlock()
			}
		}
		return rpc.DomainBody{Domain: c.Domain()}, nil
	case rpc.OpPortalCheck:
		var req rpc.PortalBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		var resp api.CheckResponse
		if err := c.post(context.Background(), "/check", api.CheckRequest{Label: req.Label}, &resp); err != nil {
			return nil, err
		}
		return rpc.PortalResult{Free: resp.Free, Holder: resp.Holder}, nil
	case rpc.OpPortalAdd:
		var req rpc.PortalBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		// The socket is bound to this container. A guest cannot name another one.
		name := c.containerName(comp)
		var resp api.ClaimResponse
		if err := c.post(context.Background(), "/claim", api.ClaimRequest{
			Container: name, Label: req.Label, Port: req.Port,
		}, &resp); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.portals = append(c.portals, api.PortalReport{
			Label: req.Label, Host: resp.Host, Port: req.Port, Container: name,
		})
		c.mu.Unlock()
		if err := c.reloadFRP(); err != nil {
			return nil, err
		}
		return rpc.PortalResult{URL: resp.URL, Host: resp.Host, Port: req.Port}, nil
	case rpc.OpPortalLs:
		name := c.containerName(comp)
		c.mu.Lock()
		var items []rpc.PortalItem
		for _, p := range c.portals {
			if p.Container == name {
				items = append(items, rpc.PortalItem{Label: p.Label, Port: p.Port})
			}
		}
		c.mu.Unlock()
		return rpc.PortalList{Portals: items}, nil
	case rpc.OpPortalRm:
		var req rpc.PortalBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		name := c.containerName(comp)
		if err := c.post(context.Background(), "/release", api.ReleaseRequest{Container: name, Label: req.Label}, nil); err != nil {
			return nil, err
		}
		c.mu.Lock()
		kept := c.portals[:0]
		for _, p := range c.portals {
			if p.Container == name && p.Label == req.Label {
				continue
			}
			kept = append(kept, p)
		}
		c.portals = kept
		c.mu.Unlock()
		return nil, c.reloadFRP()
	default:
		return nil, errUnknown
	}
}

func (c *Controller) containerName(comp *comp) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return comp.Name
}

var errUnknown = errStr("unknown op")

type errStr string

func (e errStr) Error() string { return string(e) }
