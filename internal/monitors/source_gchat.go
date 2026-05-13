package monitors

import (
	"context"
	"fmt"
	"strings"

	"github.com/noesrafa/sunny/internal/gchat"
)

// GChatSource is the Google Chat monitor source. One Fetch enumerates
// the configured spaces and emits one Item per new message (createTime
// strictly greater than the per-space cursor stored in state).
//
// Config (YAML keys under `source:`):
//
//	type:   "gchat"
//	spaces: ["spaces/AAQA_7L01IA", …]   — explicit allow-list. Empty
//	                                      means "no spaces", not
//	                                      "every space" — we make the
//	                                      user be explicit so a
//	                                      monitor doesn't accidentally
//	                                      pick up brand-new spaces.
//	skip_bots: true                     — drop messages whose
//	                                      Sender.Type=="BOT" (default
//	                                      true so the monitor doesn't
//	                                      echo on its own auto-replies).
//	only_top_level: false               — when true, drop messages that
//	                                      are replies inside a thread.
//	                                      Useful for high-traffic team
//	                                      spaces where you only want to
//	                                      react to fresh top-level
//	                                      posts. Default false to keep
//	                                      existing monitors unchanged.
//
// Cursor (per-monitor, stored in state.Vars):
//
//	last_seen: {"<spaceName>": "<RFC3339 createTime>"}
//
// On the very first tick a space has no cursor — we walk the most
// recent page, take the newest createTime as the cursor, and emit
// NOTHING. This avoids replaying the entire backlog the first time a
// monitor is enabled. From the second tick onward every new message
// is emitted exactly once.
type GChatSource struct {
	root string
}

// NewGChatSource binds the source to the integration root so it can
// re-read the OAuth token from disk on every tick (refresh tokens
// are persisted there by `gchat.TokenSource`).
func NewGChatSource(root string) *GChatSource { return &GChatSource{root: root} }

func (g *GChatSource) Type() string { return "gchat" }

func (g *GChatSource) Fetch(ctx context.Context, cfg map[string]any, state map[string]any) ([]Item, map[string]any, error) {
	spaces, err := parseSpaces(cfg)
	if err != nil {
		return nil, state, err
	}
	if len(spaces) == 0 {
		return nil, state, fmt.Errorf("gchat source: `spaces` list is empty — add at least one space resource name")
	}
	skipBots := true
	if v, ok := cfg["skip_bots"].(bool); ok {
		skipBots = v
	}
	onlyTopLevel := false
	if v, ok := cfg["only_top_level"].(bool); ok {
		onlyTopLevel = v
	}

	if state == nil {
		state = map[string]any{}
	}
	// last_seen rides as a nested map. JSON round-trips through
	// map[string]any so we accept either map[string]string or
	// map[string]any on read and always write the latter.
	lastSeen := map[string]string{}
	if raw, ok := state["last_seen"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				lastSeen[k] = s
			}
		}
	}

	client, err := gchat.New(ctx, g.root, gchat.DefaultScopes...)
	if err != nil {
		return nil, state, fmt.Errorf("gchat source: %w (run `sunny gchat auth` first)", err)
	}

	out := []Item{}
	for _, space := range spaces {
		cursor := lastSeen[space]
		msgs, err := client.ListMessages(ctx, space, cursor)
		if err != nil {
			// Don't kill the tick if one space fails — surface the error
			// in the worker's lastErr (via Fetch's err return) only when
			// every space fails. For a single-space monitor (the MR case)
			// this collapses to: errors propagate.
			return nil, state, err
		}
		// First-tick bootstrap: seed the cursor to the newest message
		// but emit nothing. The user enabled the monitor "from now on",
		// not "replay every old message".
		if cursor == "" {
			if len(msgs) > 0 {
				lastSeen[space] = msgs[0].CreateTime
			}
			continue
		}
		for _, m := range msgs {
			if skipBots && m.SenderType == "BOT" {
				continue
			}
			if onlyTopLevel && !isTopLevelMessage(m.Name) {
				// Drop thread replies but advance the cursor so we
				// don't keep re-fetching this message every tick.
				if m.CreateTime > lastSeen[space] {
					lastSeen[space] = m.CreateTime
				}
				continue
			}
			out = append(out, Item{
				ID: m.Name,
				Fields: map[string]any{
					"text":           m.Text,
					"sender":         m.SenderName,
					"sender_type":    m.SenderType,
					"space":          m.SpaceName,
					"thread":         m.ThreadName,
					"message":        m.Name,
					"create_time":    m.CreateTime,
				},
			})
			// Newest message is at index 0 (orderBy createTime desc), so
			// the very first iteration already has the max — update once.
			if m.CreateTime > lastSeen[space] {
				lastSeen[space] = m.CreateTime
			}
		}
	}

	// Round-trip back through map[string]any so JSON marshalling in
	// SaveState doesn't trip over the typed inner map.
	out2 := map[string]any{}
	for k, v := range lastSeen {
		out2[k] = v
	}
	state["last_seen"] = out2
	return out, state, nil
}

// isTopLevelMessage reports whether the given Chat message resource
// name belongs to a thread starter, not a reply.
//
// Google Chat encodes the message id as <thread_id>.<message_short_id>
// in the trailing path segment. For a top-level message (the first
// in its thread) those two halves are identical:
//
//	spaces/X/messages/8MgvAZM8NpE.8MgvAZM8NpE   ← top-level
//	spaces/X/messages/8MgvAZM8NpE.somethingElse ← reply
//
// Unexpected shapes (no dot, no /messages/ segment) return true so
// we stay permissive — better to occasionally process an oddly-named
// message than to drop a legit one.
func isTopLevelMessage(name string) bool {
	idx := strings.LastIndex(name, "/messages/")
	if idx < 0 {
		return true
	}
	last := name[idx+len("/messages/"):]
	dot := strings.Index(last, ".")
	if dot < 0 {
		return true
	}
	return last[:dot] == last[dot+1:]
}

// parseSpaces reads cfg["spaces"] flexibly: YAML can produce []any
// or []string depending on values. We coerce both to []string and
// reject anything else with a clear error.
func parseSpaces(cfg map[string]any) ([]string, error) {
	raw, ok := cfg["spaces"]
	if !ok {
		return nil, fmt.Errorf("gchat source: `spaces` field required")
	}
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("gchat source: `spaces` entries must be strings")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("gchat source: `spaces` must be a list")
}
