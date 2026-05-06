package client

import "errors"

// ErrConvNotFound is returned by SendTurn when the daemon responds
// 404. Typically this means the saved ConvID points at a
// conversation that was archived or deleted out-of-band; callers
// (session.send) catch this to transparently start fresh.
var ErrConvNotFound = errors.New("conversation not found")

// Message is one user/assistant turn in a TurnRequest.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TurnRequest is the body of POST /agents/{id}/conversations/{conv_id}/turns.
//
// ProviderState is intentionally absent: the daemon tracks it in the
// conversation's meta.json so it survives daemon restarts. Clients
// only supply transcript + working dir.
type TurnRequest struct {
	Messages []Message `json:"messages"`
	Cwd      string    `json:"cwd,omitempty"`
}
