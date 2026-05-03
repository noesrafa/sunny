package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/runs"
)

// RunLogsDialog tails a run's LogBuffer in a viewport. Re-renders on every
// spinner tick so users see live output.
type RunLogsDialog struct {
	run    *runs.Run
	vp     viewport.Model
	follow bool
	styles Styles
}

func NewRunLogsDialog(r *runs.Run, s Styles) *RunLogsDialog {
	vp := viewport.New()
	vp.SetWidth(70)
	vp.SetHeight(20)
	// Same horizontal-scroll lockdown as the chat viewport — long log
	// lines wrap to the dialog width instead of letting the user scroll
	// sideways. Left/Right key bindings disabled so they don't conflict
	// with future cursor moves inside the logs viewer.
	vp.SoftWrap = true
	vp.KeyMap.Left = key.NewBinding(key.WithDisabled())
	vp.KeyMap.Right = key.NewBinding(key.WithDisabled())
	return &RunLogsDialog{run: r, vp: vp, follow: true, styles: s}
}

func (d *RunLogsDialog) SetStyles(s Styles) { d.styles = s }

func (d *RunLogsDialog) Init() tea.Cmd { return nil }

func (d *RunLogsDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "q":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "r":
			_ = d.run.Restart()
		case "k":
			_ = d.run.Stop()
		case "c":
			d.run.Logs.Clear()
		case "f":
			d.follow = !d.follow
		}
	}
	var cmd tea.Cmd
	d.vp, cmd = d.vp.Update(msg)
	return cmd
}

func (d *RunLogsDialog) View(width, height int) string {
	boxW := width - 8
	if boxW > 110 {
		boxW = 110
	}
	if boxW < 50 {
		boxW = 50
	}
	innerW := boxW - 6
	boxH := height - 6
	if boxH > 30 {
		boxH = 30
	}
	if boxH < 12 {
		boxH = 12
	}

	d.vp.SetWidth(innerW)
	d.vp.SetHeight(boxH - 6)

	// Refresh content on every render so live output streams in.
	if d.run.Logs != nil {
		lines := d.run.Logs.Snapshot()
		d.vp.SetContent(strings.Join(lines, "\n"))
		if d.follow {
			d.vp.GotoBottom()
		}
	}

	titleText := "Logs · " + d.run.Name
	if d.run.Running() {
		titleText += " · running"
	} else if d.run.Status == runs.StatusCrashed {
		titleText += " · crashed"
	} else {
		titleText += " · stopped"
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	hints := strings.Join([]string{
		d.styles.StatusKey.Render("r") + d.styles.Hint.Render(" restart"),
		d.styles.StatusKey.Render("k") + d.styles.Hint.Render(" kill"),
		d.styles.StatusKey.Render("c") + d.styles.Hint.Render(" clear"),
		d.styles.StatusKey.Render("f") + d.styles.Hint.Render(" toggle follow"),
		d.styles.StatusKey.Render("↑↓") + d.styles.Hint.Render(" scroll"),
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" close"),
	}, d.styles.Hint.Render(" · "))

	lines := []string{title, "", d.vp.View(), "", hints}
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}
