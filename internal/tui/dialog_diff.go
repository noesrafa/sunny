package tui

import (
	"context"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/git"
)

// DiffDialog is the two-pane git diff viewer:
//
//	┌── Diff · ⌥ branch · +1 ~3 ?2 ────────────────────┐
//	│ files                ▎  diff for selected file   │
//	│ ▶ M  internal/...    ▎                           │
//	│   A  README.md       ▎                           │
//	│   ?  scratch.txt     ▎                           │
//	│ search: ____         ▎                           │
//	└──────────────────────────────────────────────────┘
//
// File list and diff body both come from the daemon at the session's
// host peer (GET /git/files, GET /git/diff). The dialog never runs
// `git` locally — that's what lets it work for sessions bound to
// remote peers, where the cwd lives on a different machine.
//
// Up/Down navigate the file list, "/" focuses the search field, esc
// unfocuses search (if focused) or closes the dialog. Mouse wheel
// scrolls the diff pane.
type DiffDialog struct {
	client *client.Client
	host   string
	cwd    string
	branch string

	changes git.ChangeStats
	styles  Styles

	files    []git.File
	filtered []int // indexes into files matching the search filter
	cursor   int   // index within `filtered`

	search        textinput.Model
	searchFocused bool

	vp viewport.Model

	// loadedPath is the path whose diff is currently in the viewport;
	// loadingPath is the path whose diff is in flight. We re-fetch
	// when the cursor moves to a new file and drop stale responses
	// whose Path no longer matches loadingPath (user moved on).
	loadedPath  string
	loadingPath string

	filesLoading bool
	diffLoading  bool
}

func NewDiffDialog(c *client.Client, host, cwd, branch string, changes git.ChangeStats, s Styles) *DiffDialog {
	vp := viewport.New()
	vp.SetWidth(60)
	vp.SetHeight(20)
	vp.SoftWrap = true
	vp.KeyMap.Left = key.NewBinding(key.WithDisabled())
	vp.KeyMap.Right = key.NewBinding(key.WithDisabled())
	// The dialog owns ↑/↓ for the file list, so don't let the viewport
	// hijack them when the focus is on the list. PageUp/PageDown still
	// scroll the diff regardless.
	vp.KeyMap.Up = key.NewBinding(key.WithDisabled())
	vp.KeyMap.Down = key.NewBinding(key.WithDisabled())

	ti := textinput.New()
	ti.Placeholder = "filtrar archivos…"
	ti.Prompt = "› "
	ti.CharLimit = 80

	return &DiffDialog{
		client:       c,
		host:         host,
		cwd:          cwd,
		branch:       branch,
		changes:      changes,
		styles:       s,
		search:       ti,
		vp:           vp,
		filesLoading: true,
	}
}

func (d *DiffDialog) SetStyles(s Styles) { d.styles = s }

// Init fires the first GET /git/files. The result lands in Update as
// gitFilesLoadedMsg, which triggers the first diff fetch.
func (d *DiffDialog) Init() tea.Cmd {
	d.vp.SetContent(d.styles.Hint.Render("(cargando…)"))
	return d.fetchFilesCmd()
}

func (d *DiffDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.MouseWheelMsg:
		// Mouse wheel is always for the diff pane. The dialog never
		// scrolls the file list with it (the list is short and arrow
		// keys handle it just fine).
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(m)
		return cmd
	case tea.KeyMsg:
		return d.handleKey(m)
	case gitFilesLoadedMsg:
		d.filesLoading = false
		if m.Err != nil {
			d.vp.SetContent(d.styles.ResultError.Render("git files: " + m.Err.Error()))
			return nil
		}
		d.files = m.Files
		d.applyFilter()
		return d.loadSelectedDiff()
	case gitDiffLoadedMsg:
		// Drop stale responses — the user may have moved to a new file
		// while this diff was in flight.
		if m.Path != d.loadingPath {
			return nil
		}
		d.diffLoading = false
		d.loadedPath = m.Path
		if m.Err != nil {
			d.vp.SetContent(d.styles.ResultError.Render("git diff: " + m.Err.Error()))
			return nil
		}
		if strings.TrimSpace(m.Body) == "" {
			d.vp.SetContent(d.styles.Hint.Render("(sin diff para " + m.Path + ")"))
			return nil
		}
		d.vp.SetContent(colorizeDiff(m.Body, d.styles))
		d.vp.GotoTop()
		return nil
	}
	var cmd tea.Cmd
	d.vp, cmd = d.vp.Update(msg)
	return cmd
}

func (d *DiffDialog) handleKey(k tea.KeyMsg) tea.Cmd {
	if d.searchFocused {
		switch k.String() {
		case "esc":
			d.searchFocused = false
			d.search.Blur()
			return nil
		case "enter", "down":
			d.searchFocused = false
			d.search.Blur()
			return nil
		case "up":
			d.searchFocused = false
			d.search.Blur()
			return d.move(-1)
		}
		prev := d.search.Value()
		var cmd tea.Cmd
		d.search, cmd = d.search.Update(k)
		if d.search.Value() != prev {
			d.applyFilter()
			return tea.Batch(cmd, d.loadSelectedDiff())
		}
		return cmd
	}

	switch k.String() {
	case "esc", "q":
		return func() tea.Msg { return CloseDialogMsg{} }
	case "/":
		d.searchFocused = true
		d.search.Focus()
		return textinput.Blink
	case "up", "k":
		return d.move(-1)
	case "down", "j":
		return d.move(1)
	case "home", "g":
		if len(d.filtered) > 0 {
			d.cursor = 0
			return d.loadSelectedDiff()
		}
		return nil
	case "end", "G":
		if len(d.filtered) > 0 {
			d.cursor = len(d.filtered) - 1
			return d.loadSelectedDiff()
		}
		return nil
	case "r":
		d.filesLoading = true
		d.loadedPath = ""
		d.loadingPath = ""
		d.vp.SetContent(d.styles.Hint.Render("(recargando…)"))
		return d.fetchFilesCmd()
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(k)
		return cmd
	}
	return nil
}

func (d *DiffDialog) move(delta int) tea.Cmd {
	if len(d.filtered) == 0 {
		return nil
	}
	d.cursor += delta
	if d.cursor < 0 {
		d.cursor = 0
	}
	if d.cursor >= len(d.filtered) {
		d.cursor = len(d.filtered) - 1
	}
	return d.loadSelectedDiff()
}

// applyFilter rebuilds the filtered index from the current search text.
// Plain substring match — dirt-simple and keeps the keystrokes responsive.
func (d *DiffDialog) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(d.search.Value()))
	d.filtered = d.filtered[:0]
	for i, f := range d.files {
		if q == "" || strings.Contains(strings.ToLower(f.Path), q) {
			d.filtered = append(d.filtered, i)
		}
	}
	if d.cursor >= len(d.filtered) {
		d.cursor = 0
	}
}

// loadSelectedDiff returns a tea.Cmd that fetches the diff for the
// currently-selected file via GET /git/diff. nil when there's nothing
// selected, when the same file is already loaded, or when no client
// is configured (dialog opened without a session).
func (d *DiffDialog) loadSelectedDiff() tea.Cmd {
	if len(d.filtered) == 0 {
		d.vp.SetContent(d.styles.Hint.Render("(sin archivos)"))
		d.loadedPath = ""
		d.loadingPath = ""
		d.vp.GotoTop()
		return nil
	}
	f := d.files[d.filtered[d.cursor]]
	if f.Path == d.loadedPath || f.Path == d.loadingPath {
		return nil
	}
	d.loadingPath = f.Path
	d.diffLoading = true
	d.vp.SetContent(d.styles.Hint.Render("(cargando " + f.Path + "…)"))
	d.vp.GotoTop()
	return d.fetchDiffCmd(f.Path)
}

func (d *DiffDialog) fetchFilesCmd() tea.Cmd {
	if d.client == nil || d.cwd == "" {
		return func() tea.Msg { return gitFilesLoadedMsg{Files: []git.File{}} }
	}
	c, cwd := d.client, d.cwd
	return func() tea.Msg {
		files, err := c.GitFiles(context.Background(), cwd)
		return gitFilesLoadedMsg{Files: files, Err: err}
	}
}

func (d *DiffDialog) fetchDiffCmd(path string) tea.Cmd {
	if d.client == nil || d.cwd == "" {
		return nil
	}
	c, cwd := d.client, d.cwd
	return func() tea.Msg {
		body, err := c.GitDiff(context.Background(), cwd, path)
		return gitDiffLoadedMsg{Path: path, Body: body, Err: err}
	}
}

// colorizeDiff applies styles to unified-diff output: green for additions,
// red for deletions, accent for hunk headers, dim for file metadata. The
// `-c color.ui=never` upstream guarantees there are no embedded ANSI
// sequences to confuse this.
func colorizeDiff(s string, st Styles) string {
	add := lipgloss.NewStyle().Foreground(colSuccess)
	del := lipgloss.NewStyle().Foreground(colDanger)
	hunk := st.ToolPrompt
	meta := st.HeaderDim
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case strings.HasPrefix(line, "diff --git"),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode"),
			strings.HasPrefix(line, "deleted file mode"),
			strings.HasPrefix(line, "similarity index"),
			strings.HasPrefix(line, "rename from"),
			strings.HasPrefix(line, "rename to"),
			strings.HasPrefix(line, "+++ "),
			strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "untracked file: "):
			b.WriteString(meta.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(hunk.Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(add.Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(del.Render(line))
		default:
			b.WriteString(line)
		}
	}
	return b.String()
}

func (d *DiffDialog) View(width, height int) string {
	boxW := width - 4
	if boxW > 130 {
		boxW = 130
	}
	if boxW < 60 {
		boxW = 60
	}
	innerW := boxW - 6
	boxH := height - 4
	if boxH > 40 {
		boxH = 40
	}
	if boxH < 14 {
		boxH = 14
	}

	listW := 32
	if listW > innerW/2 {
		listW = innerW / 2
	}
	dividerW := 1
	diffW := innerW - listW - dividerW - 2 // 2 cols of breathing room around divider

	// Reserve rows for: title (1), blank (1), body, blank (1), hint (1).
	// Title is height 1; body fills the rest.
	bodyH := boxH - 6
	if bodyH < 6 {
		bodyH = 6
	}
	d.vp.SetWidth(diffW)
	d.vp.SetHeight(bodyH - 2) // search field eats one row in the body column

	listView := d.renderFileList(listW, bodyH-2)
	searchView := d.renderSearch(listW)

	leftCol := lipgloss.JoinVertical(lipgloss.Left, listView, "", searchView)
	leftCol = lipgloss.NewStyle().Width(listW).Height(bodyH).Render(leftCol)

	dividerStyle := lipgloss.NewStyle().Foreground(colBorder)
	divider := dividerStyle.Render(strings.Repeat("│\n", bodyH))
	divider = strings.TrimSuffix(divider, "\n")

	right := lipgloss.NewStyle().Width(diffW).Height(bodyH).Render(d.vp.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, " ", divider, " ", right)

	titleText := "Diff"
	if d.branch != "" {
		titleText = "Diff · ⌥ " + d.branch
	}
	if badge := renderChangesBadge(d.changes); badge != "" {
		titleText += " · " + stripStyle(badge)
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	hint := d.renderHints()

	lines := []string{title, "", body, "", hint}
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

// stripStyle returns s unchanged — kept as a hook in case we ever decide
// the title bar should drop ANSI styling for the hatched gradient. Today
// the hatched gradient applies AFTER the title, so styled badge text in
// the title is fine.
func stripStyle(s string) string { return s }

func (d *DiffDialog) renderFileList(width, height int) string {
	if d.filesLoading {
		return d.styles.Hint.Render("cargando…")
	}
	if len(d.files) == 0 {
		return d.styles.Hint.Render("árbol limpio")
	}
	if len(d.filtered) == 0 {
		return d.styles.Hint.Render("(sin coincidencias)")
	}
	maxRows := height
	if maxRows < 1 {
		maxRows = 1
	}
	// Window the list around the cursor so long file lists scroll.
	start := 0
	if d.cursor >= maxRows {
		start = d.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(d.filtered) {
		end = len(d.filtered)
	}
	var rows []string
	for i := start; i < end; i++ {
		f := d.files[d.filtered[i]]
		selected := i == d.cursor
		rows = append(rows, formatFileRow(f, width, selected, d.styles))
	}
	return strings.Join(rows, "\n")
}

func formatFileRow(f git.File, width int, selected bool, st Styles) string {
	sym, col := bucketGlyph(f.Bucket)
	indicator := " "
	pathStyle := st.AssistantText
	if selected {
		indicator = st.UserPrompt.Render("▎")
		pathStyle = st.AssistantText.Bold(true)
	}
	glyph := lipgloss.NewStyle().Foreground(col).Bold(true).Render(sym)
	// Path truncation: keep the tail (more meaningful than the prefix) when
	// the full path doesn't fit.
	pathW := width - 4 // indicator + glyph + space
	if pathW < 8 {
		pathW = 8
	}
	path := f.Path
	if len(path) > pathW && pathW > 1 {
		path = "…" + path[len(path)-(pathW-1):]
	}
	return indicator + glyph + " " + pathStyle.Render(path)
}

func bucketGlyph(b string) (string, color.Color) {
	switch b {
	case "added":
		return "+", colSuccess
	case "deleted":
		return "−", colDanger
	case "untracked":
		return "?", colAccent
	default:
		return "~", colSecondary
	}
}

func (d *DiffDialog) renderSearch(width int) string {
	d.search.SetWidth(width - 2)
	label := d.styles.Hint.Render("/buscar")
	if d.searchFocused {
		label = lipgloss.NewStyle().Foreground(colTertiary).Bold(true).Render("/buscar")
	}
	return label + "\n" + d.search.View()
}

func (d *DiffDialog) renderHints() string {
	keys := [][2]string{
		{"↑↓", "archivo"},
		{"/", "buscar"},
		{"wheel/pgup", "scroll"},
		{"r", "reload"},
		{"esc", "cerrar"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, d.styles.StatusKey.Render(k[0])+" "+d.styles.Hint.Render(k[1]))
	}
	return strings.Join(parts, d.styles.Hint.Render(" · "))
}
