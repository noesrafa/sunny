// Package engine orchestrates one chat turn: build the system prompt
// from an agent's on-disk definition (prompt.md + skills + knowledge
// file list), dispatch to a provider, and stream events back to the
// caller.
//
// As of v0.10 the engine also runs the tool round-trip loop: when a
// provider emits ToolUse, the engine looks the tool up in its
// registry, executes it, appends the assistant + tool messages to the
// running conversation, and re-streams. The loop terminates on a
// Done event with no further tool_use blocks.
//
// claude-code and opencode are special: each has its own native
// toolset (Read, Glob, Grep, Bash, …) that it manages internally.
// Sunny's tools are NOT advertised to either one — that would create
// duplicate names and confuse the model. Agents on those providers
// keep using the provider's native tools.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/provider/opencode"
	"github.com/noesrafa/sunny/internal/store"
	"github.com/noesrafa/sunny/internal/tools"
)

// Engine routes turns to the right provider for each agent and runs
// the tool round-trip loop.
//
// Concurrency: the providers map and the tools registry are read-
// only after construction. Hot-swap of providers (after `sunny
// secrets <provider> set` via HTTP) re-instantiates the whole engine
// — see daemon.rebuildEngine.
type Engine struct {
	providers   map[string]provider.Provider
	defaultName string
	tools       *tools.Registry
}

// New constructs an engine bound to a registry of providers keyed by
// name (the same name agent.yaml's `provider:` field picks). The
// default name is used when the agent doesn't specify one. tools may
// be nil — agents on providers that ignore tool advertisements
// (claude-code) work either way.
func New(providers map[string]provider.Provider, defaultName string, toolReg *tools.Registry) *Engine {
	return &Engine{
		providers:   providers,
		defaultName: defaultName,
		tools:       toolReg,
	}
}

// TurnOptions are per-call knobs that travel with the turn but
// aren't on the agent definition.
type TurnOptions struct {
	// ProviderState is opaque token from the previous turn's Done
	// event. Empty for first turn.
	ProviderState string
	// Cwd that file/bash tools should resolve against. Empty falls
	// back to the user's home dir.
	Cwd string
}

// Turn runs one user→assistant exchange against the given agent. The
// returned channel emits provider events as they happen plus the
// engine-synthesized ToolResult events that show what tools
// actually returned. Closes when the turn finishes (Done) or fails
// (Error).
func (e *Engine) Turn(ctx context.Context, agent *store.Agent, messages []provider.Message, opts TurnOptions) (<-chan provider.Event, error) {
	if agent == nil {
		return nil, fmt.Errorf("engine: agent required")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("engine: messages required")
	}
	p, err := e.pick(agent)
	if err != nil {
		return nil, err
	}

	system, err := BuildSystemPrompt(agent)
	if err != nil {
		return nil, fmt.Errorf("build system prompt: %w", err)
	}

	// opencode driver writes ~/.config/opencode/agent/sunny-<slug>.md
	// before its first spawn — needs the slug from context.
	if p.Name() == "opencode" {
		ctx = opencode.WithAgentSlug(ctx, agent.Slug)
	}

	out := make(chan provider.Event, 32)
	go e.runTurnLoop(ctx, p, agent, system, messages, opts, out)
	return out, nil
}

// runTurnLoop is the round-trip driver. It owns the channel and
// closes it when the turn truly ends. Pseudocode:
//
//	loop:
//	  events <- provider.Stream(req)
//	  for ev := range events:
//	    if ToolUse: collect, forward
//	    if Done: if pending tools, run them, append messages, continue loop
//	    if Error: forward, end
//	    else: forward
func (e *Engine) runTurnLoop(
	ctx context.Context,
	p provider.Provider,
	agent *store.Agent,
	system []provider.SystemBlock,
	messages []provider.Message,
	opts TurnOptions,
	out chan<- provider.Event,
) {
	defer close(out)

	effort := agent.Config.Effort
	if effort == "" {
		effort = "max"
	}
	advertised := e.advertisedTools(agent)

	// Hard cap on iterations: prevents runaway tool loops if a model
	// gets confused. Crush uses 25; we mirror.
	const maxIterations = 25
	for i := 0; i < maxIterations; i++ {
		req := provider.Request{
			Model:         agent.Config.Model,
			MaxTokens:     16000,
			System:        system,
			Messages:      messages,
			Tools:         advertised,
			Effort:        effort,
			Cwd:           opts.Cwd,
			ProviderState: opts.ProviderState,
		}
		events, err := p.Stream(ctx, req)
		if err != nil {
			out <- provider.Error{Err: err}
			return
		}

		var pendingCalls []provider.ToolCall
		var assistantText strings.Builder
		var done provider.Done
		var sawDone bool

		for ev := range events {
			switch v := ev.(type) {
			case provider.TextDelta:
				assistantText.WriteString(v.Text)
				out <- ev
			case provider.ThinkingDelta:
				out <- ev
			case provider.ToolUse:
				// Only feed the round-trip loop when sunny is the one
				// running tools. When `advertised` is empty the provider
				// is running its own toolset (claude-code, opencode);
				// these events are informational — the provider already
				// executed the tool and emitted its result inside the
				// same stream. Re-running them here would (a) try to
				// dispatch tool names we don't have ("Read", "Bash") and
				// (b) feed back a role=tool message the provider would
				// reject.
				if len(advertised) > 0 {
					pendingCalls = append(pendingCalls, provider.ToolCall{
						ID:    v.ID,
						Name:  v.Name,
						Input: json.RawMessage(v.Input),
					})
				}
				out <- ev
			case provider.ToolResult:
				out <- ev
			case provider.Done:
				// Hold on to Done until we know whether this is the
				// final iteration. If tool calls are pending, the
				// engine swallows this Done and re-streams; emitting
				// it now would tell the SSE client "turn over" while
				// the engine keeps going, and the client would stop
				// reading mid-stream.
				done = v
				sawDone = true
			case provider.Error:
				out <- ev
				return
			}
		}

		if !sawDone {
			// Stream ended without Done — context cancel, EOF, etc.
			// Surfacing this as Error keeps the journal honest;
			// chat.go re-classifies into "cancelled" if it's a
			// context cancellation.
			out <- provider.Error{Err: fmt.Errorf("engine: stream closed without done event")}
			return
		}

		// No tool calls → final iteration. Forward the Done now and
		// terminate. Anthropic sets stop_reason="tool_use" reliably,
		// but Ollama Cloud sometimes returns done_reason="stop"
		// alongside tool_calls — treat the presence of pending calls
		// as the source of truth and ignore stop_reason for the loop
		// decision.
		if len(pendingCalls) == 0 {
			out <- done
			return
		}

		// Build the next round: append the assistant turn + one
		// tool message per call.
		messages = append(messages, provider.Message{
			Role:      "assistant",
			Content:   assistantText.String(),
			ToolCalls: pendingCalls,
		})
		for _, call := range pendingCalls {
			result, content, isErr := e.runTool(ctx, call, opts.Cwd)
			out <- result
			messages = append(messages, provider.Message{
				Role:      "tool",
				ToolUseID: call.ID,
				Content:   content,
				IsError:   isErr,
			})
		}
		// ProviderState only matters on the very first round — once
		// we're feeding tool results, the conversation surface is
		// the messages slice. claude-code is the exception, but it
		// doesn't take this code path (we don't advertise tools).
		opts.ProviderState = done.ProviderState
	}

	out <- provider.Error{Err: fmt.Errorf("engine: tool loop exceeded %d iterations", maxIterations)}
}

// advertisedTools returns the tools we send to the provider for this
// agent. claude-code gets none — it has its own toolset that would
// collide. Other providers get the full registry.
func (e *Engine) advertisedTools(agent *store.Agent) []provider.ToolDef {
	if e.tools == nil {
		return nil
	}
	if agent.Config.Provider == "claude-code" || agent.Config.Provider == "opencode" {
		return nil
	}
	all := e.tools.All()
	out := make([]provider.ToolDef, 0, len(all))
	for _, t := range all {
		out = append(out, provider.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}

// runTool dispatches one tool invocation. Returns the synthesized
// ToolResult event (so the caller can forward it to the client) plus
// the (content, isError) pair that goes into the message we feed
// back to the model. Failures become is_error tool_results so the
// model can see what went wrong and try again.
func (e *Engine) runTool(ctx context.Context, call provider.ToolCall, cwd string) (provider.ToolResult, string, bool) {
	if e.tools == nil {
		msg := fmt.Sprintf("no tool registry configured")
		return provider.ToolResult{ToolUseID: call.ID, Content: msg, IsError: true}, msg, true
	}
	out, err := e.tools.Run(ctx, call.Name, call.Input, cwd)
	if err != nil {
		msg := err.Error()
		return provider.ToolResult{ToolUseID: call.ID, Content: msg, IsError: true}, msg, true
	}
	return provider.ToolResult{ToolUseID: call.ID, Content: out}, out, false
}

// pick selects the provider for an agent.
func (e *Engine) pick(agent *store.Agent) (provider.Provider, error) {
	want := agent.Config.Provider
	if want != "" {
		p, ok := e.providers[want]
		if !ok {
			return nil, fmt.Errorf("engine: agent %q wants provider %q which isn't configured", agent.Slug, want)
		}
		return p, nil
	}
	if e.defaultName == "" {
		return nil, fmt.Errorf("engine: no provider configured")
	}
	p, ok := e.providers[e.defaultName]
	if !ok {
		return nil, fmt.Errorf("engine: default provider %q missing from registry", e.defaultName)
	}
	return p, nil
}

// HasProviders reports whether at least one provider is registered.
// Used by the chat handler to decide between 503 and a real attempt.
func (e *Engine) HasProviders() bool {
	return e != nil && len(e.providers) > 0
}

// BuildSystemPrompt assembles the system prompt from the agent's
// on-disk definition. Order is: prompt.md → skills (each as a
// labeled section) → knowledge file index. The last block carries a
// cache_control breakpoint so the entire prefix caches across
// turns; per-request user content sits outside this prefix.
//
// **Skill framing note**: we deliberately frame skills as pre-loaded
// behavioural guidelines, NOT as invocable tools. The claude-code
// provider has its own Skill tool that loads named skills from a
// separate registry; without the framing below the model would try
// to call Skill("greet") and get "Unknown skill" because our skills
// aren't registered there — they're already inlined in this very
// prompt.
func BuildSystemPrompt(a *store.Agent) ([]provider.SystemBlock, error) {
	var blocks []provider.SystemBlock

	if promptPath := agentPromptPath(a); promptPath != "" {
		data, err := os.ReadFile(promptPath)
		if err == nil && len(data) > 0 {
			blocks = append(blocks, provider.SystemBlock{Text: strings.TrimSpace(string(data))})
		}
	}

	if len(a.Skills) > 0 {
		var b strings.Builder
		b.WriteString("# Operational guidelines\n\n")
		b.WriteString("The named guidelines below are pre-loaded into this conversation. They are CONTEXT, not invocable tools — apply each guideline directly when its situation matches.\n\n")
		b.WriteString("**Important**: do NOT call any Skill, load_skill, or similar tool with these names — the full guidance is already in the sections below; calling a skill tool will fail because these are not registered there.\n\n")
		for _, sk := range a.Skills {
			b.WriteString("## ")
			b.WriteString(sk.Front.Name)
			b.WriteString("\n\n")
			if sk.Front.Description != "" {
				b.WriteString(strings.TrimSpace(sk.Front.Description))
				b.WriteString("\n\n")
			}
			body := strings.TrimSpace(sk.Body)
			if body != "" {
				b.WriteString(body)
				b.WriteString("\n\n")
			}
		}
		blocks = append(blocks, provider.SystemBlock{Text: strings.TrimRight(b.String(), "\n")})
	}

	if len(a.Knowledge) > 0 {
		var b strings.Builder
		b.WriteString("# Available knowledge\n\n")
		b.WriteString("Files under your knowledge directory. Use the `view` tool to open them by name (relative path under knowledge/).\n\n")
		for _, k := range a.Knowledge {
			b.WriteString("- ")
			b.WriteString(k.Name)
			b.WriteString("\n")
		}
		blocks = append(blocks, provider.SystemBlock{Text: strings.TrimRight(b.String(), "\n")})
	}

	if len(blocks) == 0 {
		blocks = append(blocks, provider.SystemBlock{
			Text: fmt.Sprintf("You are %s, a personal agent.", a.Config.Name),
		})
	}

	// Drop the cache breakpoint on the last block. Render order is
	// tools → system → messages, so this caches everything up to the
	// end of system.
	blocks[len(blocks)-1].CacheControl = true
	return blocks, nil
}

// agentPromptPath returns the absolute path of an agent's prompt.md,
// or "" if the file is not present.
func agentPromptPath(a *store.Agent) string {
	p := a.Dir + "/prompt.md"
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
