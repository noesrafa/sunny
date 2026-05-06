// Package onboarding is the interactive first-run flow (and re-runnable
// "manual doctor") behind `sunny onboarding`. It walks the user through
// every external dependency sunny touches — tailscale, brew + tap,
// claude-code, opencode, ollama key — and sets up a default agent.
//
// Each step is idempotent: probe first, render current state, offer the
// fix only when something's missing. Skippable everywhere with `s`. The
// command is safe to re-run any time.
package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/doctor"
	"github.com/noesrafa/sunny/internal/secrets"
	"github.com/noesrafa/sunny/internal/tsnet"
)

// stepID enumerates the steps in display order. Inserting a new step
// is two lines: add the constant + add a descriptor in stepDescs.
type stepID int

const (
	stepWelcome stepID = iota
	stepTailscale
	stepBrew
	stepClaudeCode
	stepOpencode
	stepOllama
	stepAgent
	stepDone
	numSteps
)

// stepStatus is the rolled-up outcome the Done summary renders.
type stepStatus int

const (
	statusPending stepStatus = iota // not visited yet
	statusOK                        // already in place / installed successfully
	statusSkipped                   // user pressed s
	statusFailed                    // install errored, key empty, etc.
)

// Model is the top-level bubbletea program. Constructed by the
// `sunny onboarding` command, runs end-to-end, exits when the user
// reaches stepDone or hits Ctrl+C.
type Model struct {
	root  string
	addr  string
	token string
	hc    *client.Client

	step    stepID
	results map[stepID]stepStatus

	// Probes — populated at Init time. Re-fetched at the end so the
	// summary reflects post-install state.
	probes probesSnapshot

	// Per-step input
	ollamaInput     textinput.Model
	agentNameInput  textinput.Model
	agentPromptArea textarea.Model

	// Subprocess tracking — at most one install runs at a time.
	running    bool
	runStep    stepID
	runLabel   string
	runStarted time.Time
	runOut     string
	runErr     error
	spinner    spinner.Model

	// doctorCache holds the final-summary probe results, computed
	// ONCE when the user first reaches stepDone. Without this, the
	// View function (which is called on every render tick) would re-
	// shell out to claude/opencode/tailscale every frame, making the
	// Done screen feel laggy and ctrl+c slow to respond.
	doctorCache    *doctor.Report
	doctorCachedAt time.Time

	// quitting flag drives the brief "opening sunny tui…" splash that
	// renders between the user pressing enter on Done and the program
	// exiting. Avoids a black flicker when the parent shell launches
	// the TUI.
	quitting bool

	// launchTUI is set when the user presses enter on Done — after
	// the bubbletea program returns, the cmd handler reads this to
	// decide whether to chain `sunny tui`. ctrl+c / esc paths leave
	// it false so accidental quits don't auto-launch the TUI.
	launchTUI bool

	// UI
	width, height int
	flash         string // ephemeral status line
}

// probesSnapshot collects all the "is this thing on the system?"
// answers in one shot. We refetch at strategic moments rather than
// every render — most of these are fork+exec calls.
type probesSnapshot struct {
	tailscale  bool
	brew       bool
	tap        bool
	claudeCode bool
	opencode   bool

	ollamaKey   bool
	agentLoaded *client.AgentDetail
}

// New constructs the onboarding model. addr/token come from the
// daemon (the caller is responsible for auto-starting it before
// invoking onboarding). root is the runtime dir for fallback file
// reads when the daemon isn't reachable.
func New(root, addr, token string) (*Model, error) {
	hc := client.NewFromBase("http://"+addr, token)

	ollamaIn := textinput.New()
	ollamaIn.Placeholder = "Ollama Cloud API key (paste it here)"
	ollamaIn.Prompt = "› "
	ollamaIn.CharLimit = 256

	nameIn := textinput.New()
	nameIn.Placeholder = "agent name (e.g. Franky, Bob, Alex…)"
	nameIn.Prompt = "› "
	nameIn.CharLimit = 64

	promptIn := textarea.New()
	promptIn.Placeholder = "system prompt — define the agent's persona / behaviour"
	promptIn.SetHeight(8)
	promptIn.CharLimit = 0

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &Model{
		root:            root,
		addr:            addr,
		token:           token,
		hc:              hc,
		step:            stepWelcome,
		results:         map[stepID]stepStatus{},
		ollamaInput:     ollamaIn,
		agentNameInput:  nameIn,
		agentPromptArea: promptIn,
		spinner:         sp,
	}, nil
}

// probesDoneMsg lands the result of the initial parallel probe pass.
type probesDoneMsg probesSnapshot

// installFinishedMsg ends an in-flight subprocess. step is the step
// that triggered it so the result lands in the right place.
type installFinishedMsg struct {
	step   stepID
	output string
	err    error
}

// flashMsg sets the ephemeral status line for ~3s.
type flashMsg string

// flashClearMsg clears the flash.
type flashClearMsg struct{}

// quitTickMsg fires after a brief splash on the Done screen so the
// transition into `sunny tui` is visually graceful instead of a
// black flicker.
type quitTickMsg struct{}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.runProbesCmd(),
	)
}

// runProbesCmd runs every external probe in parallel and emits one
// probesDoneMsg with the full snapshot. Background calls (claude, tap)
// take a few hundred ms each; the whole thing finishes in ~1s on a
// warm cache.
func (m *Model) runProbesCmd() tea.Cmd {
	hc := m.hc
	return func() tea.Msg {
		snap := probesSnapshot{}
		snap.tailscale = tsnet.Available()
		snap.brew = onPath("brew")
		snap.claudeCode = onPath("claude")
		snap.opencode = onPath("opencode")
		if snap.brew {
			snap.tap = brewTapPresent("noesrafa/tap")
		}
		if store, err := secrets.New(""); err == nil {
			_ = store // not used directly
		}
		// Use the daemon for daemon-owned state (secrets list, default
		// agent). Falls back gracefully if the daemon is offline —
		// onboarding still works, just shows everything as not-set.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if list, err := hc.ListSecrets(ctx); err == nil {
			for _, p := range list {
				if p.Provider == "ollama" {
					for _, f := range p.Fields {
						if f == "api_key" {
							snap.ollamaKey = true
						}
					}
				}
			}
		}
		if det, err := hc.GetAgent(ctx, "sunny"); err == nil {
			snap.agentLoaded = det
		}
		return probesDoneMsg(snap)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.ollamaInput.SetWidth(m.boxInnerWidth())
		m.agentNameInput.SetWidth(m.boxInnerWidth())
		m.agentPromptArea.SetWidth(m.boxInnerWidth())
		return m, nil
	case probesDoneMsg:
		m.probes = probesSnapshot(v)
		// Pre-fill the agent inputs from the loaded default agent.
		if m.probes.agentLoaded != nil {
			if m.agentNameInput.Value() == "" {
				m.agentNameInput.SetValue(m.probes.agentLoaded.Name)
			}
			if m.agentPromptArea.Value() == "" {
				m.agentPromptArea.SetValue(m.probes.agentLoaded.Prompt)
			}
		}
		// Mark already-satisfied steps so the Done summary renders ✓.
		if m.probes.tailscale {
			m.results[stepTailscale] = statusOK
		}
		if m.probes.brew && m.probes.tap {
			m.results[stepBrew] = statusOK
		}
		if m.probes.claudeCode {
			m.results[stepClaudeCode] = statusOK
		}
		if m.probes.opencode {
			m.results[stepOpencode] = statusOK
		}
		if m.probes.ollamaKey {
			m.results[stepOllama] = statusOK
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case installFinishedMsg:
		m.running = false
		m.runOut = v.output
		m.runErr = v.err
		if v.err == nil {
			m.results[v.step] = statusOK
			m.flash = m.runLabel + " ✓"
			// Auto-advance after a successful action so the user
			// doesn't have to press a second enter to move forward.
			// Probes refresh in the background — the next step's view
			// uses the freshest data when it lands.
			if m.step == v.step {
				m.advance()
			}
		} else {
			m.results[v.step] = statusFailed
			m.flash = m.runLabel + " failed: " + firstLine(v.output)
		}
		cmds := []tea.Cmd{m.runProbesCmd(), m.flashAfter(4 * time.Second)}
		// If we just landed on stepDone, kick off the doctor probe in
		// the background so the summary is ready by the time the user
		// looks at it.
		if m.step == stepDone && m.doctorCache == nil {
			cmds = append(cmds, m.runDoctorReportCmd())
		}
		return m, tea.Batch(cmds...)
	case doctorReportMsg:
		m.doctorCache = &v.report
		m.doctorCachedAt = time.Now()
		return m, nil
	case flashMsg:
		m.flash = string(v)
		return m, m.flashAfter(3 * time.Second)
	case flashClearMsg:
		m.flash = ""
		return m, nil
	case quitTickMsg:
		return m, tea.Quit
	case tea.KeyMsg:
		return m.handleKey(v)
	}
	// Pass remaining messages to whatever input is focused on the
	// current step so blink/cursor work.
	switch m.step {
	case stepOllama:
		var cmd tea.Cmd
		m.ollamaInput, cmd = m.ollamaInput.Update(msg)
		return m, cmd
	case stepAgent:
		var cmds []tea.Cmd
		var c tea.Cmd
		m.agentNameInput, c = m.agentNameInput.Update(msg)
		cmds = append(cmds, c)
		m.agentPromptArea, c = m.agentPromptArea.Update(msg)
		cmds = append(cmds, c)
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// handleKey routes keyboard input. Navigation keys (esc, ctrl+c,
// b/←, →/l/f, s) are universal; per-step bindings are dispatched
// below. Anything we don't consume falls through to the focused
// input on steps that have one (so typed characters reach the
// textinput / textarea).
func (m *Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.running {
		// Refuse most input while an install is in flight. Esc/Ctrl+C
		// abort the whole onboarding (the subprocess is detached
		// enough that it'll finish on its own; we just stop waiting).
		switch k.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		}
		return m, nil
	}
	s := k.String()

	// Universal navigation. ctrl+c always quits. esc on welcome
	// quits; on the input-heavy steps (Ollama, Agent) the per-step
	// handler claims it for "back" so the user can edit freely;
	// elsewhere esc is "back". Done's esc is "back to agent" and is
	// handled per-step below.
	switch s {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.step == stepWelcome {
			return m, tea.Quit
		}
		// Defer to per-step handlers for stepOllama/stepAgent/stepDone
		// so the input-heavy steps can intercept esc cleanly.
		if m.step == stepOllama || m.step == stepAgent || m.step == stepDone {
			break
		}
		m.step--
		return m, nil
	}

	// Per-step bindings come before generic nav so a focused input
	// can claim keys like `s` or arrow keys (e.g. typing "ssh" in
	// the prompt area shouldn't skip the step).
	switch m.step {
	case stepWelcome:
		if s == "enter" || s == "right" || s == "l" || s == "f" {
			m.advance()
		}
		return m, nil
	case stepTailscale:
		if s == "enter" || s == "right" || s == "l" || s == "f" || s == "s" {
			m.advance()
			return m, nil
		}
		if s == "b" || s == "left" {
			return m.goBack()
		}
		return m, nil
	case stepBrew:
		if s == "b" || s == "left" {
			return m.goBack()
		}
		if s == "s" {
			return m.skipCurrent()
		}
		if s == "right" || s == "l" || s == "f" {
			// Forward arrow advances unconditionally. Treat as skip
			// when the step's action wasn't taken; treat as
			// "continue" when it's already ✓.
			if m.results[m.step] != statusOK {
				m.results[m.step] = statusSkipped
			}
			m.advance()
			return m, nil
		}
		if s == "enter" {
			if !m.probes.brew {
				m.flash = "install Homebrew first: see https://brew.sh"
				return m, m.flashAfter(4 * time.Second)
			}
			if !m.probes.tap {
				return m, m.runInstallCmd(stepBrew, "adding tap noesrafa/tap",
					"brew", "tap", "noesrafa/tap")
			}
			m.advance()
		}
		return m, nil
	case stepClaudeCode:
		if s == "b" || s == "left" {
			return m.goBack()
		}
		if s == "s" {
			return m.skipCurrent()
		}
		if s == "right" || s == "l" || s == "f" {
			if m.results[m.step] != statusOK {
				m.results[m.step] = statusSkipped
			}
			m.advance()
			return m, nil
		}
		if s == "enter" {
			if m.probes.claudeCode {
				m.advance()
				return m, nil
			}
			if !m.probes.brew {
				m.flash = "need Homebrew first — go back and finish that step"
				return m, m.flashAfter(4 * time.Second)
			}
			return m, m.runInstallCmd(stepClaudeCode, "installing claude-code",
				"brew", "install", "anthropics/claude-code/claude-code")
		}
		return m, nil
	case stepOpencode:
		if s == "b" || s == "left" {
			return m.goBack()
		}
		if s == "s" {
			return m.skipCurrent()
		}
		if s == "right" || s == "l" || s == "f" {
			if m.results[m.step] != statusOK {
				m.results[m.step] = statusSkipped
			}
			m.advance()
			return m, nil
		}
		if s == "enter" {
			if m.probes.opencode {
				m.advance()
				return m, nil
			}
			if !m.probes.brew {
				m.flash = "need Homebrew first — go back and finish that step"
				return m, m.flashAfter(4 * time.Second)
			}
			return m, m.runInstallCmd(stepOpencode, "installing opencode",
				"brew", "install", "sst/tap/opencode")
		}
		return m, nil
	case stepOllama:
		// Input-heavy step. Only intercept the keys we explicitly own
		// (enter/esc/tab) — every other keystroke (arrow keys, char
		// input, paste, backspace, ctrl+a/e for word-jump, …) goes to
		// the textinput. Skip with `s` only when the input is empty,
		// otherwise the user can't actually type the letter "s".
		switch s {
		case "enter":
			val := strings.TrimSpace(m.ollamaInput.Value())
			if val == "" {
				if m.probes.ollamaKey {
					m.advance()
					return m, nil
				}
				m.flash = "paste a key first, or press esc to skip"
				return m, m.flashAfter(3 * time.Second)
			}
			m.running = true
			m.runStep = stepOllama
			m.runLabel = "saving ollama key"
			m.runStarted = time.Now()
			return m, tea.Batch(m.spinner.Tick, m.saveOllamaKeyCmd(val))
		case "tab":
			return m, nil
		case "esc":
			return m.goBack()
		}
		// Skip-with-`s` only when the input is empty (otherwise typing
		// "s" on a key would yank the user out mid-edit).
		if m.ollamaInput.Value() == "" && s == "s" {
			return m.skipCurrent()
		}
		// Forward everything else to the textinput.
		if !m.ollamaInput.Focused() {
			m.ollamaInput.Focus()
		}
		var cmd tea.Cmd
		m.ollamaInput, cmd = m.ollamaInput.Update(k)
		return m, cmd
	case stepAgent:
		// Input-heavy step. Only consume tab (focus toggle), enter
		// (save), ctrl+s (alias), esc (back). Everything else —
		// including ALL arrow keys, backspace, character input —
		// forwards to the focused input. There is intentionally no
		// `s`/arrow skip here: the agent inputs are pre-filled with
		// Franky + Sunny prompt, so "skip" is "press enter to save the
		// current values unchanged".
		switch s {
		case "tab":
			if m.agentNameInput.Focused() {
				m.agentNameInput.Blur()
				m.agentPromptArea.Focus()
			} else {
				m.agentPromptArea.Blur()
				m.agentNameInput.Focus()
			}
			return m, nil
		case "enter":
			// Enter saves from name input. From the textarea, enter
			// inserts a newline (so multi-line prompts compose
			// naturally); ctrl+s saves from the textarea.
			if m.agentPromptArea.Focused() {
				var cmd tea.Cmd
				m.agentPromptArea, cmd = m.agentPromptArea.Update(k)
				return m, cmd
			}
			m.running = true
			m.runStep = stepAgent
			m.runLabel = "saving agent"
			m.runStarted = time.Now()
			return m, tea.Batch(m.spinner.Tick, m.saveAgentCmd())
		case "ctrl+s":
			m.running = true
			m.runStep = stepAgent
			m.runLabel = "saving agent"
			m.runStarted = time.Now()
			return m, tea.Batch(m.spinner.Tick, m.saveAgentCmd())
		case "esc":
			return m.goBack()
		}
		// Forward everything else to whichever input is focused.
		if !m.agentNameInput.Focused() && !m.agentPromptArea.Focused() {
			m.agentNameInput.Focus()
		}
		var cmd tea.Cmd
		if m.agentNameInput.Focused() {
			m.agentNameInput, cmd = m.agentNameInput.Update(k)
		} else {
			m.agentPromptArea, cmd = m.agentPromptArea.Update(k)
		}
		return m, cmd
	case stepDone:
		// Final step: enter triggers the "opening sunny tui…" splash
		// then exits cleanly so the cmd handler can chain into
		// `sunny tui`. esc / ← / b go back to the agent step. ctrl+c
		// (handled at the top) hard-quits without the splash.
		switch s {
		case "enter":
			m.quitting = true
			m.launchTUI = true
			// 250 ms splash so the user sees the transition message
			// instead of a black flicker before the TUI takes over.
			// quitTickMsg is handled in Update → tea.Quit.
			return m, tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return quitTickMsg{} })
		case "esc", "left", "b":
			m.step--
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// ShouldLaunchTUI reports whether the user finished onboarding with
// enter on the Done screen. The cmd handler reads this after Run()
// returns to decide whether to exec `sunny tui`.
func (m *Model) ShouldLaunchTUI() bool { return m.launchTUI }

// goBack moves to the previous step. Common helper because several
// step branches need it.
func (m *Model) goBack() (tea.Model, tea.Cmd) {
	if m.step > stepWelcome {
		m.step--
	}
	// Reset agent-dirty when leaving the agent step so a return
	// visit re-enables nav without typing.
	return m, nil
}

// skipCurrent marks the current step as skipped and advances. Common
// helper so each step branch doesn't repeat the bookkeeping.
func (m *Model) skipCurrent() (tea.Model, tea.Cmd) {
	if m.step == stepWelcome || m.step == stepDone {
		return m, nil
	}
	if m.results[m.step] != statusOK {
		m.results[m.step] = statusSkipped
	}
	return m, m.advanceCmd()
}

// advance moves to the next step or quits when stepDone is reached.
func (m *Model) advance() {
	if m.step+1 >= numSteps {
		// stepDone is the last visible state — no further advance.
		return
	}
	m.step++
	// Re-arm focus on stepwise inputs.
	switch m.step {
	case stepOllama:
		m.ollamaInput.Focus()
	case stepAgent:
		m.agentNameInput.Focus()
	}
}

// advanceCmd is the value-returning sibling of advance — it bumps
// the step pointer AND kicks off the background doctor probe if we
// just landed on stepDone. Used by the skip-via-key paths that don't
// route through installFinishedMsg (where the install handler
// already triggers the probe).
func (m *Model) advanceCmd() tea.Cmd {
	prev := m.step
	m.advance()
	if prev != stepDone && m.step == stepDone && m.doctorCache == nil {
		return m.runDoctorReportCmd()
	}
	return nil
}

func (m *Model) flashAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return flashClearMsg{} })
}

// runInstallCmd shells out a subprocess and emits installFinishedMsg
// when it returns. Output is captured combined (stdout+stderr) so a
// failure surfaces every error line in the result panel.
func (m *Model) runInstallCmd(step stepID, label, name string, args ...string) tea.Cmd {
	m.running = true
	m.runStep = step
	m.runLabel = label
	m.runStarted = time.Now()
	m.runOut = ""
	m.runErr = nil
	cmd := exec.Command(name, args...)
	return func() tea.Msg {
		out, err := cmd.CombinedOutput()
		return installFinishedMsg{step: step, output: string(out), err: err}
	}
}

// saveOllamaKeyCmd writes the pasted key to ~/.sunny/secrets.yaml via
// the daemon (which triggers an engine rebuild so the new key takes
// effect on the next turn without a restart).
func (m *Model) saveOllamaKeyCmd(key string) tea.Cmd {
	hc := m.hc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hc.PutSecrets(ctx, "ollama", map[string]string{"api_key": key}); err != nil {
			return installFinishedMsg{step: stepOllama, err: err}
		}
		return installFinishedMsg{step: stepOllama, output: "ollama.api_key configured"}
	}
}

// saveAgentCmd patches the default agent's name + prompt and advances.
func (m *Model) saveAgentCmd() tea.Cmd {
	hc := m.hc
	name := strings.TrimSpace(m.agentNameInput.Value())
	prompt := m.agentPromptArea.Value()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		patch := client.AgentPatch{}
		if name != "" {
			patch.Name = &name
		}
		patch.Prompt = &prompt
		if _, err := hc.UpdateAgent(ctx, "sunny", patch); err != nil {
			return installFinishedMsg{step: stepAgent, err: err}
		}
		return installFinishedMsg{step: stepAgent, output: "agent updated: " + name}
	}
}

// onPath is a tiny exec.LookPath wrapper that returns bool only —
// most callers don't care about the resolved path.
func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// brewTapPresent reports whether `brew tap` lists the given tap.
// Cheap (~30 ms warm) and avoids parsing brew's tap registry by hand.
func brewTapPresent(name string) bool {
	out, err := exec.Command("brew", "tap").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// stepStatusIcon maps a stepStatus to its icon-styled fragment. Used
// in the Done summary and the per-step header.
func stepStatusIcon(st stepStatus) string {
	switch st {
	case statusOK:
		return "✓"
	case statusSkipped:
		return "·"
	case statusFailed:
		return "✗"
	}
	return " "
}

// doctorReport returns the cached probe report, computing it on
// first call. Caching is essential: the View function fires on every
// render and `doctor.Run` shells out to claude/opencode/tailscale —
// re-running it dozens of times per second made the Done screen
// laggy and slowed ctrl+c response. We cache for the lifetime of the
// onboarding session; users who want a fresh report can re-run
// `sunny onboarding`.
func (m *Model) doctorReport() doctor.Report {
	if m.doctorCache != nil {
		return *m.doctorCache
	}
	rep := doctor.Run(m.root)
	m.doctorCache = &rep
	m.doctorCachedAt = time.Now()
	return rep
}

// doctorReportMsg lands the result of an async doctor probe. We run
// it off the render loop so the user sees the Done screen instantly
// while the probes complete in the background; once the message
// arrives we swap the cache and re-render.
type doctorReportMsg struct {
	report doctor.Report
}

// runDoctorReportCmd is fired when the user first reaches stepDone.
// It runs every probe in a goroutine so the Done view paints
// immediately (with a "running…" placeholder) and the real summary
// fills in when probes complete — typically <1 s.
func (m *Model) runDoctorReportCmd() tea.Cmd {
	root := m.root
	return func() tea.Msg {
		return doctorReportMsg{report: doctor.Run(root)}
	}
}

// stripANSI just for safe rendering of subprocess output captured
// from brew (which emits color codes).
func stripANSI(s string) string {
	// Crude but effective: drop ESC[...m sequences. Same trick the
	// doctor package uses on `claude --version`.
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// bytesUsed returns a short string for "elapsed" on a running
// subprocess. The renderer uses it for the spinner line.
func elapsed(start time.Time) string {
	return fmt.Sprintf("%.0fs", time.Since(start).Seconds())
}
