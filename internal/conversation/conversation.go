// Package conversation persists chat history to disk.
//
// Layout (per-agent):
//
//	~/.sunny/agents/<slug>/conversations/<conv_id>/
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
)

// ErrNotFound is returned when an agent or conversation directory
// doesn't exist.
var ErrNotFound = errors.New("conversation not found")

// Meta is the rollup written to meta.json.
type Meta struct {
	ID        string    `json:"id"`
	AgentSlug string    `json:"agent_slug"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	MsgCount  int       `json:"msg_count"`
	Model     string    `json:"model,omitempty"`
	TotalCost float64   `json:"total_cost_usd,omitempty"`
	// ProviderState is opaque to the persistence layer. For claude-code
	// it's the session id used by --resume; the engine reads this on the
	// next turn so context survives daemon restarts.
	ProviderState string `json:"provider_state,omitempty"`
}

// Event is one entry in events.jsonl. Payload is the raw JSON body for
// the event kind — readers decode based on Kind.
type Event struct {
	Kind    string          `json:"kind"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Store is a filesystem-backed conversation registry, rooted at
// ~/.sunny/. It locates per-agent conversations by agent slug.
type Store struct {
	root string
}

func NewStore(root string) *Store { return &Store{root: root} }

// Create allocates a new conversation directory under the given agent
// and writes the initial meta.json. Title is optional ("" → "untitled").
func (s *Store) Create(agentSlug, title, model string) (*Meta, error) {
	if !validSlug(agentSlug) {
		return nil, fmt.Errorf("invalid agent slug %q", agentSlug)
	}
	if _, err := os.Stat(s.agentDir(agentSlug)); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: agent %s", ErrNotFound, agentSlug)
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
		AgentSlug: agentSlug,
		Title:     title,
		Model:     model,
		CreatedAt: now,
		UpdatedAt: now,
	}
	dir := s.convDir(agentSlug, id)
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

// List returns metas for every conversation under an agent, newest first.
// Missing agents → empty slice (not an error).
func (s *Store) List(agentSlug string) ([]*Meta, error) {
	if !validSlug(agentSlug) {
		return nil, fmt.Errorf("invalid agent slug %q", agentSlug)
	}
	convsDir := filepath.Join(s.agentDir(agentSlug), "conversations")
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
		m, err := readMeta(filepath.Join(convsDir, e.Name()))
		if err != nil {
			// Skip malformed entries instead of failing the whole list —
			// a half-written meta shouldn't take out the sidebar.
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Get returns the meta + every event for a conversation.
func (s *Store) Get(agentSlug, convID string) (*Meta, []Event, error) {
	dir, err := s.requireConv(agentSlug, convID)
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

// Delete moves a conversation directory to ~/.sunny/.trash/. Idempotent
// for missing conversations (no-op).
func (s *Store) Delete(agentSlug, convID string) error {
	dir, err := s.requireConv(agentSlug, convID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	trashDir := filepath.Join(s.root, ".trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	target := filepath.Join(trashDir, fmt.Sprintf("%s__%s__%s", agentSlug, convID, stamp))
	return os.Rename(dir, target)
}

// Append adds one event to the JSONL journal. Stamps Now() if At is zero.
func (s *Store) Append(agentSlug, convID string, ev Event) error {
	dir, err := s.requireConv(agentSlug, convID)
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
func (s *Store) UpdateMeta(agentSlug, convID string, fn func(*Meta)) error {
	dir, err := s.requireConv(agentSlug, convID)
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

func (s *Store) agentDir(slug string) string {
	return filepath.Join(s.root, "agents", slug)
}

func (s *Store) convDir(slug, id string) string {
	return filepath.Join(s.agentDir(slug), "conversations", id)
}

// requireConv resolves the conversation directory and verifies it
// exists. Used at the top of every mutator/reader to fail loud.
func (s *Store) requireConv(slug, id string) (string, error) {
	if !validSlug(slug) {
		return "", fmt.Errorf("invalid agent slug %q", slug)
	}
	if !validConvID(id) {
		return "", fmt.Errorf("invalid conv id %q", id)
	}
	dir := s.convDir(slug, id)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s/%s", ErrNotFound, slug, id)
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
		out = append(out, ev)
	}
	return out, scanner.Err()
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var convIDRe = regexp.MustCompile(`^conv_\d{13}_[a-f0-9]{8}$`)

func validSlug(s string) bool    { return s != "" && slugRe.MatchString(s) }
func validConvID(s string) bool  { return convIDRe.MatchString(s) }

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
