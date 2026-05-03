// Package anthropic implements provider.Provider on top of
// github.com/anthropics/anthropic-sdk-go.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/noesrafa/sunny/internal/provider"
)

// New returns a driver. The API key is taken from the ANTHROPIC_API_KEY
// env var if apiKey is empty.
func New(apiKey string) (*Driver, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("anthropic: ANTHROPIC_API_KEY not set")
	}
	c := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Driver{client: &c}, nil
}

type Driver struct {
	client *anthropic.Client
}

func (d *Driver) Name() string { return "anthropic" }

func (d *Driver) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	model := req.Model
	if model == "" {
		model = "claude-opus-4-7"
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 16000
	}

	// System prompt: one TextBlockParam per chunk; the last block flagged
	// with CacheControl drops the cache breakpoint covering everything
	// before (tools render at position 0; system right after).
	system := make([]anthropic.TextBlockParam, 0, len(req.System))
	for _, b := range req.System {
		blk := anthropic.TextBlockParam{Text: b.Text}
		if b.CacheControl {
			blk.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		system = append(system, blk)
	}

	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			return nil, fmt.Errorf("anthropic: unknown role %q", m.Role)
		}
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTok),
		System:    system,
		Messages:  msgs,
	}

	if req.AdaptiveThinking {
		ad := anthropic.ThinkingConfigAdaptiveParam{
			// Show the thinking summary so the TUI can render it. Default
			// on Opus 4.7 is "omitted", which would surface as a long
			// pause before output.
			Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &ad}
	}

	// Effort lives on output_config (not top-level). Default high — the
	// minimum for intelligence-sensitive work on Opus 4.7.
	effort := req.Effort
	if effort == "" {
		effort = "high"
	}
	params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(effort)}

	stream := d.client.Messages.NewStreaming(ctx, params)

	out := make(chan provider.Event, 32)
	go func() {
		defer close(out)
		acc := anthropic.Message{}
		for stream.Next() {
			ev := stream.Current()
			_ = acc.Accumulate(ev)

			switch v := ev.AsAny().(type) {
			case anthropic.ContentBlockDeltaEvent:
				switch dv := v.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					if dv.Text != "" {
						out <- provider.TextDelta{Text: dv.Text}
					}
				case anthropic.ThinkingDelta:
					if dv.Thinking != "" {
						out <- provider.ThinkingDelta{Text: dv.Thinking}
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			out <- provider.Error{Err: err}
			return
		}
		out <- provider.Done{
			StopReason:          string(acc.StopReason),
			InputTokens:         acc.Usage.InputTokens,
			OutputTokens:        acc.Usage.OutputTokens,
			CacheCreationTokens: acc.Usage.CacheCreationInputTokens,
			CacheReadTokens:     acc.Usage.CacheReadInputTokens,
		}
	}()
	return out, nil
}
