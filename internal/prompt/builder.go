// Package prompt assembles the system prompt for one agent turn.
//
// Render order (each item is one provider.SystemBlock):
//
//  1. Runtime context — sunny meta-prompt: who the agent is running
//     under, safety constraints, paths to its own skills/knowledge,
//     monitors primer. Gated by agent.yaml `no_runtime_context` and
//     env SUNNY_NO_META_PROMPT.
//  2. Persona — the agent's own voice. Either prompt.md (single
//     file) or prompt/<NN>-name.md (multi-file with priority prefix).
//     Multi-file takes precedence; both can coexist (prompt.md is
//     treated as priority 50).
//  3. Catalog — auto-listing of the agent's skills + knowledge files
//     by category. Names + descriptions only; the agent reads the
//     bodies on demand with the view tool.
//  4. Environment — dynamic block: today's date in CDMX, host info,
//     tailnet/peer info if available. Renders fresh every turn.
//
// The cache breakpoint sits at the END of block 3 (Catalog), so
// blocks 1-3 cache across turns and block 4 is always fresh.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/skill"
	"github.com/noesrafa/sunny/internal/store"
)

// Build assembles the system prompt blocks for an agent. env is
// optional — pass nil to skip the environment block (tests, agents
// running headless without a daemon ctx).
//
// The cache breakpoint sits on the LAST non-env block, so:
//   - blocks 1..N-1 (runtime + persona + catalog) cache across turns
//   - block N (environment) renders fresh per turn but sits AFTER
//     the breakpoint, so it does NOT invalidate the cached prefix.
func Build(a *store.Agent, env *Env) ([]provider.SystemBlock, error) {
	var blocks []provider.SystemBlock

	if shouldInjectRuntimeContext(a) {
		blocks = append(blocks, provider.SystemBlock{Text: buildRuntimeContext(a)})
	}

	// Track where agent-specific blocks begin so the fallback below
	// fires when the agent has no prompt.md / skills / knowledge —
	// even if the meta-prompt already filled blocks[0].
	agentBlocksStart := len(blocks)

	for _, seg := range loadPersonaSegments(a) {
		blocks = append(blocks, provider.SystemBlock{Text: seg.text})
	}

	if catalog := buildSkillKnowledgeCatalog(a); catalog != "" {
		blocks = append(blocks, provider.SystemBlock{Text: catalog})
	}

	if len(blocks) == agentBlocksStart {
		blocks = append(blocks, provider.SystemBlock{
			Text: fmt.Sprintf("You are %s, a personal agent.", a.Config.Name),
		})
	}

	// Cache breakpoint on the last static block. The env block (if
	// any) appends after this point.
	blocks[len(blocks)-1].CacheControl = true

	if envText := formatEnv(env); envText != "" {
		blocks = append(blocks, provider.SystemBlock{Text: envText})
	}

	return blocks, nil
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

// personaSegment is one piece of the agent's voice. Sources:
//   - prompt.md (single file, priority 50)
//   - prompt/<NN>-name.md (one segment per file, priority NN or 50)
type personaSegment struct {
	priority int
	name     string // for stable secondary sort + debug
	text     string
}

// personaPriorityRe captures the leading "NN-" prefix from a
// filename. "10-soul.md" → "10"; "soul.md" → no match.
var personaPriorityRe = regexp.MustCompile(`^(\d+)-`)

// loadPersonaSegments returns the agent's voice blocks in render
// order. Resolution:
//   - prompt.md (when present) is treated as one segment, priority 50.
//   - Files in prompt/*.md (when the dir exists) become segments;
//     priority comes from the leading "NN-" filename prefix, default
//     50 if absent. Files prefixed with "_" are skipped (disabled).
//
// Sort: priority asc, then filename asc — so 10-soul.md lands before
// 50-style.md, and a hand-written prompt.md slots wherever its name
// alphabetically falls among the priority-50s.
//
// Read fresh from disk each turn so a hand-edit between turns is
// picked up without going through store.Reload.
func loadPersonaSegments(a *store.Agent) []personaSegment {
	var segs []personaSegment

	// Single-file form. Empty / missing is fine — we just don't emit.
	if data, err := os.ReadFile(a.Dir + "/prompt.md"); err == nil {
		text := strings.TrimSpace(string(data))
		if text != "" {
			segs = append(segs, personaSegment{priority: 50, name: "prompt.md", text: text})
		}
	}

	// Multi-file form. Same dir as the agent — opt-in by creating it.
	promptDir := a.Dir + "/prompt"
	entries, err := os.ReadDir(promptDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
				continue
			}
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(promptDir, name))
			if readErr != nil {
				continue
			}
			text := strings.TrimSpace(string(data))
			if text == "" {
				continue
			}
			pri := 50
			if m := personaPriorityRe.FindStringSubmatch(name); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					pri = n
				}
			}
			segs = append(segs, personaSegment{priority: pri, name: name, text: text})
		}
	}

	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].priority != segs[j].priority {
			return segs[i].priority < segs[j].priority
		}
		return segs[i].name < segs[j].name
	})
	return segs
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
