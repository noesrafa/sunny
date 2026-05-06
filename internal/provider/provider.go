// Package provider abstracts the LLM backend. The interface is small
// on purpose — sunny is opinionated toward streaming, system-prompt-
// with-cache, and assistant-text deltas. Driver implementations
// translate this neutral shape into each backend's wire format.
package provider

import (
	"context"
	"encoding/json"
)

// Message is a single conversational turn passed to the provider.
//
// For plain text turns, only Role + Content are set. Tool round-trips
// use the extended fields:
//
//   - Role="assistant" + ToolCalls non-nil: model invoked tools.
//     Content may also carry the assistant's free text from the same
//     turn. Drivers translate this to the provider's tool-use block
//     shape (Anthropic content[] with tool_use; Ollama tool_calls inline).
//
//   - Role="tool" + ToolUseID set: a tool's result. Drivers translate
//     to Anthropic tool_result blocks or Ollama role:"tool" messages.
//     Content carries the rendered output; IsError surfaces failures.
type Message struct {
	Role      string         `json:"role"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []ToolCall     `json:"tool_calls,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"` // role=tool only
	IsError   bool           `json:"is_error,omitempty"`    // role=tool only
}

// ToolCall is one tool invocation inside an assistant message.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolDef is the schema sent to the provider so the model can decide
// to call it. Generated from internal/tools.Tool at request time.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// SystemBlock is one chunk of the system prompt. Marking CacheControl=true
// on a block tells the provider to drop a cache breakpoint after it. The
// engine builds these so stable content (skills, knowledge) sits before
// the breakpoint and per-request content sits after.
type SystemBlock struct {
	Text         string
	CacheControl bool
}

// Request is the input to one turn.
type Request struct {
	Model     string
	MaxTokens int
	System    []SystemBlock
	Messages  []Message
	// Tools, if non-empty, advertise tool definitions to the
	// provider so the model can request invocations. Drivers
	// translate to each backend's wire format. The engine collects
	// tool_use events from the stream, runs them, and re-issues
	// Stream with appended tool_result messages.
	Tools []ToolDef
	// Effort controls overall token spend ("low" | "medium" | "high" | "xhigh" | "max").
	// Defaults to "high" if empty.
	Effort string
	// AdaptiveThinking toggles thinking={type:"adaptive"}. When true the
	// thinking content is also requested as "summarized" so the engine
	// can surface it.
	AdaptiveThinking bool
	// Cwd is the working directory the provider should execute in. Used by
	// the claude-code provider to scope file/bash tools; the anthropic
	// provider ignores it.
	Cwd string
	// ProviderState is opaque token returned by a previous Done event,
	// passed back so the provider can resume conversation context.
	// claude-code: --resume <session_id>. anthropic API: ignored (the
	// caller carries the full Messages slice instead).
	ProviderState string
}

// Event is one item in the streamed response.
type Event interface{ providerEvent() }

// TextDelta carries one chunk of streamed assistant text.
type TextDelta struct{ Text string }

func (TextDelta) providerEvent() {}

// ThinkingDelta carries one chunk of streamed thinking text (when adaptive
// thinking with summarized display is on).
type ThinkingDelta struct{ Text string }

func (ThinkingDelta) providerEvent() {}

// Done marks the end of the assistant turn.
type Done struct {
	StopReason          string
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	// ProviderState is the token the caller should pass on the next Turn
	// to continue this conversation. Empty when the provider is stateless.
	ProviderState string
	// CostUSD is the wire-reported cost of the turn (claude-code only;
	// anthropic API does not surface it directly).
	CostUSD float64
}

// SessionState carries an opaque resume token mid-turn so callers can
// persist it before the turn finishes. Drivers emit this as soon as
// they know the token (claude-code: at the first `system.init` event)
// so a cancel or error before Done still leaves a valid `--resume`
// target in the conversation's meta.json. Without it, a turn that
// streams output for minutes and then gets cancelled would lose all
// session continuity even though the underlying provider already had
// the context loaded.
type SessionState struct{ State string }

func (SessionState) providerEvent() {}

// ToolUse is emitted when the provider's underlying engine starts a tool
// call. The claude-code provider surfaces these for visibility; the
// anthropic API provider does not yet (tools land in v0.4).
type ToolUse struct {
	ID    string
	Name  string
	Input string // serialized JSON of the input
}

func (ToolUse) providerEvent() {}

// ToolResult is emitted when a tool call finishes.
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

func (ToolResult) providerEvent() {}

func (Done) providerEvent() {}

// Error carries a fatal error from the provider stream. Non-fatal errors
// (timeouts, retries) are handled inside the driver.
type Error struct{ Err error }

func (Error) providerEvent() {}

// Provider streams events for one turn. The returned channel closes after
// either Done or Error has been emitted; callers must drain it to release
// the goroutine.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}
