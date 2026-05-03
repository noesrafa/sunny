# Sunny

Self-hosted personal agent. One binary, your data, your rules.

> **Status:** v0.17.0 — chat works end-to-end across four backends.
> Zero-config mesh: install sunny on every Tailscale node you own,
> open `sunny`, and your TUI auto-discovers every other sunny
> daemon in the same tailscale account. No keys, no codes, no
> config files. Conversations stay on the engine they were created
> on; events sync in real time.

## Install

### macOS / Linux via Homebrew

```bash
brew tap noesrafa/tap
brew install sunny
```

Currently ships binaries for **macOS Apple Silicon** and **Linux x86_64**.

### From source

```bash
git clone https://github.com/noesrafa/sunny
cd sunny
go build -o bin/sunny ./cmd/sunny
./bin/sunny version
```

Requires Go 1.26+.

## Quick start

```bash
sunny doctor         # checklist: providers, daemon, runtime, peers
sunny setup          # interactive walk-through of whatever isn't ready
sunny                # auto-starts daemon if needed; opens TUI
```

`sunny setup <provider>` targets one directly:

```bash
sunny setup claude-code   # installs `claude` CLI, then prompts you to log in
sunny setup opencode      # installs `opencode` CLI, then prompts you to log in
sunny setup anthropic     # paste your API key, saved in ~/.sunny/secrets.yaml
sunny setup ollama        # same, for Ollama Cloud
```

Pass `--print-only` to any of those to see the commands without
running them — handy over SSH or in CI.

## Multi-machine mesh

One TUI can drive several sunny daemons. The local daemon is always
implicit; remote daemons live in `~/.sunny/peers.yaml`. The recommended
way to add one is the pairing dance:

```bash
# on the remote host (VPS, raspberry pi, …)
sunny pair offer
→ Pair code: A4F7K2 (valid 5 min)

# on your laptop
sunny pair claim http://100.64.0.5:7777 A4F7K2
→ ✓ paired 100-64-0-5 → http://100.64.0.5:7777
sunny doctor          # → Peers section shows the new peer reachable
sunny                 # → ctrl+a shows local/* and 100-64-0-5/* agents in one list
```

Codes are 6 alphanumerics, single-use, and expire after 5 minutes.
Pass `--name vps` to `pair claim` if you want a friendlier label
than the auto-derived host slug.

If you'd rather wire it up by hand (you already have the token from
`sunny token`):

```bash
echo "<token>" | sunny peers add vps http://100.64.0.5:7777
```

Conversations stay on the engine that owns the agent — sunny does
not replicate data across hosts.

### Tailscale

When Tailscale is installed and logged in on the host running the
daemon, `sunny start` auto-binds an extra listener to the tailnet
IPv4 (alongside 127.0.0.1) so peers on the tailnet can reach it
without exposing the daemon to the LAN or public internet.

### Zero-config mesh (recommended)

If your sunny instances live on the same Tailscale account, you
literally don't need to do anything:

```bash
# on every machine you own
brew install sunny
sunny start
```

That's it. Open `sunny` on any of them and the TUI auto-discovers
the others — same tailscale account = same owner = trusted.

Under the hood: the daemon's middleware accepts requests from
tailnet IPs whose `tailscale whois` reports the same UserID as
this node. The TUI's discovery flow filters peers the same way.
No shared keys, no pair codes.

### Sub-mesh override (advanced)

If you share your tailnet with other people but want a private
sub-mesh between only some nodes (e.g. just your two VPS, not
your laptop), use the optional `mesh.key`:

```bash
# on your laptop
sunny mesh export
→ <base64 mesh key>

# on every node you want in the sub-mesh
echo "<key>" | sunny mesh import
sunny stop && sunny start
```

Daemons with the same key trust each other regardless of
tailscale identity. Useful for shared-tailnet setups; ignored
when not configured.

Discover what's out there manually:

```bash
sunny peers scan
→ Candidates (run sunny pair on each side to add):
    · vps-1                http://100.64.0.10:7777  [linux]
  Already paired:
    ✓ pi                   http://100.64.0.20:7777  (as "pi")
```

Or daemon-only:

```bash
sunny start          # daemon detached on 127.0.0.1:7777
sunny status         # pid, addr, uptime, healthz
sunny stop           # graceful shutdown
```

The daemon survives the TUI exiting — by design. Closing your
terminal does not stop sunny. The only ways to stop it are
`sunny stop` and a hard kill of the pid.

On the very first run, `sunny start` seeds `~/.sunny/` from defaults
baked into the binary and writes `~/.sunny/token` (mode 0600). After
that the directory is yours; sunny never overwrites your edits.

## Providers

Four drivers ship in the binary; the daemon auto-detects which are
available at boot:

| Provider | Source of credentials | Notes |
|---|---|---|
| **claude-code** | The `claude` CLI's existing login | Brings all of Claude Code's native tools (Read, Glob, Grep, Bash, …). No api_key needed. |
| **anthropic** | `secrets.anthropic.api_key` or `ANTHROPIC_API_KEY` env var | SDK streaming with cache-control breakpoints. |
| **ollama** | `secrets.ollama.api_key` (+ optional `base_url`) | Ollama Cloud `/api/chat`. `keep_alive: 10m` default. Streams thinking deltas for reasoning models. |
| **opencode** | The `opencode` CLI's own auth (`opencode auth login`) | Subprocess wrapper. Inherits opencode's full toolset and 75+ provider catalog (anthropic, ollama-cloud, opencode-zen/go, etc.) without sunny needing to know about each one. |

Per-agent override: drop a `provider:` field in any `agent.yaml` and
that agent always uses that backend regardless of the daemon
default. The order claude-code → anthropic → ollama → opencode
drives the fallback when the agent doesn't pin one.

```bash
sunny secrets                          # list configured providers (no values)
sunny secrets ollama set api_key       # reads value from stdin
sunny secrets anthropic delete         # remove a provider's section
```

Values are stored in `~/.sunny/secrets.yaml` (mode 0600). The daemon
never returns a secret value over HTTP — only the list of configured
field names.

## TUI shortcuts

```
ctrl+n   new session (pick agent + cwd)
ctrl+a   agents (create / edit / archive)
ctrl+y   secrets (paste API keys)
ctrl+r   rename current session
ctrl+l   reset chat (new conversation, same tab)
ctrl+d   git diff viewer
ctrl+k   tab switcher
ctrl+s   settings (theme)
tab      next session
ctrl+w   close session tab
ctrl+c   cancel turn in flight (does not touch input)
ctrl+q   quit (with confirmation)
```

## Idea

A Go daemon you install with `brew install sunny` and run anywhere —
Mac, VPS, Raspberry Pi. Every agent, skill, conversation, and piece
of knowledge lives as plain files you can edit, version, share, and
back up.

Three principles:

- **Data sovereignty.** Conversations, memory, secrets live on your
  host. Never in someone else's cloud.
- **Radical distribution.** One binary. `brew install` and go.
- **Ecosystem-compatible.** Filesystem layout follows Claude Code
  conventions (`SKILL.md`, agent folders). Skills move freely
  between sunny and any Claude-family client.

Inspirations: **Plex** (self-hosted server, multi-client UI),
**Ollama** (daemon + thin clients), **Claude Code** (filesystem as
source of truth for skills/agents).

## Repo layout

```
sunny/
├── cmd/sunny/                   # CLI entrypoint, dispatch + per-command files
├── internal/
│   ├── auth/                    # bearer token (~/.sunny/token, mode 0600)
│   ├── secrets/                 # ~/.sunny/secrets.yaml (mode 0600)
│   ├── agent/                   # agent.yaml loader + validation
│   ├── skill/                   # SKILL.md frontmatter parser
│   ├── store/                   # walks ~/.sunny, in-memory index, CRUD
│   ├── conversation/            # per-agent conversation persistence
│   ├── bootstrap/               # seeds ~/.sunny from embedded defaults
│   ├── lifecycle/               # pid/state/log files for start/stop/status
│   ├── engine/                  # turn loop, tool round-trip, system prompt
│   ├── provider/                # abstraction + drivers
│   │   ├── anthropic/           # SDK streaming
│   │   ├── claudecode/          # subprocess wrapper
│   │   ├── ollama/              # Ollama Cloud /api/chat
│   │   └── opencode/            # subprocess wrapper for `opencode run`
│   ├── tools/                   # view, ls, grep, glob (read-only)
│   ├── doctor/                  # probes: provider binaries, keys, daemon, runtime, peers
│   ├── peers/                   # ~/.sunny/peers.yaml: federation roster
│   ├── server/                  # HTTP API: chat, agents, conversations, secrets
│   ├── client/                  # daemon HTTP client + Federation (fan-out across peers)
│   ├── session/                 # in-process session state for TUI tabs
│   ├── state/                   # ~/.sunny/state.json (TUI layout cache)
│   └── tui/                     # Bubble Tea client
└── defaults/                    # baked into the binary via go:embed
    └── agents/sunny/            # default agent
```

## Runtime layout (`~/.sunny/`)

```
~/.sunny/
├── token                            # bearer token (mode 0600)
├── secrets.yaml                     # provider keys (mode 0600)
├── peers.yaml                       # remote daemons for the TUI (mode 0600, optional)
├── state.json                       # TUI layout cache
├── run/                             # pid, log (managed by sunny)
├── agents/
│   └── <slug>/
│       ├── agent.yaml               # name, description, model, effort, provider
│       ├── prompt.md                # system prompt
│       ├── knowledge/               # any *.md, walked recursively
│       ├── skills/<name>/SKILL.md   # claude-code-compatible
│       └── conversations/
│           └── <conv_id>/
│               ├── meta.json        # title, timestamps, msg_count, model, provider_state
│               └── events.jsonl     # append-only turn journal
└── .archive/                        # archived agents/convs (mv'd here, not deleted)
```

### `agent.yaml`

```yaml
name: My Agent
description: optional one-liner
model: claude-opus-4-7        # or gemma4:31b, gpt-oss:120b, opencode/gpt-5-nano, etc.
effort: max                   # low|medium|high|xhigh|max
provider: anthropic           # anthropic|claude-code|ollama|opencode (optional)
```

### `SKILL.md` (Claude Code convention)

```markdown
---
name: greet
description: Greet the user warmly and ask how their day is going.
---

# Greet

When the conversation opens or the user says hello, respond with a brief
warm greeting and ask how their day is going.
```

A skill is a folder. `SKILL.md` is the contract; anything else in the
folder is a resource the skill can use.

> Sunny frames skills as **pre-loaded behavioural guidelines**, not
> as tools. The model reads them inline in the system prompt and
> applies them when relevant — there's no `Skill()` invocation. The
> filesystem layout stays Claude-Code-compatible so skills move
> freely between sunny and `claude` itself.

## HTTP API

Auth: every endpoint except `/healthz` requires
`Authorization: Bearer $(sunny token)`.

| Method + path | Purpose |
|---|---|
| `GET /healthz` | Liveness probe (no auth) |
| `GET /agents` | List agent summaries |
| `POST /agents` | Create agent (slug, name, model, …) |
| `GET /agents/{slug}` | Full agent detail incl. prompt + skills + knowledge |
| `PATCH /agents/{slug}` | Partial update; nil fields untouched |
| `DELETE /agents/{slug}` | Move to `~/.sunny/.archive/` (idempotent) |
| `GET /agents/{slug}/skills/{name}` | Skill frontmatter + body |
| `GET /agents/{slug}/knowledge/{file...}` | Raw markdown (path-traversal guarded) |
| `GET /agents/{slug}/conversations` | List conversations newest-first |
| `POST /agents/{slug}/conversations` | Create empty conversation |
| `GET /agents/{slug}/conversations/{id}` | Meta + events journal |
| `DELETE /agents/{slug}/conversations/{id}` | Archive |
| `POST /agents/{slug}/conversations/{id}/turn` | SSE stream of one assistant turn |
| `GET /secrets` | List configured providers + field names (no values) |
| `PUT /secrets/{provider}` | Replace fields (`{api_key, base_url, …}`) |
| `DELETE /secrets/{provider}` | Remove provider section |

```bash
TOK=$(sunny token)
curl -s -H "Authorization: Bearer $TOK" localhost:7777/agents | jq
```

## Update / uninstall

```bash
brew upgrade sunny
brew uninstall sunny
brew untap noesrafa/tap         # optional: remove the tap entirely
rm -rf ~/.sunny                  # optional: also wipe your runtime data
```

## License

MIT — see [`LICENSE`](./LICENSE).
