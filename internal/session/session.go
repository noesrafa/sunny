// Package session models a single conversation in the TUI: cwd, model
// preference, transcript items, draft state, and pending image attachments.
//
// The session does NOT talk to providers. It used to wrap a `claude` CLI
// subprocess; that wrapper has been removed in v0.2. The TUI is now a pure
// renderer — Send/Cancel here are placeholders that return ErrNoEngine
// until v0.3 wires them to the daemon's HTTP chat endpoint.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/log/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// ErrNoEngine is returned by SendBegin when the session has no client
// configured (daemon unreachable, addr unset).
var ErrNoEngine = errors.New("session: no engine connected")

// ErrSessionBusy is returned by SendBegin when a turn is already in
// flight. Callers should normally check State first instead of relying
// on it.
var ErrSessionBusy = errors.New("session busy")

// gitBranch returns the current branch of the given directory, or "" if it's
// not a git repo or git is unavailable.
func gitBranch(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ChangeStats summarizes the working tree against HEAD. Counts are
// file-level — one file with mixed staged + unstaged edits is one Modified.
// A file is bucketed by its most "destructive" status code.
type ChangeStats struct {
	Added     int
	Modified  int
	Deleted   int
	Untracked int
}

// Total is the file count across every bucket.
func (c ChangeStats) Total() int {
	return c.Added + c.Modified + c.Deleted + c.Untracked
}

// Dirty reports whether anything is pending.
func (c ChangeStats) Dirty() bool { return c.Total() > 0 }

func gitChangeStats(cwd string) ChangeStats {
	out, err := exec.Command("git", "-C", cwd, "status", "--porcelain").Output()
	if err != nil {
		return ChangeStats{}
	}
	var c ChangeStats
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 3 {
			continue
		}
		st := line[:2]
		if st == "??" {
			c.Untracked++
			continue
		}
		x, y := rune(st[0]), rune(st[1])
		switch {
		case x == 'D' || y == 'D':
			c.Deleted++
		case x == 'M' || y == 'M' || x == 'R' || y == 'R' || x == 'C' || y == 'C':
			c.Modified++
		case x == 'A' || y == 'A':
			c.Added++
		default:
			c.Modified++
		}
	}
	return c
}

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
	ConvID    string // server-assigned conversation id (~/.sunny/agents/<slug>/conversations/<id>/)
	Cwd       string
	Title     string
	Model     string
	Effort    string
	Branch    string
	Changes   ChangeStats
	State     State
	Items     []Item
	TotalCost float64
	Turns     int
	LastErr   error
	StartedAt time.Time

	// Draft is the unsent textarea content. Saved on tab switch, restored
	// when switching back.
	Draft string

	// Attachments are images pasted into the current draft but not yet sent.
	// Cleared on Send. Each carries the [Image #N] index marker.
	Attachments   []Attachment
	attachmentSeq int

	logger        *log.Logger
	turnHadOutput bool

	// Engine wiring. The session points at a daemon-bound client; SendBegin
	// uses it to open an SSE stream. activeStream is non-nil while a turn
	// is in flight so Cancel can interrupt it.
	c           *client.Client
	agentSlug   string
	activeStream *client.Stream
	streamCancel context.CancelFunc
}

// AttachClient binds the session to a daemon. Call once at construction.
// The slug identifies which agent the daemon should run.
func (s *Session) AttachClient(c *client.Client, slug string) {
	s.c = c
	s.agentSlug = slug
}

// AgentSlug returns the slug of the agent this session is bound to.
func (s *Session) AgentSlug() string { return s.agentSlug }

// AddAttachment registers a clipboard image with the session and returns
// the 1-based marker index the caller should embed in the textarea draft.
func (s *Session) AddAttachment(path, mediaType string) int {
	s.attachmentSeq++
	s.Attachments = append(s.Attachments, Attachment{
		Index:     s.attachmentSeq,
		Path:      path,
		MediaType: mediaType,
	})
	return s.attachmentSeq
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
	// ConvID, when non-empty, restores a session bound to an existing
	// server-side conversation. SendBegin reuses it instead of creating
	// a new one. Empty on fresh sessions.
	ConvID string
	Title  string
	Draft  string

	// State-restore extras. Only consumed when reopening a previously-open
	// session — fresh sessions leave them zero.
	Items     []Item
	TotalCost float64
	Turns     int
}

// New constructs a fresh session. The ctx is unused today but reserved for
// the v0.3 path where New will dial the daemon to register the session.
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
	return &Session{
		ID:        id,
		Cwd:       cwd,
		Title:     title,
		Model:     opts.Model,
		Effort:    opts.Effort,
		Branch:    gitBranch(cwd),
		Changes:   gitChangeStats(cwd),
		ConvID:    opts.ConvID,
		Draft:     opts.Draft,
		Items:     opts.Items,
		TotalCost: opts.TotalCost,
		Turns:     opts.Turns,
		State:     StateIdle,
		logger:    logger,
	}, nil
}

// RefreshBranch re-reads the cwd's git branch and dirty state. Returns
// true if anything changed so the caller can decide to re-render.
func (s *Session) RefreshBranch() bool {
	changed := false
	if b := gitBranch(s.Cwd); b != s.Branch {
		s.Branch = b
		changed = true
	}
	if c := gitChangeStats(s.Cwd); c != s.Changes {
		s.Changes = c
		changed = true
	}
	return changed
}

// SendBegin starts an assistant turn against the daemon. The user's text
// is appended to the transcript, state flips to Thinking, and the SSE
// stream is opened. The returned Stream is non-nil on success — the
// caller (TUI) is responsible for pumping it via Stream.Next() and
// applying each event back via ApplyEvent.
//
// Side effects on the session:
//   - appends UserItem{Text, Attachments}
//   - clears pending Attachments
//   - sets State=Thinking, StartedAt=now, turnHadOutput=false
//   - records activeStream + streamCancel so Cancel() can interrupt
func (s *Session) SendBegin(ctx context.Context, text string) (*client.Stream, error) {
	if s.c == nil {
		return nil, ErrNoEngine
	}
	if s.State == StateThinking {
		return nil, ErrSessionBusy
	}

	// Lazily create the server-side conversation on first send. Subsequent
	// turns reuse the same ConvID. Server tracks claude-code's
	// ProviderState in meta.json so we don't carry it on the wire.
	if s.ConvID == "" {
		meta, err := s.c.CreateConversation(ctx, s.agentSlug, s.Title, s.Model)
		if err != nil {
			return nil, fmt.Errorf("create conversation: %w", err)
		}
		s.ConvID = meta.ID
		if s.logger != nil {
			s.logger.Debug("conversation created", "session", s.ID, "conv_id", s.ConvID)
		}
	}

	wire := buildWireMessages(s.Items, text)

	turnCtx, cancel := context.WithCancel(ctx)
	stream, err := s.c.Turn(turnCtx, s.agentSlug, s.ConvID, client.TurnRequest{
		Messages: wire,
		Cwd:      s.Cwd,
	})
	if err != nil {
		cancel()
		return nil, err
	}

	s.Items = append(s.Items, UserItem{Text: text, Attachments: s.Attachments})
	s.Attachments = nil
	s.State = StateThinking
	s.StartedAt = time.Now()
	s.turnHadOutput = false
	s.activeStream = stream
	s.streamCancel = cancel

	if s.logger != nil {
		s.logger.Debug("turn started", "session", s.ID, "conv_id", s.ConvID)
	}
	return stream, nil
}

// ApplyEvent mutates Items + State based on one event from the daemon's
// stream. Returns true when the event ends the turn (Done or Error).
//
// Streaming text deltas merge into a single trailing AssistantTextItem
// so the transcript renders as one coherent message instead of many tiny
// fragments. Tool round-trips materialize as ToolUseItem entries that
// flip Done=true once the matching ToolResult arrives.
func (s *Session) ApplyEvent(ev client.Event) (terminal bool) {
	switch e := ev.(type) {
	case client.TextDelta:
		s.appendText(e.Text)
		s.turnHadOutput = true
	case client.ThinkingDelta:
		s.appendThinking(e.Text)
	case client.ToolUse:
		s.Items = append(s.Items, ToolUseItem{
			ID:    e.ID,
			Name:  e.Name,
			Input: []byte(e.Input),
		})
		s.turnHadOutput = true
	case client.ToolResult:
		s.linkToolResult(e.ToolUseID, e.Content, e.IsError)
	case client.Done:
		if !s.turnHadOutput {
			s.Items = append(s.Items, EmptyResponseItem{})
		}
		s.Items = append(s.Items, ResultItem{
			DurationMs: int(time.Since(s.StartedAt).Milliseconds()),
			CostUSD:    e.CostUSD,
			NumTurns:   1,
		})
		s.TotalCost += e.CostUSD
		s.Turns++
		s.State = StateIdle
		s.activeStream = nil
		s.streamCancel = nil
		return true
	case client.Error:
		s.LastErr = errors.New(e.Message)
		s.Items = append(s.Items, ErrorItem{Message: e.Message})
		s.State = StateError
		s.activeStream = nil
		s.streamCancel = nil
		return true
	}
	return false
}

// Cancel interrupts the in-flight turn. No-op when idle.
func (s *Session) Cancel() error {
	if s.streamCancel != nil {
		s.streamCancel()
	}
	if s.activeStream != nil {
		_ = s.activeStream.Close()
	}
	return nil
}

// appendText merges a streaming delta into the trailing assistant text
// block. Creates a fresh AssistantTextItem if the last item isn't one.
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

// linkToolResult finds the in-flight ToolUseItem with the matching ID
// and marks it Done with the result content. Falls back to a standalone
// ToolResultItem if no matching tool_use is in the transcript (which
// shouldn't normally happen but keeps things resilient).
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

// buildWireMessages flattens a session transcript into the user/assistant
// text turns the daemon expects. Tool round-trips and thinking blocks are
// dropped — claude-code reconstructs them via --resume; the anthropic
// provider doesn't need them since it gets the full text turns.
func buildWireMessages(items []Item, newUserText string) []client.Message {
	var wire []client.Message
	for _, it := range items {
		switch v := it.(type) {
		case UserItem:
			wire = append(wire, client.Message{Role: "user", Content: v.Text})
		case AssistantTextItem:
			if v.Text != "" {
				wire = append(wire, client.Message{Role: "assistant", Content: v.Text})
			}
		}
	}
	wire = append(wire, client.Message{Role: "user", Content: newUserText})
	return wire
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
