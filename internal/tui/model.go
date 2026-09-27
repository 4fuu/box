package tui

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/box/internal/control"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const refreshEvery = 2 * time.Second

type screen int

const (
	screenComputers screen = iota
	screenPending
	screenPortals
	screenKeys
	screenEnv
	screenTokens
	screenCount
)

type mode int

const (
	modeNormal mode = iota
	modeApprove
	modeRemove
	modeRename
	modeEnvName
	modeEnvValue
	modeKeyRm
	modeEnvRm
	modeTokenAdd
	modeTokenRm
	modePair
	modeHelp
)

type styles struct {
	title, header, selected      lipgloss.Style
	tabOn, tabOff                lipgloss.Style
	on, off, warn, bad, dim      lipgloss.Style
	secret                       lipgloss.Style
	ptitle, border, cursor       lipgloss.Style
	bar, barDim, barBad, barGood lipgloss.Style
}

type model struct {
	b          Backend
	ctx        context.Context
	styles     styles
	allowShell bool
	identity   string
	status     string
	width      int
	height     int
	// theme is "dark" or "light". styleOut and styleTerm rebuild styles
	// when the theme flips. themeBusy is set while a toggle is being
	// stored, so a snapshot cannot flip the styles back mid-flight.
	theme     string
	themeBusy bool
	styleOut  io.Writer
	styleTerm string

	snap    control.Snapshot
	screen  screen
	cursor  int
	mode    mode
	input   string
	envName string
	// target is the key fingerprint or env name captured when a confirm
	// dialog opens. A later snapshot must not change what y deletes.
	target string
	// secret is the one-time password. It is cleared on the next key.
	secret string
	exp    time.Time
	busy   bool
	shell  string
}

type snapMsg struct {
	snap control.Snapshot
	err  error
}

type doneMsg struct {
	text string
	err  error
}

type pairMsg struct {
	pairing control.Pairing
	err     error
}

type tickMsg time.Time

type themeMsg struct{ err error }

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.load(), tick())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	case snapMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		m.snap = msg.snap
		if !m.themeBusy && msg.snap.Theme != "" && msg.snap.Theme != m.theme {
			m.setTheme(msg.snap.Theme)
		}
		m.clamp()
		return m, nil
	case doneMsg:
		m.busy = false
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		if msg.text != "" {
			m.status = msg.text
		}
		return m, m.load()
	case pairMsg:
		m.busy = false
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		m.mode = modePair
		m.secret = msg.pairing.Secret
		m.exp = msg.pairing.Expires
		m.status = ""
		return m, nil
	case tickMsg:
		if m.busy {
			return m, tick()
		}
		return m, tea.Batch(m.load(), tick())
	case themeMsg:
		m.themeBusy = false
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		return m, nil
	}
	return m, nil
}

// setTheme swaps the palette. It does not store the choice; storeTheme does.
func (m *model) setTheme(theme string) {
	if theme != control.ThemeLight {
		theme = control.ThemeDark
	}
	m.theme = theme
	m.styles = newStyles(m.styleOut, m.styleTerm, theme)
}

func (m *model) storeTheme(theme string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		if b == nil {
			return themeMsg{}
		}
		return themeMsg{err: b.SetTheme(ctx, theme)}
	}
}

func (m *model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		m.clearSensitive()
		m.shell = ""
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	switch m.mode {
	case modePair:
		m.secret = ""
		m.exp = time.Time{}
		m.mode = modeNormal
		return m, nil
	case modeHelp:
		m.mode = modeNormal
		return m, nil
	case modeApprove, modeRemove, modeRename, modeEnvName, modeEnvValue, modeTokenAdd:
		return m.onInput(k)
	case modeKeyRm, modeEnvRm, modeTokenRm:
		return m.onConfirm(k)
	default:
		return m.onNormal(k)
	}
}

func (m *model) onNormal(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		m.clearSensitive()
		m.shell = ""
		return m, tea.Quit
	case "tab", "right":
		m.screen = (m.screen + 1) % screenCount
		m.cursor = 0
		return m, nil
	case "shift+tab", "left":
		m.screen = (m.screen + screenCount - 1) % screenCount
		m.cursor = 0
		return m, nil
	case "1", "2", "3", "4", "5", "6":
		n := int(k.String()[0] - '1')
		if n >= int(screenCount) {
			return m, nil
		}
		m.screen = screen(n)
		m.cursor = 0
		return m, nil
	case "?":
		m.mode = modeHelp
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor+1 < m.count() {
			m.cursor++
		}
		return m, nil
	case "p":
		m.busy = true
		m.status = "pairing…"
		return m, m.pair()
	case "t":
		next := control.ThemeLight
		if m.theme == control.ThemeLight {
			next = control.ThemeDark
		}
		m.setTheme(next)
		m.themeBusy = true
		m.status = "theme " + next
		return m, m.storeTheme(next)
	case "enter":
		if m.screen == screenComputers && m.allowShell {
			name := m.computerName()
			if name == "" {
				return m, nil
			}
			m.clearSensitive()
			m.shell = name
			return m, tea.Quit
		}
		if m.screen == screenPending {
			m.mode = modeApprove
			m.input = ""
			return m, nil
		}
	case "a":
		switch m.screen {
		case screenPending:
			m.mode = modeApprove
			m.input = ""
			return m, nil
		case screenEnv:
			m.mode = modeEnvName
			m.input = ""
			m.envName = ""
			return m, nil
		case screenTokens:
			m.mode = modeTokenAdd
			m.input = ""
			return m, nil
		}
	case "r":
		if m.screen == screenComputers && m.computerName() != "" {
			m.mode = modeRemove
			m.input = ""
			return m, nil
		}
	case "n":
		if m.screen == screenComputers && m.computerName() != "" {
			m.mode = modeRename
			m.input = ""
			return m, nil
		}
	case "x", "d":
		if m.screen == screenKeys {
			fp := m.keyFingerprint()
			if fp == "" {
				return m, nil
			}
			m.target = fp
			m.mode = modeKeyRm
			return m, nil
		}
		if m.screen == screenEnv {
			name := m.envAtCursor()
			if name == "" {
				return m, nil
			}
			m.target = name
			m.mode = modeEnvRm
			return m, nil
		}
		if m.screen == screenTokens {
			tok, ok := m.tokenAtCursor()
			if !ok {
				return m, nil
			}
			m.target = strconv.FormatInt(tok.ID, 10)
			m.mode = modeTokenRm
			return m, nil
		}
	}
	return m, nil
}

func (m *model) onInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNormal
		m.input = ""
		m.envName = ""
		return m, nil
	case "enter":
		return m.submit()
	case "backspace", "ctrl+h":
		m.input = dropRune(m.input)
		return m, nil
	}
	if k.Type == tea.KeyRunes {
		m.input += string(k.Runes)
	}
	return m, nil
}

func (m *model) onConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch strings.ToLower(k.String()) {
	case "y":
		return m.confirmYes()
	case "n", "esc":
		m.mode = modeNormal
		m.target = ""
		return m, nil
	}
	return m, nil
}

func (m *model) submit() (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeApprove:
		code := strings.TrimSpace(m.input)
		m.input = ""
		m.mode = modeNormal
		if code == "" {
			m.status = "type the approval code"
			return m, nil
		}
		m.busy = true
		return m, m.approve(code)
	case modeRemove:
		typed := strings.TrimSpace(m.input)
		name := m.computerName()
		m.input = ""
		m.mode = modeNormal
		if typed != name || name == "" {
			m.status = "name did not match"
			return m, nil
		}
		m.busy = true
		return m, m.remove(name)
	case modeRename:
		next := strings.TrimSpace(m.input)
		old := m.computerName()
		m.input = ""
		m.mode = modeNormal
		if next == "" || old == "" {
			m.status = "usage: rename <name> <new>"
			return m, nil
		}
		m.busy = true
		return m, m.rename(old, next)
	case modeEnvName:
		name := strings.TrimSpace(m.input)
		m.input = ""
		if name == "" {
			m.mode = modeNormal
			return m, nil
		}
		m.envName = name
		m.mode = modeEnvValue
		return m, nil
	case modeEnvValue:
		name := m.envName
		value := m.input
		m.input = ""
		m.envName = ""
		m.mode = modeNormal
		if name == "" {
			return m, nil
		}
		m.busy = true
		return m, m.setEnv(name, value)
	case modeTokenAdd:
		comment := strings.TrimSpace(m.input)
		m.input = ""
		m.mode = modeNormal
		m.busy = true
		return m, m.addToken(comment)
	default:
		return m, nil
	}
}

func (m *model) confirmYes() (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeKeyRm:
		fp := m.target
		m.target = ""
		m.mode = modeNormal
		if fp == "" {
			return m, nil
		}
		m.busy = true
		return m, m.removeKey(fp)
	case modeEnvRm:
		name := m.target
		m.target = ""
		m.mode = modeNormal
		if name == "" {
			return m, nil
		}
		m.busy = true
		return m, m.deleteEnv(name)
	case modeTokenRm:
		raw := m.target
		m.target = ""
		m.mode = modeNormal
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return m, nil
		}
		m.busy = true
		return m, m.removeToken(id)
	default:
		m.mode = modeNormal
		m.target = ""
		return m, nil
	}
}

func (m *model) load() tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		snap, err := b.Snapshot(ctx)
		return snapMsg{snap: snap, err: err}
	}
}

func (m *model) pair() tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		p, err := b.Pair(ctx)
		return pairMsg{pairing: p, err: err}
	}
}

func (m *model) approve(code string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		text, err := b.Approve(ctx, code)
		return doneMsg{text: text, err: err}
	}
}

func (m *model) remove(name string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		err := b.Remove(ctx, name)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "removed " + name}
	}
}

func (m *model) rename(oldName, newName string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		err := b.Rename(ctx, oldName, newName)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "renamed " + oldName + " to " + newName}
	}
}

func (m *model) removeKey(fp string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		err := b.RemoveKey(ctx, fp)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "removed key"}
	}
}

func (m *model) setEnv(name, value string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		text, err := b.SetEnv(ctx, name, value)
		return doneMsg{text: text, err: err}
	}
}

func (m *model) addToken(comment string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		if _, err := b.AddToken(ctx, comment); err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "added token"}
	}
}

func (m *model) removeToken(id int64) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		if err := b.RemoveToken(ctx, id); err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "removed token"}
	}
}

func (m *model) deleteEnv(name string) tea.Cmd {
	b, ctx := m.b, m.ctx
	return func() tea.Msg {
		err := b.DeleteEnv(ctx, name)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "removed " + name}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) clearSensitive() {
	m.input = ""
	m.envName = ""
	m.target = ""
	m.secret = ""
	m.exp = time.Time{}
}

func (m *model) count() int {
	switch m.screen {
	case screenComputers:
		return len(m.snap.Computers)
	case screenPending:
		return len(m.snap.Pending)
	case screenPortals:
		return len(m.snap.Portals)
	case screenKeys:
		return len(m.snap.Keys)
	case screenEnv:
		return len(m.snap.Env)
	case screenTokens:
		return len(m.snap.Tokens)
	default:
		return 0
	}
}

func (m *model) clamp() {
	n := m.count()
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

func (m *model) idx() int {
	n := m.count()
	if n == 0 {
		return -1
	}
	if m.cursor < 0 || m.cursor >= n {
		return 0
	}
	return m.cursor
}

func (m *model) computerName() string {
	i := m.idx()
	if m.screen != screenComputers || i < 0 {
		return ""
	}
	return m.snap.Computers[i].Name
}

func (m *model) keyFingerprint() string {
	i := m.idx()
	if m.screen != screenKeys || i < 0 {
		return ""
	}
	return m.snap.Keys[i].Fingerprint
}

func (m *model) tokenAtCursor() (control.TokenView, bool) {
	i := m.idx()
	if m.screen != screenTokens || i < 0 {
		return control.TokenView{}, false
	}
	return m.snap.Tokens[i], true
}

func (m *model) envAtCursor() string {
	i := m.idx()
	if m.screen != screenEnv || i < 0 {
		return ""
	}
	return m.snap.Env[i]
}

func dropRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[:len(r)-1])
}
