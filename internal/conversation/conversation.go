// Package conversation persists chat history to disk.
//
// Layout (per-agent):
//
//	~/.sunny/agents/<agent_id>/conversations/<conv_id>/
//	  meta.json     — title, timestamps, msg_count, model, provider_state
//	  events.jsonl  — append-only journal: user, text_delta, thinking_delta,
//	                  tool_use, tool_result, done, error, cancelled
//
// The journal is the truth of what happened in a turn; meta.json is a
// rollup for cheap listing. ProviderState (claude-code session id for
// --resume) lives in meta so it survives daemon restarts.
//
// Concurrency: append-only writes to events.jsonl are line-atomic at
// reasonable record sizes. meta.json read-modify-write is NOT
// serialized — concurrent turns on the same conversation can lose a
// counter update. Acceptable for single-user personal use; revisit
// when we add mesh.
package conversation

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/agent"
)

// ErrNotFound is returned when an agent or conversation directory
// doesn't exist.
var ErrNotFound = errors.New("conversation not found")

// Meta is the rollup written to meta.json.
type Meta struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	MsgCount  int       `json:"msg_count"`
	Model     string    `json:"model,omitempty"`
	TotalCost float64   `json:"total_cost_usd,omitempty"`
	// Cwd is the working directory file/bash tools should resolve
	// against for every turn on this conv. Set at creation (from the
	// originating tab or an explicit request body) and immutable
	// afterwards — if the user wants a different cwd they spawn a new
	// conv. Empty means "no preference"; the engine falls back to
	// $HOME at runtime. Stored in meta so the conv stays self-
	// describing even if the originating tab gets closed or rebound.
	Cwd string `json:"cwd,omitempty"`
	// ProviderState is opaque to the persistence layer. For claude-code
	// it's the session id used by --resume; the engine reads this on the
	// next turn so context survives daemon restarts.
	ProviderState string `json:"provider_state,omitempty"`
	// LastMessagePreview is the rolled-up "last bit said" text used to
	// render conversation cards without scanning the journal. Updated
	// at user-send time (with the user's prompt) and at assistant-done
	// time (with the assistant's final text). Capped at ~140 chars.
	// Empty for pre-v0.39 convs that never had a turn since the
	// upgrade — List() lazy-backfills those by scanning events.jsonl
	// once and writing the result back.
	LastMessagePreview string `json:"last_message_preview,omitempty"`
}

// Event is one entry in events.jsonl. Payload is the raw JSON body for
// the event kind — readers decode based on Kind.
//
// Seq is a monotonic counter assigned by the publisher (see internal/conv.Sink)
// before Append is called. It is the wire identity of an event for resumable
// watchers: a client that disconnects at seq=42 reconnects with ?since=42 and
// only receives events with Seq > 42. Append does NOT assign Seq itself — the
// Store stays a dumb persistence layer; the Sink owns counter logic.
type Event struct {
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Store is a filesystem-backed conversation registry, rooted at
// ~/.sunny/. It locates per-agent conversations by agent id.
type Store struct {
	root string
}

func NewStore(root string) *Store { return &Store{root: root} }

// Create allocates a new conversation directory under the given agent
// and writes the initial meta.json. Title is optional ("" → "untitled").
// Cwd is the working directory the engine will use for tool execution
// on every turn against this conv; pass "" when the caller doesn't
// pin one and the engine should fall back to $HOME.
func (s *Store) Create(agentID, title, model, cwd string) (*Meta, error) {
	if !agent.ValidID(agentID) {
		return nil, fmt.Errorf("invalid agent id %q", agentID)
	}
	if _, err := os.Stat(s.agentDir(agentID)); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: agent %s", ErrNotFound, agentID)
		}
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = "untitled"
	}
	now := time.Now().UTC()
	meta := &Meta{
		ID:        id,
		AgentID:   agentID,
		Title:     title,
		Model:     model,
		Cwd:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
	}
	dir := s.convDir(agentID, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir conv: %w", err)
	}
	if err := writeMeta(dir, meta); err != nil {
		return nil, err
	}
	// Touch events.jsonl so consumers don't have to handle the
	// "exists with no events" case differently from "doesn't exist yet".
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("touch events.jsonl: %w", err)
	}
	_ = f.Close()
	return meta, nil
}

// Count returns the number of conversation directories under an
// agent without parsing any meta.json. Cheaper than List for /stats.
func (s *Store) Count(agentID string) (int, error) {
	if !agent.ValidID(agentID) {
		return 0, fmt.Errorf("invalid agent id %q", agentID)
	}
	convsDir := filepath.Join(s.agentDir(agentID), "conversations")
	entries, err := os.ReadDir(convsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n, nil
}

// List returns metas for every conversation under an agent, newest first.
// Missing agents → empty slice (not an error).
func (s *Store) List(agentID string) ([]*Meta, error) {
	if !agent.ValidID(agentID) {
		return nil, fmt.Errorf("invalid agent id %q", agentID)
	}
	convsDir := filepath.Join(s.agentDir(agentID), "conversations")
	entries, err := os.ReadDir(convsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Meta{}, nil
		}
		return nil, err
	}
	out := make([]*Meta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(convsDir, e.Name())
		m, err := readMeta(dir)
		if err != nil {
			// Skip malformed entries instead of failing the whole list —
			// a half-written meta shouldn't take out the sidebar.
			continue
		}
		// Backfill last_message_preview for convs that pre-date v0.39
		// (or were created mid-upgrade). One-time scan of events.jsonl
		// per conv, persisted, so subsequent listings are cheap. We
		// guard on MsgCount > 0 because empty convs have nothing to
		// preview anyway and we don't want to thrash disk on them.
		if m.LastMessagePreview == "" && m.MsgCount > 0 {
			if events, err := readEvents(dir); err == nil {
				if preview := ExtractPreview(events); preview != "" {
					m.LastMessagePreview = preview
					_ = writeMeta(dir, m)
				}
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// ExtractPreview walks the journal newest-first and returns a short
// excerpt suitable for the conversation list. Mirrors the app's
// previous client-side logic: the most recent user message wins; if
// the newest events are assistant text deltas, concatenate them.
// Capped at ~140 runes — long enough to read on a phone row, short
// enough to keep meta.json compact.
func ExtractPreview(events []Event) string {
	const maxRunes = 140
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		switch ev.Kind {
		case "user":
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				continue
			}
			return truncatePreview(p.Text, maxRunes)
		case "text_delta":
			// Glue consecutive text_deltas backwards so a partial
			// stream still surfaces the full assistant message so far.
			var tail strings.Builder
			for j := i; j >= 0; j-- {
				if events[j].Kind != "text_delta" {
					break
				}
				var p struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(events[j].Payload, &p); err != nil {
					continue
				}
				// Prepend by building a temporary slice — we walk
				// newest-first but want the natural order in output.
				prepend := p.Text + tail.String()
				tail.Reset()
				tail.WriteString(prepend)
			}
			return truncatePreview(tail.String(), maxRunes)
		}
	}
	return ""
}

func truncatePreview(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse internal whitespace so newlines don't waste preview
	// space — same idea as summarizeUserText for titles.
	fields := strings.Fields(s)
	joined := strings.Join(fields, " ")
	runes := []rune(joined)
	if len(runes) <= maxRunes {
		return joined
	}
	return string(runes[:maxRunes]) + "…"
}

// Get returns the meta + every event for a conversation.
func (s *Store) Get(agentID, convID string) (*Meta, []Event, error) {
	dir, err := s.requireConv(agentID, convID)
	if err != nil {
		return nil, nil, err
	}
	m, err := readMeta(dir)
	if err != nil {
		return nil, nil, err
	}
	events, err := readEvents(dir)
	if err != nil {
		return nil, nil, err
	}
	return m, events, nil
}

// Delete archives a conversation directory under ~/.sunny/.archive/.
// Idempotent for missing conversations (no-op). Restoration is manual:
// move the timestamped folder back into the agent's conversations/ dir.
func (s *Store) Delete(agentID, convID string) error {
	dir, err := s.requireConv(agentID, convID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	archiveDir := filepath.Join(s.root, ".archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	target := filepath.Join(archiveDir, fmt.Sprintf("%s__%s__%s", agentID, convID, stamp))
	return os.Rename(dir, target)
}

// Truncate rewrites events.jsonl to keep only events whose seq is
// <= keepUntil. Used by the regenerate flow: drop the last assistant
// turn (every text_delta / tool_use / tool_result / done after the
// last user event) so the engine can re-run from that point.
//
// Atomic: writes to a sibling .tmp file, then renames over events.jsonl.
// A concurrent watcher reading the file mid-rename is safe — POSIX
// rename is atomic and the watcher's open fd keeps pointing at the
// old inode until it reopens.
//
// The sink's seq counter is NOT rolled back. New events appended
// after a truncate get higher seqs (with a gap) which keeps watcher
// resume semantics correct.
func (s *Store) Truncate(agentID, convID string, keepUntil int64) error {
	dir, err := s.requireConv(agentID, convID)
	if err != nil {
		return err
	}
	events, err := readEvents(dir)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "events.jsonl.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	for _, e := range events {
		if e.Seq > keepUntil {
			continue
		}
		data, err := json.Marshal(e)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("marshal event: %w", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("write event: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	return os.Rename(tmp, filepath.Join(dir, "events.jsonl"))
}

// Append adds one event to the JSONL journal. Stamps Now() if At is zero.
func (s *Store) Append(agentID, convID string, ev Event) error {
	dir, err := s.requireConv(agentID, convID)
	if err != nil {
		return err
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open events.jsonl: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// UpdateMeta read-modify-writes meta.json. The caller's fn mutates the
// loaded value in place; UpdatedAt is refreshed automatically.
func (s *Store) UpdateMeta(agentID, convID string, fn func(*Meta)) error {
	dir, err := s.requireConv(agentID, convID)
	if err != nil {
		return err
	}
	m, err := readMeta(dir)
	if err != nil {
		return err
	}
	fn(m)
	m.UpdatedAt = time.Now().UTC()
	return writeMeta(dir, m)
}

func (s *Store) agentDir(agentID string) string {
	return filepath.Join(s.root, "agents", agentID)
}

func (s *Store) convDir(agentID, id string) string {
	return filepath.Join(s.agentDir(agentID), "conversations", id)
}

// requireConv resolves the conversation directory and verifies it
// exists. Used at the top of every mutator/reader to fail loud.
func (s *Store) requireConv(agentID, id string) (string, error) {
	if !agent.ValidID(agentID) {
		return "", fmt.Errorf("invalid agent id %q", agentID)
	}
	if !validConvID(id) {
		return "", fmt.Errorf("invalid conv id %q", id)
	}
	dir := s.convDir(agentID, id)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s/%s", ErrNotFound, agentID, id)
		}
		return "", err
	}
	return dir, nil
}

func writeMeta(dir string, m *Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Atomic-ish: write to .tmp, rename. Avoids leaving a half-written
	// meta if the process is killed mid-write.
	tmp := filepath.Join(dir, "meta.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}
	return os.Rename(tmp, filepath.Join(dir, "meta.json"))
}

func readMeta(dir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("read meta.json: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse meta.json: %w", err)
	}
	return &m, nil
}

func readEvents(dir string) ([]Event, error) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<16), 1<<24) // SSE payloads can be large
	var out []Event
	// pos counts kept (non-skipped) lines so synthesized seqs stay
	// monotonic and match what the Sink would have assigned. Pre-seq
	// journals (Seq==0 on every line) end up with file-position seqs
	// 1, 2, 3, … — same shape as future journals, just without the
	// seq stored in disk.
	var pos int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// Skip malformed lines; future-format-tolerance.
			continue
		}
		pos++
		if ev.Seq == 0 {
			ev.Seq = pos
		}
		out = append(out, ev)
	}
	return out, scanner.Err()
}

var convIDRe = regexp.MustCompile(`^conv_\d{13}_[a-f0-9]{8}$`)

func validConvID(s string) bool { return convIDRe.MatchString(s) }

// newID returns a sortable, unique conversation id of the shape
// conv_<unix_ms>_<8hex>. ms timestamp gives natural sort by
// creation; 32 random bits make collisions vanishingly unlikely
// even on rapid-fire creation.
func newID() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return fmt.Sprintf("conv_%013d_%s", time.Now().UnixMilli(), hex.EncodeToString(rnd[:])), nil
}
