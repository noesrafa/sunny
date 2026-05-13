package monitors

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitHubReviewAction posts a review per PR to GitHub by shelling out
// to the `gh` CLI. Reads its work list from a previous dispatch
// action's parsed output:
//
//	vars["dispatch"]["reviews"] = [
//	  {"url": "...", "decision": "APPROVE"|"COMMENT"|"REQUEST_CHANGES", "body": "..."},
//	  ...
//	]
//
// Zero-config in YAML — the action knows where to look:
//
//	then:
//	  - dispatch: { agent: sunny, prompt: ... }   # produces reviews via JSON block
//	  - github_review: {}
//
// Failure modes (none of which fail the whole monitor):
//
//   - `gh` not on PATH → returns an error; chain stops here. The
//     previous chat reply / reactions already happened, so the user
//     still gets the review, just not on GitHub.
//   - `gh` auth missing → same surfacing as above.
//   - A single PR review fails (403, repo deleted, can't approve own
//     PR) → the action logs the failure inside the result map but
//     continues with the remaining reviews. Per-PR outcomes are
//     visible in the monitor's history.
//
// `gh pr review` requires a body for COMMENT and REQUEST_CHANGES;
// APPROVE accepts an empty body. We always pass --body to keep the
// CLI happy and the audit trail useful.
type GitHubReviewAction struct{}

func NewGitHubReviewAction() *GitHubReviewAction { return &GitHubReviewAction{} }

func (a *GitHubReviewAction) Type() string { return "github_review" }

func (a *GitHubReviewAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	reviews, err := collectReviews(cfg, vars)
	if err != nil {
		return nil, err
	}
	if len(reviews) == 0 {
		return map[string]any{"reviewed": 0, "skipped": "no reviews in dispatch output"}, nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("github_review: `gh` CLI not on PATH (install via `brew install gh` and run `gh auth login`)")
	}

	type outcome struct {
		URL      string `json:"url"`
		Decision string `json:"decision"`
		OK       bool   `json:"ok"`
		Err      string `json:"err,omitempty"`
	}
	results := make([]outcome, 0, len(reviews))
	successCount := 0

	for _, r := range reviews {
		url, _ := r["url"].(string)
		decisionRaw, _ := r["decision"].(string)
		body, _ := r["body"].(string)
		if url == "" {
			results = append(results, outcome{Err: "missing url"})
			continue
		}
		flag, err := decisionFlag(decisionRaw)
		if err != nil {
			results = append(results, outcome{URL: url, Decision: decisionRaw, Err: err.Error()})
			continue
		}
		if body == "" {
			body = "(no body)"
		}
		cmd := exec.CommandContext(ctx, "gh", "pr", "review", url, flag, "--body", body)
		out, err := cmd.CombinedOutput()
		if err != nil {
			results = append(results, outcome{
				URL:      url,
				Decision: decisionRaw,
				Err:      strings.TrimSpace(string(out)) + " (" + err.Error() + ")",
			})
			continue
		}
		results = append(results, outcome{URL: url, Decision: decisionRaw, OK: true})
		successCount++
	}

	return map[string]any{
		"reviewed": successCount,
		"total":    len(reviews),
		"outcomes": results,
	}, nil
}

// collectReviews pulls the structured review list out of vars. By
// default it reads vars["dispatch"]["reviews"] (the JSON block the
// dispatch action parsed off the agent's response). Callers can
// override the source action's name via cfg["from"] if they have a
// non-default chain.
func collectReviews(cfg map[string]any, vars map[string]any) ([]map[string]any, error) {
	source := "dispatch"
	if v, ok := cfg["from"].(string); ok && v != "" {
		source = v
	}
	upstream, ok := vars[source].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("github_review: vars[%q] is not a map — expected upstream dispatch result", source)
	}
	raw, ok := upstream["reviews"]
	if !ok {
		return nil, nil
	}
	// YAML unmarshalling can give us []any or []map[string]any
	// depending on path. Accept both.
	switch v := raw.(type) {
	case []map[string]any:
		return v, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, x := range v {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, nil
	}
	return nil, nil
}

// decisionFlag maps a textual decision the agent emitted into the
// gh-CLI flag. Case-insensitive + tolerant of common typos / synonyms
// so a missing dash or extra word doesn't kill the action.
func decisionFlag(decision string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "APPROVE", "APPROVED", "✅":
		return "--approve", nil
	case "REQUEST_CHANGES", "REQUEST-CHANGES", "REQUEST CHANGES", "CHANGES", "❌":
		return "--request-changes", nil
	case "COMMENT", "COMMENTS", "⚠️", "⚠":
		return "--comment", nil
	}
	return "", fmt.Errorf("unknown decision %q (expected APPROVE / COMMENT / REQUEST_CHANGES)", decision)
}
