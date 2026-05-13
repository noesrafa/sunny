package gchat

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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

// React adds an emoji reaction to a message and returns the created
// reaction's resource name (shape "spaces/X/messages/Y/reactions/Z").
// Callers can pass that name back to Unreact when they need to remove
// the same reaction later (e.g. swapping 👀 for ✅ at the end of a
// monitor's chain).
//
// 409 / ALREADY_EXISTS is swallowed — the desired state is "this
// emoji exists on this message", and it does. The returned name is
// empty in that case because Google doesn't return the existing
// reaction's body on the conflict.
func (c *Client) React(ctx context.Context, messageName, emoji string) (string, error) {
	if messageName == "" {
		return "", fmt.Errorf("react: message name required")
	}
	if emoji == "" {
		return "", fmt.Errorf("react: emoji required")
	}
	r, err := c.svc.Spaces.Messages.Reactions.Create(messageName, &chat.Reaction{
		Emoji: &chat.Emoji{Unicode: emoji},
	}).Context(ctx).Do()
	if err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "ALREADY_EXISTS") {
			return "", nil
		}
		return "", fmt.Errorf("react %s: %w", messageName, err)
	}
	if r == nil {
		return "", nil
	}
	return r.Name, nil
}

// Unreact removes the authenticated user's reaction matching `emoji`
// from a message. Idempotent — if no matching reaction exists (e.g.
// already removed, or we never added one), returns nil.
//
// Implementation: list reactions on the message with a server-side
// filter (`emoji.unicode = X AND user.name = users/me`), delete the
// first match. The filter syntax is per Google's Chat API quirks —
// `users/me` is a magic alias for the authenticated user.
func (c *Client) Unreact(ctx context.Context, messageName, emoji string) error {
	if messageName == "" {
		return fmt.Errorf("unreact: message name required")
	}
	if emoji == "" {
		return fmt.Errorf("unreact: emoji required")
	}
	filter := fmt.Sprintf("emoji.unicode = %q AND user.name = \"users/me\"", emoji)
	resp, err := c.svc.Spaces.Messages.Reactions.List(messageName).
		Filter(filter).
		PageSize(1).
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("unreact list %s: %w", messageName, err)
	}
	if resp == nil || len(resp.Reactions) == 0 {
		return nil
	}
	_, err = c.svc.Spaces.Messages.Reactions.Delete(resp.Reactions[0].Name).Context(ctx).Do()
	if err != nil {
		// 404 = already gone; treat as success.
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NOT_FOUND") {
			return nil
		}
		return fmt.Errorf("unreact delete %s: %w", resp.Reactions[0].Name, err)
	}
	return nil
}

// Reply posts a message in the same thread as a previous message.
// spaceName is "spaces/X". threadName is "spaces/X/threads/Y" (empty
// = create a new thread). text is the body — pre-format with
// FormatForChat() if it came from a Markdown-aware source like a
// model response.
//
// messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD tells
// Google: if the thread name we passed is bogus or already closed,
// create a new thread instead of erroring. Safer than the strict
// reply mode for an autonomous monitor.
func (c *Client) Reply(ctx context.Context, spaceName, threadName, text string) (string, error) {
	if spaceName == "" {
		return "", fmt.Errorf("reply: space name required")
	}
	if text == "" {
		return "", fmt.Errorf("reply: text required")
	}
	msg := &chat.Message{Text: text}
	if threadName != "" {
		msg.Thread = &chat.Thread{Name: threadName}
	}
	created, err := c.svc.Spaces.Messages.Create(spaceName, msg).
		MessageReplyOption("REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD").
		Context(ctx).
		Do()
	if err != nil {
		return "", fmt.Errorf("reply in %s: %w", spaceName, err)
	}
	if created == nil {
		return "", nil
	}
	return created.Name, nil
}

// Edit replaces the `text` field on a previously-posted message.
// `messageName` is the full resource name returned by Reply.
// Other fields (thread membership, sender, createTime) are
// untouched. The "edited" badge that Google Chat shows in the UI
// is added automatically by the API.
func (c *Client) Edit(ctx context.Context, messageName, text string) error {
	if messageName == "" {
		return fmt.Errorf("edit: message name required")
	}
	_, err := c.svc.Spaces.Messages.Patch(messageName, &chat.Message{Text: text}).
		UpdateMask("text").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("edit %s: %w", messageName, err)
	}
	return nil
}

// FormatForChat strips Markdown that Google Chat doesn't render and
// keeps the few formatting glyphs that DO render (single-asterisk
// bold). The goal is "agent's prose lands readable", not "perfect
// fidelity" — a heading like "## Bien" turns into bare "Bien".
//
// Mappings:
//
//	# / ## / ### …    → bare line (Chat has no headings)
//	**bold**          → *bold*    (Chat bold uses single asterisk)
//	*italic*          → *italic*  (left as-is; Chat italic uses _italic_, see note)
//	`inline`          → bare      (Chat has no inline code)
//	```block```       → indented  (Chat has no code blocks)
//	[txt](url)        → "txt (url)"
//	---               → ""        (no horizontal rule)
//	\n\n\n+           → "\n\n"    (collapse paragraph runs)
//
// Note on italic: Google Chat uses `_italic_`, but we leave `*italic*`
// as-is — converting blindly would also clobber bold. The agent's
// reviews use bold far more than italic anyway, and a stray asterisk
// is more readable than a misrendered emphasis.
func FormatForChat(s string) string {
	s = reHeading.ReplaceAllString(s, "$1")
	s = reBoldItalic.ReplaceAllString(s, "*$1*")
	s = reBold.ReplaceAllString(s, "*$1*")
	s = reHr.ReplaceAllString(s, "")
	s = reCodeBlock.ReplaceAllStringFunc(s, func(m string) string {
		// Strip the fence and indent the body 2 spaces.
		inner := reCodeFence.ReplaceAllString(m, "")
		lines := strings.Split(inner, "\n")
		for i := range lines {
			lines[i] = "  " + lines[i]
		}
		return strings.Join(lines, "\n")
	})
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1 ($2)")
	s = reBlankRun.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

var (
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reBoldItalic = regexp.MustCompile(`\*\*\*(.+?)\*\*\*`)
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reHr         = regexp.MustCompile(`(?m)^-{3,}$`)
	reCodeBlock  = regexp.MustCompile("(?s)```[a-zA-Z]*\\n?(.+?)```")
	reCodeFence  = regexp.MustCompile("```[a-zA-Z]*\\n?|```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBlankRun   = regexp.MustCompile(`\n{3,}`)
)
