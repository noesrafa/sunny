package monitors

import (
	"context"
	"encoding/json"
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
	// Strip the optional trailing GH-REVIEWS block before exposing
	// `result` to downstream actions — the block is a machine-readable
	// hint for github_review and would just clutter the chat reply.
	clean, reviews := parseGitHubReviews(out)
	return map[string]any{
		"result":  clean,
		"emoji":   extractVerdictEmoji(clean),
		"head":    firstLine(clean),
		"reviews": reviews,
	}, nil
}

// parseGitHubReviews extracts a trailing block of the form
//
//	<!-- GH-REVIEWS-START -->
//	{"reviews":[{"url":"...","decision":"APPROVE","body":"..."}, ...]}
//	<!-- GH-REVIEWS-END -->
//
// from the agent's response. Returns the response with the block
// stripped (clean) plus the parsed reviews list.
//
// Missing block → clean=s, reviews=nil. Malformed JSON → clean=s,
// reviews=nil (we don't fail the whole turn over a bad emit; the
// chat reply still goes through, the GitHub step just no-ops).
func parseGitHubReviews(s string) (clean string, reviews []map[string]any) {
	const startTag = "<!-- GH-REVIEWS-START -->"
	const endTag = "<!-- GH-REVIEWS-END -->"
	start := strings.Index(s, startTag)
	if start < 0 {
		return s, nil
	}
	tail := s[start+len(startTag):]
	end := strings.Index(tail, endTag)
	if end < 0 {
		return s, nil
	}
	jsonRaw := strings.TrimSpace(tail[:end])
	var wire struct {
		Reviews []map[string]any `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(jsonRaw), &wire); err != nil {
		return s, nil
	}
	cleanBuf := strings.TrimSpace(s[:start] + tail[end+len(endTag):])
	return cleanBuf, wire.Reviews
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
