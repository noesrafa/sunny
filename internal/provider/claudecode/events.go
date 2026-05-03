// Package claudecode implements provider.Provider on top of the `claude`
// CLI binary. The wire format mirrors what `claude --output-format
// stream-json` produces; types here are union-shaped per event kind.
package claudecode

import "encoding/json"

// rawEvent is one line emitted by `claude --output-format stream-json`.
// Fields are union-typed across event kinds; check Type/Subtype before
// reading.
type rawEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	UUID      string `json:"uuid,omitempty"`

	Cwd   string `json:"cwd,omitempty"`
	Model string `json:"model,omitempty"`

	Message *cliMessage `json:"message,omitempty"`

	IsError      bool    `json:"is_error,omitempty"`
	DurationMs   int     `json:"duration_ms,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	Result       string  `json:"result,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`

	// Raw holds the original line. Useful for surfacing parse errors and
	// for debugging-only logging; never used in business logic.
	Raw json.RawMessage `json:"-"`
}

type cliMessage struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Model   string         `json:"model"`
	Content []contentBlock `json:"content"`
	Usage   *usage         `json:"usage,omitempty"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}
