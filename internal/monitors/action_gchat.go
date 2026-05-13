package monitors

import (
	"context"
	"fmt"

	"github.com/noesrafa/sunny/internal/gchat"
)

// GChatReactAction adds an emoji reaction to the Item's source message
// in Google Chat. YAML config:
//
//	- gchat_react:
//	    emoji:   "👀"
//	    message: "${item.message}"   # optional — defaults to item.message
//
// Returns a map {name, emoji, message} so later actions can chain
// off the created reaction (e.g. ${gchat_react.name} to unreact the
// same one specifically).
type GChatReactAction struct {
	root string
}

func NewGChatReactAction(root string) *GChatReactAction { return &GChatReactAction{root: root} }

func (a *GChatReactAction) Type() string { return "gchat_react" }

func (a *GChatReactAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	emoji, _ := cfg["emoji"].(string)
	if emoji == "" {
		return nil, fmt.Errorf("gchat_react: `emoji` required")
	}
	msg, _ := cfg["message"].(string)
	if msg == "" {
		msg, _ = item.Fields["message"].(string)
	}
	if msg == "" {
		return nil, fmt.Errorf("gchat_react: no message resource name (set `message` or use a source that populates item.message)")
	}
	client, err := gchat.New(ctx, a.root, gchat.DefaultScopes...)
	if err != nil {
		return nil, fmt.Errorf("gchat_react: %w", err)
	}
	name, err := client.React(ctx, msg, emoji)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "emoji": emoji, "message": msg}, nil
}

// GChatUnreactAction removes one of the authenticated user's emoji
// reactions from a message. YAML config:
//
//	- gchat_unreact:
//	    emoji:   "👀"
//	    message: "${item.message}"   # optional — defaults to item.message
//
// Idempotent: removing a reaction that isn't there returns nil.
// Pairs with gchat_react at the end of a chain — react 👀 while
// processing, unreact 👀 once done.
type GChatUnreactAction struct {
	root string
}

func NewGChatUnreactAction(root string) *GChatUnreactAction { return &GChatUnreactAction{root: root} }

func (a *GChatUnreactAction) Type() string { return "gchat_unreact" }

func (a *GChatUnreactAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	emoji, _ := cfg["emoji"].(string)
	if emoji == "" {
		return nil, fmt.Errorf("gchat_unreact: `emoji` required")
	}
	msg, _ := cfg["message"].(string)
	if msg == "" {
		msg, _ = item.Fields["message"].(string)
	}
	if msg == "" {
		return nil, fmt.Errorf("gchat_unreact: no message resource name")
	}
	client, err := gchat.New(ctx, a.root, gchat.DefaultScopes...)
	if err != nil {
		return nil, fmt.Errorf("gchat_unreact: %w", err)
	}
	if err := client.Unreact(ctx, msg, emoji); err != nil {
		return nil, err
	}
	return map[string]any{"emoji": emoji, "message": msg}, nil
}

// GChatReplyAction posts a reply in the same thread as the Item's
// source message. Returns {name, space, thread, len} so later actions
// (gchat_edit) can target the created message via ${gchat_reply.name}.
//
// YAML config:
//
//	- gchat_reply:
//	    text:   "Revisando..."           # required (substituted)
//	    space:  "${item.space}"          # optional — defaults to item.space
//	    thread: "${item.thread}"         # optional — defaults to item.thread
//	    format: true                     # optional — strip Markdown before send (default true)
type GChatReplyAction struct {
	root string
}

func NewGChatReplyAction(root string) *GChatReplyAction { return &GChatReplyAction{root: root} }

func (a *GChatReplyAction) Type() string { return "gchat_reply" }

func (a *GChatReplyAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	text, _ := cfg["text"].(string)
	if text == "" {
		return nil, fmt.Errorf("gchat_reply: `text` required")
	}
	space, _ := cfg["space"].(string)
	if space == "" {
		space, _ = item.Fields["space"].(string)
	}
	if space == "" {
		return nil, fmt.Errorf("gchat_reply: no space resource name (set `space` or use a source that populates item.space)")
	}
	thread, _ := cfg["thread"].(string)
	if thread == "" {
		thread, _ = item.Fields["thread"].(string)
	}
	doFormat := true
	if v, ok := cfg["format"].(bool); ok {
		doFormat = v
	}
	if doFormat {
		text = gchat.FormatForChat(text)
	}
	client, err := gchat.New(ctx, a.root, gchat.DefaultScopes...)
	if err != nil {
		return nil, fmt.Errorf("gchat_reply: %w", err)
	}
	name, err := client.Reply(ctx, space, thread, text)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "space": space, "thread": thread, "len": len(text)}, nil
}

// GChatEditAction replaces the text of a previously-posted message.
// Used to "promote" a placeholder reply (e.g. "Revisando…") into its
// final content (e.g. the agent's review) without spawning a second
// message in the thread.
//
// YAML config:
//
//	- gchat_edit:
//	    message: "${gchat_reply.name}"   # required: resource name of the reply to edit
//	    text:    "${dispatch.result}"    # required: new content
//	    format:  true                    # optional — strip Markdown before send (default true)
type GChatEditAction struct {
	root string
}

func NewGChatEditAction(root string) *GChatEditAction { return &GChatEditAction{root: root} }

func (a *GChatEditAction) Type() string { return "gchat_edit" }

func (a *GChatEditAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	msg, _ := cfg["message"].(string)
	if msg == "" {
		return nil, fmt.Errorf("gchat_edit: `message` required (resource name of the message to edit)")
	}
	text, _ := cfg["text"].(string)
	if text == "" {
		return nil, fmt.Errorf("gchat_edit: `text` required")
	}
	doFormat := true
	if v, ok := cfg["format"].(bool); ok {
		doFormat = v
	}
	if doFormat {
		text = gchat.FormatForChat(text)
	}
	client, err := gchat.New(ctx, a.root, gchat.DefaultScopes...)
	if err != nil {
		return nil, fmt.Errorf("gchat_edit: %w", err)
	}
	if err := client.Edit(ctx, msg, text); err != nil {
		return nil, err
	}
	return map[string]any{"message": msg, "len": len(text)}, nil
}
