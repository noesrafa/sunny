package tui

import (
	"context"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/log/v2"

	"github.com/noesrafa/sunny/internal/anim"
	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/list"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/sysstats"
)

type Model struct {
	width  int
	height int
	ready  bool

	ctx    context.Context
	styles Styles
	keymap KeyMap
	logger *log.Logger

	manager       *session.Manager
	client        *client.Client
	defaultAgent  string // slug to send turns at (v0.3.x: hardcoded "sunny")
	overlay       *Overlay
	initialCwd    string
	defaultModel  string
	defaultEffort string
	themeID       string // active theme id (may be AutoThemeID); persisted in state.json
	bgIsLight     bool   // last known terminal background polarity, refreshed via tea.BackgroundColorMsg
	skipPerms     bool
	lastErr       error

	// chat is the new list-based transcript viewport. It owns the full
	// drag-to-select-and-copy state machine (multi-click, backward drag,
	// auto-scroll past edges) so the parent Model just routes mouse + key
	// events through.
	chat *chatModel

	textarea textarea.Model
	spinner  spinner.Model

	md      *glamour.TermRenderer
	mdW     int
	mdCache map[string]string

	// Morphing-string spinner shown during assistant streaming. One global
	// instance — the active session's "Thinking" label drives it.
	thinkingAnim *anim.Anim

	// logoFrame is the gradient-sweep counter. Driven by logoTickMsg, read
	// by renderLogo to shift the per-column color ramp each tick.
	logoFrame int

	// sysStats holds the most recent CPU + RAM sample. Refreshed by a
	// background tick (sysStatsTickCmd → sysStatsResultMsg).
	sysStats sysstats.Stats

	// State persistence is debounced: every saveState() call just sets
	// saveDirty=true; a saveTickCmd flushes to disk at most once every
	// `saveFlushInterval`. This used to be a per-event MarshalIndent +
	// atomic rename of the full state.json (~150 KB), which on an active
	// transcript meant several MB/min of disk writes.
	saveDirty bool
}

type Options struct {
	Logger                   *log.Logger
	DefaultModel             string
	DefaultEffort            string
	DangerousSkipPermissions bool
	// InitialTheme is the persisted theme ID from state.json. Empty falls
	// back to the default (Themes[0]).
	InitialTheme string

	// DaemonAddr is "host:port" of the sunny daemon. Required for chat —
	// without it submitting a message returns ErrNoEngine. main.go
	// auto-starts the daemon and passes its addr here.
	DaemonAddr string
	// DefaultAgent is the slug to send turns at. v0.3.x hardcodes "sunny";
	// session-vs-agent picking lands later.
	DefaultAgent string
}

const (
	headerHeight   = 1
	statusHeight   = 1
	textareaMinH = 3
	textareaMaxH = 12
	// mainPadLeft adds breathing room between the chat / input and the
	// terminal's left edge. Rendered as PaddingLeft on the main column's
	// outer container; every "content width" calculation subtracts it so
	// nothing overflows.
	mainPadLeft = 2
	// mdCacheMax bounds the markdown render cache. Past this size we drop the
	// whole map — long sessions otherwise grow it forever.
	mdCacheMax = 512
	// inputTopGap is the number of empty rows reserved above the input
	// box so the assistant attribution row doesn't sit flush against the
	// input border. Reserved in layout() and rendered in renderMain().
	inputTopGap = 4
)

func NewModel(ctx context.Context, mgr *session.Manager, initialCwd string, opts Options) Model {
	// Apply the persisted theme before building styles so every Style picks
	// up the right palette on first render. Empty/unknown IDs fall back to
	// Auto so fresh installs follow the terminal background out of the box.
	themeID := opts.InitialTheme
	if themeID == "" {
		themeID = AutoThemeID
	}
	// Resolve Auto with bgIsLight=false at startup; the real background
	// arrives via tea.BackgroundColorMsg once the program is running and
	// repaint() will re-resolve if the terminal turns out to be light.
	resolved := ResolveTheme(themeID, false)
	SetPalette(resolved.P)
	st := DefaultStyles()
	km := DefaultKeyMap()

	logger := opts.Logger
	if logger == nil {
		logger = log.NewWithOptions(io.Discard, log.Options{})
	}

	// Editor — patterned exactly off Crush's textarea init at
	// /tmp/charm-crush/internal/ui/model/ui.go:271. Same init order, same
	// style application, same dynamic height, same cursor block + blink.
	ta := textarea.New()
	ta.SetStyles(st.EditorTextarea)
	ta.Placeholder = "escribe tu mensaje y enter para enviar (ctrl+j newline · \\+enter también)"
	ta.Prompt = "› "
	ta.CharLimit = -1
	ta.ShowLineNumbers = false
	// Virtual cursor: renders inline as a styled cell inside the textarea
	// View() output. Sidesteps the real-cursor offset math (Crush relies on
	// ultraviolet's exact layout coords; we don't have that machinery yet).
	ta.SetVirtualCursor(true)
	ta.DynamicHeight = true
	ta.MinHeight = textareaMinH
	ta.MaxHeight = textareaMaxH
	ta.KeyMap.WordForward = key.NewBinding(key.WithKeys("alt+right", "alt+f", "ctrl+right"))
	ta.KeyMap.WordBackward = key.NewBinding(key.WithKeys("alt+left", "alt+b", "ctrl+left"))
	ta.KeyMap.DeleteWordForward = key.NewBinding(key.WithKeys("alt+d", "ctrl+delete"))
	ta.KeyMap.DeleteWordBackward = key.NewBinding(key.WithKeys("alt+backspace", "ctrl+backspace"))
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter", "shift+enter"))
	// Disable textarea's built-in ctrl+v paste — it only knows how to read
	// text from the clipboard (atotto.ReadAll) and silently drops image-only
	// clipboards. We intercept ctrl+v ourselves so we can do the image-aware
	// paste flow first, falling back to text when no image is present.
	ta.KeyMap.Paste = key.NewBinding(key.WithDisabled())
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(colWarning)

	chatVP := newChatModel()
	chatVP.SetSize(80, 20)

	thinking := anim.New(anim.Settings{
		Size:       12,
		GradFrom:   colSecondary, // Dolly magenta
		GradTo:     colPrimary,   // Charple purple
		LabelColor: colText,      // Ash off-white
	})

	defModel := opts.DefaultModel
	if defModel == "" {
		defModel = "opus"
	}
	defEffort := opts.DefaultEffort
	if defEffort == "" {
		defEffort = "max"
	}

	var cli *client.Client
	if opts.DaemonAddr != "" {
		cli = client.New(opts.DaemonAddr)
	}
	defaultAgent := opts.DefaultAgent
	if defaultAgent == "" {
		defaultAgent = "sunny"
	}
	if cli != nil {
		// Attach every existing session (today: just the bootstrapped one)
		// to the same client + slug. Session-per-agent picking is a v0.4
		// concern.
		for _, s := range mgr.Sessions {
			s.AttachClient(cli, defaultAgent)
		}
	}

	return Model{
		ctx:           ctx,
		styles:        st,
		keymap:        km,
		logger:        logger,
		manager:       mgr,
		client:        cli,
		defaultAgent:  defaultAgent,
		overlay:       &Overlay{},
		chat:          chatVP,
		textarea:      ta,
		spinner:       sp,
		thinkingAnim:  thinking,
		initialCwd:    initialCwd,
		defaultModel:  defModel,
		defaultEffort: defEffort,
		themeID:       themeID,
		skipPerms:     opts.DangerousSkipPermissions,
	}
}

// waitForChatEvent reads one event off the SSE stream and surfaces it as
// a tea.Msg. The Update handler applies it back to the session, then
// re-arms the next wait via this same helper. On EOF or error the
// stream emits chatStreamDoneMsg instead.
func waitForChatEvent(sessionID string, stream *client.Stream) tea.Cmd {
	return func() tea.Msg {
		ev, ok, err := stream.Next()
		if err != nil {
			return chatStreamDoneMsg{SessionID: sessionID, Stream: stream, Err: err}
		}
		if !ok {
			return chatStreamDoneMsg{SessionID: sessionID, Stream: stream}
		}
		return chatEventMsg{SessionID: sessionID, Stream: stream, Event: ev}
	}
}

func (m Model) Init() tea.Cmd {
	// The logo gradient sweep ticks for the lifetime of the program. The
	// list-based chat is cheap enough now that we don't need to gate the
	// logo animation on activity — visually you always see the brand mark
	// breathing.
	//
	// tea.RequestBackgroundColor asks the terminal for its bg via OSC 11.
	// Bubbletea's input parser owns the response and surfaces it as
	// tea.BackgroundColorMsg, so we never have to worry about it leaking
	// to the textarea. bgPollCmd re-asks every 30s so macOS appearance
	// changes (Ghostty updates bg in lockstep) flip Auto themes within
	// half a minute without sub-second polling overhead.
	cmds := []tea.Cmd{textarea.Blink, branchTickCmd(), logoTickCmd(), sysStatsSampleCmd(), sysStatsTickCmd(), saveTickCmd(), tea.RequestBackgroundColor, bgPollCmd()}
	if m.anyThinking() {
		cmds = append(cmds, m.spinner.Tick)
	}
	return tea.Batch(cmds...)
}

func (m Model) anyThinking() bool {
	for _, s := range m.manager.Sessions {
		if s.State == session.StateThinking {
			return true
		}
	}
	return false
}

// collectTiles assembles the picker entries: one per session.
func (m Model) collectTiles() []TileItem {
	var items []TileItem
	for i, s := range m.manager.Sessions {
		items = append(items, TileItem{
			Kind:   "claude",
			Index:  i,
			Label:  s.Title,
			Detail: s.Cwd,
			Active: i == m.manager.Active,
		})
	}
	return items
}

// cycleTab moves focus to the next/prev session. Wraps.
func (m *Model) cycleTab(by int) {
	total := m.manager.Len()
	if total == 0 {
		return
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	next := ((m.manager.Active+by)%total + total) % total
	m.manager.Active = next
	if cur := m.manager.Current(); cur != nil {
		m.textarea.SetValue(cur.Draft)
		m.textarea.CursorEnd()
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
	}
}

// Update is the Bubble Tea entry point. It dispatches into focused
// sub-handlers (one per major message family) and ends with a fallthrough
// that lets the textarea + viewport see whatever no specific handler claimed.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if next, cmd, handled := m.updateAppMsg(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.updateMouse(msg); handled {
		return next, cmd
	}

	var cmds []tea.Cmd
	// Image paste: intercept BEFORE the textarea consumes the paste, so we
	// can swap binary clipboard content for an "[Image #N]" marker. If the
	// clipboard has no image, fall through and the textarea pastes text.
	pasteHandled := false
	if !m.overlay.HasOpen() {
		if pm, ok := msg.(tea.PasteMsg); ok {
			if m.tryImagePaste(pm.Content) {
				pasteHandled = true
			}
		}
	}
	if m.overlay.HasOpen() {
		if _, isKey := msg.(tea.KeyMsg); isKey {
			return m, m.overlay.UpdateTop(msg)
		}
		cmds = append(cmds, m.overlay.UpdateTop(msg))
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.applyResize(msg)
		// Some terminals (Ghostty included) flush a resize when the OS
		// appearance changes. Re-asking for bg here means Auto mode flips
		// before the next 30s poll fires.
		cmds = append(cmds, tea.RequestBackgroundColor)
	case tea.KeyMsg:
		next, cmd, handled := m.updateKey(msg)
		if handled {
			return next, cmd
		}
		m = next
		cmds = append(cmds, cmd)
	case chatEventMsg:
		cmds = append(cmds, m.handleChatEvent(msg))
	case chatStreamDoneMsg:
		m.handleChatStreamDone(msg)
	case spinner.TickMsg:
		next, cmd := m.handleSpinnerTick(msg)
		m = next
		cmds = append(cmds, cmd)
	case anim.StepMsg:
		cmds = append(cmds, m.handleAnimStep(msg))
	case branchTickMsg:
		cmds = append(cmds, m.handleBranchTick())
	case logoTickMsg:
		m.logoFrame++
		cmds = append(cmds, logoTickCmd())
	case sysStatsTickMsg:
		// Tick fires the actual sample (off-thread); sample posts back via
		// sysStatsResultMsg. We re-arm the tick from the result handler so
		// a hung `top` doesn't queue overlapping samples.
		cmds = append(cmds, sysStatsSampleCmd())
	case sysStatsResultMsg:
		m.sysStats = msg.Stats
		cmds = append(cmds, sysStatsTickCmd())
	case saveTickMsg:
		if m.saveDirty {
			m.flushState()
		}
		cmds = append(cmds, saveTickCmd())
	case tea.BackgroundColorMsg:
		// Terminal told us its current background. Every paired theme
		// auto-flips dark↔light when the polarity changes (ResolveTheme
		// handles the swap), so we repaint unconditionally — not just on
		// AutoThemeID. Without this, switching macOS appearance leaves
		// the user staring at e.g. dark Tokyo Night on a freshly-light
		// terminal until they re-pick the theme manually.
		nowLight := !msg.IsDark()
		if nowLight != m.bgIsLight {
			m.bgIsLight = nowLight
			m.repaint(m.themeID)
		}
	case bgPollMsg:
		// Re-ask the terminal so macOS theme switches (Ghostty repaints
		// its bg) propagate without a restart. Cheap: ~30 OSC 11 round
		// trips per minute is rounding error.
		cmds = append(cmds, tea.RequestBackgroundColor, bgPollCmd())
	}

	if !m.overlay.HasOpen() && !pasteHandled {
		var cmd tea.Cmd
		prevValue := m.textarea.Value()
		// Snapshot whether the user was reading the latest message
		// BEFORE the textarea consumes the key. If they were pinned to
		// bottom and typing grows the textarea, we want to stay pinned
		// after layout shrinks the viewport; if they had scrolled up
		// to read history, we leave their position alone.
		wasAtBottom := m.chat.AtBottom()
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
		if m.textarea.Value() != prevValue {
			m.syncAttachmentMarkers() // drop attachments whose marker the user broke
			m.layout()                // dynamic textarea height
			if wasAtBottom {
				m.chat.ScrollToBottom()
			}
		}
		// Keyboard scroll: PgUp/PgDown/Home/End forward to the chat list.
		// Other keys are textarea territory.
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "pgup":
				m.chat.PageUp()
			case "pgdown":
				m.chat.PageDown()
			case "home":
				m.chat.ScrollToTop()
			case "end":
				m.chat.ScrollToBottom()
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) applyResize(msg tea.WindowSizeMsg) {
	m.width, m.height = msg.Width, msg.Height
	m.layout()
	m.md = nil
	m.mdCache = nil
	m.refreshViewport()
	m.ready = true
}

func (m *Model) layout() {
	mainW := m.width - sidebarWidth - sidebarGap - mainPadLeft
	if mainW < 20 {
		mainW = 20
	}

	// textarea grows itself via DynamicHeight; we just read what it picked.
	taH := m.textarea.Height()
	if taH < textareaMinH {
		taH = textareaMinH
	}

	bodyH := m.height - headerHeight - statusHeight
	if bodyH < 6 {
		bodyH = 6
	}
	inputBoxH := taH + 2 // border
	hintH := 1           // hint row below input
	gapH := inputTopGap  // breathing room above the input box
	vpH := bodyH - inputBoxH - hintH - gapH
	if vpH < 3 {
		vpH = 3
	}
	m.chat.SetSize(mainW, vpH)

	taW := mainW - 4
	if taW < 10 {
		taW = 10
	}
	m.textarea.SetWidth(taW)
}

// refreshViewport rebuilds the chat list from the active session's items.
// The chat list owns its own render cache + selection overlay so this is
// idempotent and cheap when the items array hasn't changed.
func (m *Model) refreshViewport() {
	cur := m.manager.Current()
	if cur == nil {
		m.chat.SetItems(nil)
		return
	}
	if len(cur.Items) == 0 && cur.State == session.StateIdle {
		// Render the welcome screen as a single pseudo-item so the list
		// shows it. Wrapping a plain string in a stringItem keeps the
		// list machinery happy without a special-case render path.
		m.chat.SetItems([]list.Item{stringItem(m.welcomeText())})
		return
	}
	ctx := RenderContext{
		Width:     m.chat.Width(),
		Styles:    m.styles,
		LiveFrame: m.spinner.View(),
		Markdown:  m.markdown,
		ModelName: cur.Model,
	}
	if ctx.Width <= 0 {
		ctx.Width = m.width
	}
	items := buildChatItems(cur, ctx)
	if cur.State == session.StateThinking {
		label := cur.LiveStatus()
		if label == "" {
			label = "thinking"
		}
		m.thinkingAnim.SetLabel(label)
		items = append(items, stringItem("  "+m.thinkingAnim.Render()))
	}
	m.chat.SetItems(items)
}

func (m *Model) welcomeText() string {
	s := m.styles
	bullet := s.Hint.Render("•")
	row := func(k, d string) string {
		return bullet + " " + s.StatusKey.Render(k) + " " + s.Hint.Render(d)
	}
	return strings.Join([]string{
		s.HeaderLogo.Render("☀ sunny"),
		"",
		s.AssistantText.Render("escribe un mensaje para empezar a chatear con claude code"),
		"",
		row("ctrl+n", "añade una sesión en otro directorio"),
		row("ctrl+r", "renombrar la sesión actual"),
		row("ctrl+l", "resetear el chat (nueva conversación)"),
		row("ctrl+d", "ver el diff del repo"),
		row("tab   ", "cambia entre sesiones"),
		row("ctrl+w", "cierra la sesión actual"),
		row("ctrl+c", "cancela el turno actual (no toca el input)"),
		row("esc   ", "sale de sunny"),
		"",
		row("ctrl+j / alt+enter", "nueva línea"),
		row("ctrl+←/→", "saltar palabras"),
		row("ctrl+del", "borrar palabra"),
		row("pgup/pgdn", "scroll del chat"),
		row("home/end", "saltar al inicio / fin"),
	}, "\n")
}

func (m *Model) markdown(text string) string {
	w := m.chat.Width() - 4
	if w < 20 {
		w = 20
	}
	if m.md == nil || m.mdW != w {
		m.mdCache = nil
		// Force chroma to re-register its style under the active palette.
		// See resetChromaStyle for why glamour caches it globally.
		resetChromaStyle()
		r, err := glamour.NewTermRenderer(
			// Themed style derived from the active palette, so headers,
			// links, code, syntax highlighting, etc. all swap with ctrl+s.
			glamour.WithStyles(markdownStyleConfig()),
			glamour.WithWordWrap(w),
			glamour.WithEmoji(),
		)
		if err != nil {
			return wrap(text, w)
		}
		m.md = r
		m.mdW = w
	}
	if m.mdCache == nil {
		m.mdCache = map[string]string{}
	}
	if cached, ok := m.mdCache[text]; ok {
		return cached
	}
	out, err := m.md.Render(text)
	if err != nil {
		return wrap(text, w)
	}
	rendered := strings.TrimRight(out, "\n")
	if len(m.mdCache) >= mdCacheMax {
		// Cheap eviction: drop everything once the cache fills up. Re-renders
		// will warm it again over the next few frames.
		m.mdCache = map[string]string{}
	}
	m.mdCache[text] = rendered
	return rendered
}

func (m Model) Run(ctx context.Context) error {
	m.pruneOrphanImages()
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithFilter(mouseEventFilter),
	)
	go func() {
		<-ctx.Done()
		p.Quit()
	}()
	finalModel, err := p.Run()
	// Persist before tearing down sessions so we capture the final draft.
	// We bypass the dirty-bit check here — even if no event marked the
	// state dirty in the last 5s, we want shutdown to leave a fresh
	// snapshot on disk (textarea content + active tab index can change
	// silently without going through saveState()).
	if fm, ok := finalModel.(Model); ok {
		fm.flushStateNow()
	} else {
		m.flushStateNow()
	}
	if m.manager != nil {
		m.manager.CloseAll()
	}
	return err
}

