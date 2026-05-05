package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BusEvent mirrors internal/events.Event on the wire. Renamed
// from Event in this package to avoid colliding with the chat
// stream's Event interface.
type BusEvent struct {
	Type        string `json:"type"`
	Slug        string `json:"slug,omitempty"`
	ConvID      string `json:"conv_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	MonitorName string `json:"monitor_name,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

// SubscribeEvents opens a Server-Sent-Events connection to GET
// /events and returns a channel that drains each event as it
// arrives. The channel closes when ctx is cancelled, the
// connection drops, or the daemon shuts down — call sites should
// treat closure as "reconnect" intent.
//
// Heartbeat lines (`: …`) are silently skipped. Malformed payloads
// are dropped with no signal — sunny doesn't have a logging seam
// at this layer; future versions can add one if it becomes useful.
func (c *Client) SubscribeEvents(ctx context.Context) (<-chan BusEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/events", nil)
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
		return nil, fmt.Errorf("GET /events: %d", resp.StatusCode)
	}

	out := make(chan BusEvent, 32)
	go pumpBusEvents(resp.Body, out)
	return out, nil
}

// pumpBusEvents parses an SSE stream into BusEvent values. SSE frames are
// `event: <type>\ndata: <json>\n\n`. We only care about the data
// line — the type tag is duplicated inside the JSON payload.
func pumpBusEvents(body io.ReadCloser, out chan<- BusEvent) {
	defer close(out)
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<14), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // skip "event:", ":", "id:", and the blank separator
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var ev BusEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		out <- ev
	}
}

// reconnectDelay is the constant backoff between reconnect attempts
// in Federation.SubscribeAll. Kept short on purpose — sunny is a
// long-lived TUI and a flapping peer should reconnect aggressively
// rather than hide events for a minute.
const reconnectDelay = 2 * time.Second
