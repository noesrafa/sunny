package tui

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// DirPicker is a peer-aware directory browser, embeddable inside any
// dialog that needs to ask the user "which directory?". The picker
// walks the daemon's filesystem via GET /fs/list — NOT the local
// process — so the same code path works for the local daemon and
// every remote peer.
//
// Lifecycle:
//
//  1. NewDirPicker(client, cwd, …) — pass the client of the peer
//     whose filesystem you want to browse.
//  2. Caller forwards the result of Init() into the bubbletea
//     program once.
//  3. Caller forwards every msg through Update(); the picker
//     internally handles dirPickerLoadedMsg and key navigation
//     (only when Focused()).
//  4. At confirm time, caller reads Cwd() and (optionally)
//     guards on Loading() / LoadErr().
//
// Render: the caller composes the layout. Use SearchView() for the
// text input row, ListView() for the entry list, and HintLine() for
// the navigation hint. This split lets each host dialog place a
// label, a different header, etc. between them.
type DirPicker struct {
	cwd      string
	entries  []string
	filtered []int
	selected int
	search   textinput.Model

	loading bool
	loadErr string
	loadGen int

	focused bool
	client  *client.Client
	styles  Styles
}

// dirPickerLoadedMsg is the internal completion message for the
// async /fs/list call. Tagged with the picker's loadGen so a
// response that lands after the user has navigated past it is
// dropped instead of clobbering the current view.
type dirPickerLoadedMsg struct {
	Gen     int
	Path    string
	Entries []string
	Err     error
}

// NewDirPicker constructs a picker bound to one peer's client. cwd
// is the initial directory; placeholder is the search-input
// placeholder text (defaults to "buscar carpeta…" when empty).
func NewDirPicker(c *client.Client, cwd, placeholder string, s Styles) *DirPicker {
	ti := textinput.New()
	if placeholder == "" {
		placeholder = "buscar carpeta…"
	}
	ti.Placeholder = placeholder
	ti.Prompt = "› "
	ti.CharLimit = 0
	ti.SetWidth(50)
	ti.Focus()
	return &DirPicker{
		cwd:     cwd,
		search:  ti,
		styles:  s,
		client:  c,
		loading: c != nil,
		focused: true,
	}
}

// Cwd returns the current directory the user is browsing — i.e. the
// candidate result if they confirm now.
func (p *DirPicker) Cwd() string { return p.cwd }

// Loading reports whether a /fs/list call is in flight for the
// current cwd. Confirmers should refuse on true.
func (p *DirPicker) Loading() bool { return p.loading }

// LoadErr returns the most recent /fs/list error message, or "".
// Confirmers should refuse on non-empty.
func (p *DirPicker) LoadErr() string { return p.loadErr }

func (p *DirPicker) SetStyles(s Styles) { p.styles = s }
func (p *DirPicker) SetSearchWidth(w int) {
	if w > 0 {
		p.search.SetWidth(w)
	}
}

// Focus marks the picker as the keyboard target. Update() honors
// arrow keys, the search input, and descend/ascend only when
// focused. Caller controls focus via tab cycles in multi-field
// dialogs.
func (p *DirPicker) Focus() {
	p.focused = true
	p.search.Focus()
}

// Blur removes keyboard focus from the picker.
func (p *DirPicker) Blur() {
	p.focused = false
	p.search.Blur()
}

// Focused reports whether the picker currently owns the keyboard.
func (p *DirPicker) Focused() bool { return p.focused }

// Init kicks off the first /fs/list. Returns nil when no client is
// configured (offline test scaffolding).
func (p *DirPicker) Init() tea.Cmd {
	if p.client == nil {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, p.loadDirCmd(p.cwd))
}

// Update applies one message. dirPickerLoadedMsg is consumed
// regardless of focus; key messages only when focused. Returns
// the cmd produced (which the host dialog must forward).
func (p *DirPicker) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(dirPickerLoadedMsg); ok {
		if m.Gen != p.loadGen {
			return nil
		}
		p.loading = false
		if m.Err != nil {
			p.loadErr = m.Err.Error()
			p.entries = nil
			p.filtered = nil
			return nil
		}
		p.loadErr = ""
		p.cwd = m.Path
		p.entries = m.Entries
		p.refilter()
		p.selected = 0
		return nil
	}
	if !p.focused {
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "up", "ctrl+p":
		if p.selected > 0 {
			p.selected--
		}
		return nil
	case "down", "ctrl+n":
		if p.selected < len(p.filtered)-1 {
			p.selected++
		}
		return nil
	case "right":
		return p.descend()
	case "left":
		if p.search.Value() == "" {
			return p.ascend()
		}
	case "backspace":
		if p.search.Value() == "" {
			return p.ascend()
		}
	}
	prev := p.search.Value()
	var cmd tea.Cmd
	p.search, cmd = p.search.Update(msg)
	if p.search.Value() != prev {
		p.refilter()
	}
	return cmd
}

// descend dives into the highlighted directory. The /fs/list
// response will replace the entry list; the picker stays loading
// until it lands so the user sees a clear in-flight state.
func (p *DirPicker) descend() tea.Cmd {
	if len(p.filtered) == 0 || p.cwd == "" {
		return nil
	}
	name := p.entries[p.filtered[p.selected]]
	next := joinPath(p.cwd, name)
	p.cwd = next
	p.search.SetValue("")
	p.loading = true
	p.loadErr = ""
	return p.loadDirCmd(next)
}

// ascend walks one level up.
func (p *DirPicker) ascend() tea.Cmd {
	if p.cwd == "" {
		return nil
	}
	parent := parentPath(p.cwd)
	if parent == "" || parent == p.cwd {
		return nil
	}
	p.cwd = parent
	p.search.SetValue("")
	p.loading = true
	p.loadErr = ""
	return p.loadDirCmd(parent)
}

func (p *DirPicker) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.search.Value()))
	p.filtered = p.filtered[:0]
	for i, name := range p.entries {
		if q == "" || strings.Contains(strings.ToLower(name), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	if p.selected >= len(p.filtered) {
		p.selected = 0
	}
}

func (p *DirPicker) loadDirCmd(path string) tea.Cmd {
	c := p.client
	if c == nil {
		return nil
	}
	p.loadGen++
	gen := p.loadGen
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resp, err := c.ListDir(ctx, path)
		if err != nil {
			return dirPickerLoadedMsg{Gen: gen, Path: path, Err: err}
		}
		names := make([]string, 0, len(resp.Entries))
		for _, e := range resp.Entries {
			names = append(names, e.Name)
		}
		sort.Slice(names, func(i, j int) bool {
			return strings.ToLower(names[i]) < strings.ToLower(names[j])
		})
		return dirPickerLoadedMsg{Gen: gen, Path: resp.Path, Entries: names}
	}
}

// SearchView renders the search input row (with a 2-space indent so
// it visually nests under a label).
func (p *DirPicker) SearchView() string {
	return "  " + p.search.View()
}

// ListView renders the directory listing limited to maxRows. innerW
// is reserved for future column layouts; today we render plain
// names. An overflow hint ("…N más") is appended when the listing
// is longer than maxRows.
func (p *DirPicker) ListView(maxRows, innerW int) string {
	_ = innerW
	if p.loading && len(p.entries) == 0 {
		empty := "  " + p.styles.Hint.Render("loading…")
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}
	if p.loadErr != "" && len(p.entries) == 0 {
		empty := "  " + p.styles.ResultError.Render("✗ "+p.loadErr)
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}
	if len(p.filtered) == 0 {
		empty := "  " + p.styles.Hint.Render("(sin coincidencias)")
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}

	start := 0
	if p.selected >= maxRows {
		start = p.selected - maxRows + 1
	}
	end := start + maxRows
	if end > len(p.filtered) {
		end = len(p.filtered)
	}

	var rows []string
	for i := start; i < end; i++ {
		name := p.entries[p.filtered[i]]
		if i == p.selected {
			marker := p.styles.UserPrompt.Render("›")
			rows = append(rows, marker+" "+p.styles.HeaderTitle.Render(name))
		} else {
			rows = append(rows, "  "+p.styles.AssistantText.Render(name))
		}
	}
	for len(rows) < maxRows {
		rows = append(rows, "")
	}
	if len(p.filtered) > maxRows {
		extra := len(p.filtered) - maxRows
		rows = append(rows, p.styles.Hint.Render("  …"+strconv.Itoa(extra)+" más"))
	}
	return strings.Join(rows, "\n")
}

// HintLine returns the navigation hint string for embedding under
// the listing. Caller may render or skip it.
func (p *DirPicker) HintLine() string {
	return p.styles.Hint.Render("↑↓ navegar · → descender · ← atrás · type para filtrar")
}

// joinPath appends name to base using the daemon's path separator.
// Forward slashes are the only target today; remote Windows daemons
// are out of scope.
func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	if strings.HasSuffix(base, "/") {
		return base + name
	}
	return base + "/" + name
}

// parentPath returns the parent directory of p, or "" when p is
// already the filesystem root. Mirrors filepath.Dir for unix paths
// but stays host-agnostic so paths from a remote daemon walk
// correctly regardless of the local TUI's OS.
func parentPath(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	p = strings.TrimRight(p, "/")
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	if idx == 0 {
		return "/"
	}
	return p[:idx]
}
