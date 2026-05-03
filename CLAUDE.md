# sunny — conventions for contributors and AI assistants

This file is the contract for how we evolve `sunny`. Read it before
proposing changes; update it when conventions change.

## Resume here (where we are right now)

**Latest release: `v0.10.0`** (read-only tools — view, ls, grep,
glob — with full round-trip loop in the engine). Brew `sunny version`
should match.

Everything that works today is enumerated in PLAN.md → "Estado
actual". The short version: chat works end-to-end across three
providers (claude-code, anthropic, ollama), conversations persist
per-agent, the agent has read-only filesystem access tools, and the
TUI restores its layout on restart.

**Single most likely next thing to pick up**: write/exec tools
(`edit`, `write`, `bash`) + their permission flow. Design notes are
in PLAN.md → "Lo que sigue". Read that first; the path is laid out.

### How to verify things quickly

```bash
# basic loop
sunny start && sunny status               # daemon up, healthz ok
sunny token                                # bearer token (mode 0600)
sunny secrets                              # list configured providers

# end-to-end smoke
TOK=$(sunny token)
curl -s -H "Authorization: Bearer $TOK" localhost:7777/agents | jq
```

### How to verify the tools

Configure ollama (or anthropic) so we have a non-claude-code
provider, create an agent on it, and ask the model to use a tool
explicitly:

```bash
sunny secrets ollama set api_key      # paste from stdin
sunny stop && sunny start              # daemon picks up new provider

curl -s -X POST -H "Authorization: Bearer $(sunny token)" \
  -H "Content-Type: application/json" \
  -d '{"slug":"gemma","name":"Gemma","model":"gemma4:31b","provider":"ollama","prompt":"You are Gemma."}' \
  localhost:7777/agents
```

Then in the TUI: `ctrl+a` → enter on `gemma`. Ask it to "read
README.md with the view tool"; the model will emit a tool_use, the
engine runs `view`, feeds the result back, model continues. Same
flow with grep / glob / ls.

### Where the bodies are buried

- `internal/engine/engine.go` — `runTurnLoop` is the round-trip
  driver. If a tool invocation behaves wrong, start there.
- `internal/tools/` — one file per tool. `path.go` is the
  cwd-bounded resolver (had a macOS `/tmp` symlink bug; the fix
  EvalSymlinks both sides).
- `internal/provider/{anthropic,ollama}/` — the wire translation.
  `claude-code` deliberately doesn't get `req.Tools`; it has its
  own native toolset.
- `cmd/sunny/serve.go` `buildEngine` — auto-detection chain. If a
  provider doesn't show up, this is where to log-trace.
- `cmd/sunny/tui.go` `openTUI` — state restore + bootstrap session.
- `internal/server/chat.go` `postTurn` — SSE writer + journal append.

## Code principles (in priority order)

1. **Idiomatic Go.** Errors wrapped with `%w`, no `panic` outside `main`,
   `context.Context` propagated through anything that does I/O, small
   focused interfaces, packages with a single clear purpose.
2. **Quality over speed.** Better to reopen a PR than to merge silent
   tech debt. Prefer correct + readable to clever.
3. **Maintainability and extensibility.** Code is read more than it is
   written. Avoid premature abstraction, but also avoid structural
   bottlenecks (300-line functions, import cycles, leaky packages). New
   subsystems get their own internal package with a clean surface.
4. **Simplicity.** The simplest solution that meets the requirement
   wins. If a feature requires lots of branching or conditional config,
   stop and discuss first.

## Process

- **Confirm before non-trivial changes.** Any change beyond a one-line
  bug fix gets proposed in plain text first, then implemented after
  approval. This applies to refactors, new packages, new commands, and
  changes to public surfaces (CLI flags, HTTP routes, on-disk layout).
- **Release every merged change.** After a fix or feature lands on
  `main`: bump the version in `cmd/sunny/main.go` linker flags or via
  the release workflow, tag (`vX.Y.Z`), publish a GitHub release, and
  update the Homebrew tap so it can be tested e2e via `brew upgrade`.
- **Comments at minimum.** Only when the *why* is non-obvious from the
  code. Never restate what the code does.

## Architecture snapshot

```
cmd/sunny           — CLI entrypoint; flag parsing, command dispatch.
internal/server     — HTTP daemon; sole owner of runtime state.
internal/client     — TUI's HTTP client to the daemon.
internal/tui        — Terminal UI, provider-agnostic, talks only to the daemon.
internal/bootstrap  — Seeds ~/.sunny/ from defaults/ on first run.
internal/lifecycle  — On-disk daemon state (pid, addr, started_at).
internal/engine     — Provider-agnostic chat orchestration.
internal/provider   — Interfaces + claudecode/anthropic drivers.
internal/store      — Walks ~/.sunny/ and indexes agents/skills/knowledge.
internal/session    — In-process session bookkeeping.
defaults/           — Embedded seed tree (agents/sunny/...).
```

The TUI is a **thin client of the daemon**. It must not contain
provider logic, must not read `~/.sunny/` directly for chat, and must
survive the daemon restarting under it.

## Daemon contract

- The daemon is always launched **detached** (`Setsid`). Closing the
  TUI never kills the daemon.
- `sunny stop` is the only command that terminates a running daemon.
- `sunny start` spawns a one-shot detached daemon and waits for
  `/healthz`. If the daemon does not become healthy in time (or its
  process exits during boot), `start` reaps it, clears
  `~/.sunny/run/state.json`, and returns an error with a tail of the
  log — it does not leave a half-broken daemon behind.
- Auto-start: invoking `sunny` (no args) opens the TUI; if no daemon is
  running, it spawns one first. If that spawn fails, the TUI does not
  open — the user sees the failure synchronously.
- The daemon owns `~/.sunny/`. Bootstrap seeds the tree on first run
  by checking for `~/.sunny/agents/` (the real "fresh install"
  marker). Once seeded, the directory belongs to the user.

## State restore

The TUI persists its layout (open tabs, active idx, drafts, theme) to
`~/.sunny/state.json`. On startup, sessions are rebuilt from this file:
agent slug, conv id, model, effort, draft, and the cached transcript
items. `~/.sunny/agents/<slug>/conversations/<id>/events.jsonl` remains
the canonical source for chat content; the cached items are a UI
optimization so the chat re-renders instantly without an HTTP round-trip.

If the agent referenced by a saved session no longer exists (was
archived), the session restores anyway — `SendBegin` will surface the
error on the next attempt.

## Tools

The agent has four read-only tools, registered in
`internal/tools/`:

- **view** — read a file with line numbers, offset/limit support.
- **ls** — directory tree with depth + ignore (skips dotfiles +
  node_modules/.git/vendor/etc.).
- **grep** — regex search; uses `rg` if on PATH, else Go-native walker.
- **glob** — find files by `**` pattern.

All four are bounded to the session's cwd via `tools.resolveInside`,
which evaluates symlinks on both sides (important on macOS where
`/tmp` is `/private/tmp`). Outside-cwd reads hard-deny — no
permission UI yet; that's the gating dependency for write/exec
tools (edit, write, bash) which land in a follow-up PR.

The engine runs the round-trip loop in `engine.runTurnLoop`: when
the provider emits ToolUse, the engine executes via the registry,
appends the assistant + tool messages to the running conversation,
and re-streams. Cap of 25 iterations to prevent runaway loops. Tool
events are also forwarded to the SSE stream so the TUI shows the
tool-use spinner.

Provider routing for tools:
- **claude-code**: skip — it has its own native toolset (Read,
  Glob, Grep, Bash, …) registered internally. Advertising sunny's
  tools would create duplicate names and confuse the model.
- **anthropic**: full tool support via `MessageNewParams.Tools`
  + `ContentBlockParamUnion` for tool_use/tool_result.
- **ollama**: full tool support via OpenAI-style `tools[]` in
  `/api/chat` body + `message.tool_calls` parsing. Synthesizes
  call IDs when the server omits them.

The neutral wire format (in `provider.Message`) is OpenAI-shaped:
- `Role:"assistant" + ToolCalls:[…]` for the model's tool invocation
- `Role:"tool" + ToolUseID + Content + IsError` for the result

Each driver translates this to its native shape.

## Multi-agent

- Agents live at `~/.sunny/agents/<slug>/`. Each owns its skills,
  knowledge, conversations, and persona.
- CRUD over HTTP:
  - `GET /agents` — list summaries
  - `POST /agents` — create (`{slug,name,description,model,prompt}`)
  - `GET /agents/{slug}` — full detail (now includes `prompt`)
  - `PATCH /agents/{slug}` — partial update; nil fields untouched
  - `DELETE /agents/{slug}` — moves dir to `~/.sunny/.trash/`,
    idempotent
- Slug shape: `[a-z0-9][a-z0-9-]*`. Immutable after creation. To
  rename an agent, copy/move on disk and reload the daemon — the
  HTTP API doesn't support rename in v0.6.
- TUI: `ctrl+a` opens the agent picker. Enter spawns a new session
  bound to the chosen agent; `n` opens the create form, `e` edits,
  `d` deletes (with confirm). Each TUI session is bound to one
  agent for its lifetime — switching means a new session/tab.
- The in-memory `store.Store` is mutated atomically with the
  filesystem on every CRUD op. No fsnotify (yet).

## Conversation persistence

Every chat lives under its agent:

```
~/.sunny/agents/<slug>/conversations/<conv_id>/
  meta.json     — title, timestamps, msg_count, model, provider_state
  events.jsonl  — append-only journal (user, text_delta, thinking_delta,
                  tool_use, tool_result, done, error, cancelled)
```

- `conv_id` shape: `conv_<unix_ms>_<8hex>` — sortable by creation time,
  collision-resistant.
- Journal is the truth; `meta.json` is a rollup for cheap listing.
- `provider_state` (claude-code's `--resume` session id) lives in
  `meta.json` so it survives daemon restarts. The wire protocol no
  longer carries it; clients only send `messages[]` + `cwd`.
- Deleting a conversation moves the directory to `~/.sunny/.trash/`.
  No automatic emptying — the user controls the trash.
- The TUI lazily creates a server-side conversation on the first user
  message of a session and reuses the id for subsequent turns.

## Secrets

API keys live in `~/.sunny/secrets.yaml` (mode 0600), structured by
provider:

```yaml
anthropic:
  api_key: sk-ant-…
openai:
  api_key: sk-…
ollama:
  api_key: …
  base_url: https://ollama.com
```

- Env vars (`ANTHROPIC_API_KEY`, `OLLAMA_API_KEY`, …) override the
  file when both are present. Useful for CI / docker.
- Provider drivers re-read on every Stream() call — rotating a key
  takes effect on the next turn, no restart needed for the *value*.
  Adding a brand-new provider (one not in the registry yet) requires
  the registry to be rebuilt. PUT/DELETE on `/secrets` triggers that
  rebuild automatically; CLI writes don't (they write straight to
  the file).
- Mutators reload from disk before writing so concurrent edits from
  the daemon and the CLI don't stomp each other.
- Daemon NEVER returns secret values over HTTP. `GET /secrets`
  surfaces only the list of configured providers + their field names.
- CLI: `sunny secrets` (list), `sunny secrets <p>` (show fields),
  `sunny secrets <p> set <field>` (read value from stdin or
  interactive prompt), `sunny secrets <p> delete`.
- TUI: `ctrl+y` opens the secrets manager. Selecting a provider
  opens a paste form. Values are not masked — paste verification
  matters more than shoulder-surfing in a single-user CLI app.

## Ollama Cloud provider notes

Driver at `internal/provider/ollama/`. We POST `/api/chat` with bearer
auth (the `Authorization: Bearer $api_key` header). Streaming is
JSONL — one JSON object per line, terminated by `{"done": true, ...}`.

Wire shape sent:
```json
{"model":"…","messages":[…],"stream":true,"keep_alive":"10m","think":bool}
```

- `keep_alive`: 10 minutes default. Cold-loads on cloud are
  expensive; this saves the second-turn cost in interactive chats.
- `think`: mirrors the engine's `AdaptiveThinking`. Reasoning models
  (gpt-oss, deepseek-r1, qwen3-thinking) emit content into
  `message.thinking` when this is on; the driver maps it to
  `provider.ThinkingDelta`.
- HTTP transport sets `ResponseHeaderTimeout=30s` and
  `IdleConnTimeout=90s`. Body has no timeout (streaming).

Not yet implemented (sketches, not roadmap commitments):
- **Tool calling.** Ollama's `tools` request field + `message.tool_calls`
  response field. Lands when sunny gets a real tool-execution layer
  (today skills are passive prompt content).
- **Structured output (`format: "json"` or schema).** Pair feature
  with tools.
- **Per-request `options`** (temperature, top_p, num_predict).
  `provider.Request` doesn't carry these today.

Contrast with crush: they use Ollama via OpenAI-compat at `/v1/chat/
completions` (which gets them tool calls "for free" via fantasy's
abstraction). We use the native `/api/chat` because thinking deltas
and Ollama-specific knobs come through cleaner.

## Provider routing

`agent.yaml` carries an optional `provider:` field. When set, turns
against that agent always use the named provider regardless of the
daemon's default. Empty falls through to the daemon's auto-detected
default (claude-code → anthropic → ollama). This lets one TUI run
agents on different backends in parallel.

The engine holds a `map[name]provider.Provider` registry plus a
default name; both come from `buildEngine()` which probes every
known driver at construction. Drivers that fail to construct
(missing key, claude CLI not on PATH) just don't enter the registry —
agents that pinned to them get a clear error on the next turn.

## Auth contract

- `~/.sunny/token` (32 random bytes, base64url, mode 0600) is generated
  on first daemon boot and reused thereafter. File permissions are the
  trust boundary — the daemon never exposes the token over HTTP.
- All HTTP routes require `Authorization: Bearer <token>` except
  `/healthz` (so liveness probes work without credentials).
- Clients (TUI, curl, future bridges) read the file directly. The TUI
  loads it at start and caches it in memory.
- `sunny token` prints the current token. `sunny token rotate`
  generates a new one — but the running daemon caches its bearer in
  memory, so rotation only takes effect after `sunny stop && sunny
  start`. Open TUI sessions must also be relaunched.
- Empty token disables auth (test/dev only); the daemon never sets
  this on its own.

## Known sharp edges (not yet fixed)

- `lifecycle.IsAlive` uses `signal(0)` only. If the PID gets recycled
  to another of the user's processes between daemon crash and the next
  `sunny` invocation, we'd misreport "alive." Low risk in practice;
  fix would be to also validate `started_at` or the binary path.
- Concurrent `sunny start` invocations could both see no state and
  both spawn a daemon. The second one fails to bind the port and dies;
  the loser leaves no trace. Real fix is a `flock` on `state.json`.
- macOS launchd integration (survive Mac reboot, auto-respawn on
  crash) is not implemented yet. Manually `sunny start` for now.
