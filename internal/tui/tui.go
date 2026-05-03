// Package tui is sunny's terminal client. It connects to a running sunny
// daemon over HTTP, lists agents in a sidebar, and provides a chat-style
// input pane. There is no engine wired up yet — submitting a message just
// echoes it locally.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/noesrafa/sunny/internal/client"
)

type focus int

const (
	focusSidebar focus = iota
	focusInput
)

const sidebarWidth = 28

type Model struct {
	c *client.Client

	width, height int

	agents   []client.AgentSummary
	selected int
	loadErr  error

	chat      viewport.Model
	chatLines []string

	input textarea.Model

	focus focus
	ready bool
}

func New(c *client.Client) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message…"
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.CharLimit = 0
	ta.Focus()

	vp := viewport.New(0, 0)

	return Model{
		c:     c,
		chat:  vp,
		input: ta,
		focus: focusInput,
	}
}

type agentsLoadedMsg struct {
	agents []client.AgentSummary
	err    error
}

func loadAgents(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		a, err := c.ListAgents(ctx)
		return agentsLoadedMsg{agents: a, err: err}
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, loadAgents(m.c))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		m.ready = true

	case agentsLoadedMsg:
		m.agents = msg.agents
		m.loadErr = msg.err
		if msg.err != nil {
			m.appendChat(errorStyle.Render("⚠ daemon unreachable: ") + msg.err.Error())
			m.appendChat(mutedStyle.Render("hint: run `sunny start` (or check --addr)"))
		} else {
			m.appendChat(mutedStyle.Render(fmt.Sprintf("connected — %d agent(s) loaded.", len(msg.agents))))
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.toggleFocus()
			return m, nil
		}

		switch m.focus {
		case focusSidebar:
			switch msg.String() {
			case "j", "down":
				if m.selected < len(m.agents)-1 {
					m.selected++
				}
			case "k", "up":
				if m.selected > 0 {
					m.selected--
				}
			case "q":
				return m, tea.Quit
			}
		case focusInput:
			if msg.Type == tea.KeyEnter && !msg.Alt {
				content := strings.TrimSpace(m.input.Value())
				if content != "" {
					m.appendChat(selectedItemStyle.Render("you: ") + content)
					m.appendChat(mutedStyle.Render("(no engine yet — message is local only)"))
				}
				m.input.Reset()
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			cmds = append(cmds, cmd)
		}

	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) toggleFocus() {
	if m.focus == focusInput {
		m.focus = focusSidebar
		m.input.Blur()
	} else {
		m.focus = focusInput
		m.input.Focus()
	}
}

func (m *Model) appendChat(line string) {
	m.chatLines = append(m.chatLines, line)
	m.chat.SetContent(strings.Join(m.chatLines, "\n"))
	m.chat.GotoBottom()
}

func (m *Model) applyLayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	sb := sidebarWidth
	if m.width < 70 {
		sb = m.width / 4
	}
	// Reserve space for borders + padding (each box adds 2 width, 2 height).
	mainW := m.width - sb
	inputH := m.input.Height() + 2 // box border (top+bottom)
	chatW := mainW - 2
	chatH := m.height - inputH - 2

	if chatW < 1 {
		chatW = 1
	}
	if chatH < 1 {
		chatH = 1
	}

	m.chat.Width = chatW
	m.chat.Height = chatH
	m.input.SetWidth(chatW)
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, m.renderSidebar(), m.renderMain())
}

func (m Model) renderSidebar() string {
	var b strings.Builder
	b.WriteString(sidebarHeader.Render("AGENTS"))
	b.WriteString("\n\n")
	if len(m.agents) == 0 && m.loadErr == nil {
		b.WriteString(mutedStyle.Render("…loading"))
	}
	for i, a := range m.agents {
		prefix := "  "
		style := itemStyle
		if i == m.selected {
			prefix = "▸ "
			style = selectedItemStyle
		}
		b.WriteString(style.Render(prefix + a.Name))
		b.WriteString("\n")
	}

	box := borderStyle
	if m.focus == focusSidebar {
		box = activeBorder
	}
	sb := sidebarWidth
	if m.width < 70 {
		sb = m.width / 4
	}
	return box.Width(sb).Height(m.height - 2).Render(b.String())
}

func (m Model) renderMain() string {
	chatBox := borderStyle.Render(m.chat.View())

	inputBox := borderStyle
	if m.focus == focusInput {
		inputBox = activeBorder
	}
	inputView := inputBox.Render(m.input.View())

	return lipgloss.JoinVertical(lipgloss.Left, chatBox, inputView)
}

// Run is the entry point invoked by `sunny tui` / `sunny`.
func Run(addr string) error {
	c := client.New(addr)
	p := tea.NewProgram(New(c), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
