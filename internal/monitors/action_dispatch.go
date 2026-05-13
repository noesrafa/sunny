package monitors

import (
	"context"
	"fmt"
	"strings"
)

// DispatchFunc is the seam between the monitors package and the
// daemon's engine: given an agent id + a final prompt, run a
// one-shot turn and return the model's text. Implementation lives
// in cmd/sunny/serve.go (closes over engine + store) so this
// package stays free of engine/store imports.
type DispatchFunc func(ctx context.Context, agentID, prompt string) (string, error)

// DispatchAction sends a templated prompt to a named agent and
// returns the model's response. Synchronous on purpose — chained
// rules need `${dispatch.result}` to be present in subsequent
// actions of the same rule.
type DispatchAction struct {
	Fn DispatchFunc
}

func NewDispatchAction(fn DispatchFunc) *DispatchAction {
	return &DispatchAction{Fn: fn}
}

func (a *DispatchAction) Type() string { return "dispatch" }

// Run reads `agent` and `prompt` from cfg, expands any ${item.X}
// or ${ns.field} placeholders against the current item + accumulated
// vars, and invokes the configured DispatchFunc.
//
// Returns a map with three keys to enable conditional chaining:
//
//	result — the agent's full response text (string).
//	emoji  — the first verdict glyph (✅ / ⚠️  / ❌) found in the
//	         first ~200 bytes of the response. Empty when the agent
//	         didn't prefix with one. Used by downstream gchat_react
//	         to pick "approve" vs "request changes" reactions.
//	head   — the first non-empty line of the response, useful as a
//	         summary chip somewhere visible.
//
// Backwards compatible with the previous string-only return: rules.go's
// Substitute special-cases `${dispatch.result}` against a map, so
// existing YAMLs that reference ${dispatch.result} keep working.
func (a *DispatchAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	if a.Fn == nil {
		return nil, fmt.Errorf("dispatch: no engine wired")
	}
	agentID, _ := cfg["agent"].(string)
	promptTmpl, _ := cfg["prompt"].(string)
	if agentID == "" || promptTmpl == "" {
		return nil, fmt.Errorf("dispatch: `agent` and `prompt` required")
	}
	prompt := Substitute(promptTmpl, item, vars)
	out, err := a.Fn(ctx, agentID, prompt)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"result": out,
		"emoji":  extractVerdictEmoji(out),
		"head":   firstLine(out),
	}, nil
}

// extractVerdictEmoji finds the earliest of ✅ / ⚠️ / ❌ in the first
// ~200 bytes of s. Returns "" when no verdict glyph is present in
// that window — callers should fall back to a neutral default (or
// skip the conditional react).
//
// We scan only the head because the agent is instructed to put the
// verdict at the top; a later mention of the same glyph inside the
// review body shouldn't override the agent's stated verdict.
func extractVerdictEmoji(s string) string {
	const headLimit = 200
	head := s
	if len(head) > headLimit {
		head = head[:headLimit]
	}
	bestIdx := -1
	best := ""
	for _, want := range []string{"✅", "⚠️", "❌"} {
		i := strings.Index(head, want)
		if i < 0 {
			continue
		}
		if bestIdx < 0 || i < bestIdx {
			bestIdx = i
			best = want
		}
	}
	return best
}

// firstLine returns the first non-empty trimmed line of s, capped at
// 160 chars so it's safe to surface in a chip / notification.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if len(t) > 160 {
			t = t[:159] + "…"
		}
		return t
	}
	return ""
}
