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
	// The agent emits a single structured JSON block. We compose the
	// chat reply from that same data so the per-PR review body shows
	// up identically in both places (Chat thread + GitHub PR review).
	// No re-prompting, no second pass.
	chatReply, emoji, reviews := parseAgentOutput(out)
	return map[string]any{
		"result":  chatReply,
		"emoji":   emoji,
		"head":    firstLine(chatReply),
		"reviews": reviews,
	}, nil
}

// parseAgentOutput pulls a single structured JSON block out of the
// agent's response and produces three things from it:
//
//   - chatReply: a Markdown-light rendering composed from the JSON's
//     summary + per-PR bodies. This is what gchat_edit posts in the
//     thread.
//   - emoji:     ✅ when verdict=APPROVE, ❌ otherwise. Drives the
//     final gchat_react in the chain.
//   - reviews:   the raw per-PR list, fed straight to github_review.
//
// Expected JSON shape (inside markers):
//
//	<!-- GH-REVIEWS-START -->
//	{
//	  "verdict": "APPROVE" | "REQUEST_CHANGES",
//	  "summary": "one-line executive summary",
//	  "reviews": [
//	    {"url":"...","title":"...","decision":"APPROVE","body":"..."}
//	  ]
//	}
//	<!-- GH-REVIEWS-END -->
//
// Fallback when no JSON block is present (or the JSON is malformed):
// the raw agent text becomes the chat reply, emoji is sniffed with
// the legacy extractor, and reviews is nil so github_review no-ops.
// This keeps the chain alive even when the agent forgets the
// structured format.
func parseAgentOutput(s string) (chatReply string, emoji string, reviews []map[string]any) {
	block := extractJSONBlock(s)
	if block == "" {
		return s, extractVerdictEmoji(s), nil
	}
	var out struct {
		Verdict string           `json:"verdict"`
		Summary string           `json:"summary"`
		Reviews []map[string]any `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(block), &out); err != nil {
		return s, extractVerdictEmoji(s), nil
	}
	emoji = verdictToEmoji(out.Verdict)
	chatReply = composeChatReply(emoji, out.Summary, out.Reviews)
	return chatReply, emoji, out.Reviews
}

// extractJSONBlock returns the text between the GH-REVIEWS markers,
// trimmed. Empty string when markers are missing or malformed.
func extractJSONBlock(s string) string {
	const startTag = "<!-- GH-REVIEWS-START -->"
	const endTag = "<!-- GH-REVIEWS-END -->"
	i := strings.Index(s, startTag)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], endTag)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i+len(startTag) : i+j])
}

// verdictToEmoji collapses an agent-emitted verdict into the binary
// emoji the user wants. Anything that smells like "request changes"
// (or contains a ❌) flips to ❌; everything else is ✅. There's no
// neutral middle ground by design.
func verdictToEmoji(v string) string {
	up := strings.ToUpper(strings.TrimSpace(v))
	if strings.Contains(up, "REQUEST") || strings.Contains(up, "CHANGES") || strings.Contains(v, "❌") {
		return "❌"
	}
	return "✅"
}

// composeChatReply renders the structured agent output as the text
// that lands in the Google Chat thread. Same per-PR bodies that go
// to GitHub get reused here verbatim — one source of truth.
//
// Layout:
//
//	<emoji> <summary>
//
//	*PR #<num>* <decision-emoji> — <title>
//	<body>
//
//	*PR #<num>* <decision-emoji> — <title>
//	<body>
func composeChatReply(emoji, summary string, reviews []map[string]any) string {
	var b strings.Builder
	b.WriteString(emoji)
	if summary != "" {
		b.WriteString(" ")
		b.WriteString(summary)
	}
	b.WriteString("\n\n")
	for i, r := range reviews {
		title, _ := r["title"].(string)
		url, _ := r["url"].(string)
		body, _ := r["body"].(string)
		decision, _ := r["decision"].(string)
		prNum := extractPRNumber(url)
		prEmoji := verdictToEmoji(decision)
		header := fmt.Sprintf("*PR #%s* %s", prNum, prEmoji)
		if title != "" {
			header += " — " + title
		}
		b.WriteString(header)
		b.WriteString("\n")
		b.WriteString(body)
		if i < len(reviews)-1 {
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// extractPRNumber returns the trailing path segment of a GitHub PR
// URL ("https://github.com/owner/repo/pull/346" → "346"). Empty when
// the URL doesn't look like a PR URL.
func extractPRNumber(url string) string {
	if i := strings.LastIndex(url, "/pull/"); i >= 0 {
		num := url[i+len("/pull/"):]
		// Trim anything past the number (query string, fragment, etc.)
		for j, r := range num {
			if r < '0' || r > '9' {
				return num[:j]
			}
		}
		return num
	}
	return ""
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
