// Package engine orchestrates one chat turn: build the system prompt from
// an agent's on-disk definition (prompt.md + skills + knowledge file list),
// dispatch to a provider, and stream events back to the caller.
//
// v0.3.0 deliberately stops short of tool use — skills are surfaced to the
// model as a list of capabilities (frontmatter only) so it can reference
// them in prose. Actual tool wiring lands in v0.4.
package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/store"
)

// Engine routes turns to the right provider for each agent.
//
// Multiple drivers can live side-by-side: an agent.yaml can declare
// `provider: ollama` to override the daemon default, while another
// agent uses `provider: anthropic`, all in the same TUI.
//
// Concurrency: registry is read-only after construction. Hot-swap of
// providers (after `sunny secrets <provider> set`) re-instantiates
// the whole engine — see daemon.rebuildEngine.
type Engine struct {
	providers   map[string]provider.Provider
	defaultName string
}

// New constructs an engine bound to a registry of providers keyed by
// name (the same name agent.yaml's `provider:` field picks). The
// default name is used when the agent doesn't specify one. Both can
// be empty — the engine becomes a 503-er, useful while the daemon
// has no API keys yet.
func New(providers map[string]provider.Provider, defaultName string) *Engine {
	return &Engine{providers: providers, defaultName: defaultName}
}

// TurnOptions are per-call knobs that travel with the turn but aren't on
// the agent definition.
type TurnOptions struct {
	// ProviderState is opaque token from the previous turn's Done event.
	// Empty for first turn.
	ProviderState string
	// Cwd that file/bash tools should resolve against. Empty falls back
	// to the user's home dir.
	Cwd string
}

// Turn runs one user→assistant exchange against the given agent.
// Provider is picked by agent.Config.Provider with fallback to the
// engine default; agents pinned to an unconfigured provider get a
// clear error instead of silently falling back.
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

	effort := agent.Config.Effort
	if effort == "" {
		effort = "max"
	}
	req := provider.Request{
		Model:         agent.Config.Model,
		MaxTokens:     16000,
		System:        system,
		Messages:      messages,
		Effort:        effort,
		Cwd:           opts.Cwd,
		ProviderState: opts.ProviderState,
	}
	return p.Stream(ctx, req)
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

// ProviderName surfaces the active default for logging / UI. Returns
// "" when no default is configured.
func (e *Engine) ProviderName() string {
	if e == nil {
		return ""
	}
	return e.defaultName
}

// HasProviders reports whether at least one provider is registered.
// Used by the chat handler to decide between 503 and a real attempt.
func (e *Engine) HasProviders() bool {
	return e != nil && len(e.providers) > 0
}

// BuildSystemPrompt assembles the system prompt from the agent's on-disk
// definition. Order is: prompt.md → skills (each as a labeled section) →
// knowledge file index. The last block carries a cache_control breakpoint
// so the entire prefix caches across turns; per-request user content sits
// outside this prefix.
//
// Layout decisions:
//   - prompt.md is the persona / first impression — goes first.
//   - Skills are listed by name + description + body. Stable enough to
//     belong inside the cached prefix.
//   - Knowledge is enumerated as a file index ("you have access to N files
//     under knowledge/"). The agent can reference them by name; v0.4 adds
//     a tool to actually open them.
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
		b.WriteString("# Available skills\n\n")
		b.WriteString("These are skills you have access to. Each skill describes a capability you can apply when relevant.\n\n")
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
		b.WriteString("Files under your knowledge directory. You can reference them by name; tools to open them land in a future version.\n\n")
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

	// Drop the cache breakpoint on the last block. Render order is tools →
	// system → messages, so this caches everything up to the end of system.
	blocks[len(blocks)-1].CacheControl = true
	return blocks, nil
}

// agentPromptPath returns the absolute path of an agent's prompt.md, or ""
// if the file is not present.
func agentPromptPath(a *store.Agent) string {
	p := a.Dir + "/prompt.md"
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
