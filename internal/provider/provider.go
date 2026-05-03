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
	StopReason            string
	InputTokens           int64
	OutputTokens          int64
	CacheCreationTokens   int64
	CacheReadTokens       int64
}

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
