package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Below this size the layout cannot survive, so a warning replaces it.
// Between this and a full-width terminal, tabs shorten, low-priority table
// columns drop out, and cards stack in one column.
const (
	minWidth  = 30
	minHeight = 10
)

var screenNames = []string{"summary", "computers", "pending", "portals", "keys", "env", "tokens"}

func (m *model) View() string {
	if m.width > 0 && m.height > 0 {
		if m.width < minWidth || m.height < minHeight {
			return m.viewTooSmall()
		}
		return m.viewSized()
	}
	// Size unknown (tests, unusual clients): one stacked layout, no overlay.
	if m.mode == modePair {
		return m.viewPair()
	}
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteString(m.viewTabs())
	b.WriteString(m.viewBody())
	if form := m.viewForm(); form != "" {
		b.WriteByte('\n')
		b.WriteString(form)
	}
	b.WriteByte('\n')
	if m.status != "" {
		b.WriteString(m.statusStyle().Render(m.status))
		b.WriteByte('\n')
	}
	b.WriteString(m.styles.dim.Render(m.hint()))
	b.WriteByte('\n')
	return b.String()
}

// viewSized is the framed layout: header, tabs, bordered pane, status bar,
// with modals centered over it.
func (m *model) viewSized() string {
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteString(m.viewTabs())
	b.WriteString(m.viewBody())
	b.WriteString(m.viewBar())
	out := m.padToHeight(b.String())
	if box := m.viewModal(); box != "" {
		out = overlay(out, box, m.width, m.height)
	}
	return out
}

// viewBody is the summary cards, or the framed pane every other screen uses.
func (m *model) viewBody() string {
	if m.screen == screenSummary {
		return m.viewSummary()
	}
	return m.viewPane()
}

func (m *model) viewHeader() string {
	line := m.styles.title.Render("box")
	if m.snap.Domain != "" {
		domain := "  " + m.snap.Domain
		if m.width > 0 {
			domain = trunc(domain, m.width-4)
		}
		line += m.styles.dim.Render(domain)
	}
	if m.identity != "" {
		right := m.styles.dim.Render(m.identity)
		if m.width > 0 {
			if gap := m.width - lipgloss.Width(line) - lipgloss.Width(right) - 1; gap > 0 {
				line += strings.Repeat(" ", gap) + right
			}
		} else {
			line += "  " + right
		}
	}
	return line + "\n"
}

// viewTabs draws the screen row at the widest form that fits: every name;
// then the current name with the others as their number keys; then only
// the current name and its position.
func (m *model) viewTabs() string {
	pending := len(m.snap.Pending)
	label := func(i int, short bool) string {
		name := screenNames[i]
		if short && screen(i) != m.screen {
			name = fmt.Sprint(i + 1)
		}
		if i == int(screenPending) && pending > 0 {
			if short && screen(i) != m.screen {
				return name + "!"
			}
			name = fmt.Sprintf("%s(%d)", name, pending)
		}
		return name
	}
	row := func(short bool) (string, int) {
		segs := make([]string, 0, len(screenNames))
		w := 0
		for i := range screenNames {
			l := " " + label(i, short) + " "
			if i == 0 {
				// Let the row start in column 0; the first tab loses its left pad.
				l = l[1:]
			}
			w += lipgloss.Width(l)
			switch {
			case screen(i) == m.screen:
				segs = append(segs, m.styles.tabOn.Render(l))
			case i == int(screenPending) && pending > 0:
				segs = append(segs, m.styles.warn.Render(l))
			default:
				segs = append(segs, m.styles.tabOff.Render(l))
			}
		}
		return strings.Join(segs, " "), w + len(segs) - 1
	}
	for _, short := range []bool{false, true} {
		if line, w := row(short); m.width <= 0 || w <= m.width {
			return line + "\n"
		}
	}
	cur := label(int(m.screen), false)
	pos := m.styles.dim.Render(fmt.Sprintf(" %d/%d", int(m.screen)+1, len(screenNames)))
	line := m.styles.tabOn.Render(" "+cur+" ") + pos
	if pending > 0 && m.screen != screenPending {
		line += m.styles.warn.Render(fmt.Sprintf(" · %d pending", pending))
	}
	return trunc(line, m.width) + "\n"
}

func (m *model) viewPane() string {
	var body string
	switch m.screen {
	case screenPending:
		body = m.viewPending()
	case screenPortals:
		body = m.viewPortals()
	case screenKeys:
		body = m.viewKeys()
	case screenEnv:
		body = m.viewEnv()
	case screenTokens:
		body = m.viewTokens()
	default:
		body = m.viewComputers()
	}
	return m.pane(m.paneTitle(), body)
}

func (m *model) paneTitle() string {
	start, end, n := m.window()
	name := screenNames[m.screen]
	switch {
	case n == 0:
		return name
	case end-start < n:
		return fmt.Sprintf("%s · %d-%d/%d", name, start+1, end, n)
	default:
		return fmt.Sprintf("%s · %d", name, n)
	}
}

// pane wraps body in a rounded border with the title in the top edge.
func (m *model) pane(title, body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	inner := lipgloss.Width(title) + 1
	for _, l := range lines {
		if w := lipgloss.Width(l); w > inner {
			inner = w
		}
	}
	if m.width > 0 {
		inner = m.width - 4 // stretch to the terminal width
	}
	if tw := lipgloss.Width(title); inner < tw+1 {
		inner = tw + 1
	}
	border := m.styles.border
	dash := inner - lipgloss.Width(title) - 1
	if dash < 1 {
		dash = 1
	}
	var b strings.Builder
	b.WriteString(border.Render("╭─ ") + m.styles.ptitle.Render(title) + border.Render(" "+strings.Repeat("─", dash)+"╮"))
	b.WriteByte('\n')
	for _, l := range lines {
		b.WriteString(border.Render("│ ") + pad(trunc(l, inner), inner) + border.Render(" │"))
		b.WriteByte('\n')
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", inner+2) + "╯"))
	b.WriteByte('\n')
	return b.String()
}

// viewBar is the status line: status left, key hints right. Hints degrade
// by priority when the width runs out: screen-specific, global, minimal.
func (m *model) viewBar() string {
	left, lw := "", 0
	if m.status != "" {
		left = m.barStatusStyle().Render(" " + m.status)
		lw = 1 + lipgloss.Width(m.status)
	}
	hints := []string{m.hint()}
	if hints[0] != hintBase {
		hints = append(hints, hintBase)
	}
	hints = append(hints, "? help")
	right, rw := "", 0
	for _, h := range hints {
		if m.width-lw-lipgloss.Width(h)-2 >= 0 {
			right = m.styles.barDim.Render(" " + h + " ")
			rw = lipgloss.Width(h) + 2
			break
		}
	}
	if m.width-lw-rw < 0 && m.status != "" {
		s := trunc(m.status, m.width-1)
		left = m.barStatusStyle().Render(" " + s)
		lw = 1 + lipgloss.Width(s)
	}
	gap := m.width - lw - rw
	if gap < 0 {
		gap = 0
	}
	return left + m.styles.bar.Render(strings.Repeat(" ", gap)) + right + "\n"
}

func (m *model) viewComputers() string {
	rows := make([][]string, 0, len(m.snap.Computers))
	for _, c := range m.snap.Computers {
		online := "no"
		if c.Online {
			online = "yes"
		}
		rows = append(rows, []string{
			c.Name, online, c.User, c.Address, c.AgentVersion, strings.Join(c.Portals, ","),
		})
	}
	onlineColor := func(col int, val string) (lipgloss.Style, bool) {
		if col != 1 {
			return lipgloss.Style{}, false
		}
		if val == "yes" {
			return m.styles.on, true
		}
		return m.styles.off, true
	}
	return m.table(
		[]column{{"NAME", 0}, {"ONLINE", 1}, {"USER", 3}, {"ADDRESS", 4}, {"AGENT", 5}, {"PORTALS", 2}},
		rows,
		"no computers yet - press p to pair one",
		onlineColor,
	)
}

func (m *model) viewPending() string {
	var b strings.Builder
	rows := make([][]string, 0, len(m.snap.Pending))
	for _, p := range m.snap.Pending {
		exp := ""
		if !p.ExpiresAt.IsZero() {
			exp = "in " + dur(time.Until(p.ExpiresAt))
		}
		rows = append(rows, []string{p.Name, p.Address, p.User, exp})
	}
	b.WriteString(m.table([]column{{"NAME", 0}, {"ADDRESS", 2}, {"USER", 3}, {"EXPIRES", 1}}, rows, "no pending approvals", nil))
	code := ""
	if m.mode == modeApprove {
		code = m.input + m.styles.cursor.Render("▌")
	}
	if m.shortApproval() {
		b.WriteString(m.styles.header.Render("approval code: ") + code + "\n")
		return b.String()
	}
	b.WriteByte('\n')
	b.WriteString(m.styles.header.Render("Approval"))
	b.WriteByte('\n')
	b.WriteString("code: " + code + "\n")
	b.WriteString(m.styles.dim.Render("type the code, then enter to approve"))
	b.WriteByte('\n')
	return b.String()
}

// shortApproval folds the approval form into one line on a short terminal,
// so the pending rows keep the space.
func (m *model) shortApproval() bool {
	return m.height > 0 && m.height < 16
}

func (m *model) viewPortals() string {
	rows := make([][]string, 0, len(m.snap.Portals))
	for _, p := range m.snap.Portals {
		access := "public"
		if p.Private {
			access = "private"
		}
		rows = append(rows, []string{p.Label, p.Computer, fmt.Sprintf("%d", p.Port), access, p.Host})
	}
	return m.table([]column{{"LABEL", 0}, {"COMPUTER", 1}, {"PORT", 2}, {"ACCESS", 3}, {"HOST", 4}}, rows, "no portals", nil)
}

func (m *model) viewKeys() string {
	rows := make([][]string, 0, len(m.snap.Keys))
	for _, k := range m.snap.Keys {
		when := ""
		if !k.BoundAt.IsZero() {
			when = k.BoundAt.UTC().Format(time.DateOnly)
		}
		rows = append(rows, []string{k.Fingerprint, k.Comment, when})
	}
	return m.table([]column{{"FINGERPRINT", 0}, {"COMMENT", 1}, {"BOUND", 2}}, rows, "no keys bound", nil)
}

func (m *model) viewEnv() string {
	var b strings.Builder
	b.WriteString(m.styles.dim.Render("names only"))
	b.WriteByte('\n')
	rows := make([][]string, 0, len(m.snap.Env))
	for _, name := range m.snap.Env {
		rows = append(rows, []string{name})
	}
	b.WriteString(m.table([]column{{"NAME", 0}}, rows, "no env names", nil))
	return b.String()
}

func (m *model) viewTokens() string {
	rows := make([][]string, 0, len(m.snap.Tokens))
	for _, t := range m.snap.Tokens {
		when := "none"
		if !t.Expires.IsZero() {
			when = t.Expires.UTC().Format(time.DateOnly)
		}
		rows = append(rows, []string{fmt.Sprintf("%d", t.ID), t.Comment, when, t.Token})
	}
	var b strings.Builder
	b.WriteString(m.table([]column{{"ID", 0}, {"COMMENT", 1}, {"EXPIRES", 2}, {"TOKEN", 3}}, rows, "no tokens", nil))
	for _, l := range m.tokenLines() {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// tokenLines shows the selected token in full. The operator copies it from
// here, so it wraps onto more lines instead of being cut at the pane edge.
func (m *model) tokenLines() []string {
	tok, ok := m.tokenAtCursor()
	if !ok {
		return nil
	}
	line := "token: " + tok.Token
	inner := m.width - 4
	if m.width <= 0 || lipgloss.Width(line) <= inner {
		return []string{line}
	}
	out := []string{"token:"}
	for r := []rune(tok.Token); len(r) > 0; {
		n := min(inner, len(r))
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}

// viewForm is the inline form used when the terminal size is unknown and a
// modal cannot be placed.
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
	case modeTokenAdd:
		return "token add\ncomment: " + m.input
	case modeTokenRm:
		return "remove token " + m.target + "? y/n"
	default:
		return ""
	}
}

// viewModal renders the active dialog as a centered box; empty when the mode
// has no modal. modeApprove stays inline: approval is the pending screen's
// main widget, not a dialog.
func (m *model) viewModal() string {
	cur := m.styles.cursor.Render("▌")
	dim := m.styles.dim
	switch m.mode {
	case modeRemove:
		return m.modal("remove computer", []string{
			"Remove " + m.computerName() + "?",
			"",
			"type the name again: " + m.input + cur,
			"",
			dim.Render("enter confirms · esc cancels"),
		})
	case modeRename:
		return m.modal("rename computer", []string{
			"rename " + m.computerName(),
			"",
			"new name: " + m.input + cur,
			"",
			dim.Render("enter confirms · esc cancels"),
		})
	case modeEnvName:
		return m.modal("set env", []string{
			"name: " + m.input + cur,
			"",
			dim.Render("enter confirms · esc cancels"),
		})
	case modeEnvValue:
		return m.modal("set env", []string{
			"name:  " + m.envName,
			"value: " + m.input + cur,
			"",
			dim.Render("enter confirms · esc cancels"),
		})
	case modeTokenAdd:
		return m.modal("add token", []string{
			"comment: " + m.input + cur,
			"",
			dim.Render("enter confirms · esc cancels"),
		})
	case modeKeyRm:
		return m.confirmBox("remove key", "remove key "+m.target+"?")
	case modeEnvRm:
		return m.confirmBox("remove env", "remove env "+m.target+"?")
	case modeTokenRm:
		return m.confirmBox("remove token", "remove token "+m.target+"?")
	case modePair:
		lines := []string{"", m.styles.secret.Render(m.secret)}
		if !m.exp.IsZero() {
			lines = append(lines, "", dim.Render("expires: "+m.exp.UTC().Format(time.RFC3339)))
		}
		lines = append(lines, "", m.styles.warn.Render("Shown once. Any key hides it."))
		return m.modal("One-time password", lines)
	case modeHelp:
		return m.modal("help", m.helpLines())
	default:
		return ""
	}
}

func (m *model) confirmBox(title, question string) string {
	return m.modal(title, []string{
		question,
		"",
		m.styles.dim.Render("y confirm · n/esc cancel"),
	})
}

// modal builds a rounded box: ╭─ title ──╮, padded lines, ╰──╯.
func (m *model) modal(title string, lines []string) string {
	inner := lipgloss.Width(title) + 1
	for _, l := range lines {
		if w := lipgloss.Width(l); w > inner {
			inner = w
		}
	}
	if inner < 24 {
		inner = 24
	}
	if m.width > 0 && inner > m.width-6 {
		inner = m.width - 6
	}
	if inner < 8 {
		inner = 8
	}
	border := m.styles.border
	dash := inner - lipgloss.Width(title) - 1
	if dash < 1 {
		dash = 1
	}
	var b strings.Builder
	b.WriteString(border.Render("╭─ ") + m.styles.ptitle.Render(title) + border.Render(" "+strings.Repeat("─", dash)+"╮"))
	b.WriteByte('\n')
	for _, l := range lines {
		// Wrap rather than cut: a modal holds a secret or a name the
		// operator has to read whole.
		for _, part := range strings.Split(ansi.Hardwrap(l, inner, true), "\n") {
			b.WriteString(border.Render("│ ") + pad(part, inner) + border.Render(" │"))
			b.WriteByte('\n')
		}
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", inner+2) + "╯"))
	return b.String()
}

func (m *model) helpLines() []string {
	type binding struct{ key, what string }
	global := []binding{
		{"tab / shift+tab", "switch screen"},
		{"1-7", "jump to a screen"},
		{"j / k", "move, or scroll the summary"},
		{"p", "pair a computer"},
		{"?", "this help"},
		{"q", "quit"},
	}
	var local []binding
	switch m.screen {
	case screenSummary:
		local = []binding{{"j / k", "scroll"}}
	case screenComputers:
		if m.allowShell {
			local = append(local, binding{"enter", "open a shell"})
		}
		local = append(local, binding{"r", "remove"}, binding{"n", "rename"})
	case screenPending:
		local = []binding{{"enter / a", "approve"}}
	case screenKeys:
		local = []binding{{"x", "remove key"}}
	case screenEnv:
		local = []binding{{"a", "set a variable"}, {"x", "remove"}}
	case screenTokens:
		local = []binding{{"a", "add a token"}, {"x", "remove"}}
	}
	w := 0
	for _, b := range append(global, local...) {
		if len(b.key) > w {
			w = len(b.key)
		}
	}
	put := func(bs []binding, lines []string) []string {
		for _, b := range bs {
			lines = append(lines, m.styles.ptitle.Render(pad(b.key, w))+"  "+b.what)
		}
		return lines
	}
	lines := put(global, nil)
	if len(local) > 0 {
		lines = append(lines, "")
		lines = put(local, lines)
	}
	return append(lines, "", m.styles.dim.Render("any key closes"))
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

func (m *model) viewTooSmall() string {
	lines := []string{
		m.styles.warn.Render("terminal too small"),
		m.styles.dim.Render(fmt.Sprintf("need at least %d x %d", minWidth, minHeight)),
	}
	top := (m.height - len(lines)) / 2
	if top < 0 {
		top = 0
	}
	var b strings.Builder
	for i := 0; i < top; i++ {
		b.WriteByte('\n')
	}
	for _, l := range lines {
		left := (m.width - lipgloss.Width(l)) / 2
		if left < 0 {
			left = 0
		}
		b.WriteString(strings.Repeat(" ", left) + l + "\n")
	}
	return b.String()
}

const hintBase = "1-7 screens · j/k move · p pair · ? help · q quit"

func (m *model) hint() string {
	if m.mode != modeNormal && m.mode != modePair && m.mode != modeHelp {
		return "enter confirms · esc cancels"
	}
	base := hintBase
	switch m.screen {
	case screenSummary:
		return "1-7 screens · j/k scroll · p pair · ? help · q quit"
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
	case screenTokens:
		return base + " · a add · x remove"
	default:
		return base
	}
}

// colorize picks a style for one table cell. ok=false means plain.
type colorize func(col int, val string) (style lipgloss.Style, ok bool)

// column is a table header. prio orders what gives way on a narrow
// terminal: the highest prio drops out first, and 0 always stays.
type column struct {
	title string
	prio  int
}

// fitColumns picks which columns show in avail cells and how wide each is.
// When the table is too wide it first caps any one column at two fifths of
// the width, so a long name cannot push every other column out. Then it
// drops the least important column until the rest fit, unless shrinking
// that column would do and still leave it readable. Space left over goes
// back to the capped columns. Cells in a narrowed column end in "…".
func fitColumns(cols []column, natural []int, avail int) (keep []int, widths []int) {
	w := append([]int(nil), natural...)
	keep = make([]int, len(cols))
	for i := range keep {
		keep[i] = i
	}
	total := func() int {
		t := 2 * (len(keep) - 1)
		for _, i := range keep {
			t += w[i]
		}
		return t
	}
	if avail > 0 && total() > avail {
		limit := max(avail*2/5, 8)
		for i := range w {
			if w[i] > limit {
				w[i] = max(limit, lipgloss.Width(cols[i].title))
			}
		}
		for len(keep) > 1 && total() > avail {
			least := 0
			for j, i := range keep {
				if cols[i].prio > cols[keep[least]].prio {
					least = j
				}
			}
			i := keep[least]
			if w[i]-(total()-avail) >= max(6, lipgloss.Width(cols[i].title)) {
				break
			}
			keep = append(keep[:least], keep[least+1:]...)
		}
		for _, i := range keep {
			if slack := avail - total(); slack > 0 && w[i] < natural[i] {
				w[i] += min(slack, natural[i]-w[i])
			}
		}
		if over := total() - avail; over > 0 {
			least := 0
			for j, i := range keep {
				if cols[i].prio > cols[keep[least]].prio {
					least = j
				}
			}
			w[keep[least]] = max(w[keep[least]]-over, 1)
		}
	}
	widths = make([]int, len(keep))
	for j, i := range keep {
		widths[j] = w[i]
	}
	return keep, widths
}

func (m *model) table(cols []column, rows [][]string, empty string, color colorize) string {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(c.title)
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
	avail := 0
	if m.width > 0 {
		avail = m.width - 6 // pane borders and the row marker
	}
	keep, kw := fitColumns(cols, widths, avail)
	pick := func(row []string) []string {
		out := make([]string, len(keep))
		for j, i := range keep {
			if i < len(row) {
				out[j] = ellipsis(row[i], kw[j])
			}
		}
		return out
	}
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.title
	}
	if color != nil {
		inner := color
		color = func(col int, val string) (lipgloss.Style, bool) {
			if col >= len(keep) {
				return lipgloss.Style{}, false
			}
			return inner(keep[col], val)
		}
	}
	widths = kw
	var b strings.Builder
	b.WriteString(m.styles.header.Render("  " + m.rowLine(pick(headers), widths, false, nil)))
	b.WriteByte('\n')
	if len(rows) == 0 {
		b.WriteString(m.styles.dim.Render("  " + empty))
		b.WriteByte('\n')
		return b.String()
	}
	start, end, _ := m.window()
	sel := m.idx()
	for i := start; i < end && i < len(rows); i++ {
		line := m.rowLine(pick(rows[i]), widths, i == sel, color)
		if i == sel {
			// Run the highlight to the pane's right edge, not just the
			// last column. The marker takes two cells of the pane width.
			if m.width > 0 {
				if d := m.width - 6 - lipgloss.Width(line); d > 0 {
					line += m.styles.selected.Render(strings.Repeat(" ", d))
				}
			}
			line = m.styles.cursor.Render("❯") + " " + line
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// window is the visible row range: the list scrolls to keep the cursor
// inside when it is longer than the screen allows.
func (m *model) window() (start, end, n int) {
	n = m.count()
	budget := m.rowBudget()
	if budget <= 0 || n <= budget {
		return 0, n, n
	}
	i := m.idx()
	if i < 0 {
		i = 0
	}
	start = i - budget/2
	if start < 0 {
		start = 0
	}
	if start+budget > n {
		start = n - budget
	}
	return start, start + budget, n
}

// rowBudget is how many table rows fit. Zero means unbounded.
func (m *model) rowBudget() int {
	if m.height <= 0 {
		return 0
	}
	// header, tabs, pane borders, status bar, table header.
	b := m.height - 6
	switch m.screen {
	case screenPending:
		if m.shortApproval() {
			b-- // one-line approval form
		} else {
			b -= 4 // approval section
		}
	case screenEnv:
		b -= 1 // "names only"
	case screenTokens:
		b -= max(len(m.tokenLines()), 1) // the selected token, wrapped
	}
	return max(b, 1)
}

// padToHeight fills to the screen height so the status bar sits on the last
// row and modals center against the full screen.
func (m *model) padToHeight(out string) string {
	if m.height <= 0 {
		return out
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > m.height {
		// Too tall for the screen: keep the top and the status bar.
		bar := lines[len(lines)-1]
		return strings.Join(append(lines[:m.height-1], bar), "\n")
	}
	if len(lines) == m.height {
		return strings.Join(lines, "\n")
	}
	fill := m.height - len(lines)
	bar := lines[len(lines)-1]
	body := append(lines[:len(lines)-1], make([]string, fill)...)
	return strings.Join(append(body, bar), "\n")
}

// overlay centers box on top of base. The box is opaque: base cells under it
// are replaced, not blended.
func overlay(base, box string, width, height int) string {
	b := strings.Split(base, "\n")
	for len(b) < height {
		b = append(b, "")
	}
	ml := strings.Split(strings.TrimRight(box, "\n"), "\n")
	mw := 0
	for _, l := range ml {
		if w := lipgloss.Width(l); w > mw {
			mw = w
		}
	}
	left := (width - mw) / 2
	if left < 0 {
		left = 0
	}
	top := (height - len(ml)) / 2
	if top < 0 {
		top = 0
	}
	for i, l := range ml {
		row := top + i
		if row >= len(b) {
			break
		}
		if d := mw - lipgloss.Width(l); d > 0 {
			l += strings.Repeat(" ", d)
		}
		head := trunc(b[row], left)
		if d := left - lipgloss.Width(head); d > 0 {
			head += strings.Repeat(" ", d)
		}
		b[row] = head + l + dropCells(b[row], left+mw)
	}
	return strings.Join(b, "\n")
}

// dropCells removes the first n display cells, keeping ANSI sequences so the
// tail keeps its colors.
func dropCells(s string, n int) string {
	if n <= 0 {
		return s
	}
	var prefix strings.Builder
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			prefix.WriteString(s[i:j])
			i = j
			continue
		}
		size, rw := 1, 1
		if s[i] >= 0x80 {
			r, sz := decodeRune(s[i:])
			rw = lipgloss.Width(string(r))
			size = sz
		}
		if w+rw <= n {
			w += rw
			i += size
			continue
		}
		if w < n {
			// A wide rune crosses the cut; hold the column with a space.
			return prefix.String() + " " + s[i+size:]
		}
		return prefix.String() + s[i:]
	}
	return ""
}

func (m *model) statusStyle() lipgloss.Style {
	switch {
	case statusBad(m.status):
		return m.styles.bad
	case statusWarn(m.status):
		return m.styles.warn
	case statusGood(m.status):
		return m.styles.on
	default:
		return m.styles.dim
	}
}

func (m *model) barStatusStyle() lipgloss.Style {
	switch {
	case statusBad(m.status):
		return m.styles.barBad
	case statusWarn(m.status):
		return m.styles.barWarn
	case statusGood(m.status):
		return m.styles.barGood
	default:
		return m.styles.bar
	}
}

func statusBad(s string) bool {
	for _, w := range []string{"not", "invalid", "match", "error", "unknown", "fail"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// statusWarn marks notices that are neither success nor failure: a
// computer going offline.
func statusWarn(s string) bool {
	return strings.Contains(s, "offline")
}

func statusGood(s string) bool {
	for _, w := range []string{"removed", "renamed", "added", "approved", "paired", "joined", "online"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// rowLine pads and styles cells. The selected row gets the active fill on
// every cell and gap: one style per segment, so an inner color cannot
// reset the highlight of the rest of the line.
func (m *model) rowLine(cols []string, widths []int, sel bool, color colorize) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			gap := "  "
			if sel {
				gap = m.styles.selected.Render(gap)
			}
			b.WriteString(gap)
		}
		cell := c
		if i < len(widths) {
			cell = pad(cell, widths[i])
		}
		st, ok := lipgloss.Style{}, false
		if color != nil {
			st, ok = color(i, c)
		}
		if sel {
			if ok {
				st = st.Background(m.styles.rowActive)
			} else {
				// A zero lipgloss.Style renders through the default
				// renderer, which strips attributes without a tty.
				// styles.selected comes from the session renderer.
				st = m.styles.selected
			}
			ok = true
		}
		if ok {
			cell = st.Render(cell)
		}
		b.WriteString(cell)
	}
	line := b.String()
	if !sel {
		line = strings.TrimRight(line, " ")
	}
	// Two cells go to the row marker (or its space) before the line.
	if m.width > 0 && lipgloss.Width(line) > m.width-6 {
		line = trunc(line, m.width-6)
	}
	return line
}

// ellipsis cuts plain text to n cells, marking the cut with "…".
func ellipsis(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	if n <= 1 {
		return trunc(s, n)
	}
	return trunc(s, n-1) + "…"
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
	ansi := false
	// ANSI sequences are passed through so a colored cell is not cut mid-code.
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			ansi = true
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
			if ansi {
				b.WriteString("\x1b[0m") // do not leak an open style past the cut
			}
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
