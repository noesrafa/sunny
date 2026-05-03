// Package client is the TUI's HTTP client to the sunny daemon.
//
// One method that matters: Turn opens a streaming chat turn and returns a
// Stream the caller can pump for events until Done or Error. The Stream
// is cancelable via its context — closing it interrupts the in-flight
// request.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	base string
	hc   *http.Client
}

func New(addr string) *Client {
	return &Client{
		base: "http://" + addr,
		// No global timeout — turns can be long. The caller's context
		// owns lifetime.
		hc: &http.Client{},
	}
}

// AgentSummary is one row of GET /agents.
type AgentSummary struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Skills      int    `json:"skills"`
	Knowledge   int    `json:"knowledge"`
}

func (c *Client) ListAgents(ctx context.Context) ([]AgentSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agents", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /agents: %d: %s", resp.StatusCode, string(body))
	}
	var out []AgentSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Message is one user/assistant turn passed to the daemon.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TurnRequest is the body of POST /agents/{slug}/turn.
type TurnRequest struct {
	Messages      []Message `json:"messages"`
	ProviderState string    `json:"provider_state,omitempty"`
	Cwd           string    `json:"cwd,omitempty"`
}

// Event is a typed sum over the SSE event stream.
type Event interface{ chatEvent() }

type TextDelta struct{ Text string }
type ThinkingDelta struct{ Text string }
type ToolUse struct {
	ID    string
	Name  string
	Input string
}
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}
type Done struct {
	StopReason          string
	ProviderState       string
	CostUSD             float64
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}
type Error struct{ Message string }

func (TextDelta) chatEvent()     {}
func (ThinkingDelta) chatEvent() {}
func (ToolUse) chatEvent()       {}
func (ToolResult) chatEvent()    {}
func (Done) chatEvent()          {}
func (Error) chatEvent()         {}

// Stream is an open SSE response. Next blocks until the daemon emits one
// event, the context is cancelled, or the stream ends. On a terminal
// event (Done or Error) Next returns ok=true with that event, then on
// the next call returns ok=false.
type Stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	resp   *http.Response
	scan   *bufio.Scanner
	closed bool
}

// Turn opens POST /agents/{slug}/turn and returns the live SSE stream.
// Cancellation: pass a cancellable ctx; calling Stream.Close also
// cancels.
func (c *Client) Turn(parent context.Context, slug string, body TurnRequest) (*Stream, error) {
	ctx, cancel := context.WithCancel(parent)

	buf, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/agents/"+slug+"/turn", strings.NewReader(string(buf)))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.hc.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("POST /agents/%s/turn: %d: %s", slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scan := bufio.NewScanner(resp.Body)
	// SSE event payloads can be large (tool_result with file contents,
	// etc.) — bump well past Scanner's default 64KB.
	scan.Buffer(make([]byte, 1<<16), 1<<24)
	return &Stream{ctx: ctx, cancel: cancel, resp: resp, scan: scan}, nil
}

// Next blocks until the next SSE event arrives, the context is cancelled,
// or the stream ends. Returns (event, true, nil) on a successful read,
// (nil, false, nil) on clean end-of-stream, and (nil, false, err) on
// failure (network, malformed payload, cancellation).
func (s *Stream) Next() (Event, bool, error) {
	if s.closed {
		return nil, false, nil
	}

	var (
		eventName string
		dataLines []string
	)
	for s.scan.Scan() {
		// Bail early if the caller cancelled.
		if err := s.ctx.Err(); err != nil {
			return nil, false, err
		}
		line := s.scan.Text()
		switch {
		case line == "":
			// Blank line terminates an event. Decode and return.
			if eventName == "" {
				continue
			}
			ev, err := decodeEvent(eventName, strings.Join(dataLines, "\n"))
			if err != nil {
				return nil, false, err
			}
			if _, terminal := ev.(Done); terminal {
				// Don't close yet — let the caller drain. But the next
				// read will return ok=false.
				s.closed = true
			}
			if _, terminal := ev.(Error); terminal {
				s.closed = true
			}
			return ev, true, nil
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, ":"):
			// SSE comment / heartbeat — ignore.
		}
	}
	if err := s.scan.Err(); err != nil {
		return nil, false, err
	}
	// Clean EOF — the daemon closed the connection without a Done event.
	// Treat as orderly end.
	return nil, false, nil
}

// Close cancels the in-flight request and releases the response body.
func (s *Stream) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.resp != nil {
		_ = s.resp.Body.Close()
	}
	s.closed = true
	return nil
}

func decodeEvent(name, data string) (Event, error) {
	switch name {
	case "text_delta":
		var p struct{ Text string }
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("text_delta: %w", err)
		}
		return TextDelta{Text: p.Text}, nil
	case "thinking_delta":
		var p struct{ Text string }
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("thinking_delta: %w", err)
		}
		return ThinkingDelta{Text: p.Text}, nil
	case "tool_use":
		var p struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input string `json:"input"`
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("tool_use: %w", err)
		}
		return ToolUse{ID: p.ID, Name: p.Name, Input: p.Input}, nil
	case "tool_result":
		var p struct {
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
			IsError   bool   `json:"is_error"`
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("tool_result: %w", err)
		}
		return ToolResult{ToolUseID: p.ToolUseID, Content: p.Content, IsError: p.IsError}, nil
	case "done":
		var p struct {
			StopReason    string  `json:"stop_reason"`
			ProviderState string  `json:"provider_state"`
			CostUSD       float64 `json:"cost_usd"`
			Usage         struct {
				InputTokens         int64 `json:"input_tokens"`
				OutputTokens        int64 `json:"output_tokens"`
				CacheCreationTokens int64 `json:"cache_creation_tokens"`
				CacheReadTokens     int64 `json:"cache_read_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("done: %w", err)
		}
		return Done{
			StopReason:          p.StopReason,
			ProviderState:       p.ProviderState,
			CostUSD:             p.CostUSD,
			InputTokens:         p.Usage.InputTokens,
			OutputTokens:        p.Usage.OutputTokens,
			CacheCreationTokens: p.Usage.CacheCreationTokens,
			CacheReadTokens:     p.Usage.CacheReadTokens,
		}, nil
	case "error":
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("error: %w", err)
		}
		return Error{Message: p.Message}, nil
	}
	return nil, fmt.Errorf("unknown SSE event %q", name)
}

