package onboarding

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/doctor"
	"github.com/noesrafa/sunny/internal/tui"
)

// View renders the current step inside a centered card. Compact —
// each step is title + 1-paragraph "why" + state line + action hint.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.SetContent(m.renderBody())
	return v
}

// renderBody builds the full content of the centered card.
func (m *Model) renderBody() string {
	w := m.boxWidth()
	innerW := m.boxInnerWidth()

	title := tui.HatchedTitle(m.headerLabel(), innerW, tui.ColorPrimary(), tui.ColorAccent(), styleHeader())

	body := m.bodyForStep(innerW)
	footer := m.footer()

	flash := ""
	if m.flash != "" {
		flash = "\n" + lipgloss.NewStyle().Foreground(tui.ColorMuted()).Render("  "+m.flash)
	}

	box := lipgloss.NewStyle().
		Width(w).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tui.ColorBorder())

	content := title + "\n\n" + body + flash + "\n\n" + footer
	rendered := box.Render(content)

	// Center the card vertically + horizontally so the onboarding
	// feels like a focused experience, not crammed into the corner.
	if m.height <= 0 || m.width <= 0 {
		return rendered
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

// headerLabel returns the step title with progress count.
func (m *Model) headerLabel() string {
	steps := []string{
		"Welcome",
		"Tailscale",
		"Homebrew",
		"Claude Code",
		"OpenCode",
		"Ollama Cloud",
		"First agent",
		"Done",
	}
	if int(m.step) >= len(steps) {
		return "Onboarding"
	}
	idx := int(m.step) + 1
	total := len(steps) - 1 // welcome is "step 0" conceptually; show 7 actual steps + done
	if m.step == stepDone {
		return steps[m.step]
	}
	if m.step == stepWelcome {
		return steps[m.step]
	}
	return fmt.Sprintf("%s · step %d of %d", steps[m.step], idx-1, total)
}

// bodyForStep dispatches to the per-step renderer.
func (m *Model) bodyForStep(innerW int) string {
	switch m.step {
	case stepWelcome:
		return m.viewWelcome(innerW)
	case stepTailscale:
		return m.viewTailscale(innerW)
	case stepBrew:
		return m.viewBrew(innerW)
	case stepClaudeCode:
		return m.viewClaudeCode(innerW)
	case stepOpencode:
		return m.viewOpencode(innerW)
	case stepOllama:
		return m.viewOllama(innerW)
	case stepAgent:
		return m.viewAgent(innerW)
	case stepDone:
		return m.viewDone(innerW)
	}
	return ""
}

// viewWelcome is the entry screen — sets the tone and tells the user
// the rules: skip whatever, re-run any time.
func (m *Model) viewWelcome(w int) string {
	hint := lipgloss.NewStyle().Foreground(tui.ColorMuted())
	body := wrap(
		"Sunny es tu agente personal self-hosted: un binario, tu data, tus reglas. "+
			"Esta pasada de 60 segundos te conecta los providers (Anthropic, OpenAI, Ollama, …), "+
			"valida que tienes lo que necesitas instalado, y te deja un primer agente listo.",
		w,
	) + "\n\n" + hint.Render(
		"Cualquier paso lo puedes saltar con `s`. Puedes correr `sunny onboarding` "+
			"cuando quieras — sirve también como doctor interactivo para arreglar lo "+
			"que se rompa después.",
	)
	return body
}

func (m *Model) viewTailscale(w int) string {
	body := wrap(
		"Tailscale habilita federación zero-config: si tienes sunny en otra máquina "+
			"con la misma cuenta de tailscale, las dos se descubren solas y comparten "+
			"agentes y conversaciones. Es opcional — sunny corre perfecto solo en local.",
		w,
	)
	state := ""
	if m.probes.tailscale {
		state = okBadge("tailscale CLI on PATH — ready for federation")
	} else {
		state = warnBadge("tailscale not installed — install at https://tailscale.com/download (opcional)")
	}
	return body + "\n\n" + state
}

func (m *Model) viewBrew(w int) string {
	body := wrap(
		"Homebrew es como sunny y los providers se instalan en macOS. El tap "+
			"`noesrafa/tap` despliega los paquetes que vamos a usar más abajo "+
			"(claude-code, sunny mismo).",
		w,
	)
	var lines []string
	if m.probes.brew {
		lines = append(lines, okBadge("brew on PATH"))
	} else {
		lines = append(lines, failBadge("brew missing — install desde https://brew.sh y vuelve"))
	}
	if m.probes.brew && m.probes.tap {
		lines = append(lines, okBadge("tap noesrafa/tap present"))
	} else if m.probes.brew {
		lines = append(lines, dimBadge("tap noesrafa/tap — pulsa enter para agregarlo"))
	}
	return body + "\n\n" + strings.Join(lines, "\n")
}

func (m *Model) viewClaudeCode(w int) string {
	body := wrap(
		"claude-code es el CLI oficial de Anthropic. Se autentica con tu cuenta "+
			"Claude (no necesita API key) y es el provider por defecto de sunny — "+
			"el que vas a usar 90% del tiempo.",
		w,
	)
	state := ""
	if m.probes.claudeCode {
		state = okBadge("claude on PATH — ready")
	} else {
		state = dimBadge("claude not installed — pulsa enter para `brew install anthropics/claude-code/claude-code`")
	}
	if m.runErr != nil && m.runStep == stepClaudeCode {
		state += "\n" + failBadge("install failed: "+firstLine(m.runOut))
	}
	return body + "\n\n" + state
}

func (m *Model) viewOpencode(w int) string {
	body := wrap(
		"opencode es un CLI multi-provider (GPT, Gemini, Mistral, Ollama, …) — "+
			"útil cuando quieres rotar entre modelos sin múltiples integraciones. "+
			"Maneja su propio auth con `opencode auth login`. Opcional pero práctico.",
		w,
	)
	state := ""
	if m.probes.opencode {
		state = okBadge("opencode on PATH — ready")
	} else {
		state = dimBadge("opencode not installed — pulsa enter para `brew install sst/tap/opencode`")
	}
	if m.runErr != nil && m.runStep == stepOpencode {
		state += "\n" + failBadge("install failed: "+firstLine(m.runOut))
	}
	return body + "\n\n" + state
}

func (m *Model) viewOllama(w int) string {
	body := wrap(
		"Ollama Cloud te da acceso a modelos como gemma3 sin correr nada localmente. "+
			"Pega tu API key de ollama.com/settings/keys — la guardamos en "+
			"~/.sunny/secrets.yaml (mode 0600).",
		w,
	)
	state := ""
	if m.probes.ollamaKey {
		state = okBadge("ollama.api_key already configured — pulsa enter para continuar (o pega una key nueva para reemplazar)")
	}
	return body + "\n\n" + state + "\n\n" + m.ollamaInput.View()
}

func (m *Model) viewAgent(w int) string {
	body := wrap(
		"Tu primer agente. Por defecto se llama Franky con un prompt vacío — aquí "+
			"puedes renombrarlo y darle persona. Lo puedes editar después en cualquier "+
			"momento desde el TUI con la tecla `r` en el picker (ctrl+a).",
		w,
	)
	nameLabel := fieldLabel("name", m.agentNameInput.Focused())
	promptLabel := fieldLabel("prompt", m.agentPromptArea.Focused())
	return body + "\n\n" +
		nameLabel + "\n" + m.agentNameInput.View() + "\n\n" +
		promptLabel + "\n" + m.agentPromptArea.View()
}

func (m *Model) viewDone(w int) string {
	body := wrap(
		"Listo. Resumen abajo. Si algo quedó pendiente puedes correr `sunny onboarding` "+
			"otra vez para arreglarlo — es 100% idempotente.",
		w,
	)
	rep := freshDoctorReport(m.root)
	var lines []string
	for _, p := range rep.Providers {
		lines = append(lines, renderDoctorRow(p))
	}
	if rep.Tailscale != nil {
		lines = append(lines, renderDoctorRow(*rep.Tailscale))
	}
	lines = append(lines, renderDoctorRow(rep.Daemon))
	lines = append(lines, renderDoctorRow(rep.Runtime))
	hint := lipgloss.NewStyle().Foreground(tui.ColorMuted()).Render(
		"Para arrancar: `sunny` te abre el TUI. `sunny doctor` te da este resumen "+
			"sin entrar al onboarding.",
	)
	return body + "\n\n" + strings.Join(lines, "\n") + "\n\n" + hint
}

// footer is the per-step keybindings line. Adapted to context so the
// hint matches what's actually possible right now.
func (m *Model) footer() string {
	if m.running {
		return lipgloss.NewStyle().Foreground(tui.ColorWarning()).Render(
			fmt.Sprintf("%s %s · %s", m.spinner.View(), m.runLabel, elapsed(m.runStarted)),
		)
	}
	keyStyle := lipgloss.NewStyle().Foreground(tui.ColorPrimary()).Bold(true)
	dim := lipgloss.NewStyle().Foreground(tui.ColorMuted())
	join := func(pairs ...[2]string) string {
		var parts []string
		for _, p := range pairs {
			parts = append(parts, keyStyle.Render(p[0])+dim.Render(" "+p[1]))
		}
		return strings.Join(parts, dim.Render("  "))
	}

	switch m.step {
	case stepWelcome:
		return join([2]string{"enter", "start"}, [2]string{"→", "next"}, [2]string{"esc", "quit"})
	case stepDone:
		return join([2]string{"enter", "exit"})
	case stepAgent:
		return join(
			[2]string{"enter", "save"},
			[2]string{"shift+enter", "newline"},
			[2]string{"tab", "next field"},
			[2]string{"→", "skip"},
			[2]string{"←", "back"},
		)
	case stepOllama:
		return join(
			[2]string{"enter", "save"},
			[2]string{"→", "skip"},
			[2]string{"←", "back"},
		)
	default:
		return join(
			[2]string{"enter", "do it"},
			[2]string{"→", "skip"},
			[2]string{"←", "back"},
		)
	}
}

// renderDoctorRow renders one row of the final summary in the same
// shape `sunny doctor` uses. Keeps visual continuity between the two
// commands.
func renderDoctorRow(r doctor.Result) string {
	icon := "·"
	col := tui.ColorMuted()
	switch r.Status {
	case doctor.StatusOK:
		icon = "✓"
		col = tui.ColorSuccess()
	case doctor.StatusWarn:
		icon = "⚠"
		col = tui.ColorWarning()
	case doctor.StatusFail:
		icon = "✗"
		col = tui.ColorDanger()
	}
	iconStyled := lipgloss.NewStyle().Foreground(col).Render(icon)
	tail := r.Detail
	if r.Hint != "" {
		tail = r.Detail + " — try: " + r.Hint
	}
	return "  " + iconStyled + "  " + r.Name +
		lipgloss.NewStyle().Foreground(tui.ColorMuted()).Render(" — "+tail)
}

// boxWidth / boxInnerWidth — the card has a fixed-ish width for
// readability on wide terminals. Caps at the terminal width minus a
// margin so it doesn't overflow on narrow screens.
func (m *Model) boxWidth() int {
	max := 78
	if m.width > 0 && m.width-4 < max {
		max = m.width - 4
	}
	if max < 36 {
		max = 36
	}
	return max
}

func (m *Model) boxInnerWidth() int {
	w := m.boxWidth() - 6
	if w < 24 {
		w = 24
	}
	return w
}

// styleHeader returns the title style — bold + primary fg.
func styleHeader() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(tui.ColorPrimary()).Bold(true)
}

// fieldLabel marks the focused field with a chevron + bold; unfocused
// fields stay dim. Mirrors the existing TUI form aesthetic.
func fieldLabel(text string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().Foreground(tui.ColorPrimary()).Bold(true).Render("▸ "+text)
	}
	return lipgloss.NewStyle().Foreground(tui.ColorMuted()).Render("  " + text)
}

// Badges with tone-appropriate icons and color.
func okBadge(text string) string {
	return lipgloss.NewStyle().Foreground(tui.ColorSuccess()).Render("  ✓ "+text)
}
func warnBadge(text string) string {
	return lipgloss.NewStyle().Foreground(tui.ColorWarning()).Render("  ⚠ "+text)
}
func failBadge(text string) string {
	return lipgloss.NewStyle().Foreground(tui.ColorDanger()).Render("  ✗ "+text)
}
func dimBadge(text string) string {
	return lipgloss.NewStyle().Foreground(tui.ColorMuted()).Render("  · "+text)
}

// wrap soft-wraps body text at width without breaking words. Lipgloss
// has Width() but it forces a hard size; this helper gives us a
// natural reading width for prose.
func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var out []string
	for _, para := range strings.Split(s, "\n\n") {
		out = append(out, wrapPara(para, width))
	}
	return strings.Join(out, "\n\n")
}

func wrapPara(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	lines = append(lines, cur)
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	s = stripANSI(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

