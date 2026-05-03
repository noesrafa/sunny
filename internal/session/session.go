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
)

// ErrNoEngine is returned by Send while the daemon's chat endpoint isn't
// wired up. v0.3 replaces this with a real HTTP call.
var ErrNoEngine = errors.New("session: no engine connected")

// ErrSessionBusy is returned by Send when a turn is already in flight.
// Callers should normally check State first instead of relying on it.
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
	RemoteID  string // engine-assigned conversation id (set by /chat once it lands)
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
}

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
	ResumeID                 string // engine conversation id to resume
	Title                    string
	Draft                    string

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
		RemoteID:  opts.ResumeID,
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

// Send is a placeholder that always errors with ErrNoEngine. The user's
// turn IS appended to the transcript so the input feels responsive, but
// no provider is contacted. v0.3 replaces the body with an HTTP POST to
// the daemon and an SSE stream consumer.
func (s *Session) Send(text string) error {
	if s.State == StateThinking {
		return ErrSessionBusy
	}
	s.Items = append(s.Items, UserItem{Text: text, Attachments: s.Attachments})
	s.Attachments = nil
	s.LastErr = ErrNoEngine
	s.State = StateError
	s.logger.Debug("send rejected", "reason", "no engine wired")
	return ErrNoEngine
}

// Cancel is a no-op. v0.3 routes this to the daemon's "abort current turn"
// endpoint.
func (s *Session) Cancel() error { return nil }

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
