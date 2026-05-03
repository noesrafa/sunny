package session

import (
	"encoding/json"
	"fmt"
)

// Item is a sealed interface for entries in a session transcript.
// Render lives in the tui package to keep this package UI-free.
type Item interface{ sealed() }

// Attachment is a single image attached to a user turn. Path is absolute.
// Index is the 1-based marker number that appears in the user text as
// "[Image #N]" — kept here so the transcript renderer can rejoin them.
type Attachment struct {
	Index     int    `json:"index"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

type UserItem struct {
	Text        string       `json:"Text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}
type AssistantTextItem struct{ Text string }
type ThinkingItem struct{ Text string }

// ToolUseItem encapsulates a tool invocation and its eventual result.
// Done is false while the tool is executing; once the matching tool_result
// arrives, Done flips to true and Result/IsError are populated.
type ToolUseItem struct {
	ID      string
	Name    string
	Input   json.RawMessage
	Done    bool
	IsError bool
	Result  string
}

// ToolResultItem is a fallback for tool_result events that don't match any
// preceding tool_use (shouldn't normally happen, but kept for resilience).
type ToolResultItem struct{ Content string }

type ResultItem struct {
	IsError    bool
	DurationMs int
	CostUSD    float64
	NumTurns   int
}
type EmptyResponseItem struct{}
type ErrorItem struct{ Message string }

func (UserItem) sealed()          {}
func (AssistantTextItem) sealed() {}
func (ThinkingItem) sealed()      {}
func (ToolUseItem) sealed()       {}
func (ToolResultItem) sealed()    {}
func (ResultItem) sealed()        {}
func (EmptyResponseItem) sealed() {}
func (ErrorItem) sealed()         {}

// envelope is the wire format for one Item: a "kind" tag plus the concrete
// payload. Used by MarshalItems / UnmarshalItems so we can persist the
// transcript across runs.
type envelope struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// MarshalItems serializes a slice of Items as a tagged-union JSON array.
func MarshalItems(items []Item) ([]byte, error) {
	envs := make([]envelope, 0, len(items))
	for _, it := range items {
		kind, payload, err := encodeItem(it)
		if err != nil {
			return nil, err
		}
		envs = append(envs, envelope{Kind: kind, Data: payload})
	}
	return json.Marshal(envs)
}

// UnmarshalItems is the inverse of MarshalItems. Unknown kinds are skipped
// silently so format upgrades don't crash older binaries.
func UnmarshalItems(raw []byte) ([]Item, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var envs []envelope
	if err := json.Unmarshal(raw, &envs); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(envs))
	for _, e := range envs {
		it, ok, err := decodeItem(e.Kind, e.Data)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func encodeItem(it Item) (string, json.RawMessage, error) {
	switch v := it.(type) {
	case UserItem:
		b, err := json.Marshal(v)
		return "user", b, err
	case AssistantTextItem:
		b, err := json.Marshal(v)
		return "assistant_text", b, err
	case ThinkingItem:
		b, err := json.Marshal(v)
		return "thinking", b, err
	case ToolUseItem:
		b, err := json.Marshal(v)
		return "tool_use", b, err
	case ToolResultItem:
		b, err := json.Marshal(v)
		return "tool_result", b, err
	case ResultItem:
		b, err := json.Marshal(v)
		return "result", b, err
	case EmptyResponseItem:
		return "empty_response", nil, nil
	case ErrorItem:
		b, err := json.Marshal(v)
		return "error", b, err
	}
	return "", nil, fmt.Errorf("session: unknown item type %T", it)
}

func decodeItem(kind string, raw json.RawMessage) (Item, bool, error) {
	switch kind {
	case "user":
		var v UserItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	case "assistant_text":
		var v AssistantTextItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	case "thinking":
		var v ThinkingItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	case "tool_use":
		var v ToolUseItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	case "tool_result":
		var v ToolResultItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	case "result":
		var v ResultItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	case "empty_response":
		return EmptyResponseItem{}, true, nil
	case "error":
		var v ErrorItem
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, err
		}
		return v, true, nil
	}
	return nil, false, nil
}
