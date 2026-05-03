package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// This file owns the model's reactions to the secrets-CRUD message
// flow (paste form → put → refresh). Dispatch lives in updateAppMsg.

// putSecretsCmd persists a provider's fields via PUT /secrets/<p>.
// Emits SecretsSavedMsg — the form uses it to close on success or
// show the error; the manager dialog uses it to refresh its list.
func (m Model) putSecretsCmd(req SubmitSecretsMsg) tea.Cmd {
	c := m.client
	if c == nil {
		return func() tea.Msg { return SecretsSavedMsg{Provider: req.Provider, Err: errNoClient} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := c.PutSecrets(ctx, req.Provider, req.Fields)
		return SecretsSavedMsg{Provider: req.Provider, Err: err}
	}
}

// deleteSecretsCmd removes a provider's section. Reuses
// SecretsSavedMsg so the manager dialog re-renders the same way it
// does for puts.
func (m Model) deleteSecretsCmd(provider string) tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := c.DeleteSecrets(ctx, provider)
		return SecretsSavedMsg{Provider: provider, Err: err}
	}
}
