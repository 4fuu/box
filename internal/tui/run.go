package tui

import (
	"context"
	"errors"
	"io"
	"os"

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
	return &model{
		b:          cfg.Backend,
		ctx:        ctx,
		allowShell: cfg.AllowShell,
		identity:   cfg.Identity,
		status:     cfg.Notice,
		width:      cfg.Width,
		height:     cfg.Height,
		styles:     newStyles(out, cfg.Term),
	}
}

// newStyles picks a color profile. An SSH session's writer is not a local
// tty, so detection would turn color off; TERM from the client decides.
func newStyles(w io.Writer, term string) styles {
	if w == nil {
		w = io.Discard
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
		title:    r.NewStyle().Bold(true),
		header:   r.NewStyle().Bold(true),
		selected: r.NewStyle().Reverse(true),
		tab:      r.NewStyle().Bold(true),
		on:       r.NewStyle().Foreground(lipgloss.Color("10")),
		off:      r.NewStyle().Foreground(lipgloss.Color("8")),
		warn:     r.NewStyle().Foreground(lipgloss.Color("11")),
		bad:      r.NewStyle().Foreground(lipgloss.Color("9")),
		dim:      r.NewStyle().Faint(true),
		secret:   r.NewStyle().Bold(true),
	}
}
