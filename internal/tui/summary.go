package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/charmbracelet/lipgloss"
)

// tileWidth is the narrowest a fleet tile gets. Below two of them side by
// side, the tiles fold into one overview card.
const tileWidth = 22

// histLen is how many summary samples a sparkline keeps: one per refresh,
// so about 80 seconds.
const histLen = 40

// history is what the sparklines draw. It lives in the TUI only and starts
// empty each time the TUI opens.
type history struct {
	at       time.Time
	requests int64
	reqRate  []float64
	machines map[string]*machineHist
}

type machineHist struct {
	since      time.Time
	sent, recv uint64
	load       []float64
	rate       []float64
	rtt        []float64
	up, down   float64
}

func push(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > histLen {
		s = s[len(s)-histLen:]
	}
	return s
}

// recordHistory turns the counters in m.sum into per-second rates. A
// reconnect resets a tunnel's counters, so a new Since starts over.
func (m *model) recordHistory() {
	if m.hist == nil {
		m.hist = &history{machines: map[string]*machineHist{}}
	}
	h := m.hist
	now := m.sum.Now
	dt := now.Sub(h.at).Seconds()
	fresh := h.at.IsZero() || dt <= 0
	if !fresh {
		h.reqRate = push(h.reqRate, float64(m.sum.Server.Requests-h.requests)/dt)
	}
	h.at, h.requests = now, m.sum.Server.Requests
	seen := map[string]bool{}
	for _, c := range m.sum.Machines {
		seen[c.Name] = true
		if !c.Online {
			delete(h.machines, c.Name)
			continue
		}
		mh := h.machines[c.Name]
		t := c.Tunnel
		if mh == nil || !mh.since.Equal(t.Since) {
			mh = &machineHist{since: t.Since}
			h.machines[c.Name] = mh
		} else if !fresh && t.BytesSent >= mh.sent && t.BytesReceived >= mh.recv {
			mh.down = float64(t.BytesSent-mh.sent) / dt
			mh.up = float64(t.BytesReceived-mh.recv) / dt
			mh.rate = push(mh.rate, mh.up+mh.down)
		}
		mh.sent, mh.recv = t.BytesSent, t.BytesReceived
		if t.RTT > 0 {
			mh.rtt = push(mh.rtt, float64(t.RTT)/float64(time.Millisecond))
		}
		if c.Stat != nil {
			mh.load = push(mh.load, c.Stat.CPU)
		}
	}
	for name := range h.machines {
		if !seen[name] {
			delete(h.machines, name)
		}
	}
}

func (m *model) machineHist(name string) *machineHist {
	if m.hist == nil || m.hist.machines[name] == nil {
		return &machineHist{}
	}
	return m.hist.machines[name]
}

// summaryArea is how many lines the summary may use between the tabs and
// the status bar. Zero means unbounded.
func (m *model) summaryArea() int {
	if m.height <= 0 {
		return 0
	}
	return max(m.height-3, 1)
}

func (m *model) scrollBy(d int) {
	top := 0
	if area := m.summaryArea(); area > 0 {
		top = max(len(m.summaryLines())-area, 0)
	}
	m.scroll = min(max(m.scroll+d, 0), top)
}

// viewSummary is the visible window of the summary lines.
func (m *model) viewSummary() string {
	lines := m.summaryLines()
	if area := m.summaryArea(); area > 0 && len(lines) > area {
		start := min(max(m.scroll, 0), len(lines)-area)
		lines = lines[start : start+area]
	}
	return strings.Join(lines, "\n") + "\n"
}

// summaryWidth is the width the cards lay out in. An unknown size lays out
// for a common terminal.
func (m *model) summaryWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

// summaryLines is the whole summary: server tiles, one card per computer,
// then recent events. Every line is exactly summaryWidth cells.
func (m *model) summaryLines() []string {
	w := m.summaryWidth()
	var out []string
	if w >= 2*tileWidth+1 {
		out = append(out, grid(m.tiles(), w, tileWidth, m.styles.frame)...)
	} else {
		out = append(out, grid([]card{m.overview()}, w, w, m.styles.frame)...)
	}
	switch {
	case m.sumErr != "":
		out = append(out, pad(m.styles.bad.Render(trunc(" summary: "+m.sumErr, w)), w))
	case !m.sumLoaded:
		out = append(out, pad(m.styles.dim.Render(" loading…"), w))
	}
	if len(m.sum.Machines) > 0 {
		cards := make([]card, 0, len(m.sum.Machines))
		for _, c := range m.sum.Machines {
			cards = append(cards, m.machineCard(c))
		}
		out = append(out, grid(cards, w, 34, m.styles.frame)...)
	}
	if m.sumLoaded {
		out = append(out, grid([]card{m.eventsCard()}, w, w, m.styles.frame)...)
	}
	return out
}

// card is one framed block. lines are the content, unpadded.
type card struct {
	title string
	style lipgloss.Style
	lines func(inner int) []string
}

// fleet is the fleet-wide numbers the tiles and the overview both show.
type fleet struct {
	online, total     int
	pending           int
	up, down          float64
	rtt               float64 // average smoothed RTT in ms; 0 when unknown
	reqRate           []float64
	portals, private  int
	tokens, keys      int
	expired, expiring int
}

func (m *model) fleet() fleet {
	f := fleet{total: len(m.snap.Computers), pending: len(m.snap.Pending)}
	for _, c := range m.snap.Computers {
		if c.Online {
			f.online++
		}
	}
	n := 0
	for _, c := range m.sum.Machines {
		if !c.Online {
			continue
		}
		mh := m.machineHist(c.Name)
		f.up += mh.up
		f.down += mh.down
		if c.Tunnel.RTT > 0 {
			f.rtt += float64(c.Tunnel.RTT) / float64(time.Millisecond)
			n++
		}
	}
	if n > 0 {
		f.rtt /= float64(n)
	}
	if m.hist != nil {
		f.reqRate = m.hist.reqRate
	}
	f.portals = len(m.snap.Portals)
	for _, p := range m.snap.Portals {
		if p.Private {
			f.private++
		}
	}
	f.tokens, f.keys = len(m.snap.Tokens), len(m.snap.Keys)
	now := time.Now()
	for _, t := range m.snap.Tokens {
		switch {
		case t.Expires.IsZero():
		case t.Expires.Before(now):
			f.expired++
		case t.Expires.Before(now.Add(7 * 24 * time.Hour)):
			f.expiring++
		}
	}
	return f
}

func (f fleet) onlineStyle(st styles) lipgloss.Style {
	switch {
	case f.total == 0:
		return st.dim
	case f.online == 0:
		return st.bad
	case f.online < f.total:
		return st.warn
	}
	return st.on
}

func (f fleet) onlineFrac() float64 {
	if f.total == 0 {
		return 0
	}
	return float64(f.online) / float64(f.total)
}

func (f fleet) lastRate() float64 {
	if len(f.reqRate) == 0 {
		return 0
	}
	return f.reqRate[len(f.reqRate)-1]
}

// tokenLine flags expired and soon-expiring tokens, else counts them.
func (f fleet) tokenLine(st styles) string {
	switch {
	case f.expired > 0:
		return st.bad.Render(fmt.Sprintf("%d token%s expired", f.expired, plural(f.expired)))
	case f.expiring > 0:
		return st.warn.Render(fmt.Sprintf("%d token%s expiring", f.expiring, plural(f.expiring)))
	}
	return st.dim.Render(fmt.Sprintf("%d token%s · %d key%s", f.tokens, plural(f.tokens), f.keys, plural(f.keys)))
}

func (f fleet) rttText(st styles) string {
	if f.rtt <= 0 {
		return st.dim.Render("rtt —")
	}
	return st.dim.Render("rtt ") + rttStyle(st, f.rtt).Render(ms(f.rtt))
}

func (m *model) uptime() string {
	s := m.sum
	if s.Started.IsZero() || s.Now.IsZero() {
		return m.styles.dim.Render("up —")
	}
	return m.styles.dim.Render("up ") + m.styles.value.Render(dur(s.Now.Sub(s.Started)))
}

// tiles are the fleet-wide numbers, one card per subject.
func (m *model) tiles() []card {
	st := m.styles
	s := m.sum.Server
	f := m.fleet()
	return []card{
		{title: "computers", style: st.ptitle, lines: func(iw int) []string {
			pend := st.dim.Render("no pending joins")
			if f.pending > 0 {
				pend = st.warn.Render(fmt.Sprintf("%d pending join%s", f.pending, plural(f.pending)))
			}
			return []string{
				st.value.Render(fmt.Sprintf("%d/%d", f.online, f.total)) + st.dim.Render(" online"),
				gauge(f.onlineFrac(), iw, f.onlineStyle(st), st.frame),
				pend,
			}
		}},
		{title: "ssh", style: st.ptitle, lines: func(int) []string {
			return []string{
				st.value.Render(fmt.Sprint(s.Sessions)) + st.dim.Render(" open"),
				st.dim.Render(fmt.Sprintf("%d since start", s.SessionsTotal)),
				st.dim.Render(fmt.Sprintf("%d console%s", s.Consoles, plural(int(s.Consoles)))),
			}
		}},
		{title: "http", style: st.ptitle, lines: func(iw int) []string {
			bad := st.dim
			if s.RequestsFailed > 0 {
				bad = st.warn
			}
			return []string{
				st.value.Render(fmt.Sprint(s.Requests)) + st.dim.Render(fmt.Sprintf(" req · %.1f/s", f.lastRate())),
				spark(f.reqRate, iw, st.on, st.frame),
				bad.Render(fmt.Sprintf("%d denied · %d failed", s.RequestsDenied, s.RequestsFailed)),
			}
		}},
		{title: "tunnels", style: st.ptitle, lines: func(int) []string {
			return []string{
				st.value.Render("↓"+size(f.down)+"/s") + " " + st.value.Render("↑"+size(f.up)+"/s"),
				f.rttText(st),
				st.dim.Render(fmt.Sprintf("%d up · %d down", f.online, f.total-f.online)),
			}
		}},
		{title: "server", style: st.ptitle, lines: func(int) []string {
			return []string{
				m.uptime(),
				st.dim.Render(fmt.Sprintf("%d portal%s · %d priv", f.portals, plural(f.portals), f.private)),
				f.tokenLine(st),
			}
		}},
	}
}

// overview is the tiles folded into one card for a terminal too narrow to
// put two tiles side by side.
func (m *model) overview() card {
	st := m.styles
	s := m.sum.Server
	f := m.fleet()
	return card{title: "overview", style: st.ptitle, lines: func(iw int) []string {
		online := st.value.Render(fmt.Sprintf("%d/%d", f.online, f.total))
		lines := []string{
			meter("up", func(w int) string { return gauge(f.onlineFrac(), w, f.onlineStyle(st), st.frame) }, online, iw, st),
		}
		if f.pending > 0 {
			lines = append(lines, meter("join", nil, st.warn.Render(fmt.Sprintf("%d pending", f.pending)), iw, st))
		}
		return append(lines,
			meter("ssh", nil, st.value.Render(fmt.Sprint(s.Sessions))+st.dim.Render(fmt.Sprintf(" open · %d total", s.SessionsTotal)), iw, st),
			meter("http", nil, st.value.Render(fmt.Sprint(s.Requests))+st.dim.Render(fmt.Sprintf(" req · %d denied · %d failed", s.RequestsDenied, s.RequestsFailed)), iw, st),
			meter("net", nil, st.value.Render("↓"+size(f.down)+" ↑"+size(f.up))+" "+f.rttText(st), iw, st),
			meter("host", nil, m.uptime()+st.dim.Render(fmt.Sprintf(" · %d portal%s", f.portals, plural(f.portals))), iw, st),
			meter("auth", nil, f.tokenLine(st), iw, st),
		)
	}}
}

// machineCard is one computer: tunnel health, load, traffic, streams.
func (m *model) machineCard(c control.MachineLive) card {
	st := m.styles
	var view control.ComputerView
	for _, v := range m.snap.Computers {
		if v.Name == c.Name {
			view = v
		}
	}
	if !c.Online {
		return card{title: "○ " + c.Name, style: st.off, lines: func(iw int) []string {
			who := view.User
			if who == "" {
				who = "—"
			}
			return []string{st.dim.Render(who), st.off.Render("offline")}
		}}
	}
	mh := m.machineHist(c.Name)
	t := c.Tunnel
	return card{title: "● " + c.Name, style: st.on, lines: func(iw int) []string {
		who := view.User
		if view.Address != "" {
			who += " · " + hostOnly(view.Address)
		}
		if view.AgentVersion != "" {
			who += " · agent " + view.AgentVersion
		}
		rttMS := float64(t.RTT) / float64(time.Millisecond)
		// 250ms fills the bar: past that a shell already feels slow.
		lines := []string{
			st.dim.Render(strings.TrimPrefix(who, " · ")),
			meter("rtt", func(w int) string { return gauge(rttMS/250, w, rttStyle(st, rttMS), st.frame) }, rttStyle(st, rttMS).Render(ms(rttMS)), iw, st),
		}
		if c.Stat != nil {
			lines = append(lines,
				meter("load", func(w int) string { return spark(mh.load, w, st.on, st.frame) }, st.value.Render(fmt.Sprintf("%.2f", c.Stat.CPU)), iw, st),
				meter("used", nil, st.dim.Render("mem ")+st.value.Render(size(float64(c.Stat.Memory)))+st.dim.Render(" · disk ")+st.value.Render(size(float64(c.Stat.Disk))), iw, st),
			)
		} else {
			why := "no reply"
			if c.StatErr != "" && strings.Contains(c.StatErr, "deadline") {
				why = "timed out"
			}
			lines = append(lines, meter("load", nil, st.warn.Render(why), iw, st))
		}
		lines = append(lines,
			meter("net", func(w int) string { return spark(mh.rate, w, st.on, st.frame) }, st.value.Render("↓"+size(mh.down)+" ↑"+size(mh.up)), iw, st),
			meter("ssh", nil, st.value.Render(fmt.Sprint(t.SSHOpen))+st.dim.Render(fmt.Sprintf(" open · %d total", t.SSHTotal)), iw, st),
			meter("http", nil, st.value.Render(fmt.Sprint(t.PortalOpen))+st.dim.Render(fmt.Sprintf(" open · %d total", t.PortalTotal)), iw, st),
		)
		tail := "tunnel " + dur(time.Since(t.Since))
		if !m.sum.Now.IsZero() && !t.Since.IsZero() {
			tail = "tunnel " + dur(m.sum.Now.Sub(t.Since))
		}
		if c.Stat != nil && c.Stat.Uptime > 0 {
			tail += " · host " + dur(time.Duration(c.Stat.Uptime)*time.Second)
		}
		if t.PacketsSent > 0 && t.PacketsLost > 0 {
			tail += fmt.Sprintf(" · lost %.1f%%", 100*float64(t.PacketsLost)/float64(t.PacketsSent))
		}
		return append(lines, st.dim.Render(tail))
	}}
}

func (m *model) eventsCard() card {
	st := m.styles
	s := m.sum
	title := fmt.Sprintf("events · %d held · %d total", s.EventsHeld, s.EventsTotal)
	return card{title: title, style: st.ptitle, lines: func(iw int) []string {
		if len(s.Events) == 0 {
			return []string{st.dim.Render("no events")}
		}
		var out []string
		for i := len(s.Events) - 1; i >= 0; i-- {
			e := s.Events[i]
			age := dur(s.Now.Sub(e.Time))
			out = append(out, st.dim.Render(pad(age, 6))+st.value.Render(e.Topic)+st.dim.Render(" "+e.From+" ")+e.Body)
		}
		return out
	}}
}

// visual draws itself in w cells.
type visual func(w int) string

// meter is one labelled row. With a visual, the visual takes the free
// middle and the value sits on the right; below three free cells the
// visual is dropped. Without one, the value follows the label.
func meter(label string, vis visual, value string, iw int, st styles) string {
	const lw = 5
	line := st.dim.Render(pad(label, lw))
	if vis == nil {
		return line + value
	}
	free := iw - lw - lipgloss.Width(value) - 1
	if free >= 3 {
		line += vis(free) + " "
	} else if free > 0 {
		line += strings.Repeat(" ", free+1)
	}
	return line + value
}

// gauge draws frac of w cells filled. frac is clamped to [0, 1].
func gauge(frac float64, w int, fill, empty lipgloss.Style) string {
	if math.IsNaN(frac) || frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	if w <= 0 {
		return ""
	}
	n := int(math.Round(frac * float64(w)))
	if frac > 0 && n == 0 {
		n = 1
	}
	return fill.Render(strings.Repeat("█", n)) + empty.Render(strings.Repeat("░", w-n))
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// spark draws the last w samples, scaled to the largest one shown.
// Missing samples on the left are a faint baseline.
func spark(vals []float64, w int, fill, empty lipgloss.Style) string {
	if w <= 0 {
		return ""
	}
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	top := 0.0
	for _, v := range vals {
		top = max(top, v)
	}
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if top > 0 {
			i = int(math.Round(v / top * float64(len(sparkRunes)-1)))
		}
		b.WriteRune(sparkRunes[min(max(i, 0), len(sparkRunes)-1)])
	}
	return empty.Render(strings.Repeat("▁", w-len(vals))) + fill.Render(b.String())
}

// grid lays cards out in as many columns as fit at minW cells each, and
// gives every card in a row the same height. Each returned line is w wide.
func grid(cards []card, w, minW int, frame lipgloss.Style) []string {
	if len(cards) == 0 {
		return nil
	}
	cols := max(min((w+1)/(minW+1), len(cards)), 1)
	var out []string
	for i := 0; i < len(cards); i += cols {
		row := cards[i:min(i+cols, len(cards))]
		// The last row keeps the column width of the rows above.
		n := cols
		widths := make([]int, n)
		base := (w - (n - 1)) / n
		for j := range widths {
			widths[j] = base
			if j < (w-(n-1))%n {
				widths[j]++
			}
		}
		bodies := make([][]string, len(row))
		h := 0
		for j, c := range row {
			bodies[j] = c.lines(widths[j] - 4)
			h = max(h, len(bodies[j]))
		}
		boxes := make([][]string, len(row))
		for j, c := range row {
			for len(bodies[j]) < h {
				bodies[j] = append(bodies[j], "")
			}
			boxes[j] = box(c.title, c.style, bodies[j], widths[j], frame)
		}
		for k := 0; k < h+2; k++ {
			var line strings.Builder
			used := 0
			for j := range boxes {
				if j > 0 {
					line.WriteByte(' ')
					used++
				}
				line.WriteString(boxes[j][k])
				used += widths[j]
			}
			line.WriteString(strings.Repeat(" ", max(w-used, 0)))
			out = append(out, line.String())
		}
	}
	return out
}

// box frames lines in w cells: ╭─ title ─╮, │ line │, ╰──╯.
func box(title string, ts lipgloss.Style, lines []string, w int, frame lipgloss.Style) []string {
	inner := max(w-4, 1)
	title = trunc(title, max(inner-1, 1))
	dash := max(inner-lipgloss.Width(title)-1, 0)
	out := []string{frame.Render("╭─ ") + ts.Render(title) + frame.Render(" "+strings.Repeat("─", dash)+"╮")}
	for _, l := range lines {
		out = append(out, frame.Render("│ ")+pad(trunc(l, inner), inner)+frame.Render(" │"))
	}
	return append(out, frame.Render("╰"+strings.Repeat("─", inner+2)+"╯"))
}

func rttStyle(st styles, v float64) lipgloss.Style {
	switch {
	case v >= 200:
		return st.bad
	case v >= 80:
		return st.warn
	default:
		return st.on
	}
}

func ms(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1fms", v)
	}
	return fmt.Sprintf("%.0fms", v)
}

// size is a compact binary size: 812B, 1.2K, 34M, 3.1G.
func size(v float64) string {
	if v < 0 || math.IsNaN(v) {
		v = 0
	}
	units := []string{"B", "K", "M", "G", "T", "P"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f%s", v, units[i])
	}
	if v < 10 {
		return fmt.Sprintf("%.1f%s", v, units[i])
	}
	return fmt.Sprintf("%.0f%s", v, units[i])
}

// dur is a compact age: 42s, 5m, 3h12m, 4d6h.
func dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// hostOnly drops the port from an address.
func hostOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 && !strings.HasSuffix(addr, "]") {
		return strings.Trim(addr[:i], "[]")
	}
	return addr
}
