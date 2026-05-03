package claude

import "encoding/json"

// Event is a single line emitted by `claude --output-format stream-json`.
// Fields are union-typed across event kinds; check Type/Subtype before reading.
type Event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	UUID      string `json:"uuid,omitempty"`

	Cwd   string `json:"cwd,omitempty"`
	Model string `json:"model,omitempty"`

	Message *Message `json:"message,omitempty"`

	IsError      bool    `json:"is_error,omitempty"`
	DurationMs   int     `json:"duration_ms,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	Result       string  `json:"result,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`

	RateLimitInfo *RateLimitInfo `json:"rate_limit_info,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// RateLimitInfo is what Claude Code emits in `rate_limit_event` messages.
// resetsAt is a Unix timestamp.
type RateLimitInfo struct {
	Status         string `json:"status"`
	ResetsAt       int64  `json:"resetsAt"`
	RateLimitType  string `json:"rateLimitType"`
	OverageStatus  string `json:"overageStatus"`
	OverageResetsAt int64 `json:"overageResetsAt"`
	IsUsingOverage bool   `json:"isUsingOverage"`
}

type Message struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Model   string         `json:"model"`
	Content []ContentBlock `json:"content"`
	Usage   *Usage         `json:"usage,omitempty"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}
