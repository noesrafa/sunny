package tui

import (
	tea "charm.land/bubbletea/v2"
)

// Dialog is a modal that takes focus over the main UI. The root model routes
// keyboard messages to the top-of-stack dialog first. Dialogs communicate
// results by emitting tea.Msgs that the root model handles below.
type Dialog interface {
	Init() tea.Cmd
	Update(msg tea.Msg) tea.Cmd
	View(width, height int) string
}

type Overlay struct {
	stack []Dialog
}

func (o *Overlay) Open(d Dialog) tea.Cmd {
	o.stack = append(o.stack, d)
	return d.Init()
}

func (o *Overlay) CloseTop() {
	if len(o.stack) == 0 {
		return
	}
	o.stack = o.stack[:len(o.stack)-1]
}

func (o *Overlay) HasOpen() bool { return len(o.stack) > 0 }

func (o *Overlay) Top() Dialog {
	if len(o.stack) == 0 {
		return nil
	}
	return o.stack[len(o.stack)-1]
}

func (o *Overlay) UpdateTop(msg tea.Msg) tea.Cmd {
	if len(o.stack) == 0 {
		return nil
	}
	return o.stack[len(o.stack)-1].Update(msg)
}

func (o *Overlay) ViewTop(width, height int) string {
	if len(o.stack) == 0 {
		return ""
	}
	return o.stack[len(o.stack)-1].View(width, height)
}

// StyleAware is implemented by dialogs that cache a copy of the root
// model's Styles at construction time. The root calls RefreshStyles after
// a theme swap so open dialogs (notably the live-preview SettingsDialog)
// repaint with the new palette instead of staying frozen on the colors
// they were built with.
type StyleAware interface {
	SetStyles(s Styles)
}

// BgAware is for dialogs that render polarity-dependent content (e.g. the
// SettingsDialog's swatches, which preview each flavor under the live
// terminal background). Updated when tea.BackgroundColorMsg flips bg.
type BgAware interface {
	SetBgIsLight(bgIsLight bool)
}

// RefreshStyles propagates a fresh Styles copy into every open dialog that
// cares (StyleAware). Cheap no-op for dialogs that don't implement it.
func (o *Overlay) RefreshStyles(s Styles) {
	for _, d := range o.stack {
		if sa, ok := d.(StyleAware); ok {
			sa.SetStyles(s)
		}
	}
}

// RefreshBgIsLight notifies every BgAware dialog that the terminal bg
// polarity changed, so previews and swatches re-resolve.
func (o *Overlay) RefreshBgIsLight(bgIsLight bool) {
	for _, d := range o.stack {
		if ba, ok := d.(BgAware); ok {
			ba.SetBgIsLight(bgIsLight)
		}
	}
}

// CloseDialogMsg dismisses the top dialog.
type CloseDialogMsg struct{}

// CreateSessionMsg requests the root model to spawn a new session at cwd
// bound to AgentSlug. AgentSlug empty falls back to the model's default.
type CreateSessionMsg struct {
	Cwd       string
	Model     string
	Effort    string
	AgentSlug string
}

// RenameSessionMsg requests the root model to rename the current session.
type RenameSessionMsg struct {
	NewTitle string
}

// ConfirmQuitMsg signals the root model to terminate the program.
type ConfirmQuitMsg struct{}

// ConfirmCloseSessionMsg is emitted from the close-session confirmation dialog
// to actually drop the active claude tab. The root model handles the close.
type ConfirmCloseSessionMsg struct{}

// ConfirmNewConvMsg requests the root model to spawn a fresh claude
// conversation in the active session (same cwd / model / effort), discarding
// the current transcript.
type ConfirmNewConvMsg struct{}

// Run management messages — emitted by run dialogs and consumed at the root.
type OpenRunEditMsg struct{}
type OpenRunLogsMsg struct{ ID string }
type CreateRunMsg struct{ Name, Command, Cwd string }
type DeleteRunMsg struct{ ID string }

// Terminal-pane messages — flow from the new-pane dialog to the root model.
type CreatePaneMsg struct{ Name, Command, Cwd string }
type ClosePaneMsg struct{ ID string }

// SwitchTabMsg is emitted by the tile picker; root jumps to the chosen tab.
type SwitchTabMsg struct {
	Kind  string // "claude" | "pane"
	Index int
}

// PreviewThemeMsg is emitted while the user navigates the settings picker
// with arrow keys. The root model swaps the active palette in place — no
// dialog close, no persistence — so the user sees a live preview of the
// hovered theme. Cancelling (esc) sends one last preview targeting the
// original theme to undo the visual changes.
type PreviewThemeMsg struct{ ID string }

// ApplyThemeMsg commits the picked theme: swap palette, close the dialog,
// persist the choice to state.json. Emitted only when the user presses
// enter on a row in the picker.
type ApplyThemeMsg struct{ ID string }

// CancelSettingsMsg is emitted on esc. Reverts the live preview back to
// the theme the user had at dialog-open time and closes the dialog.
type CancelSettingsMsg struct{ OriginalID string }
