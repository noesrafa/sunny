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
		scopes = []string{ScopeSpacesReadonly}
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
