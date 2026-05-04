package client

import (
	"context"
	"encoding/json"
	"net/http"
)

// ConversationMeta mirrors internal/conversation.Meta over the wire.
type ConversationMeta struct {
	ID            string  `json:"id"`
	AgentSlug     string  `json:"agent_slug"`
	Title         string  `json:"title"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	MsgCount      int     `json:"msg_count"`
	Model         string  `json:"model,omitempty"`
	TotalCost     float64 `json:"total_cost_usd,omitempty"`
	ProviderState string  `json:"provider_state,omitempty"`
}

// JournalEvent is one persisted entry from events.jsonl. Kind matches
// the SSE event name; Payload is the same JSON object that streamed.
type JournalEvent struct {
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	At      string          `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// CreateConversation allocates a new conversation under an agent.
// Title and model are optional — empty falls back to "untitled" /
// the agent's default model.
func (c *Client) CreateConversation(ctx context.Context, slug, title, model string) (*ConversationMeta, error) {
	body := map[string]string{"title": title, "model": model}
	resp, err := c.doJSON(ctx, http.MethodPost, "/agents/"+slug+"/conversations", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, errorFromBody("POST /conversations", resp)
	}
	var out ConversationMeta
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListConversations returns metas (newest first) for an agent.
func (c *Client) ListConversations(ctx context.Context, slug string) ([]ConversationMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agents/"+slug+"/conversations", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /conversations", resp)
	}
	var out []ConversationMeta
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetConversation returns the conversation meta + the full event journal.
func (c *Client) GetConversation(ctx context.Context, slug, convID string) (*ConversationMeta, []JournalEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agents/"+slug+"/conversations/"+convID, nil)
	if err != nil {
		return nil, nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, errorFromBody("GET /conversations/"+convID, resp)
	}
	var out struct {
		Meta   ConversationMeta `json:"meta"`
		Events []JournalEvent   `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, err
	}
	return &out.Meta, out.Events, nil
}

// DeleteConversation moves a conversation to ~/.sunny/.archive/. Idempotent.
func (c *Client) DeleteConversation(ctx context.Context, slug, convID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/agents/"+slug+"/conversations/"+convID, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errorFromBody("DELETE /conversations/"+convID, resp)
	}
	return nil
}
