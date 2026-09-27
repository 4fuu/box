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

func (b ServiceBackend) Snapshot(context.Context) (control.Snapshot, error) {
	return b.Svc.Snapshot()
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

func (b ServiceBackend) Pair(context.Context) (control.Pairing, error) {
	return b.Svc.PairClient()
}

func (b ServiceBackend) SetEnv(_ context.Context, name, value string) (string, error) {
	return b.Svc.SetEnv(name, value)
}

func (b ServiceBackend) DeleteEnv(_ context.Context, name string) error {
	return b.Svc.DeleteEnv(name)
}

func (b ServiceBackend) AddToken(_ context.Context, comment string) (control.TokenView, error) {
	return b.Svc.AddToken(comment, 0)
}

func (b ServiceBackend) RemoveToken(_ context.Context, id int64) error {
	return b.Svc.RemoveToken(id)
}
