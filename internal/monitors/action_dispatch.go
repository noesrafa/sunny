package monitors

import (
	"context"
	"fmt"
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
// vars, and invokes the configured DispatchFunc. The string return
// value lands in the next action's vars under "dispatch", so
// `${dispatch.result}` is a valid placeholder downstream.
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
	return a.Fn(ctx, agentID, prompt)
}
