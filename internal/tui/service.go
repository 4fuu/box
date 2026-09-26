package tui

import (
	"context"

	"github.com/4fuu/box/internal/control"
)

// ServiceBackend calls the process's control.Service. The dashboard does not
// use this; it goes through the localhost socket, which calls the same service.
type ServiceBackend struct {
	Svc  *control.Service
	From string
}

func (b ServiceBackend) Snapshot(context.Context) (Snapshot, error) {
	s, err := b.Svc.Snapshot()
	if err != nil {
		return Snapshot{}, err
	}
	return fromControl(s), nil
}

func (b ServiceBackend) Approve(_ context.Context, code string) (string, error) {
	return b.Svc.Approve(code, b.From)
}

func (b ServiceBackend) Remove(_ context.Context, name string) error {
	return b.Svc.Remove(name)
}

func (b ServiceBackend) Rename(_ context.Context, oldName, newName string) error {
	return b.Svc.Rename(oldName, newName)
}

func (b ServiceBackend) RemoveKey(_ context.Context, fingerprint string) error {
	return b.Svc.RemoveKey(fingerprint)
}

func (b ServiceBackend) Pair(context.Context) (Pairing, error) {
	p, err := b.Svc.PairClient()
	if err != nil {
		return Pairing{}, err
	}
	return Pairing{Secret: p.Secret, Expires: p.Expires}, nil
}

func (b ServiceBackend) SetEnv(_ context.Context, name, value string) (string, error) {
	return b.Svc.SetEnv(name, value)
}

func (b ServiceBackend) DeleteEnv(_ context.Context, name string) error {
	return b.Svc.DeleteEnv(name)
}

func fromControl(s control.Snapshot) Snapshot {
	out := Snapshot{
		Domain:    s.Domain,
		Computers: make([]Computer, 0, len(s.Computers)),
		Pending:   make([]Pending, 0, len(s.Pending)),
		Portals:   make([]Portal, 0, len(s.Portals)),
		Keys:      make([]Key, 0, len(s.Keys)),
		Env:       append([]string{}, s.Env...),
	}
	for _, c := range s.Computers {
		out.Computers = append(out.Computers, Computer{
			Name: c.Name, Online: c.Online, User: c.User, Address: c.Address,
			AgentVersion: c.AgentVersion, Portals: append([]string{}, c.Portals...),
		})
	}
	for _, p := range s.Pending {
		out.Pending = append(out.Pending, Pending{
			Name: p.Name, Address: p.Address, User: p.User, ExpiresAt: p.ExpiresAt,
		})
	}
	for _, p := range s.Portals {
		out.Portals = append(out.Portals, Portal{
			Label: p.Label, Host: p.Host, Computer: p.Computer, Port: p.Port,
		})
	}
	for _, k := range s.Keys {
		out.Keys = append(out.Keys, Key{
			Fingerprint: k.Fingerprint, Comment: k.Comment, BoundAt: k.BoundAt,
		})
	}
	if out.Env == nil {
		out.Env = []string{}
	}
	return out
}
