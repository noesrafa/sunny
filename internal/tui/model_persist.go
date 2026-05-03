package tui

import (
	imgclip "github.com/noesrafa/sunny/internal/clipboard"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/state"
)

// saveState marks the state as dirty so the next saveTickMsg flushes it.
// Cheap (no I/O), so it's safe to call from any event handler. The actual
// disk write happens in flushState().
func (m *Model) saveState() {
	m.saveDirty = true
}

// flushState performs the actual MarshalIndent + atomic rename. Only called
// from the save tick (debounced) and from the quit path (synchronous, so
// the user's last actions before exit aren't lost).
func (m *Model) flushState() {
	m.saveDirty = false
	m.flushStateNow()
}

// flushStateNow snapshots all sessions + active tab to ~/.sunny/state.json.
func (m *Model) flushStateNow() {
	if m.manager == nil {
		return
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	var sessions []state.SavedSession
	for _, s := range m.manager.Sessions {
		raw, mErr := session.MarshalItems(s.Items)
		if mErr != nil {
			m.logger.Warn("marshal items", "session", s.ID, "err", mErr)
			raw = nil
		}
		eff := s.Effort
		if eff == "" {
			eff = m.defaultEffort
		}
		sessions = append(sessions, state.SavedSession{
			Title:     s.Title,
			Cwd:       s.Cwd,
			Model:     s.Model,
			Effort:    eff,
			Draft:     s.Draft,
			AgentSlug: s.AgentSlug(),
			ConvID:    s.ConvID,
			Items:     raw,
			TotalCost: s.TotalCost,
			Turns:     s.Turns,
		})
	}
	st := &state.State{
		Sessions:   sessions,
		ActiveIdx:  m.manager.Active,
		ActiveKind: "claude",
		Theme:      m.themeID,
	}
	if err := state.Save(st); err != nil {
		m.logger.Error("save state failed", "err", err)
	} else {
		m.logger.Info("state saved", "sessions", len(sessions), "idx", m.manager.Active)
	}
}

// pruneOrphanImages walks every session's transcript, collects the
// image paths still referenced by past UserItems, and deletes any other
// file under ~/.sunny/images/. Pending (unsent) attachments aren't
// included because the app just started — there's nothing pending yet.
// Best-effort; failures only get logged.
func (m Model) pruneOrphanImages() {
	if m.manager == nil {
		return
	}
	refs := map[string]bool{}
	for _, s := range m.manager.Sessions {
		for _, it := range s.Items {
			u, ok := it.(session.UserItem)
			if !ok {
				continue
			}
			for _, a := range u.Attachments {
				refs[a.Path] = true
			}
		}
	}
	n, err := imgclip.PruneOrphans(refs)
	if err != nil {
		m.logger.Warn("prune images", "err", err)
		return
	}
	if n > 0 {
		m.logger.Info("pruned orphan images", "count", n)
	}
}
