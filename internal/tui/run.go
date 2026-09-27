package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"unsafe"

	"github.com/4fuu/box/internal/control"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Run draws the TUI until the operator quits.
// The returned name is a computer whose shell should be opened; empty means
// the session is over. ErrProgramKilled (the SSH session or process context
// ended) is not an error.
func Run(cfg Config) (string, error) {
	if cfg.Backend == nil {
		return "", errors.New("no backend")
	}
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	m := newModel(cfg, ctx)
	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithoutSignalHandler(),
		tea.WithContext(ctx),
	}
	if cfg.In != nil {
		opts = append(opts, tea.WithInput(cfg.In))
	}
	if cfg.Out != nil {
		opts = append(opts, tea.WithOutput(cfg.Out))
	}
	if len(cfg.Environ) > 0 {
		opts = append(opts, tea.WithEnvironment(cfg.Environ))
	}
	p := tea.NewProgram(m, opts...)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The renderer learns its width only from WindowSizeMsg, and width 0 skips
	// erase-to-end-of-line, so a cleared one-time password stays on screen.
	// Send from a goroutine races the first flush. The message has to already
	// be the program's first inbox item when the loop starts.
	if cfg.Width > 0 || cfg.Height > 0 {
		queueInitial(p, tea.WindowSizeMsg{Width: cfg.Width, Height: cfg.Height})
	}
	if cfg.Resize != nil {
		go forwardResize(runCtx, p, cfg.Resize)
	}
	final, err := p.Run()
	cancel()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	fm, ok := final.(*model)
	if !ok || fm == nil {
		return "", nil
	}
	return fm.shell, nil
}

// queueInitial parks msg ahead of anything Run will receive. bubbletea's
// inbox is unbuffered and created inside NewProgram, so the only way to
// have the size waiting as the first message — before the first flush —
// is to replace that channel. A goroutine Send is not early enough.
func queueInitial(p *tea.Program, msg tea.Msg) {
	field := reflect.ValueOf(p).Elem().FieldByName("msgs")
	if !field.IsValid() || field.Kind() != reflect.Chan {
		// Same-goroutine Send before Run blocks forever on the unbuffered inbox.
		go p.Send(msg)
		return
	}
	inbox := make(chan tea.Msg, 1)
	inbox <- msg
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(inbox))
}

func forwardResize(ctx context.Context, p *tea.Program, resize <-chan Size) {
	for {
		select {
		case <-ctx.Done():
			return
		case sz, ok := <-resize:
			if !ok {
				return
			}
			p.Send(tea.WindowSizeMsg{Width: sz.Width, Height: sz.Height})
		}
	}
}

func newModel(cfg Config, ctx context.Context) *model {
	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}
	theme := cfg.Theme
	if theme != control.ThemeLight {
		theme = control.ThemeDark
	}
	return &model{
		b:          cfg.Backend,
		ctx:        ctx,
		allowShell: cfg.AllowShell,
		identity:   cfg.Identity,
		status:     cfg.Notice,
		width:      cfg.Width,
		height:     cfg.Height,
		styles:     newStyles(out, cfg.Term, theme),
		theme:      theme,
		styleOut:   out,
		styleTerm:  cfg.Term,
	}
}

// Semantic palette: one accent, one muted gray, and three signal colors,
// per theme. Body text stays the terminal's default foreground, so a light
// terminal with the dark theme (or the reverse) keeps readable text; only
// the accents differ. Hex colors quantize to the terminal's 256 profile.
type palette struct {
	accent, success, warning, danger, muted lipgloss.Color
	barBg, barFg, tabFg                     lipgloss.Color
}

var palettes = map[string]palette{
	"dark": {
		accent:  lipgloss.Color("#D8B4FE"), // soft purple
		success: lipgloss.Color("#7ED1A6"), // green
		warning: lipgloss.Color("#F5C26B"), // amber
		danger:  lipgloss.Color("#F28B82"), // red
		muted:   lipgloss.Color("8"),
		barBg:   lipgloss.Color("236"),
		barFg:   lipgloss.Color("252"),
		tabFg:   lipgloss.Color("#1A1B26"),
	},
	"light": {
		accent:  lipgloss.Color("#8250DF"), // violet
		success: lipgloss.Color("#1A7F37"), // green
		warning: lipgloss.Color("#9A6700"), // amber
		danger:  lipgloss.Color("#CF222E"), // red
		muted:   lipgloss.Color("8"),
		barBg:   lipgloss.Color("254"),
		barFg:   lipgloss.Color("238"),
		tabFg:   lipgloss.Color("#FFFFFF"),
	},
}

// newStyles picks a color profile. An SSH session's writer is not a local
// tty, so detection would turn color off; TERM from the client decides.
// theme is "dark" or "light"; anything else is dark.
func newStyles(w io.Writer, term, theme string) styles {
	if w == nil {
		w = io.Discard
	}
	p, ok := palettes[theme]
	if !ok {
		p = palettes["dark"]
	}
	r := lipgloss.NewRenderer(w)
	switch term {
	case "dumb":
		r.SetColorProfile(termenv.Ascii)
	case "":
	default:
		r.SetColorProfile(termenv.ANSI256)
	}
	return styles{
		title:    r.NewStyle().Bold(true).Foreground(p.accent),
		header:   r.NewStyle().Bold(true),
		selected: r.NewStyle().Reverse(true),
		tabOn:    r.NewStyle().Bold(true).Foreground(p.tabFg).Background(p.accent),
		tabOff:   r.NewStyle().Foreground(p.muted),
		on:       r.NewStyle().Foreground(p.success),
		off:      r.NewStyle().Foreground(p.muted),
		warn:     r.NewStyle().Foreground(p.warning),
		bad:      r.NewStyle().Foreground(p.danger),
		dim:      r.NewStyle().Faint(true),
		secret:   r.NewStyle().Bold(true).Foreground(p.accent),
		ptitle:   r.NewStyle().Bold(true).Foreground(p.accent),
		border:   r.NewStyle().Foreground(p.accent),
		cursor:   r.NewStyle().Bold(true).Foreground(p.accent),
		bar:      r.NewStyle().Background(p.barBg).Foreground(p.barFg),
		barDim:   r.NewStyle().Background(p.barBg).Foreground(p.muted),
		barBad:   r.NewStyle().Background(p.barBg).Foreground(p.danger),
		barGood:  r.NewStyle().Background(p.barBg).Foreground(p.success),
	}
}
