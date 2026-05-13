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
// The action falls back to item.Fields["message"] when cfg.message is
// empty so the common case is `{emoji: "..."}` — handy when chaining.
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
	if err := client.React(ctx, msg, emoji); err != nil {
		return nil, err
	}
	return map[string]any{"emoji": emoji, "message": msg}, nil
}

// GChatReplyAction posts a reply in the same thread as the Item's
// source message. YAML config:
//
//	- gchat_reply:
//	    text:   "Revisando..."           # required (substituted)
//	    space:  "${item.space}"          # optional — defaults to item.space
//	    thread: "${item.thread}"         # optional — defaults to item.thread
//	    format: true                     # optional — strip Markdown before send (default true)
//
// Markdown coming out of a model dispatch (## headings, **bold**,
// ```code```) is normalized to Chat-friendly plain text by default
// via gchat.FormatForChat. Set `format: false` if your text is
// already raw Chat formatting.
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
	if err := client.Reply(ctx, space, thread, text); err != nil {
		return nil, err
	}
	return map[string]any{"space": space, "thread": thread, "len": len(text)}, nil
}
