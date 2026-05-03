// Package anim is a minimal morphing-string spinner inspired by Crush's
// /tmp/charm-crush/internal/ui/anim/anim.go. The full Crush version uses an
// internal csync package and prerendered frames; we keep just the visible
// behavior: N cycling glyphs from a hex-ish charset, each painted with a
// horizontal gradient between two colors, optional "Label…" suffix.
//
// Drive it from a Bubble Tea Update by handling StepMsg and re-issuing
// Step() to keep the chain alive.
package anim

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	fps          = 20
	cyclingChars = 12
	charset      = "0123456789abcdefABCDEF~!@#$%^&*()+=_"
	ellipsisStep = 8 // change ellipsis every N frames
)

var idSeq atomic.Int64

func nextID() string { return fmt.Sprintf("anim%d", idSeq.Add(1)) }

// StepMsg is the tea.Msg fired by Step(). The Update loop should re-issue
// Step on the matching anim id.
type StepMsg struct{ ID string }

// Settings configures a new Anim.
type Settings struct {
	Size       int         // chars (default cyclingChars)
	GradFrom   color.Color // left side of gradient
	GradTo     color.Color // right side
	LabelColor color.Color // for the trailing label
}

// Anim is a morphing-string spinner. Safe to call Render concurrently with
// the Step msg processing.
type Anim struct {
	id       string
	size     int
	gradFrom color.Color
	gradTo   color.Color
	labelCol color.Color

	mu       sync.Mutex
	chars    []rune
	frame    int
	label    string
}

// New creates an Anim with the given settings (sensible defaults if zero).
func New(s Settings) *Anim {
	if s.Size <= 0 {
		s.Size = cyclingChars
	}
	if s.GradFrom == nil {
		s.GradFrom = lipgloss.Color("#FF60FF") // Dolly
	}
	if s.GradTo == nil {
		s.GradTo = lipgloss.Color("#6B50FF") // Charple
	}
	if s.LabelColor == nil {
		s.LabelColor = lipgloss.Color("#DFDBDD") // Ash
	}
	a := &Anim{
		id:       nextID(),
		size:     s.Size,
		gradFrom: s.GradFrom,
		gradTo:   s.GradTo,
		labelCol: s.LabelColor,
		chars:    make([]rune, s.Size),
	}
	for i := range a.chars {
		a.chars[i] = pick()
	}
	return a
}

// ID identifies this anim — used so multiple anims can coexist and StepMsg
// can be routed to the right one.
func (a *Anim) ID() string { return a.id }

// SetLabel updates the suffix label (e.g. "Thinking", "Working…").
func (a *Anim) SetLabel(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.label = s
}

// SetColors swaps the gradient endpoints and label color. Lets the TUI
// retheme the spinner without rebuilding the Anim (which would lose its
// id and reset the morph state).
func (a *Anim) SetColors(gradFrom, gradTo, label color.Color) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if gradFrom != nil {
		a.gradFrom = gradFrom
	}
	if gradTo != nil {
		a.gradTo = gradTo
	}
	if label != nil {
		a.labelCol = label
	}
}

// Step returns a tea.Cmd that fires StepMsg after one frame interval. Call
// from Init() and re-call from Update() on each StepMsg to keep ticking.
func (a *Anim) Step() tea.Cmd {
	id := a.id
	return tea.Tick(time.Second/fps, func(time.Time) tea.Msg {
		return StepMsg{ID: id}
	})
}

// Tick advances the animation one frame. Call from Update on matching StepMsg.
func (a *Anim) Tick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.frame++
	// Replace ~half the chars each frame for a pleasant morph.
	for i := range a.chars {
		if rand.IntN(2) == 0 {
			a.chars[i] = pick()
		}
	}
}

// Render returns the styled spinner string.
func (a *Anim) Render() string {
	a.mu.Lock()
	chars := append([]rune(nil), a.chars...)
	frame := a.frame
	label := a.label
	a.mu.Unlock()

	// Gradient ramp across positions.
	ramp := lipgloss.Blend1D(len(chars), a.gradFrom, a.gradTo)
	var b strings.Builder
	for i, r := range chars {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[i]).Render(string(r)))
	}
	if label != "" {
		dots := ellipsis(frame)
		b.WriteString(" ")
		b.WriteString(lipgloss.NewStyle().Foreground(a.labelCol).Render(label + dots))
	}
	return b.String()
}

func pick() rune {
	return rune(charset[rand.IntN(len(charset))])
}

func ellipsis(frame int) string {
	switch (frame / ellipsisStep) % 4 {
	case 1:
		return "."
	case 2:
		return ".."
	case 3:
		return "..."
	default:
		return ""
	}
}
