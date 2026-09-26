package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelRendersTableAndApproval(t *testing.T) {
	m := &model{
		width: 120,
		snap: Snapshot{
			Domain: "box.example.com",
			Computers: []Computer{{
				Name: "home", Online: true, User: "alice",
				Address: "10.0.0.2:1", AgentVersion: "1.0",
				Portals: []string{"web"},
			}},
			Pending: []Pending{{
				Name: "laptop", Address: "10.0.0.3:9", User: "bob",
				ExpiresAt: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
			}},
		},
	}
	table := m.View()
	for _, want := range []string{"NAME", "ONLINE", "USER", "ADDRESS", "AGENT", "PORTALS", "home", "alice", "10.0.0.2:1", "1.0", "web", "yes"} {
		if !strings.Contains(table, want) {
			t.Fatalf("computer table missing %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "a3Kf9Q") {
		t.Fatal("approval code rendered")
	}
	m.screen = screenPending
	form := m.View()
	for _, want := range []string{"laptop", "bob", "Approval", "code:", "enter to approve"} {
		if !strings.Contains(form, want) {
			t.Fatalf("approval form missing %q:\n%s", want, form)
		}
	}
}

func TestRemoveAsksForTheName(t *testing.T) {
	f := &fakeBackend{}
	m := &model{
		b: f,
		snap: Snapshot{Computers: []Computer{{
			Name: "home", User: "alice",
		}}},
		mode: modeRemove,
	}
	m.input = "nope"
	next, cmd := m.submit()
	m = next.(*model)
	if cmd != nil {
		t.Fatal("mismatch ran a command")
	}
	if f.removed != "" {
		t.Fatal("removed without the name")
	}
	if !strings.Contains(m.status, "did not match") {
		t.Fatalf("status %q", m.status)
	}
	if !strings.Contains(m.View(), "did not match") {
		t.Fatal(m.View())
	}

	m.mode = modeRemove
	m.input = "home"
	next, cmd = m.submit()
	m = next.(*model)
	if cmd == nil {
		t.Fatal("match did not remove")
	}
	msg := cmd()
	updated, _ := m.Update(msg)
	m = updated.(*model)
	if f.removed != "home" {
		t.Fatalf("removed %q", f.removed)
	}
	if strings.Contains(m.View(), "type the name again") {
		t.Fatal("confirm form still open")
	}
}

func TestPairSecretShownOnce(t *testing.T) {
	const secret = "one-time-secret"
	m := &model{
		mode:   modePair,
		secret: secret,
		exp:    time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
	}
	if !strings.Contains(m.View(), secret) {
		t.Fatal("password not shown")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*model)
	if m.secret != "" || strings.Contains(m.View(), secret) {
		t.Fatal("password lingered after a key")
	}
}

type fakeBackend struct {
	removed string
}

func (f *fakeBackend) Snapshot(context.Context) (Snapshot, error) { return Snapshot{}, nil }
func (f *fakeBackend) Approve(context.Context, string) (string, error) {
	return "", nil
}
func (f *fakeBackend) Remove(_ context.Context, name string) error {
	f.removed = name
	return nil
}
func (f *fakeBackend) Rename(context.Context, string, string) error { return nil }
func (f *fakeBackend) RemoveKey(context.Context, string) error      { return nil }
func (f *fakeBackend) Pair(context.Context) (Pairing, error)        { return Pairing{}, nil }
func (f *fakeBackend) SetEnv(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeBackend) DeleteEnv(context.Context, string) error { return nil }
