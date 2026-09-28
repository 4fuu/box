package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/tunnel"
	"github.com/charmbracelet/lipgloss"
)

const longToken = "tok_0123456789abcdef0123456789abcdef0123456789abcdef"

func richModel(w, h int) *model {
	now := time.Now()
	stat := &tunnel.StatResponse{CPU: 0.42, Memory: 3 << 30, Disk: 40 << 30, Uptime: 86400 * 3}
	return &model{
		width:  w,
		height: h,
		// Color on, so the width checks count cells and not escape bytes.
		styles:   newStyles(io.Discard, "xterm-256color"),
		identity: "SHA256:tTabkei6ZGvYZ3RDiAHnWmfqrZX3aNA72wpZK6Q+4ks",
		loaded:   true,
		snap: control.Snapshot{
			Domain: "a-rather-long-domain.box.example.com",
			Computers: []control.ComputerView{
				{Name: "home", Online: true, User: "alice", Address: "203.0.113.20:51820", AgentVersion: "2026.928.1", Portals: []string{"web", "grafana", "lock"}},
				{Name: "work-laptop-with-a-long-name", User: "bob"},
			},
			Pending: []control.PendingView{{Name: "vm", Address: "198.51.100.7:40000", User: "carol", ExpiresAt: now.Add(9 * time.Minute)}},
			Portals: []control.PortalView{{Label: "web", Host: "web.a-rather-long-domain.box.example.com", Computer: "home", Port: 3000, Private: true}},
			Keys:    []control.KeyView{{Fingerprint: "SHA256:tTabkei6ZGvYZ3RDiAHnWmfqrZX3aNA72wpZK6Q+4ks", Comment: "laptop", BoundAt: now}},
			Env:     []string{"OPENAI_API_KEY"},
			Tokens:  []control.TokenView{{ID: 1, Token: longToken, Comment: "door sensor", Expires: now.Add(48 * time.Hour)}},
		},
		sumLoaded: true,
		sum: control.Summary{
			Now: now, Started: now.Add(-50 * time.Hour),
			Server: control.ServerCounts{Consoles: 1, Sessions: 2, SessionsTotal: 17, Requests: 1234, RequestsDenied: 3, RequestsFailed: 1},
			Machines: []control.MachineLive{
				{Name: "home", Online: true, Stat: stat, Tunnel: tunnel.ConnStats{
					Since: now.Add(-3 * time.Hour), RTT: 23 * time.Millisecond,
					BytesSent: 1 << 20, BytesReceived: 2 << 20, PacketsSent: 1000, PacketsLost: 2,
					SSHOpen: 1, SSHTotal: 5, PortalOpen: 2, PortalTotal: 40,
				}},
				{Name: "work-laptop-with-a-long-name"},
			},
			EventsTotal: 12, EventsHeld: 12,
			Events: []event.Item{{ID: 12, Topic: "door", From: "home", Body: "opened by someone with a rather long description", Time: now.Add(-2 * time.Minute)}},
		},
	}
}

// Every screen and dialog, at every size from the minimum up, stays inside
// the terminal: no line wider than it, no more lines than it has.
func TestLayoutFitsEverySize(t *testing.T) {
	modes := []mode{modeNormal, modeHelp, modePair, modeRemove, modeTokenRm, modeEnvValue}
	for _, w := range []int{minWidth, 36, 40, 56, 80, 120} {
		for _, h := range []int{minHeight, 24} {
			for sc := screen(0); sc < screenCount; sc++ {
				for _, md := range modes {
					m := richModel(w, h)
					m.screen = sc
					m.mode = md
					m.secret = "abcd-efgh-jkmn-pqrs-tuvw"
					m.envName = "OPENAI_API_KEY"
					m.target = "1"
					m.recordHistory()
					out := m.View()
					lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
					name := fmt.Sprintf("%dx%d %s mode %d", w, h, screenNames[sc], md)
					if len(lines) > h {
						t.Errorf("%s: %d lines, height %d", name, len(lines), h)
					}
					for i, l := range lines {
						if lw := lipgloss.Width(l); lw > w {
							t.Errorf("%s: line %d is %d wide:\n%s", name, i, lw, l)
						}
					}
				}
			}
		}
	}
}

func TestTooSmallBelowMinimum(t *testing.T) {
	m := richModel(minWidth-1, 24)
	if !strings.Contains(m.View(), "terminal too small") {
		t.Fatal(m.View())
	}
}

// The token and the one-time password must be readable whole at the
// narrowest size: they wrap, they are not cut.
func TestSecretsWrapWhenNarrow(t *testing.T) {
	m := richModel(minWidth, 24)
	m.screen = screenTokens
	flat := strings.ReplaceAll(plain(m.View()), "\n", "")
	flat = strings.NewReplacer("│", "", " ", "").Replace(flat)
	if !strings.Contains(flat, longToken) {
		t.Fatalf("token cut at width %d:\n%s", minWidth, m.View())
	}

	m = richModel(minWidth, 24)
	m.mode = modePair
	m.secret = "abcd-efgh-jkmn-pqrs-tuvw"
	if !strings.Contains(plain(m.View()), m.secret) {
		t.Fatalf("password cut:\n%s", m.View())
	}
}

func TestTabsShortenWithWidth(t *testing.T) {
	m := richModel(120, 24)
	m.screen = screenKeys
	if got := plain(m.viewTabs()); !strings.Contains(got, "computers") || !strings.Contains(got, "tokens") {
		t.Fatalf("wide tabs %q", got)
	}
	m.width = 44
	got := plain(m.viewTabs())
	if !strings.Contains(got, "keys") || strings.Contains(got, "computers") || !strings.Contains(got, "3!") {
		t.Fatalf("short tabs %q", got)
	}
	m.width = 26 // below the minimum, to reach the last form
	got = plain(m.viewTabs())
	if !strings.Contains(got, "keys") || !strings.Contains(got, "5/7") {
		t.Fatalf("narrow tabs %q", got)
	}
}

func TestNarrowTableKeepsImportantColumns(t *testing.T) {
	m := richModel(40, 24)
	m.screen = screenComputers
	out := plain(m.View())
	for _, want := range []string{"NAME", "ONLINE", "home"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "AGENT") {
		t.Fatalf("low-priority column kept at width 40:\n%s", out)
	}
}

func TestFitColumns(t *testing.T) {
	cols := []column{{"A", 0}, {"B", 2}, {"C", 1}}
	check := func(natural []int, avail int, wantKeep, wantW string) {
		t.Helper()
		keep, w := fitColumns(cols, natural, avail)
		if fmt.Sprint(keep) != wantKeep || fmt.Sprint(w) != wantW {
			t.Fatalf("fit %v in %d: keep %v widths %v, want %s %s", natural, avail, keep, w, wantKeep, wantW)
		}
	}
	check([]int{5, 5, 5}, 0, "[0 1 2]", "[5 5 5]")
	check([]int{5, 5, 5}, 19, "[0 1 2]", "[5 5 5]")
	// B is least important; shrinking it to 4 is too little, so it goes.
	check([]int{5, 5, 5}, 16, "[0 2]", "[5 5]")
	// Shrinking B to 6 still reads, so it stays narrowed.
	check([]int{5, 9, 5}, 20, "[0 1 2]", "[5 6 5]")
	// A long first column is capped instead of pushing the others out,
	// then gets back the space left over.
	check([]int{28, 6, 6}, 34, "[0 1 2]", "[18 6 6]")
}

func TestHistoryRates(t *testing.T) {
	m := richModel(80, 24)
	t0 := time.Now()
	since := t0.Add(-time.Hour)
	sample := func(at time.Time, sent, recv uint64, req int64) {
		m.sum = control.Summary{Now: at, Server: control.ServerCounts{Requests: req}, Machines: []control.MachineLive{{
			Name: "home", Online: true,
			Tunnel: tunnel.ConnStats{Since: since, BytesSent: sent, BytesReceived: recv, RTT: 10 * time.Millisecond},
			Stat:   &tunnel.StatResponse{CPU: 1},
		}}}
		m.recordHistory()
	}
	sample(t0, 1000, 0, 10)
	sample(t0.Add(2*time.Second), 3000, 400, 30)
	mh := m.machineHist("home")
	if mh.down != 1000 || mh.up != 200 || len(mh.rate) != 1 || mh.rate[0] != 1200 {
		t.Fatalf("rates down=%v up=%v series=%v", mh.down, mh.up, mh.rate)
	}
	if got := m.hist.reqRate; len(got) != 1 || got[0] != 10 {
		t.Fatalf("request rate %v", got)
	}
	// A reconnect resets the counters; the history starts over instead of
	// showing a negative rate.
	since = t0.Add(time.Second)
	sample(t0.Add(4*time.Second), 10, 10, 30)
	if mh := m.machineHist("home"); len(mh.rate) != 0 || len(mh.load) != 1 {
		t.Fatalf("reconnect kept history: %+v", mh)
	}
	// A computer that goes away drops its history.
	m.sum.Machines = nil
	m.recordHistory()
	if len(m.hist.machines) != 0 {
		t.Fatalf("stale history %v", m.hist.machines)
	}
}

func TestSummaryShowsLiveState(t *testing.T) {
	m := richModel(120, 40)
	out := plain(m.View())
	for _, want := range []string{"1/2", "online", "1 pending join", "2 open", "1234", "3 denied", "up 2d2h", "● home", "23ms", "0.42", "3.0G", "1 open · 5 total", "○ work-laptop", "offline", "door", "12 total"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, longToken) {
		t.Fatal("summary shows a token")
	}
}

func TestSummaryScrolls(t *testing.T) {
	m := richModel(minWidth, minHeight)
	total := len(m.summaryLines())
	area := m.summaryArea()
	if total <= area {
		t.Fatalf("summary fits in %d lines; nothing to scroll", area)
	}
	for i := 0; i < total*2; i++ {
		m.scrollBy(1)
	}
	if m.scroll != total-area {
		t.Fatalf("scroll %d, want clamped to %d", m.scroll, total-area)
	}
	m.scrollBy(-total * 2)
	if m.scroll != 0 {
		t.Fatalf("scroll %d after scrolling up", m.scroll)
	}
}

func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
