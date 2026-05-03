package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/log/v2"

	"github.com/noesrafa/sunny/internal/claude"
)

// imageMarkerRE matches the placeholders we drop into the textarea on paste.
// Captures the number so we can rejoin marker → Attachment when sending.
var imageMarkerRE = regexp.MustCompile(`\[Image #(\d+)\]`)

// ErrSessionBusy is returned by Send when a turn is already in flight. It is
// an expected state, not a fault — callers should normally check State first
// instead of relying on this.
var ErrSessionBusy = errors.New("session busy")

// gitBranch returns the current branch of the given directory, or "" if it's
// not a git repo or git is unavailable. Uses --show-current which handles
// fresh repos with no commits gracefully (rev-parse fails on those).
func gitBranch(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ChangeStats summarizes the working tree against HEAD. Counts are file-level
// (one file with mixed staged + unstaged edits is still one Modified). Path
// is bucketed by the most "destructive" status it carries (Deleted wins over
// Modified, Modified over Added) so the indicator never under-reports a
// pending removal.
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

// gitChangeStats parses `git status --porcelain` into per-bucket file counts.
// Returns a zero ChangeStats when cwd isn't a git repo.
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
		// Untracked: "??" wins outright.
		if st == "??" {
			c.Untracked++
			continue
		}
		// Walk the two status columns picking the most destructive code.
		x, y := rune(st[0]), rune(st[1])
		switch {
		case x == 'D' || y == 'D':
			c.Deleted++
		case x == 'M' || y == 'M' || x == 'R' || y == 'R' || x == 'C' || y == 'C':
			c.Modified++
		case x == 'A' || y == 'A':
			c.Added++
		default:
			c.Modified++ // fallback bucket for unfamiliar codes
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
	RemoteID  string // claude session_id (set after first init)
	Cwd       string
	Title     string
	Model     string
	Effort    string // claude --effort level (low/medium/high/xhigh/max)
	Branch    string // git branch of Cwd (cached at creation)
	Changes   ChangeStats // per-status counts of pending changes
	State     State
	Items     []Item
	TotalCost float64
	Turns     int
	LastErr   error
	StartedAt time.Time

	// RateLimit captures the last rate_limit_event from claude. Shared across
	// the user's account, but we store on the most-recently-active session.
	RateLimit *claude.RateLimitInfo

	// Draft is the current unsent textarea content. Saved when the user
	// switches sessions and restored when they switch back.
	Draft string

	// Attachments are images the user has pasted into the current draft but
	// not yet sent. Cleared on Send. Each carries the [Image #N] index that
	// appears as a marker inside Draft.
	Attachments   []Attachment
	attachmentSeq int

	Stream *claude.Stream

	logger        *log.Logger
	turnHadOutput bool
}

// AddAttachment registers a clipboard image with the session and returns
// the 1-based index the caller should embed as "[Image #<idx>]" in the
// textarea draft. Indices are monotonic per session lifetime; they are
// not reused after sending so the user can paste, send, then paste again
// without seeing duplicate markers in transcript.
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
	ResumeID                 string // claude session_id to --resume on startup
	Title                    string // override default basename(cwd) title (used by state restore)
	Draft                    string // pre-populate the textarea draft (state restore)

	// Idle skips spawning a claude CLI subprocess. Send returns
	// claude.ErrIdle. Used by sunny while the daemon's chat endpoint is
	// not yet wired up.
	Idle bool

	// State-restore extras. Only consumed when reopening a previously-open
	// session — fresh sessions leave them zero.
	Items     []Item
	TotalCost float64
	Turns     int
}

func New(ctx context.Context, cwd string, opts Options) (*Session, error) {
	if cwd == "" {
		return nil, fmt.Errorf("session: cwd required")
	}
	stream, err := claude.NewStream(ctx, claude.StreamOpts{
		Cwd:                      cwd,
		Model:                    opts.Model,
		Effort:                   opts.Effort,
		DangerousSkipPermissions: opts.DangerousSkipPermissions,
		SessionID:                opts.ResumeID,
		Idle:                     opts.Idle,
	})
	if err != nil {
		return nil, err
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
		RemoteID:  opts.ResumeID, // optimistic; will be confirmed when init event arrives
		Draft:     opts.Draft,
		Items:     opts.Items,
		TotalCost: opts.TotalCost,
		Turns:     opts.Turns,
		Stream:    stream,
		State:     StateIdle,
		logger:    logger,
	}, nil
}

// RefreshBranch re-reads the current git branch of Cwd and updates Branch.
// Returns true if the branch or dirty state changed, so the caller can decide
// whether to re-render. Cheap (two git invocations), but callers should still
// throttle.
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

// Cancel interrupts the current turn (SIGINT to the claude subprocess).
func (s *Session) Cancel() error {
	if s.Stream == nil {
		return nil
	}
	s.logger.Info("session cancel requested")
	return s.Stream.Cancel()
}

// Resume spawns a fresh claude subprocess attached to this session's
// RemoteID (claude --resume), replacing s.Stream. Used when the previous
// stream died — typically because the user hit ctrl+c and claude exited
// instead of cleanly aborting the turn — so the user can keep chatting in
// the same session without losing the transcript or context.
//
// Caller is responsible for re-arming the events-channel reader (the TUI
// does this via waitForSession) since the channel identity changed.
func (s *Session) Resume(ctx context.Context, skipPerms bool) error {
	stream, err := claude.NewStream(ctx, claude.StreamOpts{
		Cwd:                      s.Cwd,
		Model:                    s.Model,
		Effort:                   s.Effort,
		DangerousSkipPermissions: skipPerms,
		SessionID:                s.RemoteID,
	})
	if err != nil {
		return err
	}
	s.Stream = stream
	if s.State == StateError || s.State == StateThinking {
		s.State = StateIdle
	}
	s.LastErr = nil
	s.turnHadOutput = false
	s.logger.Info("session resumed", "remote_id", s.RemoteID)
	return nil
}

// Send dispatches a user turn. Caller must check State == StateIdle;
// otherwise ErrSessionBusy is returned without side effects.
//
// If the text contains "[Image #N]" markers and the session has matching
// pending Attachments, they're spliced into the wire payload as image
// content blocks (in order) and the attachment list is cleared. Markers
// without a matching attachment are passed through as plain text.
func (s *Session) Send(text string) error {
	if s.State == StateThinking {
		return ErrSessionBusy
	}
	blocks, used, err := s.buildContentBlocks(text)
	if err != nil {
		return err
	}
	s.logger.Debug("send", "len", len(text), "attachments", len(used))
	s.Items = append(s.Items, UserItem{Text: text, Attachments: used})
	s.Attachments = nil
	s.State = StateThinking
	s.StartedAt = time.Now()
	s.turnHadOutput = false
	if err := s.Stream.SendBlocks(blocks); err != nil {
		s.State = StateError
		s.LastErr = err
		s.logger.Error("send failed", "err", err)
		return err
	}
	return nil
}

// buildContentBlocks splits text by [Image #N] markers and interleaves
// matching image blocks. Returns the wire blocks plus the resolved set of
// attachments (so the UserItem can record what actually got sent).
func (s *Session) buildContentBlocks(text string) ([]map[string]any, []Attachment, error) {
	byIdx := make(map[int]Attachment, len(s.Attachments))
	for _, a := range s.Attachments {
		byIdx[a.Index] = a
	}

	var blocks []map[string]any
	var used []Attachment
	matches := imageMarkerRE.FindAllStringSubmatchIndex(text, -1)
	pos := 0
	flushText := func(end int) {
		if end <= pos {
			return
		}
		chunk := text[pos:end]
		if strings.TrimSpace(chunk) == "" && len(blocks) > 0 {
			// Avoid empty text blocks between images, but keep purely-whitespace
			// chunks if they're the only thing in the message (handled below).
			return
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": chunk})
	}
	for _, m := range matches {
		start, end := m[0], m[1]
		idx, _ := strconv.Atoi(text[m[2]:m[3]])
		att, ok := byIdx[idx]
		if !ok {
			continue // leave the literal "[Image #N]" in place — text passthrough
		}
		flushText(start)
		data, err := os.ReadFile(att.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("read attachment %d: %w", idx, err)
		}
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": att.MediaType,
				"data":       base64.StdEncoding.EncodeToString(data),
			},
		})
		used = append(used, att)
		delete(byIdx, idx) // a marker only resolves once
		pos = end
	}
	flushText(len(text))
	if len(blocks) == 0 {
		blocks = []map[string]any{{"type": "text", "text": text}}
	}
	return blocks, used, nil
}

// HandleEvent ingests one decoded claude event and updates transcript + state.
func (s *Session) HandleEvent(ev claude.Event) {
	s.logger.Debug("event", "type", ev.Type, "subtype", ev.Subtype)
	switch ev.Type {
	case "rate_limit_event":
		if ev.RateLimitInfo != nil {
			s.RateLimit = ev.RateLimitInfo
		}
	case "system":
		if ev.Subtype == "init" && s.RemoteID == "" {
			s.RemoteID = ev.SessionID
			s.Model = ev.Model
			s.logger.Info("session init", "remote", ev.SessionID, "model", ev.Model)
		}
	case "assistant":
		if ev.Message == nil {
			return
		}
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					s.Items = append(s.Items, AssistantTextItem{Text: b.Text})
					s.turnHadOutput = true
				}
			case "thinking":
				if strings.TrimSpace(b.Text) != "" {
					s.Items = append(s.Items, ThinkingItem{Text: b.Text})
				}
			case "tool_use":
				s.Items = append(s.Items, ToolUseItem{ID: b.ID, Name: b.Name, Input: b.Input})
				s.turnHadOutput = true
				s.logger.Info("tool_use", "name", b.Name, "id", b.ID)
			}
		}
	case "user":
		if ev.Message == nil {
			return
		}
		for _, b := range ev.Message.Content {
			if b.Type == "tool_result" {
				if !s.linkToolResult(b.ToolUseID, b.Content, b.IsError) {
					s.Items = append(s.Items, ToolResultItem{Content: extractToolResult(b.Content)})
				}
			}
		}
	case "result":
		if !s.turnHadOutput {
			s.Items = append(s.Items, EmptyResponseItem{})
		}
		s.Items = append(s.Items, ResultItem{
			IsError:    ev.IsError,
			DurationMs: ev.DurationMs,
			CostUSD:    ev.TotalCostUSD,
			NumTurns:   ev.NumTurns,
		})
		s.TotalCost += ev.TotalCostUSD
		s.Turns++
		s.State = StateIdle
		s.logger.Info("turn complete",
			"duration_ms", ev.DurationMs,
			"cost_usd", ev.TotalCostUSD,
			"is_error", ev.IsError,
		)
	}
}

// linkToolResult finds the most recent ToolUseItem with the given id and
// attaches the result inline. Returns false if no match was found.
func (s *Session) linkToolResult(id string, content json.RawMessage, isError bool) bool {
	if id == "" {
		return false
	}
	for i := len(s.Items) - 1; i >= 0; i-- {
		tu, ok := s.Items[i].(ToolUseItem)
		if !ok {
			continue
		}
		if tu.ID != id {
			continue
		}
		tu.Done = true
		tu.IsError = isError
		tu.Result = extractToolResult(content)
		s.Items[i] = tu
		s.logger.Info("tool_result", "name", tu.Name, "id", id, "is_error", isError, "result_len", len(tu.Result))
		return true
	}
	return false
}

// LiveStatus returns a short verb describing what the session is doing right now.
// Empty string when idle.
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

func (s *Session) Close() error {
	if s.Stream == nil {
		return nil
	}
	s.logger.Info("session closing")
	return s.Stream.Close()
}

// Reset replaces the underlying claude process with a fresh one — same cwd,
// model, effort — and clears the in-memory transcript. The previous Stream
// is closed best-effort. Returns the new Stream so callers can rebind any
// event-pump goroutines (e.g. waitForSession).
func (s *Session) Reset(ctx context.Context, dangerousSkipPermissions bool) error {
	if s.Stream != nil {
		_ = s.Stream.Close()
	}
	stream, err := claude.NewStream(ctx, claude.StreamOpts{
		Cwd:                      s.Cwd,
		Model:                    s.Model,
		Effort:                   s.Effort,
		DangerousSkipPermissions: dangerousSkipPermissions,
	})
	if err != nil {
		return err
	}
	s.Stream = stream
	s.RemoteID = ""
	s.Items = nil
	s.TotalCost = 0
	s.Turns = 0
	s.LastErr = nil
	s.State = StateIdle
	s.turnHadOutput = false
	s.Attachments = nil
	s.Draft = ""
	s.logger.Info("session reset")
	return nil
}

func extractToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, b := range arr {
			if t, ok := b["text"].(string); ok {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(raw)
}
