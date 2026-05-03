package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/runs"
)

// RunsDialog is the modal that shows registered runs and the actions on them.
// Crush-style hatched header + selection bar.
type RunsDialog struct {
	mgr      *runs.Manager
	selected int
	styles   Styles
	err      string
}

func NewRunsDialog(mgr *runs.Manager, s Styles) *RunsDialog {
	return &RunsDialog{mgr: mgr, styles: s}
}

func (d *RunsDialog) SetStyles(s Styles) { d.styles = s }

func (d *RunsDialog) Init() tea.Cmd { return nil }

func (d *RunsDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "up", "k":
			if d.selected > 0 {
				d.selected--
			}
			return nil
		case "down", "j":
			if d.selected < d.mgr.Len()-1 {
				d.selected++
			}
			return nil
		case "n":
			return func() tea.Msg { return OpenRunEditMsg{} }
		case "enter", " ":
			r := d.mgr.Index(d.selected)
			if r == nil {
				return nil
			}
			if r.Running() {
				if err := r.Stop(); err != nil {
					d.err = err.Error()
				}
			} else {
				if err := r.Start(); err != nil {
					d.err = err.Error()
				}
			}
			return nil
		case "r":
			if r := d.mgr.Index(d.selected); r != nil {
				if err := r.Restart(); err != nil {
					d.err = err.Error()
				}
			}
			return nil
		case "l":
			if r := d.mgr.Index(d.selected); r != nil {
				return func() tea.Msg { return OpenRunLogsMsg{ID: r.ID} }
			}
		case "d":
			if r := d.mgr.Index(d.selected); r != nil {
				return func() tea.Msg { return DeleteRunMsg{ID: r.ID} }
			}
		}
	}
	return nil
}

func (d *RunsDialog) View(width, height int) string {
	boxW := 78
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 50 {
		boxW = 50
	}
	innerW := boxW - 6

	title := HatchedTitle("Runs", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	var body []string
	all := d.mgr.All()
	if len(all) == 0 {
		body = append(body, d.styles.Hint.Render("(no runs registered — press n to add one)"))
	}
	for i, r := range all {
		body = append(body, d.renderRunRow(r, i == d.selected, innerW))
	}

	hints := strings.Join([]string{
		d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" toggle"),
		d.styles.StatusKey.Render("r") + d.styles.Hint.Render(" restart"),
		d.styles.StatusKey.Render("l") + d.styles.Hint.Render(" logs"),
		d.styles.StatusKey.Render("n") + d.styles.Hint.Render(" new"),
		d.styles.StatusKey.Render("d") + d.styles.Hint.Render(" delete"),
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancel"),
	}, d.styles.Hint.Render(" · "))

	lines := []string{title, ""}
	lines = append(lines, body...)
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *RunsDialog) renderRunRow(r *runs.Run, active bool, width int) string {
	icon := runStatusIcon(r, d.styles)
	indicator := "  "
	nameStyle := d.styles.AssistantText
	if active {
		indicator = d.styles.UserPrompt.Render("▎ ")
		nameStyle = d.styles.AssistantText.Bold(true)
	}

	name := nameStyle.Render(padRight(r.Name, 14))
	cmdMaxW := width - 24
	if cmdMaxW < 10 {
		cmdMaxW = 10
	}
	cmd := r.Command
	if lipgloss.Width(cmd) > cmdMaxW {
		cmd = cmd[:cmdMaxW-1] + "…"
	}
	cmdR := d.styles.HeaderDim.Render(cmd)

	var meta string
	switch r.Status {
	case runs.StatusRunning:
		meta = d.styles.StatusIdle.Render(shortDuration(r.Uptime()))
	case runs.StatusCrashed:
		meta = d.styles.ResultError.Render(fmt.Sprintf("exit %d", r.ExitCode))
	default:
		meta = d.styles.Hint.Render("idle")
	}
	return indicator + icon + " " + name + cmdR + " " + meta
}

func runStatusIcon(r *runs.Run, s Styles) string {
	switch r.Status {
	case runs.StatusRunning:
		return s.StatusIdle.Render("●")
	case runs.StatusCrashed:
		return s.ResultError.Render("✗")
	default:
		return s.Hint.Render("○")
	}
}
