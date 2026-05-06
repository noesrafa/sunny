package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// WatchConversation opens a Server-Sent-Events connection to GET
// /agents/{id}/conversations/{conv_id}/watch and returns a channel that
// drains every journal event as it arrives. The channel closes when
// ctx is cancelled, the connection drops, or the daemon shuts down —
// callers should treat closure as "reconnect" intent (re-call with
// since = lastSeenSeq to resume without gaps).
//
// since=0 streams the full conversation history followed by live
// tailing. since=N skips events with Seq <= N — pass the highest seq
// you've already applied to resume cleanly after a reconnect.
//
// Heartbeat lines (`: …`) are silently skipped. Malformed payloads
// are dropped.
func (c *Client) WatchConversation(ctx context.Context, agentID, convID string, since int64) (<-chan JournalEvent, error) {
	url := c.base + "/agents/" + agentID + "/conversations/" + convID + "/watch"
	if since > 0 {
		url += "?since=" + strconv.FormatInt(since, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.auth(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET /watch: %d", resp.StatusCode)
	}

	out := make(chan JournalEvent, 64)
	go pumpWatchEvents(resp.Body, out)
	return out, nil
}

// pumpWatchEvents parses the watch SSE stream into JournalEvent values.
// The wire format is plain `data: <json>\n\n` frames — no `event:`
// tag because every payload has the same shape (kind lives in the JSON
// body).
func pumpWatchEvents(body io.ReadCloser, out chan<- JournalEvent) {
	defer close(out)
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<16), 1<<24)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // skip event:, :, blank separator, etc.
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var ev JournalEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		out <- ev
	}
}

// SendTurnResult is the 202 response body of a successful POST
// /turns. UserSeq is the journal seq the daemon assigned to the
// user's message — the sender uses it to dedup the self-echo when
// the same event arrives over its watch stream.
type SendTurnResult struct {
	ConvID  string `json:"conv_id"`
	UserSeq int64  `json:"user_seq"`
}

// SendTurn enqueues a new turn against a conversation. The daemon
// responds 202 immediately and processes the turn in the background;
// observe progress via WatchConversation against the same convID.
//
// Returns ErrConvNotFound when the conv vanished server-side (so the
// caller can recover by creating a new conversation), ErrTurnBusy
// when another turn is already running on this conv (the TUI should
// normally check state before sending), and other errors verbatim.
func (c *Client) SendTurn(ctx context.Context, agentID, convID string, body TurnRequest) (*SendTurnResult, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/agents/"+agentID+"/conversations/"+convID+"/turns", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		var out SendTurnResult
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("decode 202 body: %w", err)
		}
		return &out, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrConvNotFound, errorFromBody("POST /turns", resp).Error())
	case http.StatusConflict:
		return nil, ErrTurnBusy
	default:
		return nil, errorFromBody("POST /turns", resp)
	}
}

// RegenerateLastTurn truncates the conversation's journal back to the
// most recent user message and re-runs the engine so the assistant
// produces a fresh reply. Same trust model as SendTurn — the client
// constructs the message slice up to and including the user prompt
// to regenerate from. The daemon clears claude-code's session state
// so --resume doesn't try to chain off a now-deleted assistant turn.
//
// Streaming is identical to SendTurn: 202, then watch the conv for
// new events. The watcher's seq counter doesn't roll back, so
// resume-from-seq still works seamlessly.
//
// Returns ErrConvNotFound, ErrTurnBusy, and other errors verbatim
// like SendTurn. Returns a wrapped error when the journal has no
// user event to regenerate from (a fresh conv with zero turns).
func (c *Client) RegenerateLastTurn(ctx context.Context, agentID, convID string, body TurnRequest) error {
	resp, err := c.doJSON(ctx, http.MethodPost,
		"/agents/"+agentID+"/conversations/"+convID+"/regenerate", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrConvNotFound, errorFromBody("POST /regenerate", resp).Error())
	case http.StatusConflict:
		return ErrTurnBusy
	default:
		return errorFromBody("POST /regenerate", resp)
	}
}

// CancelTurn signals the daemon to interrupt the in-flight turn on
// this conversation. Idempotent — succeeds whether or not a turn was
// actually running.
func (c *Client) CancelTurn(ctx context.Context, agentID, convID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.base+"/agents/"+agentID+"/conversations/"+convID+"/turn", nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return errorFromBody("DELETE /turn", resp)
	}
	return nil
}

// ErrTurnBusy is returned by SendTurn when the conversation already
// has a turn in flight. The caller should wait for the in-flight
// turn to finish (visible via WatchConversation done/error/cancelled
// events) before retrying.
var ErrTurnBusy = errSentinel("turn busy")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
