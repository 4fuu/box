package server

import (
	"context"
	"fmt"
	"net"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/rpc"
)

// dialClient is the server's NodeClient. A dial failure is unreachable.
// An error returned by the node is passed through unchanged.
type dialClient struct {
	dial func(ctx context.Context, nodeID, proxy string) (net.Conn, error)
}

func (d dialClient) call(ctx context.Context, nodeID, op string, req, resp any) error {
	if d.dial == nil {
		return control.ErrUnreachable
	}
	conn, err := d.dial(ctx, nodeID, "rpc")
	if err != nil {
		return fmt.Errorf("%w", control.ErrUnreachable)
	}
	defer conn.Close()
	return rpc.Call(conn, op, req, resp)
}

func (d dialClient) Create(ctx context.Context, nodeID string, body rpc.CreateBody) error {
	return d.call(ctx, nodeID, rpc.OpCreate, body, nil)
}

func (d dialClient) Delete(ctx context.Context, nodeID, name string) error {
	return d.call(ctx, nodeID, rpc.OpDelete, rpc.NameBody{Name: name}, nil)
}

func (d dialClient) Restart(ctx context.Context, nodeID string, body rpc.CreateBody) error {
	return d.call(ctx, nodeID, rpc.OpRestart, body, nil)
}

func (d dialClient) Rename(ctx context.Context, nodeID, oldName, newName string) error {
	return d.call(ctx, nodeID, rpc.OpRename, rpc.RenameBody{Old: oldName, New: newName}, nil)
}

func (d dialClient) Resize(ctx context.Context, nodeID string, body rpc.ResizeBody) error {
	return d.call(ctx, nodeID, rpc.OpResize, body, nil)
}

func (d dialClient) Pull(ctx context.Context, nodeID string, body rpc.PullBody) error {
	return d.call(ctx, nodeID, rpc.OpPull, body, nil)
}

func (d dialClient) SyncKeys(ctx context.Context, nodeID string, lines []string) error {
	return d.call(ctx, nodeID, rpc.OpSyncKeys, rpc.KeysBody{AuthorizedKeys: lines}, nil)
}

func (d dialClient) HostKey(ctx context.Context, nodeID, name string) (string, error) {
	var out rpc.HostKeyBody
	if err := d.call(ctx, nodeID, rpc.OpHostKey, rpc.NameBody{Name: name}, &out); err != nil {
		return "", err
	}
	return out.Public, nil
}

func (d dialClient) Stat(ctx context.Context, nodeID, name string) (rpc.StatBody, error) {
	var out rpc.StatBody
	err := d.call(ctx, nodeID, rpc.OpStat, rpc.NameBody{Name: name}, &out)
	return out, err
}
