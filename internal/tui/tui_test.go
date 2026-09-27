package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/control"
	tea "github.com/charmbracelet/bubbletea"
)

func TestModelRendersTableAndApproval(t *testing.T) {
	m := &model{
		width: 120,
		snap: control.Snapshot{
			Domain: "box.example.com",
			Computers: []control.ComputerView{{
				Name: "home", Online: true, User: "alice",
				Address: "10.0.0.2:1", AgentVersion: "1.0",
				Portals: []string{"web"},
			}},
			Pending: []control.PendingView{{
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
		snap: control.Snapshot{Computers: []control.ComputerView{{
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

func TestEnterSelectsThatComputer(t *testing.T) {
	m := &model{
		allowShell: true,
		cursor:     1,
		snap: control.Snapshot{Computers: []control.ComputerView{
			{Name: "home"},
			{Name: "work"},
		}},
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)
	if cmd == nil {
		t.Fatal("enter did not quit into a shell")
	}
	if m.shell != "work" {
		t.Fatalf("shell %q, want the selected row", m.shell)
	}
}

func TestConfirmKeepsCapturedTarget(t *testing.T) {
	f := &fakeBackend{}
	m := &model{
		b:      f,
		screen: screenKeys,
		snap: control.Snapshot{Keys: []control.KeyView{
			{Fingerprint: "SHA256:one"},
			{Fingerprint: "SHA256:two"},
		}},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*model)
	if m.target != "SHA256:one" || m.mode != modeKeyRm {
		t.Fatalf("target %q mode %d", m.target, m.mode)
	}
	// A refresh puts a different key on the same row. y must still delete the
	// fingerprint captured when the dialog opened.
	m.snap.Keys = []control.KeyView{{Fingerprint: "SHA256:other"}}
	if !strings.Contains(m.View(), "SHA256:one") {
		t.Fatal(m.View())
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*model)
	if cmd == nil {
		t.Fatal("confirm did not run")
	}
	_ = cmd()
	if f.removedKey != "SHA256:one" {
		t.Fatalf("removed %q", f.removedKey)
	}

	f.removedEnv = ""
	m.busy = false
	m.mode = modeNormal
	m.screen = screenEnv
	m.cursor = 0
	m.snap.Env = []string{"TOKEN", "PATH"}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*model)
	if m.target != "TOKEN" {
		t.Fatalf("env target %q", m.target)
	}
	m.snap.Env = []string{"OTHER"}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*model)
	if cmd == nil {
		t.Fatal("env confirm did not run")
	}
	_ = cmd()
	if f.removedEnv != "TOKEN" {
		t.Fatalf("removed env %q", f.removedEnv)
	}
}

func TestInitialWindowSizeErases(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Run(Config{
		Context: ctx,
		In:      strings.NewReader("q"),
		Out:     &buf,
		Backend: &fakeBackend{},
		Width:   80,
		Height:  24,
		Term:    "xterm",
	})
	if err != nil {
		t.Fatal(err)
	}
	// bubbletea appends erase-to-end-of-line only after WindowSizeMsg sets
	// the renderer width. Width 0 leaves a cleared password on screen.
	if !bytes.Contains(buf.Bytes(), []byte("\x1b[K")) {
		t.Fatalf("renderer width was still 0; output %q", buf.String())
	}
}

func TestTokenStaysVisible(t *testing.T) {
	const secret = "tokensecretTokensecretTokensecret12"
	m := &model{
		screen: screenTokens,
		snap: control.Snapshot{Tokens: []control.TokenView{{
			ID: 7, Token: secret, Comment: "door",
			Expires: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		}}},
	}
	if !strings.Contains(m.View(), secret) {
		t.Fatalf("token hidden:\n%s", m.View())
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*model)
	if !strings.Contains(m.View(), secret) || strings.Contains(m.View(), "Shown once") {
		t.Fatalf("token did not stay:\n%s", m.View())
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
	removed    string
	removedKey string
	removedEnv string
}

func (f *fakeBackend) Snapshot(context.Context) (control.Snapshot, error) {
	return control.Snapshot{}, nil
}
func (f *fakeBackend) Approve(context.Context, string) (string, error) { return "", nil }
func (f *fakeBackend) Remove(_ context.Context, name string) error {
	f.removed = name
	return nil
}
func (f *fakeBackend) Rename(context.Context, string, string) error { return nil }
func (f *fakeBackend) RemoveKey(_ context.Context, fp string) error {
	f.removedKey = fp
	return nil
}
func (f *fakeBackend) Pair(context.Context) (control.Pairing, error) {
	return control.Pairing{}, nil
}
func (f *fakeBackend) SetEnv(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeBackend) DeleteEnv(_ context.Context, name string) error {
	f.removedEnv = name
	return nil
}
func (f *fakeBackend) AddToken(context.Context, string) (control.TokenView, error) {
	return control.TokenView{}, nil
}
func (f *fakeBackend) RemoveToken(context.Context, int64) error { return nil }
