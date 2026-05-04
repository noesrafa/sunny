package session

import (
	"encoding/json"
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
