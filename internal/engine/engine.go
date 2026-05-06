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
	"sort"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/provider/opencode"
	"github.com/noesrafa/sunny/internal/skill"
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

// ProviderNames returns the registered provider names, sorted
// alphabetically. Used by /stats.
func (e *Engine) ProviderNames() []string {
	out := make([]string, 0, len(e.providers))
	for name := range e.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DefaultProvider returns the name of the provider used when an
// agent has no `provider:` field. Empty when no providers are
// registered.
func (e *Engine) DefaultProvider() string { return e.defaultName }

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

	// opencode driver writes ~/.config/opencode/agent/sunny-<id>.md
	// before its first spawn — needs the id from context.
	if p.Name() == "opencode" {
		ctx = opencode.WithAgentID(ctx, agent.ID)
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
	advertised := e.advertisedTools(agent, p)

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
			case provider.SessionState:
				// Pass through so the server can persist the resume
				// token before the turn ends. claude-code emits this
				// on its first system.init.
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
//
// We key the skip on the picked provider's Name() instead of
// agent.Config.Provider because an agent may leave Provider empty
// and fall through to the daemon default. If the default ends up
// being claude-code, advertising sunny's tools would let the engine
// collect tool_use events for claude-code's native tools (Bash,
// Read, Edit, Grep) and try to dispatch them through sunny's
// registry, which fails with "unknown tool" and then breaks the
// next iteration ("last message must be role=user").
func (e *Engine) advertisedTools(agent *store.Agent, p provider.Provider) []provider.ToolDef {
	if e.tools == nil {
		return nil
	}
	if p != nil {
		if name := p.Name(); name == "claude-code" || name == "opencode" {
			return nil
		}
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
			return nil, fmt.Errorf("engine: agent %q wants provider %q which isn't configured", agent.ID, want)
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

// runtimeContextHeader is the agent-independent part of the sunny
// meta-prompt: tells the model it's running inside sunny and lists
// the safety constraints (commands that would kill the daemon).
const runtimeContextHeader = `# Runtime context (injected by sunny)

You are running inside sunny (https://github.com/noesrafa/sunny), a
local daemon at http://localhost:7777 that orchestrates AI agents.

- Your data lives at ` + "`~/.sunny/`" + `. The journal of this conversation
  is in ` + "`~/.sunny/agents/<your-slug>/conversations/<conv-id>/`" + `.
- Other agents share this daemon. Listing/CRUD via ` + "`/agents`" + `.
- The daemon hosts THIS conversation. **Do NOT run ` + "`sunny stop`" + `,
  ` + "`sunny restart`" + `, or ` + "`sunny update`" + `** — they kill the daemon,
  which terminates this turn mid-stream and may corrupt the
  journal.
- Read-only safe: ` + "`sunny status`" + `, ` + "`sunny doctor`" + `, ` + "`sunny token`" + `,
  ` + "`sunny peers`" + `, ` + "`curl localhost:7777/healthz`" + `.
- The user can talk to other sunny daemons across their tailscale
  network; if they say "in the vps" they may mean a remote daemon.`

// skillKnowledgeRules is the static guidance about how skills and
// knowledge are organized on disk: format, priority over
// environment skills, and the categorization rule for new entries.
// Paired with a path block built from the agent's Dir so the model
// has absolute paths to work with.
const skillKnowledgeRules = "SKILL.md is YAML frontmatter (`name`, `description`, optional\n" +
	"`allowed-tools`) followed by a markdown body. Knowledge files\n" +
	"are plain markdown (`.md`).\n" +
	"\n" +
	"**Prefer your own skills** over any skills your provider exposes\n" +
	"(claude-code's, opencode's, or others) — yours are curated for\n" +
	"this agent. Read SKILL.md files with the view tool. Do NOT call\n" +
	"any Skill or load_skill tool with these names — they are not in\n" +
	"any external registry.\n" +
	"\n" +
	"When you create a new skill or knowledge file, **place it under\n" +
	"the category that best fits**. If no existing category fits,\n" +
	"create a new one: a fresh subdirectory under `skills/` or\n" +
	"`knowledge/` plus an `INDEX.md` whose body opens with one line\n" +
	"describing what lives there."

// monitorsPrimer teaches the agent the monitor YAML schema, where
// the daemon-side scheduler picks them up, and the v1 source/action
// types. The agent creates monitors with its own write/edit tools;
// the TUI is read-only with an enable/disable toggle.
const monitorsPrimer = "Monitors are YAML files the daemon's scheduler picks up\n" +
	"automatically — write or edit them like any other file. The\n" +
	"watchdog scans this directory every few seconds; rule edits to\n" +
	"an enabled monitor apply on its next tick without a restart.\n" +
	"\n" +
	"File shape (one per `.yaml`, the file's basename is the monitor\n" +
	"name when `name` is omitted):\n" +
	"\n" +
	"```yaml\n" +
	"name: example\n" +
	"enabled: true            # toggle off → scheduler stops it on next scan\n" +
	"interval: 30s            # 1s, 5m, 1h — Go duration\n" +
	"source:\n" +
	"  type: shell            # v1: only `shell`\n" +
	"  command: |             # must print a JSON array of objects to stdout\n" +
	"    curl -s https://api.example.com/items\n" +
	"rules:\n" +
	"  - name: react-on-error\n" +
	"    when:\n" +
	"      text_matches: \"error\"   # regex on item.text; also `all`/`any` composers\n" +
	"    then:\n" +
	"      - dispatch:                # v1: only `dispatch`\n" +
	"          agent: sunny           # any agent slug — runs a one-shot turn\n" +
	"          prompt: \"Issue: ${item.text}\"\n" +
	"```\n" +
	"\n" +
	"Each item the source produces should have an `id` field for\n" +
	"deduplication (the same id won't fire rules twice across ticks).\n" +
	"Variable substitution: `${item.<field>}` resolves to that field\n" +
	"of the matched item; `${dispatch.result}` is the model's\n" +
	"response from a previous `dispatch` action in the same rule\n" +
	"(handy for chaining: dispatch → reply with the result)."

// buildRuntimeContext stitches the meta-prompt together for one
// agent: header + safety rules + the agent's own skills/knowledge
// paths + monitors path + the categorization/priority rules.
func buildRuntimeContext(a *store.Agent) string {
	// monitors live alongside agents/ under the daemon root; derive
	// the path from a.Dir so we don't need to thread the root.
	root := a.Dir
	if i := strings.LastIndex(root, "/agents/"); i >= 0 {
		root = root[:i]
	}
	monitorsDir := root + "/monitors"

	return runtimeContextHeader + "\n" +
		"\n" +
		"## Skills & knowledge\n" +
		"\n" +
		"Your skills and knowledge are markdown files on disk,\n" +
		"organized by category subdirectory:\n" +
		"\n" +
		"- Skills:    `" + a.Dir + "/skills/<category>/<name>/SKILL.md`\n" +
		"- Knowledge: `" + a.Dir + "/knowledge/<category>/<file>.md`\n" +
		"\n" +
		skillKnowledgeRules + "\n" +
		"\n" +
		"## Monitors\n" +
		"\n" +
		"Monitors live at `" + monitorsDir + "/<name>.yaml`. Each is a\n" +
		"polling rule chain (interval + source + rules) the daemon\n" +
		"runs in the background.\n" +
		"\n" +
		monitorsPrimer
}

// shouldInjectRuntimeContext is the gate. Off when the agent opted
// out via agent.yaml or when the operator set SUNNY_NO_META_PROMPT
// in the environment (test/dev escape hatch).
func shouldInjectRuntimeContext(a *store.Agent) bool {
	if a != nil && a.Config != nil && a.Config.NoRuntimeContext {
		return false
	}
	if v := os.Getenv("SUNNY_NO_META_PROMPT"); v == "1" || v == "true" {
		return false
	}
	return true
}

// BuildSystemPrompt assembles the system prompt from the agent's
// on-disk definition. Order is: runtime context (sunny meta + the
// agent's own paths/rules) → prompt.md → skills+knowledge catalog
// (per-category listing of names + descriptions only — bodies are
// loaded on demand via the view tool). The last block carries a
// cache_control breakpoint so the prefix caches across turns;
// per-request user content sits outside this prefix.
//
// Progressive disclosure is deliberate: even with dozens of skills
// the prompt stays compact. The agent reads SKILL.md (or knowledge
// .md files) only when they're relevant.
func BuildSystemPrompt(a *store.Agent) ([]provider.SystemBlock, error) {
	var blocks []provider.SystemBlock

	if shouldInjectRuntimeContext(a) {
		blocks = append(blocks, provider.SystemBlock{Text: buildRuntimeContext(a)})
	}

	// Track where agent-specific blocks begin so the fallback below
	// fires when the agent has no prompt.md / skills / knowledge —
	// even if the meta-prompt already filled blocks[0].
	agentBlocksStart := len(blocks)

	if promptPath := agentPromptPath(a); promptPath != "" {
		data, err := os.ReadFile(promptPath)
		if err == nil && len(data) > 0 {
			blocks = append(blocks, provider.SystemBlock{Text: strings.TrimSpace(string(data))})
		}
	}

	if catalog := buildSkillKnowledgeCatalog(a); catalog != "" {
		blocks = append(blocks, provider.SystemBlock{Text: catalog})
	}

	if len(blocks) == agentBlocksStart {
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

// buildSkillKnowledgeCatalog renders the per-category index of the
// agent's skills and knowledge files. Returns "" when the agent has
// neither, so the caller can omit the block entirely. Skills are
// shown as `name — description`; knowledge files as their relative
// path (so the model can pass the path straight to view).
func buildSkillKnowledgeCatalog(a *store.Agent) string {
	if len(a.Skills) == 0 && len(a.Knowledge) == 0 {
		return ""
	}
	var b strings.Builder

	if len(a.Skills) > 0 {
		b.WriteString("# Skills available\n\n")
		b.WriteString("Names + descriptions only. Read the body with the `view` tool when a skill applies.\n\n")
		bySkillCat := map[string][]*skill.Skill{}
		for _, sk := range a.Skills {
			bySkillCat[sk.Category] = append(bySkillCat[sk.Category], sk)
		}
		for _, cat := range a.SkillCategories {
			b.WriteString("## ")
			b.WriteString(cat.Name)
			if cat.Description != "" {
				b.WriteString(" — ")
				b.WriteString(cat.Description)
			}
			b.WriteString("\n\n")
			list := bySkillCat[cat.Name]
			if len(list) == 0 {
				b.WriteString("_(no skills in this category yet)_\n\n")
				continue
			}
			for _, sk := range list {
				b.WriteString("- `")
				b.WriteString(sk.Front.Name)
				b.WriteString("` — ")
				b.WriteString(strings.TrimSpace(sk.Front.Description))
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	if len(a.Knowledge) > 0 {
		b.WriteString("# Knowledge available\n\n")
		b.WriteString("Markdown files under your knowledge directory. Open them with the `view` tool.\n\n")
		byKnowCat := map[string][]store.KnowledgeFile{}
		for _, k := range a.Knowledge {
			byKnowCat[k.Category] = append(byKnowCat[k.Category], k)
		}
		for _, cat := range a.KnowledgeCategories {
			b.WriteString("## ")
			b.WriteString(cat.Name)
			if cat.Description != "" {
				b.WriteString(" — ")
				b.WriteString(cat.Description)
			}
			b.WriteString("\n\n")
			list := byKnowCat[cat.Name]
			if len(list) == 0 {
				b.WriteString("_(no files in this category yet)_\n\n")
				continue
			}
			for _, k := range list {
				b.WriteString("- ")
				b.WriteString(k.Name)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
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
