// Package provider abstracts the LLM backend. v0.3.0 ships one driver
// (anthropic). The interface is small on purpose — sunny is opinionated
// toward streaming, system-prompt-with-cache, and assistant-text deltas.
// Tools land in v0.4.
package provider

import "context"

// Message is a single conversational turn passed to the provider.
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
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
