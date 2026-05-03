package tui

import "charm.land/bubbles/v2/key"

type KeyMap struct {
	Send          key.Binding
	Newline       key.Binding
	Quit          key.Binding // ctrl+q → open quit confirm dialog (esc was too easy to hit by accident)
	ClearOrCancel key.Binding // ctrl+c: cancel current turn (SIGINT to claude); no-op when idle, never touches the textarea
	NewSession    key.Binding
	NextSession   key.Binding
	PrevSession   key.Binding
	CloseSession  key.Binding
	Rename        key.Binding // ctrl+r — rename the active session
	NewConv       key.Binding // ctrl+l — fresh claude conversation in the same session/cwd
	Diff          key.Binding // ctrl+d — open the git diff viewer
	Runs          key.Binding // open the runs manager modal
	TilePicker    key.Binding // ctrl+k — searchable tab switcher
	Agents        key.Binding // ctrl+a — agent picker / manager
	Secrets       key.Binding // ctrl+y — secrets manager (api keys)
	Settings      key.Binding // ctrl+s — open settings modal (theme picker)
	Game          key.Binding // ctrl+g — open minigames modal (snake)
	ScrollUp      key.Binding
	ScrollDn      key.Binding
	ScrollTop     key.Binding // home — jump to start of transcript
	ScrollBottom  key.Binding // end — jump to bottom + re-enable follow
	Paste         key.Binding // ctrl+v — image-aware paste (image first, then text)
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:       key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"), key.WithHelp("ctrl+j", "newline")),
		Quit:          key.NewBinding(key.WithKeys("ctrl+q"), key.WithHelp("ctrl+q", "quit")),
		ClearOrCancel: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel turn")),
		NewSession:    key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "new")),
		NextSession:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		PrevSession:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		CloseSession:  key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("ctrl+w", "close")),
		Rename:        key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "rename")),
		NewConv:       key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "reset chat")),
		Diff:          key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "diff")),
		Runs:          key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "runs")),
		TilePicker:    key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "switch tab")),
		Agents:        key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "agents")),
		Secrets:       key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "secrets")),
		Settings:      key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "settings")),
		Game:          key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "game")),
		ScrollUp:      key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll up")),
		ScrollDn:      key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "scroll down")),
		ScrollTop:     key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "top")),
		ScrollBottom:  key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "bottom")),
		Paste:         key.NewBinding(key.WithKeys("ctrl+v"), key.WithHelp("ctrl+v", "paste image/text")),
	}
}
