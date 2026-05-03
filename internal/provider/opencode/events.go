// Package opencode implements provider.Provider on top of the `opencode`
// CLI binary. The wire format mirrors what `opencode run --format json`
// produces: one JSON event per line, each tagged with a `type` and the
// owning `sessionID`.
package opencode

import "encoding/json"

// rawEvent is one line emitted by `opencode run --format json`. The
// emit() function in opencode's run.ts always wraps payloads as
// `{type, timestamp, sessionID, ...rest}`. The "rest" is union-typed
// per event kind so we keep it as RawMessage and decode lazily.
type rawEvent struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp,omitempty"`
	SessionID string          `json:"sessionID,omitempty"`
	Part      json.RawMessage `json:"part,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
	// Raw holds the original line for parse-error surfacing.
	Raw json.RawMessage `json:"-"`
}

// textPart matches the shape of `text` and `reasoning` events. Only
// emitted when part.time.end is set, so this always carries the final
// text for the block (no token-level streaming in JSON mode).
type textPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
}

// toolPart matches the shape of `tool_use` events. Only emitted once
// the tool reaches a terminal state (status: completed | error). State
// carries the input/output/error captured by opencode's tool runner.
type toolPart struct {
	ID        string    `json:"id"`
	MessageID string    `json:"messageID"`
	CallID    string    `json:"callID"`
	Tool      string    `json:"tool"`
	State     toolState `json:"state"`
}

type toolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
	Title  string          `json:"title,omitempty"`
}

// stepFinishPart matches `step_finish` events. We pull tokens, cost,
// and the stop reason from here for our Done event.
type stepFinishPart struct {
	ID        string  `json:"id"`
	MessageID string  `json:"messageID"`
	Type      string  `json:"type"`
	Reason    string  `json:"reason"`
	Cost      float64 `json:"cost"`
	Tokens    *tokens `json:"tokens,omitempty"`
}

type tokens struct {
	Total     int64       `json:"total"`
	Input     int64       `json:"input"`
	Output    int64       `json:"output"`
	Reasoning int64       `json:"reasoning"`
	Cache     *cacheUsage `json:"cache,omitempty"`
}

type cacheUsage struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}
