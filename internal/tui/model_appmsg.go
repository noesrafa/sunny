package tui

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/session"
)

// updateAppMsg is the central dispatch for in-app messages emitted
// by dialogs and other sub-models. The big switch routes to family-
// specific handlers that live next door:
//
//   - model_agents.go  — agent CRUD flow
//   - model_secrets.go — secrets paste flow
//
// When handled is true the caller MUST return immediately so other
// dispatchers don't see the same message twice.
func (m Model) updateAppMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {

	// --- core dialog plumbing ---
	case CloseDialogMsg:
		m.overlay.CloseTop()
		return m, nil, true
	case OpenSubDialogMsg:
		// A dialog is launching another dialog (e.g. picker → confirm).
		return m, m.overlay.Open(v.Dialog), true
	case ConfirmQuitMsg:
		// Force a synchronous flush so the in-flight draft + transcript
		// don't get lost to the debounce window.
		if m.saveDirty {
			m.flushState()
		}
		return m, tea.Quit, true

	// --- session lifecycle ---
	case ConfirmCloseSessionMsg:
		m.overlay.CloseTop()
		m.handleCloseTab()
		return m, nil, true
	case ConfirmNewConvMsg:
		// Rebind the tab to a fresh conversation on the daemon —
		// the daemon owns "what conv this tab points at", so the
		// TUI does not mutate ConvID locally. RebindConv handles
		// cancelling the old watch and starting a new one against
		// the fresh conv id; we re-arm waitForJournalEvent on the
		// new channel so events keep flowing.
		m.overlay.CloseTop()
		cur := m.manager.Current()
		if cur == nil {
			return m, nil, true
		}
		if err := cur.RebindConv(m.ctx); err != nil {
			cur.LastErr = err
			cur.State = session.StateError
			m.logger.Error("rebind conv failed", "err", err, "session", cur.ID)
			m.layout()
			m.refreshViewport()
			return m, nil, true
		}
		m.textarea.Reset()
		cur.Draft = ""
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
		m.saveState()
		return m, waitForJournalEvent(cur.ID, cur.WatchEvents()), true
	case CreateSessionMsg:
		return m.createSession(v)
	case RenameSessionMsg:
		m.overlay.CloseTop()
		cur := m.manager.Current()
		if cur != nil {
			cur.Title = v.NewTitle
			m.logger.Info("session renamed", "session", cur.ID, "title", v.NewTitle)
		}
		m.saveState()
		// Persist to the daemon so other viewers (and the next TUI
		// boot) pick up the new title. The PATCH publishes
		// tab.updated to the bus; our applyTabsRefresh handler
		// dedups our own self-echo by matching tab id.
		var cmd tea.Cmd
		if cur != nil && cur.TabID != "" {
			cmd = m.patchTabTitleCmd(cur.Host(), cur.TabID, v.NewTitle)
		}
		return m, cmd, true
	case SwitchTabMsg:
		m.overlay.CloseTop()
		m.switchToTab(v.Kind, v.Index)
		return m, nil, true

	// --- theme picker ---
	case PreviewThemeMsg:
		// Live preview while user navigates the picker. Repaint only —
		// don't close or persist; the dialog still owns the decision.
		m.repaint(v.ID)
		return m, nil, true
	case ApplyThemeMsg:
		m.overlay.CloseTop()
		m.repaint(v.ID)
		m.saveState()
		m.logger.Info("theme applied", "id", v.ID)
		return m, nil, true
	case CancelSettingsMsg:
		// User pressed esc — roll back to whatever was active before
		// they opened the dialog.
		m.overlay.CloseTop()
		m.repaint(v.OriginalID)
		return m, nil, true

	// --- agent CRUD (handlers in model_agents.go) ---
	case SwitchAgentMsg:
		m.overlay.CloseTop()
		return m.switchAgent(v)
	case OpenAgentFormMsg:
		// Replace the picker with the form (no nested stack — the picker
		// will be re-opened by AgentSavedMsg if needed).
		m.overlay.CloseTop()
		return m, m.overlay.Open(NewAgentFormDialog(m.client, v, m.styles)), true
	case SubmitAgentFormMsg:
		return m, m.submitAgentForm(v), true
	case AgentSavedMsg:
		if v.Err != nil {
			// Form already shows the error; nothing else to do.
			return m, nil, true
		}
		// Close the form, reopen the picker so the user sees the change.
		m.overlay.CloseTop()
		curID, curHost := "", ""
		if cur := m.manager.Current(); cur != nil {
			curID = cur.AgentID()
			curHost = cur.Host()
		}
		return m, m.overlay.Open(NewAgentPickerDialog(m.fed, curID, curHost, m.styles)), true
	case OpenAgentRenameMsg:
		// Picker → rename dialog. Close the picker first so esc on the
		// rename dialog returns straight to chat (not back to the
		// picker), matching the form/archive flows.
		m.overlay.CloseTop()
		return m, m.overlay.Open(NewAgentRenameDialog(v.ID, v.Name, m.styles)), true
	case SubmitAgentRenameMsg:
		m.overlay.CloseTop()
		return m, m.renameAgentCmd(v), true
	case DeleteAgentMsg:
		// Confirm dialog approved; close confirm, archive async.
		m.overlay.CloseTop()
		return m, m.deleteAgentCmd(v.ID), true
	case AgentChangedMsg:
		// Bubbles down to the open picker so it refreshes.
		return m, m.overlay.UpdateTop(v), true

	// --- secrets (handlers in model_secrets.go) ---
	case SubmitSecretsMsg:
		// Form submitted — close it, run PUT async.
		m.overlay.CloseTop()
		return m, m.putSecretsCmd(v), true
	case SecretsSavedMsg:
		// Forward to the manager dialog so it reloads its list.
		return m, m.overlay.UpdateTop(v), true
	case DeleteSecretsMsg:
		return m, m.deleteSecretsCmd(v.Provider), true

	// --- federated event multiplexer ---
	case busEventMsg:
		// Re-arm the wait first so we don't drop events while the
		// dispatch below runs synchronously.
		next := m.waitForBusEvent()
		// Mark non-active peers as active so their header pill
		// shows the activity dot. Active peer's events drive a
		// direct UI refresh instead.
		if v.Event.Host != "" && v.Event.Host != m.activePeer {
			m.peerActivity[v.Event.Host] = time.Now()
		}
		switch v.Event.Type {
		case "agent.created", "agent.updated", "agent.deleted":
			// Refresh the picker if it's open. Hint string surfaces
			// the change inline so the user notices.
			status := v.Event.Host + " · " + v.Event.Type
			return m, tea.Batch(next, m.overlay.UpdateTop(AgentChangedMsg{Status: status})), true
		case "tab.opened", "tab.closed", "tab.updated":
			// Refetch tabs for the originating peer and reconcile.
			// This handles both our own echoes (no-op) and remote
			// changes from another TUI on the same daemon.
			cmds := []tea.Cmd{next, m.refetchTabsCmd(v.Event.Host)}
			return m, tea.Batch(cmds...), true
		case "run.created", "run.updated", "run.deleted",
			"run.started", "run.stopped", "run.exited":
			// Any run-state change → refetch the list for that peer.
			// The handler above stores the new list and forwards to
			// the open dialog (if any) so the manager / form / logs
			// view refreshes in lockstep.
			return m, tea.Batch(next, m.fetchRunsCmd(v.Event.Host)), true
		case "monitor.toggled", "monitor.fired", "monitor.error":
			// Monitor state change → refresh that peer's list so the
			// sidebar pill + manager dialog update.
			return m, tea.Batch(next, m.fetchMonitorsCmd(v.Event.Host)), true
		}
		return m, next, true
	case tabsRefreshedMsg:
		// applyTabsRefresh mutates m via its pointer receiver
		// (adds sessions, refreshes the viewport). We MUST run it
		// before the return statement captures m — Go's left-to-
		// right evaluation order for return expressions would
		// otherwise snapshot m before the mutations land, dropping
		// the newly-added sessions on the floor.
		cmd := m.applyTabsRefresh(v)
		return m, cmd, true
	case tabPatchFailedMsg:
		m.logger.Warn("tab patch failed", "peer", v.Host, "tab", v.TabID, "err", v.Err)
		return m, nil, true

	// --- runs ---
	case runsLoadedMsg:
		// Fired by both the initial fetch and the runActionCmd
		// follow-ups. When Runs is nil but Err is also nil, the
		// caller asked us to fetch fresh — e.g. after a successful
		// start/stop. Distinguish by whether the message originated
		// from a real HTTP response (Runs slice non-nil OR Err
		// non-nil) vs a refresh trigger (both nil).
		if v.Err == nil && v.Runs == nil {
			return m, m.fetchRunsCmd(v.Host), true
		}
		m.applyRunsLoaded(v)
		// Forward to any open run dialog so it can refresh its view.
		if m.overlay.HasOpen() {
			return m, m.overlay.UpdateTop(v), true
		}
		return m, nil, true
	case runActionFailedMsg:
		m.logger.Warn("run action failed", "peer", v.Host, "run", v.RunID, "action", v.Action, "err", v.Err)
		// Surface to the open dialog (manager / form / logs) which
		// chooses what to render — list dialogs show inline; form
		// dialogs show in their err line.
		if m.overlay.HasOpen() {
			return m, m.overlay.UpdateTop(v), true
		}
		return m, nil, true
	case OpenRunsMsg:
		return m, m.overlay.Open(NewRunManagerDialog(m.activePeer, m.runsForActivePeer(), m.styles)), true
	case OpenRunFormMsg:
		// Edit when ID is non-empty, create otherwise. The form is
		// a sibling of the manager (we close it before opening so
		// esc returns straight to chat).
		var existing *client.Run
		if v.ID != "" {
			for _, r := range m.runsForActivePeer() {
				if r.ID == v.ID {
					rc := r
					existing = &rc
					break
				}
			}
		}
		m.overlay.CloseTop()
		return m, m.overlay.Open(NewRunFormDialog(m.clientFor(m.activePeer), m.activePeer, existing, m.styles)), true
	case OpenRunLogsMsg:
		c := m.clientFor(m.activePeer)
		var name string
		for _, r := range m.runsForActivePeer() {
			if r.ID == v.ID {
				name = r.Name
				break
			}
		}
		m.overlay.CloseTop()
		return m, m.overlay.Open(NewRunLogsDialog(c, v.ID, name, m.styles)), true
	case CreateRunMsg:
		m.overlay.CloseTop()
		return m, m.createRunCmd(m.activePeer, v), true
	case UpdateRunMsg:
		m.overlay.CloseTop()
		return m, m.updateRunCmd(m.activePeer, v), true
	case DeleteRunMsg:
		m.overlay.CloseTop()
		return m, m.runActionCmd(m.activePeer, v.ID, "delete"), true
	case StartRunMsg:
		return m, m.runActionCmd(m.activePeer, v.ID, "start"), true
	case StopRunMsg:
		return m, m.runActionCmd(m.activePeer, v.ID, "stop"), true
	case RestartRunMsg:
		return m, m.runActionCmd(m.activePeer, v.ID, "restart"), true

	// --- monitors ---
	case monitorsLoadedMsg:
		// Refresh trigger: caller passes Host with no Monitors/Err so
		// we re-fetch fresh. Same pattern as runsLoadedMsg.
		if v.Err == nil && v.Monitors == nil {
			return m, m.fetchMonitorsCmd(v.Host), true
		}
		m.applyMonitorsLoaded(v)
		if m.overlay.HasOpen() {
			return m, m.overlay.UpdateTop(v), true
		}
		return m, nil, true
	case monitorActionFailedMsg:
		m.logger.Warn("monitor action failed", "peer", v.Host, "name", v.Name, "action", v.Action, "err", v.Err)
		if m.overlay.HasOpen() {
			return m, m.overlay.UpdateTop(v), true
		}
		return m, nil, true
	case monitorHistoryLoadedMsg:
		if m.overlay.HasOpen() {
			return m, m.overlay.UpdateTop(v), true
		}
		return m, nil, true
	case OpenMonitorsMsg:
		return m, m.overlay.Open(NewMonitorManagerDialog(m.activePeer, m.allMonitorsForActivePeer(), m.styles)), true
	case OpenMonitorHistoryMsg:
		host, name := m.activePeer, v.Name
		// Replace the manager with the history viewer so esc returns
		// straight to chat. The viewer fires its own load command.
		m.overlay.CloseTop()
		dialog := NewMonitorHistoryDialog(host, name, m.styles)
		return m, tea.Batch(
			m.overlay.Open(dialog),
			m.fetchMonitorHistoryCmd(host, name, 100),
		), true
	case ToggleMonitorMsg:
		return m, m.toggleMonitorCmd(m.activePeer, v.Name, v.Enabled), true
	case busEventClosedMsg:
		// Multiplexer terminated (ctx cancelled, peers gone). Stop
		// re-arming; future versions can show a "real-time sync
		// paused" indicator. For now silent.
		return m, nil, true
	}
	return m, nil, false
}

// repaint swaps the active palette and re-applies it everywhere a
// Style got copied at construction time. Called by all three settings
// flows (preview, apply, cancel); only apply also closes the overlay
// and saves. Also called from the tea.BackgroundColorMsg handler when
// Auto mode needs to flip dark↔light.
func (m *Model) repaint(id string) {
	t := ResolveTheme(id, m.bgIsLight)
	SetPalette(t.P)
	m.styles = DefaultStyles()
	m.themeID = id

	m.textarea.SetStyles(m.styles.EditorTextarea)
	m.spinner.Style = lipgloss.NewStyle().Foreground(colWarning)
	if m.thinkingAnim != nil {
		m.thinkingAnim.SetColors(colSecondary, colPrimary, colText)
	}

	m.md = nil
	m.mdCache = nil

	m.overlay.RefreshStyles(m.styles)
	m.overlay.RefreshBgIsLight(m.bgIsLight)

	m.refreshViewport()
}

// createSession opens a new tab on the target peer's daemon (POST
// /tabs), then materializes it as a local session.Session bound to
// the daemon's tab id. v.Host targets a specific peer; empty means
// the currently active peer.
//
// Server-side tab creation is what makes multi-viewer sync work:
// every other TUI on the same daemon sees a tab.opened event and
// reconciles. The cost is one HTTP round-trip in the foreground —
// fast for local peers (≤ 5 ms), tolerable for tailnet peers.
func (m Model) createSession(v CreateSessionMsg) (Model, tea.Cmd, bool) {
	m.overlay.CloseTop()
	if v.Model != "" {
		m.defaultModel = v.Model
	}
	if v.Effort != "" {
		m.defaultEffort = v.Effort
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	agentID := v.AgentID
	if agentID == "" {
		agentID = m.defaultAgent
	}
	host := v.Host
	if host == "" {
		host = m.activePeer
	}
	peerClient := m.clientFor(host)
	if peerClient == nil {
		m.lastErr = errNoClient
		m.logger.Error("create session: no client for peer", "peer", host)
		return m, nil, true
	}
	tab, err := peerClient.OpenTab(m.ctx, client.OpenTabRequest{
		AgentID: agentID,
		Cwd:     v.Cwd,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("open tab failed", "err", err, "peer", host, "agent", agentID)
		return m, nil, true
	}
	s, err := session.New(m.ctx, v.Cwd, session.Options{
		Logger:                   m.logger,
		Model:                    v.Model,
		Effort:                   v.Effort,
		DangerousSkipPermissions: m.skipPerms,
		AgentID:                  agentID,
		Host:                     host,
		TabID:                    tab.ID,
		ConvID:                   tab.ConvID,
		Title:                    tab.Title,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("create session failed", "err", err, "cwd", v.Cwd)
		return m, nil, true
	}
	s.Bind(m.ctx, peerClient, agentID, host)

	mgr := m.peerManagers[host]
	if mgr == nil {
		// Should never happen — peer should have a manager — but
		// degrade by lazily creating one.
		mgr = session.NewManager()
		m.peerManagers[host] = mgr
		m.peerOrder = append(m.peerOrder, host)
	}
	mgr.Add(s)

	// Auto-switch to the target peer so the user sees the new
	// session they just opened. Without this, opening a tab on a
	// non-active peer feels like nothing happened.
	if host != m.activePeer {
		m.switchToPeer(host)
	}
	m.textarea.Reset()
	m.layout()
	m.refreshViewport()
	m.saveState()
	return m, waitForJournalEvent(s.ID, s.WatchEvents()), true
}

func (m *Model) switchToTab(kind string, index int) {
	if kind != "claude" {
		return
	}
	m.manager.Active = index
	if cur := m.manager.Current(); cur != nil {
		m.textarea.SetValue(cur.Draft)
		m.textarea.CursorEnd()
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
	}
}

// handleJournalEvent applies one watch event to the matching
// session anywhere in the federation and returns the next read
// tea.Cmd. When the event is for a session on a non-active peer,
// the visible UI doesn't refresh — but we mark peerActivity so the
// header switcher pulses an activity dot. The session's watch
// goroutine auto-reconnects under us, so we always re-arm.
func (m *Model) handleJournalEvent(msg journalEventMsg) tea.Cmd {
	sess := m.findSession(msg.SessionID)
	if sess == nil {
		return nil
	}
	// Stale event from an old watch (rotated out by RebindConv).
	// Drop it; a fresh waitForJournalEvent is already armed against
	// the session's current channel.
	if msg.Ch != sess.WatchEvents() {
		return nil
	}
	terminal := sess.ApplyJournalEvent(msg.Event)
	if sess.Host() == m.activePeer {
		if cur := m.manager.Current(); cur != nil && cur.ID == sess.ID {
			m.refreshViewport()
			if terminal {
				m.chat.ScrollToBottom()
			}
		}
	} else {
		// Activity dot ages out naturally — we just stamp now;
		// the renderer compares against time.Since() to fade.
		m.peerActivity[sess.Host()] = time.Now()
	}
	if terminal {
		m.saveState()
	}
	return waitForJournalEvent(sess.ID, sess.WatchEvents())
}

// handleJournalWatchClosed fires when a session's watch channel is
// closed for good (Close called or ctx cancellation). Falls the
// session out of Thinking so the textarea unlocks; the session
// itself stays in its peer's manager since the user may still want
// to read the transcript.
func (m *Model) handleJournalWatchClosed(msg journalWatchClosedMsg) {
	sess := m.findSession(msg.SessionID)
	if sess == nil {
		return
	}
	// Old watch closing after a RebindConv rotation — the new watch
	// is live, leave the session alone.
	if msg.Ch != sess.WatchEvents() {
		return
	}
	if sess.State == session.StateThinking {
		sess.State = session.StateIdle
	}
	if sess.Host() == m.activePeer {
		m.refreshViewport()
	}
	m.saveState()
}

// patchTabTitleCmd PATCHes a single tab's title on the named peer.
// Returns nil so callers can ignore the result — the bus event
// `tab.updated` triggers the reconciliation that picks up the new
// value across every viewer (including ourselves; the dedupe by
// tab id makes the self-echo a no-op).
func (m *Model) patchTabTitleCmd(peer, tabID, title string) tea.Cmd {
	c := m.clientFor(peer)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		if _, err := c.PatchTab(ctx, tabID, client.PatchTabRequest{Title: &title}); err != nil {
			return tabPatchFailedMsg{Host: peer, TabID: tabID, Err: err}
		}
		return nil
	}
}

// refetchTabsCmd kicks off an async GET /tabs against the named
// peer; the response lands as tabsRefreshedMsg so the Update loop
// can apply it without blocking. Used by tab.* bus events to
// reconcile after a remote (or local-self-echo) change.
func (m *Model) refetchTabsCmd(peer string) tea.Cmd {
	if peer == "" {
		peer = m.activePeer
	}
	c := m.clientFor(peer)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		tabs, err := c.ListTabs(ctx)
		return tabsRefreshedMsg{Host: peer, Tabs: tabs, Err: err}
	}
}

// applyTabsRefresh reconciles the named peer's session.Manager
// against a fresh tab list from the daemon. New tabs become bound
// sessions; sessions whose tab vanished are closed locally. Returns
// any new waitForJournalEvent commands so freshly-bound sessions
// start draining their watch.
func (m *Model) applyTabsRefresh(msg tabsRefreshedMsg) tea.Cmd {
	if msg.Err != nil {
		m.logger.Warn("tabs refresh", "peer", msg.Host, "err", msg.Err)
		return nil
	}
	mgr := m.peerManagers[msg.Host]
	if mgr == nil {
		// Peer not modeled locally yet (e.g. tailnet just added);
		// peerSyncTick will pick it up.
		return nil
	}
	c := m.clientFor(msg.Host)
	if c == nil {
		return nil
	}

	wantedByID := map[string]client.Tab{}
	for _, t := range msg.Tabs {
		wantedByID[t.ID] = t
	}
	haveByID := map[string]*session.Session{}
	for _, s := range mgr.Sessions {
		haveByID[s.TabID] = s
	}

	// Close sessions whose tab is gone server-side, and pick up
	// title/cwd updates on tabs we already have. This is what
	// makes a remote rename land in our UI without a refresh.
	for _, s := range mgr.Sessions {
		t, ok := wantedByID[s.TabID]
		if !ok && s.TabID != "" {
			m.logger.Info("tab gone server-side; closing session", "peer", msg.Host, "tab", s.TabID)
			mgr.Close(s.ID)
			continue
		}
		if ok && t.Title != "" && t.Title != s.Title {
			s.Title = t.Title
		}
	}

	// Add sessions for tabs we don't have yet (opened by another
	// viewer). Bind starts the watch and synchronously fetches
	// transcript history so the new tab appears already populated.
	var cmds []tea.Cmd
	for _, t := range msg.Tabs {
		if _, ok := haveByID[t.ID]; ok {
			continue
		}
		s, err := session.New(m.ctx, fallbackCwd(t.Cwd), session.Options{
			Logger:  m.logger,
			Title:   t.Title,
			AgentID: t.AgentID,
			Host:    msg.Host,
			TabID:   t.ID,
			ConvID:  t.ConvID,
		})
		if err != nil {
			m.logger.Warn("session for new tab", "peer", msg.Host, "tab", t.ID, "err", err)
			continue
		}
		s.Bind(m.ctx, c, t.AgentID, msg.Host)
		mgr.Add(s)
		cmds = append(cmds, waitForJournalEvent(s.ID, s.WatchEvents()))
	}

	// If the active peer's tabs changed, refresh the visible UI.
	if msg.Host == m.activePeer {
		if cur := m.manager.Current(); cur != nil {
			m.textarea.SetValue(cur.Draft)
			m.textarea.CursorEnd()
		} else {
			m.textarea.Reset()
		}
		m.layout()
		m.refreshViewport()
	}
	m.saveState()
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// fallbackCwd substitutes the user's home dir when a tab has no
// stored cwd (legacy tabs from before the field was set).
func fallbackCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// findSession looks up a session by ID across every peer's manager.
// Used by the journal/watch handlers because incoming events carry
// only the session ID, not the peer.
func (m *Model) findSession(id string) *session.Session {
	for _, name := range m.peerOrder {
		mgr := m.peerManagers[name]
		if mgr == nil {
			continue
		}
		if s := mgr.ByID(id); s != nil {
			return s
		}
	}
	return nil
}

func (m Model) openQuitDialog() tea.Cmd {
	anyThinking := false
	for _, s := range m.manager.Sessions {
		if s.State == session.StateThinking {
			anyThinking = true
			break
		}
	}
	d := NewQuitDialog(m.styles, len(m.manager.Sessions), anyThinking)
	return m.overlay.Open(d)
}
