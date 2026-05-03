// Package tools is the agent's runtime registry of invokable
// capabilities. The four shipped tools are read-only — view, ls,
// grep, glob — bounded to the session's cwd. Write/exec tools (edit,
// write, bash) need a permission roundtrip and ship in a follow-up
// PR with their own approval surface.
//
// Design echoes crush's tool layout (one file per tool, schema
// declared inline, simple Run signature) so future ports stay
// straightforward.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is the runtime contract every agent tool implements. The
// schema is JSON Schema (the format every modern LLM provider's
// `tools` field expects); each provider driver passes it verbatim.
type Tool interface {
	// Name is the identifier the model invokes. Must match
	// [a-z][a-z0-9_]* (provider conventions). Stable across
	// versions — renaming breaks live conversations.
	Name() string
	// Description is what the model reads when picking a tool.
	// First sentence wins; keep it pithy.
	Description() string
	// InputSchema is the JSON Schema for params. Returned as raw
	// bytes so we marshal it once at startup.
	InputSchema() json.RawMessage
	// Run executes against cwd. The returned string is the content
	// sent back as the tool_result; an error becomes an is_error
	// tool_result so the model can recover.
	Run(ctx context.Context, params json.RawMessage, cwd string) (string, error)
}

// Registry is the per-daemon catalogue of available tools. Read-only
// after construction.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry builds a registry from a fixed set of tools, panicking
// on duplicate names — duplicates are always a programming error.
func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(ts))}
	for _, t := range ts {
		if _, dup := r.tools[t.Name()]; dup {
			panic("tools: duplicate registration: " + t.Name())
		}
		r.tools[t.Name()] = t
	}
	return r
}

// All returns every registered tool, in registration order. Used by
// providers that need to enumerate tools when building a request.
func (r *Registry) All() []Tool {
	if r == nil {
		return nil
	}
	out := make([]Tool, 0, len(r.tools))
	// Stable order matters for prompt-cache friendliness, so we
	// iterate by name (sorted) instead of map order.
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// Get returns a tool by name, ok=false if absent.
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}

// Run dispatches to the named tool. Used by the engine's round-trip
// loop after a provider emits tool_use.
func (r *Registry) Run(ctx context.Context, name string, params json.RawMessage, cwd string) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("tools: unknown tool %q", name)
	}
	return t.Run(ctx, params, cwd)
}

// Default builds the standard read-only registry shipped with sunny.
// The order chosen here also drives the order tools appear in the
// model's prompt — file inspection first, then navigation, then
// search, since that's the order an agent typically reaches for them.
func Default() *Registry {
	return NewRegistry(
		viewTool{},
		lsTool{},
		grepTool{},
		globTool{},
	)
}

// sortStrings is a tiny sort.Strings shim kept inline so this package
// has no transitive sort dep — keeps the test surface obvious.
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j-1] > ss[j]; j-- {
			ss[j-1], ss[j] = ss[j], ss[j-1]
		}
	}
}
