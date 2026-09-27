package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/tunnel"
)

func (a *agent) listen() error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(a.dir, "agent.sock")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	a.mu.Lock()
	a.ln = ln
	a.mu.Unlock()
	go a.acceptLoop(ln)
	return nil
}

func (a *agent) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = rpc.Serve(c, a.handleGuest)
		}(c)
	}
}

func (a *agent) stopListen() {
	a.mu.Lock()
	ln := a.ln
	a.ln = nil
	a.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (a *agent) handleGuest(op string, body json.RawMessage) (any, error) {
	switch op {
	case rpc.OpDomain:
		var out rpc.DomainBody
		if err := a.call(rpc.OpDomain, nil, &out); err != nil {
			return nil, err
		}
		if out.Domain != "" {
			_ = a.save(func(c *computer) { c.Domain = out.Domain })
		}
		return out, nil
	case rpc.OpPortalCheck:
		var req tunnel.PortalCheckRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		var resp tunnel.PortalCheckResponse
		if err := a.call(tunnel.OpPortalCheck, req, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	case rpc.OpPortalAdd:
		var req tunnel.PortalAddRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		var resp tunnel.PortalAddResponse
		if err := a.call(tunnel.OpPortalAdd, req, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	case rpc.OpPortalLs:
		var resp tunnel.PortalList
		if err := a.call(tunnel.OpPortalLs, nil, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	case rpc.OpPortalRm:
		var req tunnel.PortalRmRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		if err := a.call(tunnel.OpPortalRm, req, nil); err != nil {
			return nil, err
		}
		return nil, nil
	case rpc.OpEventPub:
		var req tunnel.EventPublish
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		var resp tunnel.EventItem
		if err := a.call(tunnel.OpEventPub, req, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	case rpc.OpEventGet:
		var req tunnel.EventQuery
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		var resp tunnel.EventList
		if err := a.call(tunnel.OpEventGet, req, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	default:
		return nil, errors.New("unsupported")
	}
}

func (a *agent) call(op string, req, resp any) error {
	a.mu.Lock()
	s := a.sess
	ctx := a.ctx
	a.mu.Unlock()
	if s == nil {
		return errors.New("tunnel down")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.Call(cctx, op, req, resp)
}

func (a *agent) onControl(op string, body json.RawMessage) (any, error) {
	switch op {
	case tunnel.OpKeys:
		var req tunnel.KeysRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		keys := append([]string(nil), req.AuthorizedKeys...)
		return nil, a.commitAccess(keys, a.env)
	case tunnel.OpEnv:
		var req tunnel.EnvRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		return nil, a.commitAccess(a.keys, maps.Clone(req.Vars))
	case tunnel.OpStat:
		return readStat(), nil
	default:
		return nil, errors.New("unsupported")
	}
}

func decodeBody(body json.RawMessage, dest any) error {
	if len(body) == 0 || string(body) == "null" {
		return nil
	}
	return json.Unmarshal(body, dest)
}
