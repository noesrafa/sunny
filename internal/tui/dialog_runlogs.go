package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// RunLogsDialog tails one run's stdout/stderr. Opens with a
// snapshot of the last 200 lines (so the user sees recent context
// even on long-lived runs that haven't logged anything since the
// dialog opened), then subscribes to the SSE /logs/watch stream so
// fresh lines appear live. Esc closes; closing also cancels the
// watch via the dialog's ctx.
type RunLogsDialog struct {
	host    string
	runID   string
	runName string

	client *client.Client
	styles Styles

	lines    []client.LogLine
	maxLines int

	ctx    context.Context
	cancel context.CancelFunc
	stream <-chan client.LogLine

	loadErr string
}

// runLogSnapshotMsg carries the result of the initial RunLogs call
// (a static slice). The watch stream is hooked up immediately
// after; live tail messages flow through runLogLineMsg.
type runLogSnapshotMsg struct {
	Lines []client.LogLine
	Err   error
}

// runLogLineMsg is one fresh line drained off the SSE stream.
// Closed indicates the stream ended (run exited or connection
// dropped) — the dialog stops re-arming the wait.
type runLogLineMsg struct {
	Line   client.LogLine
	Closed bool
}

func NewRunLogsDialog(c *client.Client, runID, runName string, s Styles) *RunLogsDialog {
	ctx, cancel := context.WithCancel(context.Background())
	return &RunLogsDialog{
		host:     "", // host is decorative — the client is already bound
		runID:    runID,
		runName:  runName,
		client:   c,
		styles:   s,
		maxLines: 5000, // keep last 5k lines in memory; chat-room sized
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (d *RunLogsDialog) SetStyles(s Styles) { d.styles = s }

func (d *RunLogsDialog) Init() tea.Cmd {
	return tea.Batch(d.snapshotCmd(), d.watchCmd())
}

// snapshotCmd fetches the last 200 lines synchronously so the
// initial render is non-empty even for runs that aren't currently
// emitting anything. The watch stream takes over from there.
func (d *RunLogsDialog) snapshotCmd() tea.Cmd {
	if d.client == nil {
		return nil
	}
	id := d.runID
	c := d.client
	ctx := d.ctx
	return func() tea.Msg {
		lines, err := c.RunLogs(ctx, id, 200)
		return runLogSnapshotMsg{Lines: lines, Err: err}
	}
}

// watchCmd opens the SSE stream and returns the first line; the
// Update handler re-arms after each line so the stream keeps
// draining without blocking the bubbletea loop.
func (d *RunLogsDialog) watchCmd() tea.Cmd {
	if d.client == nil {
		return nil
	}
	if d.stream != nil {
		return d.waitNext()
	}
	id := d.runID
	c := d.client
	ctx := d.ctx
	return func() tea.Msg {
		stream, err := c.WatchRunLogs(ctx, id)
		if err != nil {
			return runLogLineMsg{Closed: true}
		}
		// First read happens here so we hand the dialog back a
		// connected stream + the first line in one shot.
		line, ok := <-stream
		return openedStreamMsg{Stream: stream, First: line, Got: ok}
	}
}

// openedStreamMsg threads the stream pointer back into the dialog
// state on the bubbletea goroutine — direct field assignment from
// inside a tea.Cmd is not safe.
type openedStreamMsg struct {
	Stream <-chan client.LogLine
	First  client.LogLine
	Got    bool
}

func (d *RunLogsDialog) waitNext() tea.Cmd {
	stream := d.stream
	if stream == nil {
		return nil
	}
	return func() tea.Msg {
		line, ok := <-stream
		if !ok {
			return runLogLineMsg{Closed: true}
		}
		return runLogLineMsg{Line: line}
	}
}

func (d *RunLogsDialog) Update(msg tea.Msg) tea.Cmd {
	switch v := msg.(type) {
	case runLogSnapshotMsg:
		if v.Err != nil {
			d.loadErr = v.Err.Error()
			return nil
		}
		d.lines = v.Lines
		d.cap()
		return nil
	case openedStreamMsg:
		d.stream = v.Stream
		if v.Got {
			d.lines = append(d.lines, v.First)
			d.cap()
		}
		return d.waitNext()
	case runLogLineMsg:
		if v.Closed {
			d.stream = nil
			return nil
		}
		d.lines = append(d.lines, v.Line)
		d.cap()
		return d.waitNext()
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			d.cancel()
			return func() tea.Msg { return CloseDialogMsg{} }
		}
	}
	return nil
}

// cap trims d.lines to maxLines so the in-memory buffer never
// grows without bound on long-running services.
func (d *RunLogsDialog) cap() {
	if len(d.lines) <= d.maxLines {
		return
	}
	d.lines = d.lines[len(d.lines)-d.maxLines:]
}

func (d *RunLogsDialog) View(width, height int) string {
	boxW := width - 4
	if boxW < 50 {
		boxW = 50
	}
	if boxW > 140 {
		boxW = 140
	}
	innerW := boxW - 6
	listH := height - 8
	if listH < 8 {
		listH = 8
	}

	titleText := "Logs"
	if d.runName != "" {
		titleText += " · " + d.runName
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	body := d.renderTail(listH, innerW)
	hints := d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cerrar")

	lines := []string{title, ""}
	lines = append(lines, body)
	if d.loadErr != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.loadErr))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

// renderTail returns the last `rows` log lines, dim-prefixed with
// hh:mm:ss for context. Stderr lines are tinted with colWarning so
// errors stand out without needing color knowledge of the source.
func (d *RunLogsDialog) renderTail(rows, innerW int) string {
	if len(d.lines) == 0 {
		return "  " + d.styles.Hint.Render("(sin logs aún)")
	}
	start := 0
	if len(d.lines) > rows {
		start = len(d.lines) - rows
	}
	out := make([]string, 0, rows)
	for _, l := range d.lines[start:] {
		ts := d.styles.Hint.Render(l.Time.Local().Format("15:04:05"))
		text := l.Text
		if l.Stream == "err" {
			text = lipgloss.NewStyle().Foreground(colWarning).Render(text)
		}
		out = append(out, fmt.Sprintf(" %s  %s", ts, truncate(text, innerW-12)))
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}
