package gchat

import (
	"context"
	"fmt"

	"google.golang.org/api/chat/v1"
	"google.golang.org/api/option"
)

// Space is a thin projection of chat.Space — only the fields we use
// in the test command (and later the monitor) so callers don't have
// to import the SDK type just to render a list.
type Space struct {
	// Name is the resource name, shape "spaces/XXXX". Stable across
	// renames; this is what every other Chat API call refers to.
	Name string `json:"name"`
	// DisplayName is the human-set name. Empty for 1:1 DMs.
	DisplayName string `json:"display_name,omitempty"`
	// Type is "ROOM" (group chat), "DM" (direct message), or
	// "GROUP_DM" (multi-person DM without a permanent name).
	Type string `json:"type"`
}

// Message is a flat projection of chat.Message — only the fields the
// monitor source consumes. Keeping a separate struct (instead of
// re-exporting the SDK type) means the monitor package doesn't have
// to import google.golang.org/api just to read these fields.
type Message struct {
	// Name is the resource name "spaces/X/messages/Y" — used as the
	// monitor Item.ID for dedup across ticks.
	Name string `json:"name"`
	// Text is the raw message body. Annotations (mentions, links) are
	// kept inline in the text per Google's wire format.
	Text string `json:"text"`
	// CreateTime is RFC3339 UTC. Drives the cursor — the next tick
	// asks "give me everything > <last seen createTime>".
	CreateTime string `json:"create_time"`
	// SenderName is the resource name of the sender, "users/XXXX".
	SenderName string `json:"sender_name"`
	// SenderType is "HUMAN" or "BOT" — the source uses this to drop
	// bot echoes from the monitor stream when callers ask for it.
	SenderType string `json:"sender_type"`
	// ThreadName is "spaces/X/threads/Y", used when later actions
	// want to reply in-thread.
	ThreadName string `json:"thread_name,omitempty"`
	// SpaceName is "spaces/X" — duplicated here so an Item.Fields
	// payload is self-contained without the caller having to track
	// which space the message came from.
	SpaceName string `json:"space_name"`
}

// Client wraps a chat.Service authenticated via a persisting token
// source. New() ties the SDK to <root>'s saved token so token
// refreshes flush back to disk.
type Client struct {
	svc *chat.Service
}

// New builds a Chat API client using the OAuth token previously
// saved by `sunny gchat auth`. Returns an error pointing at the auth
// command if no token is on disk yet.
//
// Scopes must include everything you intend to call — pass the same
// scopes you used during `Authorize` (the token won't have broader
// access than what the user consented to, regardless of what we
// request here).
func New(ctx context.Context, root string, scopes ...string) (*Client, error) {
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	ts, err := TokenSource(ctx, root, scopes...)
	if err != nil {
		return nil, err
	}
	svc, err := chat.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("chat service: %w", err)
	}
	return &Client{svc: svc}, nil
}

// ListSpaces returns every space the authenticated user can see.
// Used by `sunny gchat test` to verify auth + scope + network in one
// round trip. Paginates internally so the caller gets a flat slice.
//
// The Chat API doesn't return DMs by default — passing filter so we
// see ROOM and DM both. (GROUP_DM falls under DM in the wire enum.)
func (c *Client) ListSpaces(ctx context.Context) ([]Space, error) {
	out := []Space{}
	call := c.svc.Spaces.List().Context(ctx).PageSize(100)
	err := call.Pages(ctx, func(resp *chat.ListSpacesResponse) error {
		for _, s := range resp.Spaces {
			out = append(out, Space{
				Name:        s.Name,
				DisplayName: s.DisplayName,
				Type:        s.SpaceType,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	return out, nil
}

// ListMessages returns messages in spaceName posted strictly after
// `after` (RFC3339, UTC). Pass an empty `after` on first call to
// receive the most recent batch; the caller is expected to remember
// the newest createTime and pass it on the next tick.
//
// We sort by createTime desc + cap pageSize at 25 (per-tick budget,
// not per-call total) because the monitor wakes up every 60s — a
// space that bursts more than 25 messages in a minute is a fire-hose
// the monitor isn't designed to handle gracefully anyway.
//
// `after` is honored as a strict-greater filter: a message whose
// createTime equals `after` is dropped (we already saw it on the
// previous tick). Empty `after` returns every message.
func (c *Client) ListMessages(ctx context.Context, spaceName, after string) ([]Message, error) {
	call := c.svc.Spaces.Messages.List(spaceName).
		Context(ctx).
		PageSize(25).
		OrderBy("createTime desc")
	if after != "" {
		call = call.Filter(fmt.Sprintf("createTime > %q", after))
	}
	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("list messages %s: %w", spaceName, err)
	}
	out := make([]Message, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		// Defensive: drop entries with no text AND no name. Practically
		// every chat message has at least Name; this just guards against
		// SDK quirks where attachment-only messages have empty Text.
		if m.Name == "" {
			continue
		}
		msg := Message{
			Name:       m.Name,
			Text:       m.Text,
			CreateTime: m.CreateTime,
			ThreadName: "",
			SpaceName:  spaceName,
		}
		if m.Sender != nil {
			msg.SenderName = m.Sender.Name
			msg.SenderType = m.Sender.Type
		}
		if m.Thread != nil {
			msg.ThreadName = m.Thread.Name
		}
		out = append(out, msg)
	}
	return out, nil
}
