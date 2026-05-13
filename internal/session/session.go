// Package session models a single conversation in the TUI: cwd, model
// preference, transcript items, draft state, and pending image
// attachments. It also owns the network plumbing for one chat:
//
//   - one always-on watch subscription against
//     GET /agents/{id}/conversations/{conv_id}/watch (auto-reconnects)
//   - POST /turns to send + DELETE /turn to cancel
//
// All transcript mutations flow through ApplyJournalEvent, which
// decodes one journal event from the watch stream and updates Items
// + State accordingly. Two TUIs watching the same conversation see
// identical streams of events — the data path is symmetric.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/log/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/git"
)

// ErrNoEngine is returned by Send when the session has no client
// configured (daemon unreachable, addr unset).
var ErrNoEngine = errors.New("session: no engine connected")

// ErrSessionBusy is returned by Send when a turn is already in
// flight. Callers should normally check State first instead of
// relying on it.
var ErrSessionBusy = errors.New("session busy")

type State int

const (
	StateIdle State = iota
	StateThinking
	StateError
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateThinking:
		return "thinking"
	case StateError:
		return "error"
	}
	return "?"
}

type Session struct {
	ID        string // local UI id
	TabID     string // daemon-side tab id (~/.sunny/tabs.json) — close via client.CloseTab
	ConvID    string // server-assigned conversation id
	Cwd       string
	Title     string
	Model     string
	Effort    string
	Branch    string
	Changes   git.ChangeStats
	State     State
	Items     []Item
	TotalCost float64
	Turns     int
	LastErr   error
	StartedAt time.Time
	// lastUserAt is the journal timestamp of the most recent "user"
	// event we applied. Used to compute per-turn duration on `done`
	// — that's "done.At - lastUserAt", which is identical for every
	// viewer of the conversation regardless of who actually sent.
	// Without it, durations on the viewer side were either 0 (if
	// the viewer never sent) or the wall time since the viewer's
	// last own send (way too long).
	lastUserAt time.Time

	// Draft is the unsent textarea content. Saved on tab switch,
	// restored when switching back.
	Draft string

	// Attachments are images pasted into the current draft but not yet sent.
	// Cleared on Send. Each carries the [Image #N] index marker.
	Attachments   []Attachment
	attachmentSeq int

	// PastedTexts are big text blobs pasted into the current draft but not
	// yet sent. Each is referenced by a "[Pasted text #N +K lines]" marker
	// in the textarea. Cleared on Send (the per-turn copy lives on the
	// resulting UserItem so regenerate can re-expand).
	PastedTexts   []PastedText
	pastedTextSeq int

	logger        *log.Logger
	turnHadOutput bool

	// Engine wiring. The session holds a daemon client + agent +
	// federation peer name. A long-lived watch goroutine forwards
	// every journal event into watchCh; the TUI reads from there.
	//
	// lastSeq is per-watch (passed into runWatch as a local atomic) —
	// keeping it on the Session would cross-contaminate seq counters
	// across rotations (RebindConv spawns a new goroutine while the
	// old one might still update a shared atomic, leading the new
	// watch to start with an old-conv seq and miss events).
	c            *client.Client
	agentID      string
	host         string
	watchCh      chan client.JournalEvent
	watchCancel  context.CancelFunc
	watchOpened  atomic.Bool
	initialSince int64 // seed for the next runWatch's lastSeq (set by loadHistory)
	echoSkipSeq  atomic.Int64
}

// Bind configures the session's client + agent + host AND starts the
// background watch goroutine. Replaces the older AttachClient. Call
// once at construction (or after restoring from saved state).
//
// When the session was restored with a non-empty ConvID, Bind
// synchronously fetches the journal once to pre-populate Items and
// lastSeq — so the transcript renders fully before the watch
// streams its first event. Failures are logged and the session
// starts with an empty transcript (the watch will eventually fill
// it via since=0 replay).
//
// ctx scopes the watch loop's lifetime — typically the TUI's root
// context, so the watch dies cleanly on Ctrl+C / quit.
func (s *Session) Bind(ctx context.Context, c *client.Client, agentID, host string) {
	s.c = c
	s.agentID = agentID
	if host == "" {
		host = "local"
	}
	s.host = host
	s.echoSkipSeq.Store(-1)

	if s.ConvID != "" {
		if err := s.loadHistory(ctx); err != nil && s.logger != nil {
			s.logger.Warn("loadHistory failed; transcript will fill via watch replay",
				"err", err, "session", s.ID, "conv", s.ConvID)
		}
	}

	s.watchCh = make(chan client.JournalEvent, 256)
	watchCtx, cancel := context.WithCancel(ctx)
	s.watchCancel = cancel
	go s.runWatch(watchCtx, s.watchCh, s.initialSince)
}

// RebindConv asks the daemon to rebind this session's tab to a fresh
// conversation, then resets local state so the transcript renders
// empty and the next Send goes against the new conv. The old watch
// goroutine is cancelled and a new one is spawned against the new
// conv id; callers must re-arm waitForJournalEvent against the new
// WatchEvents() channel.
//
// Stale events still buffered in the OLD channel are silently
// dropped by the TUI's staleness check (channel-reference compare in
// the journalEventMsg handler) — RebindConv does not block on them.
//
// ctx scopes the new watch goroutine's lifetime — pass the same root
// ctx that Bind got.
func (s *Session) RebindConv(ctx context.Context) error {
	if s.c == nil {
		return ErrNoEngine
	}
	if s.TabID == "" {
		return fmt.Errorf("session: no tab_id (cannot rebind)")
	}
	tab, err := s.c.RebindTabConv(ctx, s.TabID)
	if err != nil {
		return err
	}
	if s.watchCancel != nil {
		s.watchCancel()
	}
	s.ConvID = tab.ConvID
	s.initialSince = 0
	s.echoSkipSeq.Store(-1)
	s.lastUserAt = time.Time{}
	s.Items = nil
	s.Turns = 0
	s.TotalCost = 0
	s.State = StateIdle
	s.LastErr = nil
	s.StartedAt = time.Time{}
	s.turnHadOutput = false
	s.Attachments = nil
	s.attachmentSeq = 0
	s.PastedTexts = nil
	s.pastedTextSeq = 0

	s.watchCh = make(chan client.JournalEvent, 256)
	watchCtx, cancel := context.WithCancel(ctx)
	s.watchCancel = cancel
	go s.runWatch(watchCtx, s.watchCh, 0)
	return nil
}

// loadHistory synchronously fetches the conversation's journal,
// applies every event to Items, and records the highest seq in
// initialSince so the next runWatch resumes from the right point.
// Used by Bind for restored sessions; may also be useful in future
// "reload from disk" flows.
func (s *Session) loadHistory(ctx context.Context) error {
	if s.c == nil || s.ConvID == "" {
		return nil
	}
	_, events, err := s.c.GetConversation(ctx, s.agentID, s.ConvID)
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.applyKindLocked(ev.Kind, ev.At, ev.Payload)
	}
	if n := len(events); n > 0 {
		s.initialSince = events[n-1].Seq
	}
	return nil
}

// AgentID returns the id of the agent this session is bound to.
func (s *Session) AgentID() string { return s.agentID }

// TurnStart returns the journal timestamp of the most recent user
// message — the moment the in-flight turn (if any) began. Zero when
// no turn has been observed yet on this session. The sidebar's
// "thinking · X.Ys" counter reads from here; without it the viewer
// would tick from time.Time{} which is year 1.
func (s *Session) TurnStart() time.Time { return s.lastUserAt }

// Host returns the federation peer name this session lives on
// ("local" by default).
func (s *Session) Host() string {
	if s.host == "" {
		return "local"
	}
	return s.host
}

// WatchEvents returns the channel the TUI should drain for live
// journal events. Always non-nil after Bind. Closed only by Close().
func (s *Session) WatchEvents() <-chan client.JournalEvent { return s.watchCh }

// AddAttachment registers a clipboard image with the session and
// returns the 1-based marker index the caller should embed in the
// textarea draft.
func (s *Session) AddAttachment(path, mediaType string) int {
	s.attachmentSeq++
	s.Attachments = append(s.Attachments, Attachment{
		Index:     s.attachmentSeq,
		Path:      path,
		MediaType: mediaType,
	})
	return s.attachmentSeq
}

// AddPastedText registers a chunk of pasted text with the session and
// returns the 1-based marker index plus its line count. The caller
// embeds "[Pasted text #<idx> +<lines> lines]" in the textarea.
func (s *Session) AddPastedText(text string) (idx, lines int) {
	s.pastedTextSeq++
	lines = strings.Count(text, "\n") + 1
	s.PastedTexts = append(s.PastedTexts, PastedText{
		Index: s.pastedTextSeq,
		Text:  text,
		Lines: lines,
	})
	return s.pastedTextSeq, lines
}

var idCounter atomic.Int64

func newID() string {
	n := idCounter.Add(1)
	return fmt.Sprintf("s%d", n)
}

type Options struct {
	Logger                   *log.Logger
	Model                    string
	Effort                   string
	DangerousSkipPermissions bool
	// AgentID binds the session to a specific agent.
	AgentID string
	// Host is the federation peer name this session lives on.
	// Empty defaults to "local".
	Host string
	// TabID is the daemon-side tab handle (~/.sunny/tabs.json).
	// Required for everything that opens a tab on the server (i.e.
	// every session today); empty only for transient throwaways.
	TabID string
	// ConvID is the server-assigned conversation id. Always set
	// alongside TabID — the daemon creates a conv when a tab is
	// opened, so by the time a Session is constructed both ids
	// exist.
	ConvID string
	Title  string
	Draft  string
}

// New constructs a fresh session. Bind must be called separately to
// attach a daemon client + start the watch.
func New(_ context.Context, cwd string, opts Options) (*Session, error) {
	if cwd == "" {
		return nil, fmt.Errorf("session: cwd required")
	}
	id := newID()
	logger := opts.Logger
	if logger == nil {
		logger = log.NewWithOptions(io.Discard, log.Options{})
	}
	logger = logger.With("session", id, "cwd", cwd)
	logger.Info("session created", "model", opts.Model, "effort", opts.Effort)
	title := opts.Title
	if title == "" {
		title = filepath.Base(cwd)
	}
	// Branch + Changes start empty; the TUI's git tick fetches them
	// asynchronously via the session's host daemon (GET /git/status)
	// so that a session bound to a remote peer reads the right repo
	// — not whatever happens to live at the same path on the TUI's
	// machine.
	return &Session{
		ID:      id,
		TabID:   opts.TabID,
		Cwd:     cwd,
		Title:   title,
		Model:   opts.Model,
		Effort:  opts.Effort,
		ConvID:  opts.ConvID,
		Draft:   opts.Draft,
		State:   StateIdle,
		agentID: opts.AgentID,
		host:    defaultHost(opts.Host),
		logger:  logger,
	}, nil
}

func defaultHost(h string) string {
	if h == "" {
		return "local"
	}
	return h
}

// ApplyGitStatus updates Branch + Changes from a fetched daemon
// response. Returns true when either field actually changed so the
// TUI can decide whether a viewport refresh is worth it.
func (s *Session) ApplyGitStatus(branch string, changes git.ChangeStats) bool {
	changed := false
	if branch != s.Branch {
		s.Branch = branch
		changed = true
	}
	if changes != s.Changes {
		s.Changes = changes
		changed = true
	}
	return changed
}

// Send posts a new turn to the daemon. Returns immediately after the
// 202 — the response is observed via the watch stream. Side effects:
//
//   - appends a UserItem optimistically (for snappy UI)
//   - clears pending Attachments
//   - sets State=Thinking, StartedAt=now
//   - records echoSkipSeq so the watch event with the same seq is
//     dropped instead of duplicating the user message
//
// Recovery: if the saved ConvID has been archived/deleted out-of-
// band the daemon returns 404. We catch that, clear ConvID, and
// retry once with a fresh conversation.
func (s *Session) Send(ctx context.Context, text string) error {
	if s.c == nil {
		return ErrNoEngine
	}
	if s.State == StateThinking {
		return ErrSessionBusy
	}
	res, err := s.send(ctx, text, false)
	if err != nil {
		return err
	}
	// Optimistic local append + skip the watch echo of this exact seq.
	s.Items = append(s.Items, UserItem{
		Text:        text,
		Attachments: s.Attachments,
		PastedTexts: s.PastedTexts,
	})
	s.Attachments = nil
	s.PastedTexts = nil
	s.echoSkipSeq.Store(res.UserSeq)
	s.State = StateThinking
	now := time.Now()
	s.StartedAt = now
	// Sender pre-seeds lastUserAt so the sidebar's "thinking · X.Ys"
	// counter ticks from a real moment instead of waiting for the
	// watch echo (~10 ms but visible). The viewer sets lastUserAt
	// from ev.At when the user event arrives via watch.
	s.lastUserAt = now
	s.turnHadOutput = false
	if s.logger != nil {
		s.logger.Debug("turn started", "session", s.ID, "conv_id", s.ConvID, "user_seq", res.UserSeq)
	}
	return nil
}

// send builds the wire payload and POSTs it. The tab/conv pair is
// always set up by the time a session exists (server-side tabs
// model: opening a tab spawns its conv eagerly) — so we no longer
// lazy-create here. A 404 surfaces to the caller, who can prompt
// the user to close the broken tab.
func (s *Session) send(ctx context.Context, text string, _ bool) (*client.SendTurnResult, error) {
	if s.ConvID == "" {
		return nil, fmt.Errorf("session: no conv_id (tab not initialised)")
	}
	wire := buildWireMessages(s.Items, text, s.PastedTexts, s.Attachments)
	res, err := s.c.SendTurn(ctx, s.agentID, s.ConvID, client.TurnRequest{
		Messages: wire,
		Cwd:      s.Cwd,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Cancel signals the daemon to interrupt the in-flight turn. The
// watch will deliver a `cancelled` event when the engine winds down,
// which flips State back to Idle.
func (s *Session) Cancel(ctx context.Context) error {
	if s.c == nil || s.ConvID == "" {
		return nil
	}
	return s.c.CancelTurn(ctx, s.agentID, s.ConvID)
}

// Regenerate asks the daemon to drop the most recent assistant turn
// and replay the engine from the previous user message. Side effects:
//
//   - locally prunes Items back to (and including) the last UserItem
//     so the UI matches the server-truncated journal immediately —
//     without this the user would briefly see the old assistant turn
//     alongside the new one streaming in.
//   - sets State=Thinking + resets per-turn counters.
//   - posts the trimmed transcript to /regenerate.
//
// Returns ErrSessionBusy when a turn is already in flight (the user
// has to cancel first), ErrNoEngine when the session isn't bound,
// and the daemon's error verbatim otherwise. A nil error means the
// regenerate is in progress; the watch stream will deliver new
// events as they arrive.
func (s *Session) Regenerate(ctx context.Context) error {
	if s.c == nil {
		return ErrNoEngine
	}
	if s.ConvID == "" {
		return fmt.Errorf("session: no conv_id (tab not initialised)")
	}
	if s.State == StateThinking {
		return ErrSessionBusy
	}

	// Find the last UserItem in the local transcript. Everything
	// after it is the assistant turn we're about to discard
	// server-side, so prune locally too. If there's no UserItem,
	// there's nothing to regenerate from.
	cut := -1
	for i := len(s.Items) - 1; i >= 0; i-- {
		if _, ok := s.Items[i].(UserItem); ok {
			cut = i
			break
		}
	}
	if cut < 0 {
		return fmt.Errorf("session: no user message to regenerate from")
	}
	userItem := s.Items[cut].(UserItem)

	wire := buildWireMessages(s.Items[:cut], userItem.Text, userItem.PastedTexts, userItem.Attachments)
	if err := s.c.RegenerateLastTurn(ctx, s.agentID, s.ConvID, client.TurnRequest{
		Messages: wire,
		Cwd:      s.Cwd,
	}); err != nil {
		return err
	}

	// Server has truncated the journal — match local state so the
	// next watch deltas don't render below stale assistant content.
	s.Items = s.Items[:cut+1]
	s.State = StateThinking
	now := time.Now()
	s.StartedAt = now
	s.lastUserAt = now
	s.turnHadOutput = false
	s.LastErr = nil
	if s.logger != nil {
		s.logger.Debug("regenerate started", "session", s.ID, "conv_id", s.ConvID)
	}
	return nil
}

// Close cancels the watch goroutine and closes the events channel.
// Safe to call multiple times.
func (s *Session) Close() {
	if s.watchCancel != nil {
		s.watchCancel()
	}
}

// runWatch is the per-session reconnect loop. Opens a watch against
// the daemon, forwards every event to s.watchCh, and reconnects on
// disconnect. Exits when ctx is cancelled (Close called or TUI
// shutting down).
//
// watchCh is captured at goroutine spawn so a concurrent RebindConv
// can swap s.watchCh out from under us without this goroutine
// pushing into the new channel by mistake. lastSeq is also local —
// resume on reconnect is per-watch-instance, and a shared atomic
// would let an old goroutine update the seq the new goroutine reads
// (causing the new watch to start at the old conv's seq and miss
// real events on the new conv).
func (s *Session) runWatch(ctx context.Context, watchCh chan client.JournalEvent, initialSince int64) {
	defer close(watchCh)
	var lastSeq atomic.Int64
	lastSeq.Store(initialSince)
	const reconnectDelay = 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if s.ConvID == "" {
			return // session never got a conv (caller error); nothing to watch
		}
		s.watchOpened.Store(true)
		ch, err := s.c.WatchConversation(ctx, s.agentID, s.ConvID, lastSeq.Load())
		if err != nil {
			s.watchOpened.Store(false)
			if !sleepCtx(ctx, reconnectDelay) {
				return
			}
			continue
		}
		s.pumpWatch(ctx, ch, watchCh, &lastSeq)
		s.watchOpened.Store(false)
		if !sleepCtx(ctx, reconnectDelay) {
			return
		}
	}
}

// pumpWatch forwards events from one watch connection to watchCh
// until the upstream closes (network drop, daemon shutdown).
// Updates lastSeq as each event is forwarded so a reconnect can
// resume cleanly. The seq atomic is per-runWatch (see runWatch's
// doc comment for why it isn't a session field).
func (s *Session) pumpWatch(ctx context.Context, ch <-chan client.JournalEvent, watchCh chan<- client.JournalEvent, lastSeq *atomic.Int64) {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			select {
			case watchCh <- ev:
				if ev.Seq > lastSeq.Load() {
					lastSeq.Store(ev.Seq)
				}
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// ApplyJournalEvent dispatches one watch event into the session's
// transcript. Returns true when the event ends a turn (done / error
// / cancelled).
//
// The "user" event path skips appending a transcript entry when seq
// matches our own outstanding echoSkipSeq — that's our optimistic
// local append showing back up via the watch stream. We still
// record the journal timestamp (lastUserAt) BEFORE the skip though,
// because the duration math on the upcoming `done` event depends on
// it whether or not we drew the user item.
func (s *Session) ApplyJournalEvent(ev client.JournalEvent) (terminal bool) {
	if ev.Kind == "user" {
		s.lastUserAt = ev.At
		if ev.Seq == s.echoSkipSeq.Load() {
			s.echoSkipSeq.Store(-1)
			return false
		}
	}
	return s.applyKindLocked(ev.Kind, ev.At, ev.Payload)
}

// applyKindLocked is the shared dispatcher used by both
// ApplyJournalEvent (live tail) and loadHistory (bulk replay).
// "Locked" is aspirational — the session is single-threaded from the
// TUI's perspective; calls from background goroutines would need
// real locking.
//
// at is the journal timestamp of the event. For "user" events the
// caller has already stored it as lastUserAt; for "done" events it
// gives us a precise turn duration without depending on local
// wall-clock state.
func (s *Session) applyKindLocked(kind string, at time.Time, payload json.RawMessage) (terminal bool) {
	switch kind {
	case "user":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &p)
		// Bulk replay (loadHistory) goes through this path too —
		// keep lastUserAt fresh so a `done` later in the same
		// replay loop computes the right duration.
		if !at.IsZero() {
			s.lastUserAt = at
		}
		s.Items = append(s.Items, UserItem{Text: p.Text})
	case "text_delta":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &p)
		s.appendText(p.Text)
		s.turnHadOutput = true
	case "thinking_delta":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &p)
		s.appendThinking(p.Text)
	case "tool_use":
		var p struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input string `json:"input"`
		}
		_ = json.Unmarshal(payload, &p)
		s.Items = append(s.Items, ToolUseItem{
			ID:    p.ID,
			Name:  p.Name,
			Input: []byte(p.Input),
		})
		s.turnHadOutput = true
	case "tool_result":
		var p struct {
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
			IsError   bool   `json:"is_error"`
		}
		_ = json.Unmarshal(payload, &p)
		s.linkToolResult(p.ToolUseID, p.Content, p.IsError)
	case "done":
		var p struct {
			CostUSD float64 `json:"cost_usd"`
		}
		_ = json.Unmarshal(payload, &p)
		if !s.turnHadOutput {
			s.Items = append(s.Items, EmptyResponseItem{})
		}
		// Per-turn duration is journal-derived (done.At - user.At)
		// so every viewer of the conversation sees the same number,
		// not their own wall-clock-since-last-own-send. Falls back
		// to local elapsed time only if the journal didn't carry
		// timestamps (very old conversations).
		dur := 0
		switch {
		case !at.IsZero() && !s.lastUserAt.IsZero():
			dur = int(at.Sub(s.lastUserAt).Milliseconds())
		case !s.StartedAt.IsZero():
			dur = int(time.Since(s.StartedAt).Milliseconds())
		}
		s.Items = append(s.Items, ResultItem{
			DurationMs: dur,
			CostUSD:    p.CostUSD,
			NumTurns:   1,
		})
		s.TotalCost += p.CostUSD
		s.Turns++
		s.State = StateIdle
		return true
	case "error":
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &p)
		s.LastErr = errors.New(p.Message)
		s.Items = append(s.Items, ErrorItem{Message: p.Message})
		s.State = StateError
		return true
	case "cancelled":
		s.State = StateIdle
		return true
	}
	return false
}

// appendText merges a streaming delta into the trailing assistant
// text block. Creates a fresh AssistantTextItem if the last item
// isn't one.
func (s *Session) appendText(delta string) {
	if delta == "" {
		return
	}
	if n := len(s.Items); n > 0 {
		if last, ok := s.Items[n-1].(AssistantTextItem); ok {
			s.Items[n-1] = AssistantTextItem{Text: last.Text + delta}
			return
		}
	}
	s.Items = append(s.Items, AssistantTextItem{Text: delta})
}

// appendThinking merges a streaming thinking delta into the trailing
// thinking block.
func (s *Session) appendThinking(delta string) {
	if delta == "" {
		return
	}
	if n := len(s.Items); n > 0 {
		if last, ok := s.Items[n-1].(ThinkingItem); ok {
			s.Items[n-1] = ThinkingItem{Text: last.Text + delta}
			return
		}
	}
	s.Items = append(s.Items, ThinkingItem{Text: delta})
}

// linkToolResult finds the in-flight ToolUseItem with the matching
// ID and marks it Done with the result content. Falls back to a
// standalone ToolResultItem if no matching tool_use is in the
// transcript (which shouldn't normally happen but keeps things
// resilient).
func (s *Session) linkToolResult(id, content string, isError bool) {
	for i := len(s.Items) - 1; i >= 0; i-- {
		t, ok := s.Items[i].(ToolUseItem)
		if !ok {
			continue
		}
		if t.ID == id && !t.Done {
			t.Done = true
			t.Result = content
			t.IsError = isError
			s.Items[i] = t
			return
		}
	}
	s.Items = append(s.Items, ToolResultItem{Content: content})
}

// buildWireMessages flattens a session transcript into the
// user/assistant text turns the daemon expects. Tool round-trips and
// thinking blocks are dropped — claude-code reconstructs them via
// --resume; the anthropic provider doesn't need them since it gets
// the full text turns.
//
// User messages have their "[Pasted text #N +K lines]" markers expanded
// back to the source blob, and "[Image #N]" markers expanded to the
// attachment's absolute path, so the model receives content the textarea
// only ever shows in compact form. claude-code reads the path via its
// own Read tool; other providers see the path as plain text.
func buildWireMessages(items []Item, newUserText string, newPastes []PastedText, newAtts []Attachment) []client.Message {
	expand := func(text string, pastes []PastedText, atts []Attachment) string {
		text = ExpandPastedTexts(text, pastes)
		text = ExpandImageMarkers(text, atts)
		return text
	}
	var wire []client.Message
	for _, it := range items {
		switch v := it.(type) {
		case UserItem:
			wire = append(wire, client.Message{Role: "user", Content: expand(v.Text, v.PastedTexts, v.Attachments)})
		case AssistantTextItem:
			if v.Text != "" {
				wire = append(wire, client.Message{Role: "assistant", Content: v.Text})
			}
		}
	}
	wire = append(wire, client.Message{Role: "user", Content: expand(newUserText, newPastes, newAtts)})
	return wire
}

// pastedTextMarkerRe matches the marker shape inserted into the
// textarea. The line count is tolerated as any non-]-bearing tail so
// minor reformatting (e.g. the user manually edited "+50 lines" to
// "+51 lines") doesn't strand the blob.
var pastedTextMarkerRe = regexp.MustCompile(`\[Pasted text #(\d+)[^\]]*\]`)

// imageMarkerRe matches the "[Image #N]" placeholder injected by
// tryImagePaste. Strict shape — the marker is machine-inserted and
// syncAttachmentMarkers already drops the attachment if the user
// damages it, so we don't need to tolerate drift here.
var imageMarkerRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// ExpandPastedTexts replaces "[Pasted text #N ...]" markers with the
// matching blob's content. Unknown indices stay as literal text — that
// way an orphan marker (e.g. left in a draft after the TUI restart
// dropped the blob) survives as user-visible text instead of vanishing.
func ExpandPastedTexts(text string, pastes []PastedText) string {
	if len(pastes) == 0 || !strings.Contains(text, "[Pasted text #") {
		return text
	}
	byIdx := make(map[int]string, len(pastes))
	for _, p := range pastes {
		byIdx[p.Index] = p.Text
	}
	return pastedTextMarkerRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := pastedTextMarkerRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		idx, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if body, ok := byIdx[idx]; ok {
			return body
		}
		return match
	})
}

// ExpandImageMarkers replaces "[Image #N]" markers with the matching
// attachment's absolute path so the provider can resolve the image.
// claude-code reads the path natively via its Read tool; other
// providers receive it as plain text (graceful degradation — they
// don't render images yet, but the path is visible to the model so
// behavior is at worst the same as today).
//
// Unknown indices stay as literal text, mirroring [[ExpandPastedTexts]].
func ExpandImageMarkers(text string, atts []Attachment) string {
	if len(atts) == 0 || !strings.Contains(text, "[Image #") {
		return text
	}
	byIdx := make(map[int]string, len(atts))
	for _, a := range atts {
		byIdx[a.Index] = a.Path
	}
	return imageMarkerRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := imageMarkerRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		idx, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if p, ok := byIdx[idx]; ok {
			return p
		}
		return match
	})
}

// LiveStatus returns a short human-readable label like "writing" or
// "running grep" while a turn is in flight. Empty string when idle.
func (s *Session) LiveStatus() string {
	if s.State != StateThinking {
		return ""
	}
	if len(s.Items) == 0 {
		return "thinking"
	}
	switch v := s.Items[len(s.Items)-1].(type) {
	case UserItem, ThinkingItem, ToolResultItem:
		return "thinking"
	case AssistantTextItem:
		return "writing"
	case ToolUseItem:
		if !v.Done {
			return "running " + v.Name
		}
		return "thinking"
	}
	return "thinking"
}
