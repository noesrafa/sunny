// Package anthropic implements provider.Provider on top of
// github.com/anthropics/anthropic-sdk-go.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/secrets"
)

// New returns a driver bound to the given secrets store. The API key
// is read on every Stream() call so secret rotations and `sunny
// secrets anthropic set` take effect immediately, no daemon restart.
//
// Resolution order (per call):
//  1. ANTHROPIC_API_KEY env var (override for headless / CI)
//  2. secrets.yaml → anthropic.api_key
//
// Empty in both → Stream returns a clear error pointing at the fix.
func New(s *secrets.Store) (*Driver, error) {
	if s == nil {
		return nil, errors.New("anthropic: secrets store required")
	}
	// Probe at construction so callers can fall back to another
	// provider when no key is configured.
	if probe := s.GetOrEnv("anthropic", "api_key", "ANTHROPIC_API_KEY"); probe == "" {
		return nil, errors.New("anthropic: api_key not configured (set via `sunny secrets anthropic set api_key` or ANTHROPIC_API_KEY env var)")
	}
	return &Driver{secrets: s}, nil
}

type Driver struct {
	secrets *secrets.Store
}

func (d *Driver) Name() string { return "anthropic" }

func (d *Driver) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	apiKey := d.secrets.GetOrEnv("anthropic", "api_key", "ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, errors.New("anthropic: api_key missing — configure it before sending a turn")
	}
	c := anthropic.NewClient(option.WithAPIKey(apiKey))

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

	msgs, err := buildMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTok),
		System:    system,
		Messages:  msgs,
	}

	// Tools: translate sunny's neutral schema into the SDK's
	// ToolUnionParam shape. JSON schema goes through verbatim — the
	// SDK marshals it back out to wire format.
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			schemaParam, err := unmarshalSchema(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("anthropic: tool %q schema: %w", t.Name, err)
			}
			tools = append(tools, anthropic.ToolUnionParam{
				OfTool: &anthropic.ToolParam{
					Name:        t.Name,
					Description: anthropic.String(t.Description),
					InputSchema: schemaParam,
				},
			})
		}
		params.Tools = tools
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

	stream := c.Messages.NewStreaming(ctx, params)

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
		// Tool use surfaces only after the full block is accumulated
		// (Anthropic streams Input as JSON deltas; the SDK rebuilds
		// the complete object). Emit one ToolUse per block, in
		// declaration order, just before Done so the engine can run
		// them and re-stream.
		for _, block := range acc.Content {
			if t, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
				out <- provider.ToolUse{
					ID:    t.ID,
					Name:  t.Name,
					Input: string(t.Input),
				}
			}
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

// buildMessages translates sunny's neutral Message slice into the
// SDK's MessageParam shape. The interesting cases are role=assistant
// with ToolCalls (becomes content blocks: text + tool_use blocks)
// and role=tool (becomes a user message with a single tool_result
// block, since Anthropic doesn't have a "tool" role on the wire).
func buildMessages(msgs []provider.Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			blocks := make([]anthropic.ContentBlockParamUnion, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input any
				if len(tc.Input) > 0 {
					if err := json.Unmarshal(tc.Input, &input); err != nil {
						// Fall back to passing the raw string — better
						// than dropping the call entirely.
						input = string(tc.Input)
					}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case "tool":
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(m.ToolUseID, m.Content, m.IsError),
			))
		default:
			return nil, fmt.Errorf("anthropic: unknown role %q", m.Role)
		}
	}
	return out, nil
}

// unmarshalSchema reads the JSON Schema we hand drivers and reshapes
// it into the SDK's ToolInputSchemaParam (which expects properties +
// required as separate fields). Most schemas we generate are
// `{"type":"object","properties":{...},"required":[...]}` so the
// translation is mechanical.
func unmarshalSchema(raw json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	var blob struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return anthropic.ToolInputSchemaParam{}, err
	}
	props := make(map[string]any, len(blob.Properties))
	for k, v := range blob.Properties {
		var any any
		if err := json.Unmarshal(v, &any); err != nil {
			return anthropic.ToolInputSchemaParam{}, fmt.Errorf("property %q: %w", k, err)
		}
		props[k] = any
	}
	return anthropic.ToolInputSchemaParam{
		Properties: props,
		Required:   blob.Required,
	}, nil
}
