# sunny — conventions for contributors and AI assistants

This file is the contract for how we evolve `sunny`. Read it before
proposing changes; update it when conventions change.

## Resume here (where we are right now)

**Pending for next release (`v0.37`): prompt builder rework.** Three
changes that don't break any existing agent:

1. **Prompt building moved to `internal/prompt/`.** `engine.go` no
   longer carries the runtime-context strings or the catalog
   builder; `engine.Turn` calls `prompt.Build(agent, env)` directly.
   No external behavior change vs. v0.36 — same blocks, same order,
   same cache breakpoint.
2. **Multi-file persona.** Agents can now keep their voice in
   `prompt/<NN>-name.md` files alongside (or instead of) the
   single `prompt.md`. Files merge by leading numeric prefix
   (`10-soul.md` < `50-style.md`); `prompt.md` is treated as
   priority 50. `_`-prefixed files are skipped (disabled). Each
   fragment becomes its own SystemBlock so cache_control still
   sits on the LAST static block. Read fresh from disk per turn,
   so hand-edits land without `store.Reload`.
3. **System environment block.** A new dynamic block lands at the
   END of the prompt — AFTER the cache breakpoint — with date in
   CDMX, hostname, OS/arch, LAN IPv4, tailnet IPv4, and the
   daemon's listen addr. The agent now knows where it's running
   ("the user said 'in the VPS' — I'm on a Linux box at
   100.127.63.124") without burning cache. Sampled per turn via
   `prompt.SampleEnv(daemonAddr)`; engines without an envFn (tests,
   headless) just don't emit the block.

Order of blocks emitted by `prompt.Build`:

```
[1] Runtime context        (static, cached)
[2] Persona block(s)       (static, cached)         ← one per fragment
[3] Skills/knowledge cat.  (static, cache_control)  ← breakpoint
[4] Environment            (dynamic, no cache)
```

Knobs:
- `agent.yaml: no_runtime_context: true` skips block [1].
- `SUNNY_NO_META_PROMPT=1` (env) does the same globally.
- `prompt.Build(a, nil)` skips block [4] (test path).

---

**Latest release: `v0.29.0`** (interactive onboarding, sunny
uninstall, secrets catalog, secrets template seed, secrets knowledge
for the default agent). Five additions on top of v0.28:

1. **`sunny onboarding`** is the interactive first-run flow and the
   manual doctor: 8 steps (welcome / tailscale / brew + tap /
   claude-code / opencode / ollama / first agent / done) — all
   skippable, idempotent, re-runnable any time. Probes via
   `internal/doctor`, installs via brew subprocess, key writes
   through the daemon's `PUT /secrets`, agent edit through `PATCH
   /agents/sunny`. Lives in `internal/onboarding/`.
2. **`sunny uninstall`** is the cleanup sibling: stops the daemon,
   asks before removing `~/.sunny/`, detects brew vs standalone
   binary install, untaps if applicable. `--yes` for scripts,
   `--keep-data` to never prompt.
3. **`GET /secrets/catalog`** returns the canonical provider
   catalog (anthropic / openai / ollama, with fields + env vars +
   help URLs). Source of truth moved from `dialog_secrets.go` →
   `internal/secrets/catalog.go`. TUI consumes via `secrets.Catalog()`,
   external clients via the endpoint. `Client.SecretsCatalog(ctx)`.
4. **Bootstrap seeds `~/.sunny/secrets.yaml`** (mode 0600) with a
   commented stub the first time the daemon boots. Idempotent — only
   writes when the file is absent. AI agents `view`-ing the file
   immediately see the shape; users `cat`-ing it see a self-doc.
5. **Default agent knowledge file** at
   `defaults/agents/sunny/knowledge/general/secrets.md` documents
   the file location, shape, and direct-edit flow so AI agents can
   set up providers without round-tripping through the API.

**v0.28 (opaque agent IDs + version-check endpoint).** Two shifts:

a. **Agent identity is opaque.** Every agent has an `id` (shape
   `agt_<unix_ms>_<8hex>`, generated server-side) plus a mutable
   `name`. The id is the only handle on disk + on the wire; the
   name is display-only. Renaming = `PATCH /agents/{id}
   {"name":"…"}` — no file moves, no journal rewrites. The TUI
   picker hides the id entirely; `r` opens a lightweight rename
   dialog. The seeded default keeps `id: sunny` for stability;
   user-created agents get fresh `agt_…` ids.
2. **Self-update awareness.** `GET /sunny/version/check` hits the
   GitHub releases API (cached 5 min server-side) and returns
   `{current, latest, update_available, release_url}`. Pair it with
   the existing `POST /sunny/update` to offer in-app update flow.
   `dev` builds and unreachable GitHub never trigger update prompts.

**v0.18 → v0.27 highlights** (background since the doc was
updated): runs (background services with per-peer manager +
sidebar, `Ctrl+R`), monitors (scheduler + dispatch action +
history viewer, `Ctrl+B`), per-agent avatars at `/agents/{id}/
avatar`, dir picker that walks the target peer's filesystem,
self-update via `sunny update` (brew + GitHub fallback) and
HTTP `POST /sunny/update`, `/stats` snapshot endpoint,
`turn.{started,done,cancelled}` bus events, runtime-context
auto-injection into the system prompt.

The original v0.18 release notes are kept below for context.

**v0.18 (per-peer TUI + multi-viewer real-time + server-side
tabs).** Three big shifts on top of v0.17:

1. **Tabs live on the daemon**, not in each TUI. `~/.sunny/tabs.json`
   is the source of truth; the TUI fetches via `GET /tabs` at boot
   and reconciles on `tab.opened/closed/updated` bus events. Two
   TUIs against the same daemon see the same tabs in real time;
   open a tab in one, it appears in the other.
2. **Per-conversation pub/sub**: chat is now POST `/turns` (202
   fire-and-forget) + `GET /watch?since=N` (SSE with seq-based
   resume). Multiple watchers of the same conv see identical
   delta streams. Cancellation via `DELETE /turn`.
3. **Per-peer TUI**: `Ctrl+1..9` jump between federation peers
   (each peer is its own universe of tabs/agents). Header switcher
   shows pills with activity dots. Always boots in `local`.

Everything that works today is enumerated in PLAN.md → "Estado
actual". The short version: chat works end-to-end across four
backends; the TUI auto-discovers every sunny daemon in your
tailscale account; tabs sync across TUIs sharing a daemon; per-
conversation watches let any number of viewers see the same chat
in real time. Optional `mesh.key` override exists for sub-meshes
within shared tailnets. Manual `sunny pair` flow still works for
hosts off the tailnet.

**Single most likely next thing to pick up**: write/exec tools
(`edit`, `write`, `bash`) + their permission flow — the original
post-v0.10 plan that mesh work pushed back. PLAN.md → "Lo que
sigue" has the design. A close second: a "join existing
conversation" picker (`ctrl+o`?) to attach a new tab to a conv
that already exists in the journal.

### Daemon stats (since v0.18)

`GET /stats` returns a one-shot snapshot of everything contable in
the daemon. Useful for dashboards, federation peer cards, and any
external app that wants "what is this daemon doing right now"
without computing it client-side. Shape:

```json
{
  "daemon":  {"version", "instance_id", "uptime_s", "started_at",
              "providers_configured", "default_provider"},
  "counts":  {"agents", "conversations", "tabs",
              "conversations_per_agent": {<slug>: n}},
  "live":    {"turns_in_flight": [{"slug","conv_id"}],
              "bus_subscribers", "watchers": {<slug/conv>: n}},
  "system":  {"cpu_percent", "num_cpu", "memory_percent",
              "memory_total_bytes", "memory_used_bytes", "platform"},
  "process": {"goroutines", "heap_alloc_bytes", "heap_sys_bytes"}
}
```

CPU sample takes ~1s wall (two `top -l 2` snapshots; same path
the TUI sidebar uses via `internal/sysstats`). System block is
zero on non-darwin — the `Platform` and `NumCPU` fields still
populate. The `live` block reads in-memory registries that
already exist (`activeTurnsRegistry`, `Sink.LiveStats()`,
`Hub.SubCount()`); cost is microseconds.

Client helper at `internal/client/stats.go`:
```go
c := client.New(addr, token)
s, _ := c.FetchStats(ctx)
```

### How to verify things quickly

```bash
sunny doctor                               # one-screen checklist (incl. peers)
sunny start && sunny status                # daemon up, healthz ok
sunny token                                # bearer token (mode 0600)
sunny peers                                # list local + remote daemons

# end-to-end smoke
TOK=$(sunny token)
curl -s -H "Authorization: Bearer $TOK" localhost:7777/agents | jq
```

### How to verify the mesh (Phase 2a)

Spin a second daemon on another port and pair it:

```bash
# second daemon, isolated root
sunny start --addr 127.0.0.1:7778 --root /tmp/sunny-vps

# generate a code (on the "remote")
CODE=$(sunny pair offer --root /tmp/sunny-vps --addr 127.0.0.1:7778 \
       | grep "Pair code" | awk '{print $3}')

# claim it (from the "client")
sunny pair claim http://127.0.0.1:7778 $CODE --name vps
sunny doctor         # → Peers section shows vps reachable
```

Then open the TUI (`sunny`), `ctrl+a` → the picker shows agents from
both daemons prefixed with `local/…` or `vps/…`. Pressing enter on a
remote row spawns a session bound to that peer; the conversation
journal lives on the remote daemon.

### How to verify the tools

Configure ollama (or anthropic) so we have a non-claude-code
provider, create an agent on it, and ask the model to use a tool
explicitly:

```bash
sunny setup ollama                     # paste API key, save in secrets.yaml
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

### How to verify multi-viewer sync (v0.18)

```bash
sunny stop && sunny start             # MUST be the v0.18+ daemon
sunny tabs 2>/dev/null || \
  TOK=$(sunny token); curl -s -H "Authorization: Bearer $TOK" \
       localhost:7777/tabs | jq      # daemon-side tab list

# Open two TUIs in two terminals (same daemon, default :7777):
#   term1$ sunny
#   term2$ sunny
# They MUST show the same tabs (same conv per tab). If they don't:
#   - your daemon is still v0.17 (no /tabs route → curl above 404s)
#   - or each TUI started its own daemon (different --addr/--root)
# Sending a message in term1 → the matching tab in term2 should
# stream the same deltas live. ctrl+w in term1 closes the tab in
# term2 too (via the tab.closed bus event).
```

### Where the bodies are buried

- `internal/tabs/tabs.go` — daemon-side tab store backing
  `~/.sunny/tabs.json`. Single mutex; opens/closes are human-pace
  so contention is non-existent. Atomic rename on every mutation.
- `internal/server/tabs.go` — `GET/POST/DELETE/PATCH /tabs`. POST
  with no `conv_id` spawns a fresh conv as part of opening the tab
  so the TUI doesn't have to orchestrate two round-trips. Every
  mutation publishes `tab.opened/closed/updated` to events.Hub
  (carrying `tab_id`) for live reconciliation across viewers.
- `internal/conv/sink.go` — per-conversation pub/sub bus.
  `Sink.Append` assigns a monotonic seq, journals, and publishes
  in one call. Hubs are lazy per (slug, convID) and keep the seq
  counter primed from the on-disk journal so restarts don't shadow
  existing events.
- `internal/server/watch.go` — `GET /watch?since=N` SSE handler.
  Subscribes BEFORE replaying the journal (race-free); filters live
  events whose seq is already in the journal slice we sent.
- `internal/server/chat.go` — `postTurns` is now 202 fire-and-
  forget; `runTurn` runs in a goroutine and writes through the
  Sink (not the chat handler's response). Per-conv mutex via
  `activeTurnsRegistry` blocks a second turn with 409 and lets
  `DELETE /turn` cancel via the registered cancel func.
- `internal/session/session.go` — `Bind` starts a per-session
  watch goroutine that auto-reconnects with `since=lastSeq`.
  `Send` is fire-and-forget POST + optimistic local UserItem;
  `echoSkipSeq` dedupes the watch echo of our own user message.
  Transcript items are NOT persisted in state.json any more —
  Bind synchronously fetches the journal via GET /conversations/
  {id} when ConvID is set.
- `internal/state/state.go` — v8 schema, ULTRA slim:
  `{theme, peer_state: {peerName: {active_tab_id, drafts}}}`.
  Drafts are tab_id → unsent text (per-device on purpose). The
  daemon owns "what tabs exist".
- `internal/tui/model.go` — `Model.peerManagers` is the per-peer
  state map. `m.manager` always points at the active peer's
  manager. `switchToPeer` saves draft, swaps pointer, restores
  the new peer's draft. `Ctrl+1..9` → `switchToPeerByIdx(N-1)`.
  `peerSyncTick` (every 2s) reconciles the federation roster +
  refetches tabs for newly-joined peers.
- `internal/tui/model_appmsg.go` — `applyTabsRefresh` is the
  reconciliation core: any `tab.*` bus event triggers a `GET /tabs`
  on that peer; new tabs become bound sessions, dropped tabs are
  closed locally. Self-echoes are no-ops because the tab ID is
  already in the manager.
- `internal/engine/engine.go` — `runTurnLoop` is the round-trip
  driver. ToolUse events get added to `pendingCalls` ONLY when
  `advertised` is non-empty; for claude-code/opencode the events
  are informational because the provider runs the tools itself.
- `internal/tools/` — one file per tool. `path.go` is the
  cwd-bounded resolver (had a macOS `/tmp` symlink bug; the fix
  EvalSymlinks both sides).
- `internal/provider/{anthropic,ollama}/` — wire translation for
  providers where sunny advertises its own tools.
- `internal/provider/{claudecode,opencode}/` — subprocess wrappers
  for CLIs that bring their own toolset. opencode also writes a
  per-agent file at `~/.config/opencode/agent/sunny-<slug>.md` to
  carry the system prompt (opencode has no `--append-system-prompt`).
  claudecode runs each subprocess in its own process group
  (`process_unix.go` `Setpgid: true` + `cmd.Cancel` to kill the
  group on ctx cancel) so a Ctrl+C kills bash sub-shells too. A
  5-min idle watchdog (`runIdleWatchdog`) cancels the turn when
  claude goes silent — surfaces as `claudecode: unresponsive` so
  callers can distinguish from a user-initiated cancel.
- `internal/doctor/` — probes for `sunny doctor` and `sunny setup`,
  including `CheckPeers` which hits `GET /agents` against each remote
  to detect bad/expired tokens.
- `internal/peers/` — load/save `~/.sunny/peers.yaml`. The local
  daemon is always the implicit `name: local` entry and never appears
  in the file.
- `internal/pairing/` — in-memory short-code service powering the
  pair offer/claim dance. Codes are 6 chars from a no-ambiguity
  alphabet, single-use, 5min TTL. The daemon's bearer is shared
  as-is on claim (per-pairing tokens are a future improvement).
- `internal/tsnet/` — thin shell-out wrapper over the `tailscale`
  CLI. `Available()` is the cheap PATH check; `LocalIP()` and
  `Peers()` are the work calls. Sunny doesn't import libtailscale
  to keep the binary lean and the dep graph honest.
- `cmd/sunny/serve.go` `tailnetBind` — at boot, if tsnet is
  available and `tailscale ip -4` returns something, the daemon
  spawns an extra `srv.Serve(ln)` goroutine bound to that IP using
  the same port as `--addr`. Failures on the tailnet listener log
  warn and the primary bind keeps running.
- `internal/events/` — in-process pub/sub bus. Hub.Publish is non-
  blocking per subscriber (slow ones drop with a log line, never
  backpressure). Wired into agent CRUD + conversation CRUD +
  secrets PUT/DELETE + tabs CRUD + turn lifecycle so every
  mutation surfaces a one-line event. Event types:
  `agent.{created,updated,deleted}`,
  `conversation.{created,deleted,turn}`,
  `tab.{opened,closed,updated}`, `secrets.changed`,
  `turn.{started,done,cancelled}` (coarser than conversation.turn —
  one event per user→assistant exchange, marking the boundaries).
- `internal/server/events.go` — SSE handler at `GET /events` with
  30s heartbeat. Auth required.
- `internal/server/stats.go` — `GET /stats` snapshot of daemon
  counts + in-flight turns + watcher counts + host CPU/RAM.
  Reads `activeTurnsRegistry.snapshot()`, `Sink.LiveStats()`,
  `Hub.SubCount()`, `conversation.Store.Count(slug)`,
  `sysstats.Sample()`. No caching — each call recomputes; CPU
  sample dominates at ~1s wall.
- `internal/sysstats/` — macOS-only host CPU + RAM via `top`,
  `vm_stat`, `sysctl hw.memsize`. Returns zeros (with `NumCPU`
  populated) on other platforms so callers don't special-case.
- `internal/client/events.go` + `federation.go` — `Client.Subscribe
  Events(ctx)` parses one SSE stream into BusEvents; `Federation.
  SubscribeAll(ctx)` multiplexes across every peer with auto-
  reconnect every 2s on failure.
- `internal/tui/model.go` `waitForBusEvent` — bubbletea cmd that
  reads one FederatedEvent off the multiplexer and surfaces it as
  busEventMsg. The handler in model_appmsg.go re-emits AgentChanged
  Msg so any open AgentPickerDialog refreshes itself.
- `internal/mesh/` — shared `mesh.key` (32 random bytes, base64url).
  Each daemon auto-generates one at first boot. `Fingerprint()` is
  the public 8-hex prefix used by /sunny/identity. Equal compares
  in constant time.
- `internal/server/mesh.go` — TWO middlewares: `TailnetIdentity
  Auth` (zero-config: same tailscale UserID → trust without
  headers) wraps `MeshAuth` (opt-in: shared mesh.key in
  X-Sunny-Mesh → trust). Both mark via isMeshAuthed which
  requireBearer downstream honours. TailnetCache caches the full
  tsnet.Status (IPs + identity) with 5-min TTL.
- `cmd/sunny/tui.go` `discoverTailnetPeers` — at boot, if
  tailscale is up, walks the tailnet, GETs /sunny/identity in
  parallel, and `Federation.AddTailnetPeer`s any daemon whose
  UserID matches Self (no creds) OR `AddMeshPeer` if our mesh.key
  matches their fingerprint. Discovered peers don't touch
  peers.yaml — they're ephemeral per TUI session.
- `internal/client/federation.go` — wraps N `*Client` keyed by peer
  name. `ListAgents` fan-outs in parallel; per-peer failures don't
  fail the whole call. The TUI's `Model.fed` always exists (single-
  peer when there's no peers.yaml) so all chat paths route through
  `fed.For(host)`.
- `cmd/sunny/serve.go` `buildEngine` — auto-detection chain. If a
  provider doesn't show up, this is where to log-trace.
- `cmd/sunny/tui.go` `openTUI` — state restore + bootstrap session +
  loads peers.yaml and constructs the Federation passed to the model.
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
internal/prompt     — System-prompt assembly: runtime context, persona
                      (prompt.md or prompt/<NN>-name.md), skills/knowledge
                      catalog, and the dynamic env block (date in CDMX,
                      hostname, IPs, daemon addr) appended after the
                      cache breakpoint.
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

- Agents live at `~/.sunny/agents/<id>/`. Each owns its skills,
  knowledge, conversations, and persona.
- **Identity model (v0.19+):** every agent has an opaque, immutable
  `id` (shape `agt_<unix_ms>_<8hex>`, generated server-side on
  create) plus a mutable display `name` in `agent.yaml`. The id
  doubles as the directory name on disk and is the only handle on
  the wire (URLs, JSON, journal references). Renaming an agent
  patches `name` only — no files move, no journal references break.
  Hand-authored agent.yaml files may use any string matching
  `[a-z0-9][a-z0-9_-]*` as their id (the seeded default uses
  `id: sunny`).
- CRUD over HTTP:
  - `GET /agents` — list summaries (returns `id` + `name` + …)
  - `POST /agents` — create (`{name, description, model, prompt, …}`).
    Server mints the id; clients never supply one.
  - `GET /agents/{id}` — full detail (includes `prompt`)
  - `PATCH /agents/{id}` — partial update; rename is just a name patch
  - `DELETE /agents/{id}` — moves dir to `~/.sunny/.archive/`,
    idempotent
- TUI: `ctrl+a` opens the agent picker. Enter spawns a new session
  bound to the chosen agent; `n` opens the create form, `e` edits,
  `r` renames (name patch only), `a/d` archives. Each TUI session
  is bound to one agent for its lifetime — switching means a new
  session/tab.
- The in-memory `store.Store` is mutated atomically with the
  filesystem on every CRUD op. No fsnotify (yet).

## Conversation persistence

Every chat lives under its agent:

```
~/.sunny/agents/<agent_id>/conversations/<conv_id>/
  meta.json     — title, timestamps, msg_count, model, provider_state
                  (carries `agent_id`, not slug)
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
