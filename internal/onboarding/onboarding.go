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
	agentDirty      bool // true once user touched name/prompt — gates "skip" auto-advance

	// Subprocess tracking — at most one install runs at a time.
	running    bool
	runStep    stepID
	runLabel   string
	runStarted time.Time
	runOut     string
	runErr     error
	spinner    spinner.Model

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
			snap.tap = brewTapPresent("noesrafa/sunny")
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
		} else {
			m.results[v.step] = statusFailed
			m.flash = m.runLabel + " failed"
		}
		// Refresh probes so subsequent steps see the new state.
		return m, tea.Batch(m.runProbesCmd(), m.flashAfter(3*time.Second))
	case flashMsg:
		m.flash = string(v)
		return m, m.flashAfter(3 * time.Second)
	case flashClearMsg:
		m.flash = ""
		return m, nil
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

// handleKey routes keyboard input. Universal keys (esc, ctrl+c, b for
// back) work everywhere; per-step keys are dispatched below.
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
	switch s {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		// On the welcome / done step esc quits; elsewhere it's a
		// "go back" so users don't lose state by accident.
		if m.step == stepWelcome || m.step == stepDone {
			return m, tea.Quit
		}
		m.step--
		return m, nil
	case "b", "left":
		if m.step > stepWelcome {
			m.step--
		}
		return m, nil
	case "s":
		// Skip current step. Welcome / Done don't have skip semantics.
		if m.step != stepWelcome && m.step != stepDone {
			if m.results[m.step] != statusOK {
				m.results[m.step] = statusSkipped
			}
			m.advance()
		}
		return m, nil
	}

	// Per-step bindings.
	switch m.step {
	case stepWelcome:
		if s == "enter" {
			m.advance()
		}
		return m, nil
	case stepTailscale:
		if s == "enter" {
			m.advance() // there's nothing to install — daemon is system-level
		}
		return m, nil
	case stepBrew:
		if s == "enter" {
			if !m.probes.brew {
				m.flash = "install Homebrew first: see https://brew.sh"
				return m, m.flashAfter(4 * time.Second)
			}
			if !m.probes.tap {
				return m, m.runInstallCmd(stepBrew, "adding tap noesrafa/sunny",
					"brew", "tap", "noesrafa/sunny")
			}
			m.advance()
		}
		return m, nil
	case stepClaudeCode:
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
		if s == "enter" {
			val := strings.TrimSpace(m.ollamaInput.Value())
			if val == "" {
				if m.probes.ollamaKey {
					m.advance()
					return m, nil
				}
				m.flash = "paste a key first, or press s to skip"
				return m, m.flashAfter(3 * time.Second)
			}
			return m, m.saveOllamaKeyCmd(val)
		}
		// Make sure the key textinput stays focused.
		if !m.ollamaInput.Focused() {
			m.ollamaInput.Focus()
		}
		return m, nil
	case stepAgent:
		if s == "tab" {
			if m.agentNameInput.Focused() {
				m.agentNameInput.Blur()
				m.agentPromptArea.Focus()
			} else {
				m.agentPromptArea.Blur()
				m.agentNameInput.Focus()
			}
			return m, nil
		}
		if s == "ctrl+s" {
			return m, m.saveAgentCmd()
		}
		if s == "enter" {
			// Enter on the agent step submits ONLY when the prompt
			// textarea isn't focused (otherwise enter inserts a newline).
			if !m.agentPromptArea.Focused() {
				return m, m.saveAgentCmd()
			}
		}
		// Make sure something is focused.
		if !m.agentNameInput.Focused() && !m.agentPromptArea.Focused() {
			m.agentNameInput.Focus()
		}
		return m, nil
	case stepDone:
		if s == "enter" || s == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	return m, nil
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

// freshDoctorReport runs the read-only doctor probes against root.
// Used only for the final "done" summary, so the user sees the same
// status they'd get from `sunny doctor`.
func freshDoctorReport(root string) doctor.Report {
	return doctor.Run(root)
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
