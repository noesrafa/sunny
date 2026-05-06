package onboarding

// Uninstall is a separate bubbletea program that walks the user
// through `sunny uninstall` with the same look as the onboarding
// flow: a centered card with the brand mark on top, framed
// confirmation prompts, and visible consequences for each action.
//
// Lives in this package because it shares Style + the Logo wrapper
// + the same card-rendering helpers. Keeping both the onboarding
// and the uninstall in one place means the visual language stays
// in lockstep without one drifting from the other.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/tui"
)

// uninstallStep enumerates the uninstall flow stages. The model
// transitions linearly: confirm → stop → data → binary → done.
type uninstallStep int

const (
	uniConfirm uninstallStep = iota
	uniData
	uniBinary
	uniDone
)

// UninstallOptions configures the model. Cliflag wiring lives in
// cmd/sunny/uninstall_cmd.go.
type UninstallOptions struct {
	Root     string // ~/.sunny
	Yes      bool   // skip every prompt
	KeepData bool   // never offer to delete ~/.sunny/
}

// uninstallModel drives the flow.
type uninstallModel struct {
	opts UninstallOptions

	step uninstallStep

	// Probed once at Init.
	binaryPath string
	viaBrew    bool
	hasTap     bool
	rootExists bool

	// Per-step decisions.
	deleteData    bool // user answered yes to "delete ~/.sunny/"
	deleteBinary  bool // user answered yes to "delete the binary"
	removeTap     bool // user answered yes to "remove brew tap"
	selection     int  // 0 = No (default), 1 = Yes, for the active prompt

	// Action state.
	running  bool
	runLabel string
	runStart time.Time
	spinner  spinner.Model

	// Outcome log for the Done summary.
	outcomes []string

	width, height int
}

type uninstallProbedMsg struct {
	binaryPath string
	viaBrew    bool
	hasTap     bool
	rootExists bool
}

type uninstallActionDoneMsg struct {
	step    uninstallStep
	outcome string // human line for the Done summary
	err     error
}

// NewUninstallModel constructs the bubbletea program for `sunny
// uninstall`.
func NewUninstallModel(opts UninstallOptions) *uninstallModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &uninstallModel{
		opts:    opts,
		step:    uniConfirm,
		spinner: sp,
	}
}

func (m *uninstallModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.probeCmd())
}

// probeCmd inspects the system once at startup so subsequent prompts
// know what's actually there. We don't probe again — by the time the
// user is on stepBinary the daemon should already be stopped, so
// nothing's racing to change state under us.
func (m *uninstallModel) probeCmd() tea.Cmd {
	root := m.opts.Root
	return func() tea.Msg {
		bin := detectSunnyBinary()
		_, err := os.Stat(root)
		return uninstallProbedMsg{
			binaryPath: bin,
			viaBrew:    isBrewInstalled(bin),
			hasTap:     brewTapPresent("noesrafa/tap"),
			rootExists: err == nil,
		}
	}
}

func (m *uninstallModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		return m, nil
	case uninstallProbedMsg:
		m.binaryPath = v.binaryPath
		m.viaBrew = v.viaBrew
		m.hasTap = v.hasTap
		m.rootExists = v.rootExists
		// --yes skips every prompt; jump straight to the actions.
		if m.opts.Yes {
			return m, m.runConfirmAndContinue()
		}
		return m, nil
	case uninstallActionDoneMsg:
		m.running = false
		if v.outcome != "" {
			m.outcomes = append(m.outcomes, v.outcome)
		}
		// Advance through the flow as actions complete.
		switch v.step {
		case uniConfirm:
			m.step = m.nextStepAfterConfirm()
			m.selection = 0
			return m, nil
		case uniData:
			m.step = uniBinary
			m.selection = 0
			return m, nil
		case uniBinary:
			m.step = uniDone
			return m, nil
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		return m.handleKey(v)
	}
	return m, nil
}

// handleKey routes keyboard input. Selection on every prompt is two
// options: No (0) / Yes (1). Arrows / h-l toggle; enter commits.
func (m *uninstallModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.running {
		switch k.String() {
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	s := k.String()
	switch s {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		// On the Done screen esc closes; elsewhere it abandons the
		// uninstall (with whatever has happened so far still done —
		// no rollback).
		return m, tea.Quit
	}
	switch m.step {
	case uniDone:
		if s == "enter" || s == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	// Yes/No selection.
	switch s {
	case "left", "h":
		m.selection = 0
		return m, nil
	case "right", "l":
		m.selection = 1
		return m, nil
	case "y", "Y":
		m.selection = 1
		return m, m.commitSelection()
	case "n", "N":
		m.selection = 0
		return m, m.commitSelection()
	case "tab":
		m.selection = 1 - m.selection
		return m, nil
	case "enter":
		return m, m.commitSelection()
	}
	return m, nil
}

// commitSelection turns the user's Yes/No into the matching action.
// The action's outcome lands as uninstallActionDoneMsg which advances
// the step.
func (m *uninstallModel) commitSelection() tea.Cmd {
	yes := m.selection == 1
	switch m.step {
	case uniConfirm:
		if !yes {
			// User cancelled at the very first prompt.
			return tea.Quit
		}
		return m.runConfirmAndContinue()
	case uniData:
		m.deleteData = yes
		if !yes {
			return func() tea.Msg {
				return uninstallActionDoneMsg{step: uniData, outcome: "kept ~/.sunny/ — your agents are safe"}
			}
		}
		return m.runRemoveData()
	case uniBinary:
		m.deleteBinary = yes
		if !yes {
			return func() tea.Msg {
				return uninstallActionDoneMsg{step: uniBinary, outcome: "kept the sunny binary"}
			}
		}
		return m.runRemoveBinary()
	}
	return nil
}

// runConfirmAndContinue stops the daemon synchronously, then either
// jumps to the data prompt or skips to binary if --keep-data is set.
func (m *uninstallModel) runConfirmAndContinue() tea.Cmd {
	m.running = true
	m.runLabel = "stopping daemon"
	m.runStart = time.Now()
	root := m.opts.Root
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		// Same path `sunny stop` uses — invoke our own binary so we
		// share the lifecycle helper rather than reimplementing the
		// SIGTERM/timeout dance.
		cmd := exec.Command(self(), "stop", "--root", root)
		_ = cmd.Run() // ignore — "no daemon running" is expected sometimes
		return uninstallActionDoneMsg{step: uniConfirm, outcome: "daemon stopped"}
	})
}

// nextStepAfterConfirm decides where to go after the daemon is down.
// --keep-data jumps straight to the binary prompt.
func (m *uninstallModel) nextStepAfterConfirm() uninstallStep {
	if m.opts.KeepData || !m.rootExists {
		return uniBinary
	}
	if m.opts.Yes {
		// Auto-yes flow: trigger the data removal immediately, the
		// installFinished handler will advance to uniBinary.
		return uniData
	}
	return uniData
}

// runRemoveData deletes ~/.sunny/. Errors are surfaced in the outcome
// line but don't block the rest of the flow.
func (m *uninstallModel) runRemoveData() tea.Cmd {
	m.running = true
	m.runLabel = "deleting " + m.opts.Root
	m.runStart = time.Now()
	root := m.opts.Root
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		if err := os.RemoveAll(root); err != nil {
			return uninstallActionDoneMsg{
				step:    uniData,
				outcome: "✗ rm " + root + ": " + err.Error(),
				err:     err,
			}
		}
		return uninstallActionDoneMsg{step: uniData, outcome: "✓ removed " + root}
	})
}

// runRemoveBinary either runs `brew uninstall sunny` (+ optional
// untap) when sunny was brewed, or `os.Remove` for a standalone bin.
func (m *uninstallModel) runRemoveBinary() tea.Cmd {
	m.running = true
	m.runStart = time.Now()
	bin := m.binaryPath
	via := m.viaBrew
	hasTap := m.hasTap
	if via {
		m.runLabel = "brew uninstall sunny"
	} else {
		m.runLabel = "removing " + bin
	}
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		if via {
			out, err := exec.Command("brew", "uninstall", "sunny").CombinedOutput()
			if err != nil {
				return uninstallActionDoneMsg{
					step:    uniBinary,
					outcome: "✗ brew uninstall sunny: " + firstLine(string(out)),
					err:     err,
				}
			}
			outcome := "✓ brew uninstall sunny"
			if hasTap {
				if out, err := exec.Command("brew", "untap", "noesrafa/tap").CombinedOutput(); err != nil {
					outcome += "\n  ⚠ brew untap noesrafa/tap: " + firstLine(string(out))
				} else {
					outcome += "\n  ✓ brew untap noesrafa/tap"
				}
			}
			return uninstallActionDoneMsg{step: uniBinary, outcome: outcome}
		}
		if bin == "" {
			return uninstallActionDoneMsg{step: uniBinary, outcome: "· no binary path resolved (already gone)"}
		}
		if err := os.Remove(bin); err != nil {
			return uninstallActionDoneMsg{
				step:    uniBinary,
				outcome: "✗ rm " + bin + ": " + err.Error(),
				err:     err,
			}
		}
		return uninstallActionDoneMsg{step: uniBinary, outcome: "✓ removed " + bin}
	})
}

// View renders the centered card with the brand mark on top —
// identical layout to the onboarding flow so users transitioning
// between the two don't get visual whiplash.
func (m *uninstallModel) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true

	w := uninstallBoxWidth(m.width)
	innerW := w - 6
	if innerW < 24 {
		innerW = 24
	}

	title := tui.HatchedTitle(m.headerLabel(), innerW, tui.ColorPrimary(), tui.ColorAccent(), styleHeader())
	body := m.viewForStep(innerW)
	footer := m.uninstallFooter()

	box := lipgloss.NewStyle().
		Width(w).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tui.ColorBorder())

	content := title + "\n\n" + body + "\n\n" + footer
	card := box.Render(content)
	logo := tui.RenderLogo(w, 0)
	stacked := logo + "\n\n" + card

	if m.height <= 0 || m.width <= 0 {
		v.SetContent(stacked)
		return v
	}
	v.SetContent(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, stacked))
	return v
}

func (m *uninstallModel) headerLabel() string {
	switch m.step {
	case uniConfirm:
		return "Uninstall sunny · confirm"
	case uniData:
		return "Uninstall · your data"
	case uniBinary:
		return "Uninstall · the binary"
	case uniDone:
		return "Uninstall · done"
	}
	return "Uninstall"
}

// viewForStep dispatches to per-step renderers.
func (m *uninstallModel) viewForStep(w int) string {
	switch m.step {
	case uniConfirm:
		return m.viewConfirm(w)
	case uniData:
		return m.viewData(w)
	case uniBinary:
		return m.viewBinary(w)
	case uniDone:
		return m.viewDone(w)
	}
	return ""
}

func (m *uninstallModel) viewConfirm(w int) string {
	body := wrap(
		"Vamos a desinstalar sunny de este sistema. Primero paramos el daemon, "+
			"luego preguntamos sobre tu ~/.sunny (agentes y conversaciones), y al "+
			"final removemos el binario. Cada paso te pregunta — nada se elimina sin "+
			"tu OK.",
		w,
	)
	return body + "\n\n" + m.yesNoRow("¿continuar?", "no, cancelar", "sí, empezar")
}

func (m *uninstallModel) viewData(w int) string {
	hint := lipgloss.NewStyle().Foreground(tui.ColorWarning()).Bold(true).Render(
		"⚠ esto borra TODO en " + m.opts.Root,
	)
	body := wrap(
		"Tu directorio de runtime guarda agentes, conversaciones, llaves API en "+
			"secrets.yaml, y configuración. Si dices que sí, todo eso desaparece. "+
			"Si dices que no, solo se elimina el binario y la data se queda — "+
			"puedes reinstalar más tarde y todo vuelve.",
		w,
	)
	return hint + "\n\n" + body + "\n\n" + m.yesNoRow("¿borrar ~/.sunny/?", "no, conservar (default)", "sí, borrar todo")
}

func (m *uninstallModel) viewBinary(w int) string {
	if m.viaBrew {
		body := wrap(
			"Detectamos que sunny está instalado vía Homebrew ("+m.binaryPath+"). "+
				"Vamos a correr `brew uninstall sunny`. Si tienes el tap noesrafa/tap "+
				"agregado, también te ofrecemos quitarlo.",
			w,
		)
		return body + "\n\n" + m.yesNoRow("¿desinstalar via brew?", "no, dejar como está", "sí, brew uninstall sunny")
	}
	if m.binaryPath == "" {
		return wrap("No encontré el binario de sunny en PATH. Probablemente ya está fuera.", w)
	}
	body := wrap(
		"Sunny vive en "+m.binaryPath+" (instalación standalone, no via brew). "+
			"Vamos a borrarlo con `rm`.",
		w,
	)
	return body + "\n\n" + m.yesNoRow("¿borrar el binario?", "no, dejarlo", "sí, rm")
}

func (m *uninstallModel) viewDone(w int) string {
	intro := wrap(
		"Listo. Resumen abajo. Gracias por usar sunny — feedback siempre bienvenido.",
		w,
	)
	if len(m.outcomes) == 0 {
		return intro + "\n\n" + lipgloss.NewStyle().Foreground(tui.ColorMuted()).Render("(nada se modificó)")
	}
	var lines []string
	for _, o := range m.outcomes {
		// Each outcome line may itself be multi-line (e.g. brew
		// untap nested under brew uninstall). Indent + render.
		for _, sub := range strings.Split(o, "\n") {
			lines = append(lines, "  "+sub)
		}
	}
	return intro + "\n\n" + strings.Join(lines, "\n")
}

// yesNoRow renders the Yes/No selector. Selected option is bold +
// primary fg; unselected is dim. Pressing y/n is equivalent to
// selecting + enter.
func (m *uninstallModel) yesNoRow(prompt, noLabel, yesLabel string) string {
	mk := func(label string, selected bool) string {
		if selected {
			return lipgloss.NewStyle().
				Foreground(tui.ColorPrimary()).
				Bold(true).
				Render("[ " + label + " ]")
		}
		return lipgloss.NewStyle().
			Foreground(tui.ColorMuted()).
			Render("  " + label + "  ")
	}
	q := lipgloss.NewStyle().Bold(true).Render(prompt)
	return q + "\n\n  " + mk(noLabel, m.selection == 0) + "    " + mk(yesLabel, m.selection == 1)
}

func (m *uninstallModel) uninstallFooter() string {
	keyStyle := lipgloss.NewStyle().Foreground(tui.ColorPrimary()).Bold(true)
	dim := lipgloss.NewStyle().Foreground(tui.ColorMuted())
	if m.running {
		return lipgloss.NewStyle().Foreground(tui.ColorWarning()).Render(
			fmt.Sprintf("%s %s · %s", m.spinner.View(), m.runLabel, elapsed(m.runStart)),
		)
	}
	join := func(pairs ...[2]string) string {
		var parts []string
		for _, p := range pairs {
			parts = append(parts, keyStyle.Render(p[0])+dim.Render(" "+p[1]))
		}
		return strings.Join(parts, dim.Render("  "))
	}
	if m.step == uniDone {
		return join([2]string{"enter", "exit"})
	}
	return join(
		[2]string{"← / →", "select"},
		[2]string{"y / n", "shortcut"},
		[2]string{"enter", "confirm"},
		[2]string{"esc", "cancel"},
	)
}

// uninstallBoxWidth caps the card at ~72 cols on wide terminals to
// keep prose readable.
func uninstallBoxWidth(termWidth int) int {
	max := 72
	if termWidth > 0 && termWidth-4 < max {
		max = termWidth - 4
	}
	if max < 36 {
		max = 36
	}
	return max
}

// self is the absolute path to the running sunny binary so we can
// invoke `sunny stop` as a subprocess. Falls back to "sunny" on
// PATH if Executable() fails.
func self() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	return "sunny"
}

// detectSunnyBinary returns the path of the currently-running sunny
// binary, or "" if it can't be resolved. Resolves symlinks so brew's
// /opt/homebrew/bin/sunny → its real Cellar location.
func detectSunnyBinary() string {
	if path, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return real
		}
		return path
	}
	if path, err := exec.LookPath("sunny"); err == nil {
		return path
	}
	return ""
}

// isBrewInstalled reports whether the binary path looks like a
// Homebrew install (lives under brew's prefix).
func isBrewInstalled(binary string) bool {
	if binary == "" {
		return false
	}
	for _, prefix := range []string{"/opt/homebrew/", "/usr/local/Cellar/", "/home/linuxbrew/"} {
		if strings.HasPrefix(binary, prefix) {
			return true
		}
	}
	if out, err := exec.Command("brew", "list", "sunny").Output(); err == nil && len(out) > 0 {
		return true
	}
	return false
}
