package tui

import (
	"image/color"
	"os"
	"os/exec"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/session"
)

// diffFile is one entry in the working-tree change list. Status is the raw
// `git status --porcelain` two-char prefix (`??`, ` M`, `MM`, `A `, etc.).
type diffFile struct {
	Path   string
	Status string // raw two-char porcelain code
	Bucket string // "added" | "modified" | "deleted" | "untracked"
}

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
// Up/Down navigate the file list, "/" focuses the search field, esc
// unfocuses search (if focused) or closes the dialog. Mouse wheel scrolls
// the diff pane.
type DiffDialog struct {
	cwd     string
	branch  string
	changes session.ChangeStats
	styles  Styles

	files    []diffFile
	filtered []int // indexes into files matching the search filter
	cursor   int   // index within `filtered`

	search        textinput.Model
	searchFocused bool

	vp viewport.Model

	// loadedPath is the path whose diff is currently in the viewport. We
	// only re-run `git diff` when the cursor moves to a different file.
	loadedPath string
}

func NewDiffDialog(cwd, branch string, changes session.ChangeStats, s Styles) *DiffDialog {
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

	d := &DiffDialog{
		cwd:     cwd,
		branch:  branch,
		changes: changes,
		styles:  s,
		search:  ti,
		vp:      vp,
	}
	d.files = listChangedFiles(cwd)
	d.applyFilter()
	return d
}

func (d *DiffDialog) SetStyles(s Styles) { d.styles = s }

func (d *DiffDialog) Init() tea.Cmd { return nil }

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
			d.move(-1)
			return nil
		}
		prev := d.search.Value()
		var cmd tea.Cmd
		d.search, cmd = d.search.Update(k)
		if d.search.Value() != prev {
			d.applyFilter()
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
		d.move(-1)
		return nil
	case "down", "j":
		d.move(1)
		return nil
	case "home", "g":
		if len(d.filtered) > 0 {
			d.cursor = 0
			d.loadSelectedDiff()
		}
		return nil
	case "end", "G":
		if len(d.filtered) > 0 {
			d.cursor = len(d.filtered) - 1
			d.loadSelectedDiff()
		}
		return nil
	case "r":
		d.files = listChangedFiles(d.cwd)
		d.applyFilter()
		d.loadedPath = "" // force re-render
		d.loadSelectedDiff()
		return nil
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(k)
		return cmd
	}
	return nil
}

func (d *DiffDialog) move(delta int) {
	if len(d.filtered) == 0 {
		return
	}
	d.cursor += delta
	if d.cursor < 0 {
		d.cursor = 0
	}
	if d.cursor >= len(d.filtered) {
		d.cursor = len(d.filtered) - 1
	}
	d.loadSelectedDiff()
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
	d.loadSelectedDiff()
}

func (d *DiffDialog) loadSelectedDiff() {
	if len(d.filtered) == 0 {
		d.vp.SetContent(d.styles.Hint.Render("(sin archivos)"))
		d.loadedPath = ""
		d.vp.GotoTop()
		return
	}
	f := d.files[d.filtered[d.cursor]]
	if f.Path == d.loadedPath {
		return
	}
	d.loadedPath = f.Path
	d.vp.SetContent(renderFileDiff(d.cwd, f, d.styles))
	d.vp.GotoTop()
}

// listChangedFiles parses `git status --porcelain` into a sorted file list.
// Sorting buckets staged/modified before untracked so the user lands on
// "intentional" changes first.
func listChangedFiles(cwd string) []diffFile {
	if cwd == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", cwd, "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var files []diffFile
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		st := line[:2]
		path := line[3:]
		// Rename entries look like "old -> new"; we want the destination.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		bucket := classifyStatus(st)
		files = append(files, diffFile{Path: path, Status: st, Bucket: bucket})
	}
	sort.SliceStable(files, func(i, j int) bool {
		oi, oj := bucketOrder(files[i].Bucket), bucketOrder(files[j].Bucket)
		if oi != oj {
			return oi < oj
		}
		return files[i].Path < files[j].Path
	})
	return files
}

func classifyStatus(st string) string {
	if st == "??" {
		return "untracked"
	}
	x, y := rune(st[0]), rune(st[1])
	switch {
	case x == 'D' || y == 'D':
		return "deleted"
	case x == 'M' || y == 'M' || x == 'R' || y == 'R' || x == 'C' || y == 'C':
		return "modified"
	case x == 'A' || y == 'A':
		return "added"
	}
	return "modified"
}

func bucketOrder(b string) int {
	switch b {
	case "modified":
		return 0
	case "added":
		return 1
	case "deleted":
		return 2
	case "untracked":
		return 3
	}
	return 4
}

// renderFileDiff returns the colorized diff for `f` in `cwd`. For tracked
// files we run `git diff HEAD -- <path>`; for untracked files we just dump
// the file contents prefixed with "+" (mirrors how a hypothetical staging
// would render).
func renderFileDiff(cwd string, f diffFile, st Styles) string {
	if f.Bucket == "untracked" {
		body, err := os.ReadFile(cwd + string(os.PathSeparator) + f.Path)
		if err != nil {
			return st.Hint.Render("(no se pudo leer " + f.Path + ": " + err.Error() + ")")
		}
		add := lipgloss.NewStyle().Foreground(colSuccess)
		var b strings.Builder
		b.WriteString(st.HeaderDim.Render("untracked file: " + f.Path))
		b.WriteString("\n")
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			b.WriteString(add.Render("+ " + line))
			b.WriteString("\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}
	// `git diff HEAD` covers both staged and unstaged edits relative to the
	// last commit — exactly what the user wants to see before committing.
	out, err := exec.Command("git", "-C", cwd, "-c", "color.ui=never", "diff", "HEAD", "--", f.Path).Output()
	if err != nil || len(out) == 0 {
		// Fresh repo or weird state: try without HEAD.
		out, _ = exec.Command("git", "-C", cwd, "-c", "color.ui=never", "diff", "--", f.Path).Output()
	}
	body := strings.TrimRight(string(out), "\n")
	if body == "" {
		return st.Hint.Render("(sin diff para " + f.Path + ")")
	}
	return colorizeDiff(body, st)
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
			strings.HasPrefix(line, "--- "):
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

func formatFileRow(f diffFile, width int, selected bool, st Styles) string {
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
