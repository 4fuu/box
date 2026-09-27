package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) View() string {
	if m.mode == modePair {
		return m.viewPair()
	}
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteByte('\n')
	switch m.screen {
	case screenPending:
		b.WriteString(m.viewPending())
	case screenPortals:
		b.WriteString(m.viewPortals())
	case screenKeys:
		b.WriteString(m.viewKeys())
	case screenEnv:
		b.WriteString(m.viewEnv())
	default:
		b.WriteString(m.viewComputers())
	}
	if form := m.viewForm(); form != "" {
		b.WriteByte('\n')
		b.WriteString(form)
	}
	b.WriteByte('\n')
	if m.status != "" {
		style := m.styles.dim
		if strings.Contains(m.status, "not") || strings.Contains(m.status, "invalid") || strings.Contains(m.status, "match") || strings.Contains(m.status, "error") || strings.Contains(m.status, "unknown") {
			style = m.styles.bad
		}
		b.WriteString(style.Render(m.status))
		b.WriteByte('\n')
	}
	b.WriteString(m.styles.dim.Render(m.hint()))
	b.WriteByte('\n')
	return b.String()
}

func (m *model) viewHeader() string {
	title := "box"
	if m.snap.Domain != "" {
		title += "  " + m.snap.Domain
	}
	left := m.styles.title.Render(title)
	var tabs []string
	names := []string{"computers", "pending", "portals", "keys", "env"}
	for i, name := range names {
		label := name
		if i == int(screenPending) && len(m.snap.Pending) > 0 {
			label = fmt.Sprintf("pending(%d)", len(m.snap.Pending))
		}
		if screen(i) == m.screen {
			label = m.styles.tab.Render(label)
		}
		tabs = append(tabs, label)
	}
	line := left + "  " + strings.Join(tabs, "  ")
	if m.identity != "" {
		line += "  " + m.styles.dim.Render(m.identity)
	}
	return line
}

func (m *model) viewComputers() string {
	rows := make([][]string, 0, len(m.snap.Computers))
	for _, c := range m.snap.Computers {
		online := m.styles.off.Render("no")
		if c.Online {
			online = m.styles.on.Render("yes")
		}
		rows = append(rows, []string{
			c.Name, online, c.User, c.Address, c.AgentVersion, strings.Join(c.Portals, ","),
		})
	}
	return m.table(
		[]string{"NAME", "ONLINE", "USER", "ADDRESS", "AGENT", "PORTALS"},
		rows,
	)
}

func (m *model) viewPending() string {
	var b strings.Builder
	rows := make([][]string, 0, len(m.snap.Pending))
	for _, p := range m.snap.Pending {
		exp := ""
		if !p.ExpiresAt.IsZero() {
			exp = p.ExpiresAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, []string{p.Name, p.Address, p.User, exp})
	}
	b.WriteString(m.table([]string{"NAME", "ADDRESS", "USER", "EXPIRES"}, rows))
	b.WriteByte('\n')
	b.WriteString(m.styles.header.Render("Approval"))
	b.WriteByte('\n')
	code := ""
	if m.mode == modeApprove {
		code = m.input
	}
	b.WriteString("code: " + code + "\n")
	b.WriteString("type the code, then enter to approve\n")
	return b.String()
}

func (m *model) viewPortals() string {
	rows := make([][]string, 0, len(m.snap.Portals))
	for _, p := range m.snap.Portals {
		rows = append(rows, []string{p.Label, p.Computer, fmt.Sprintf("%d", p.Port), p.Host})
	}
	return m.table([]string{"LABEL", "COMPUTER", "PORT", "HOST"}, rows)
}

func (m *model) viewKeys() string {
	rows := make([][]string, 0, len(m.snap.Keys))
	for _, k := range m.snap.Keys {
		when := ""
		if !k.BoundAt.IsZero() {
			when = k.BoundAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, []string{k.Fingerprint, k.Comment, when})
	}
	return m.table([]string{"FINGERPRINT", "COMMENT", "BOUND"}, rows)
}

func (m *model) viewEnv() string {
	var b strings.Builder
	b.WriteString(m.styles.dim.Render("names only"))
	b.WriteByte('\n')
	rows := make([][]string, 0, len(m.snap.Env))
	for _, name := range m.snap.Env {
		rows = append(rows, []string{name})
	}
	b.WriteString(m.table([]string{"NAME"}, rows))
	return b.String()
}

func (m *model) viewForm() string {
	switch m.mode {
	case modeRemove:
		name := m.computerName()
		return "remove " + name + "\ntype the name again: " + m.input
	case modeRename:
		return "rename " + m.computerName() + "\nnew name: " + m.input
	case modeEnvName:
		return "env set\nname: " + m.input
	case modeEnvValue:
		return "env set " + m.envName + "\nvalue: " + m.input
	case modeKeyRm:
		return "remove key " + m.target + "? y/n"
	case modeEnvRm:
		return "remove env " + m.target + "? y/n"
	default:
		return ""
	}
}

func (m *model) viewPair() string {
	var b strings.Builder
	b.WriteString(m.styles.header.Render("One-time password"))
	b.WriteString("\n\n")
	b.WriteString(m.styles.secret.Render(m.secret))
	b.WriteByte('\n')
	if !m.exp.IsZero() {
		b.WriteString("expires: " + m.exp.UTC().Format(time.RFC3339) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.warn.Render("Shown once. Any key hides it."))
	b.WriteByte('\n')
	return b.String()
}

func (m *model) hint() string {
	if m.mode != modeNormal && m.mode != modePair {
		return "enter confirms · esc cancels"
	}
	base := "1-5 screens · j/k move · p pair · q quit"
	switch m.screen {
	case screenComputers:
		extra := " · r remove · n rename"
		if m.allowShell {
			extra = " · enter shell" + extra
		}
		return base + extra
	case screenPending:
		return base + " · enter approve"
	case screenKeys:
		return base + " · x remove key"
	case screenEnv:
		return base + " · a set · x remove"
	default:
		return base
	}
}

func (m *model) table(headers []string, rows [][]string) string {
	if len(rows) == 0 {
		return m.styles.header.Render(m.rowLine(headers, nil)) + "\n" + m.styles.dim.Render("none") + "\n"
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				break
			}
			if n := lipgloss.Width(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	var b strings.Builder
	b.WriteString(m.styles.header.Render(m.rowLine(headers, widths)))
	b.WriteByte('\n')
	for i, row := range rows {
		line := m.rowLine(row, widths)
		if i == m.idx() {
			line = m.styles.selected.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) rowLine(cols []string, widths []int) string {
	var b strings.Builder
	for i, c := range cols {
		cell := c
		if i < len(widths) {
			cell = pad(cell, widths[i])
		}
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(cell)
	}
	line := strings.TrimRight(b.String(), " ")
	if m.width > 0 && lipgloss.Width(line) > m.width {
		line = trunc(line, m.width)
	}
	return line
}

func pad(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func trunc(s string, n int) string {
	if n <= 0 || lipgloss.Width(s) <= n {
		return s
	}
	var b strings.Builder
	w := 0
	// ANSI sequences are passed through so a colored cell is not cut mid-code.
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := rune(s[i]), 1
		if s[i] >= 0x80 {
			r, size = decodeRune(s[i:])
		}
		rw := lipgloss.Width(string(r))
		if w+rw > n {
			break
		}
		b.WriteRune(r)
		w += rw
		i += size
	}
	return b.String()
}

func decodeRune(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	if size <= 0 {
		return utf8.RuneError, 1
	}
	return r, size
}
