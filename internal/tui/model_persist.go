package tui

import (
	imgclip "github.com/noesrafa/sunny/internal/clipboard"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/state"
)

// saveState marks the state as dirty so the next saveTickMsg
// flushes it. Cheap (no I/O), safe to call from any handler.
func (m *Model) saveState() {
	m.saveDirty = true
}

// flushState performs the actual MarshalIndent + atomic rename.
// Called from the save tick (debounced) and from the quit path
// (synchronous).
func (m *Model) flushState() {
	m.saveDirty = false
	m.flushStateNow()
}

// flushStateNow snapshots ephemeral per-TUI prefs (theme + per-peer
// drafts + active tab id) into ~/.sunny/state.json. The "what tabs
// exist" question is answered by the daemon's tabs.json, NOT here —
// so this file stays small and per-device.
func (m *Model) flushStateNow() {
	if m.peerManagers == nil {
		return
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	peerPrefs := map[string]*state.PeerPrefs{}
	for name, mgr := range m.peerManagers {
		drafts := map[string]string{}
		for _, s := range mgr.Sessions {
			if s.Draft != "" && s.TabID != "" {
				drafts[s.TabID] = s.Draft
			}
		}
		var activeTabID string
		if cur := mgr.Current(); cur != nil {
			activeTabID = cur.TabID
		}
		peerPrefs[name] = &state.PeerPrefs{
			ActiveTabID: activeTabID,
			Drafts:      drafts,
		}
	}
	st := &state.State{
		Theme:     m.themeID,
		PeerState: peerPrefs,
	}
	if err := state.Save(st); err != nil {
		m.logger.Error("save state failed", "err", err)
	}
}

// pruneOrphanImages walks every session's transcript across every
// peer, collects the image paths still referenced by past
// UserItems, and deletes any other file under ~/.sunny/images/.
// Best-effort; failures only get logged.
func (m Model) pruneOrphanImages() {
	if m.peerManagers == nil {
		return
	}
	refs := map[string]bool{}
	for _, mgr := range m.peerManagers {
		for _, s := range mgr.Sessions {
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
