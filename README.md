<div align="center">

# sunny

**Self-hosted personal agent. One binary, your data, your rules.**

[![License: MIT](https://img.shields.io/badge/license-MIT-black)](./LICENSE)
[![Version](https://img.shields.io/github/v/release/noesrafa/sunny?label=release&color=black)](https://github.com/noesrafa/sunny/releases)
[![Platform](https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux-black)](#install)
[![Status](https://img.shields.io/badge/status-active-success)](#)

</div>

> Sunny is a Go daemon you run on every machine you care about — Mac,
> VPS, Raspberry Pi. Each daemon owns its conversations, secrets,
> agents, and skills as plain files on disk. The TUI (and the
> upcoming mobile app) talk to one or many daemons over HTTP. When
> several daemons share a Tailscale account they auto-discover each
> other, and any client connected to a daemon sees the same chats
> in real time.

---

## Contents

- [Why sunny](#why-sunny)
- [Install](#install)
- [Setup on a new machine](#setup-on-a-new-machine) — the full from-zero walkthrough
  - [1. Daemon up](#1-get-the-daemon-up)
  - [2. Pick at least one AI backend](#2-pick-at-least-one-ai-backend)
  - [3. Optional: Tailscale for multi-machine](#3-optional-tailscale-for-multi-machine)
- [Daily use](#daily-use)
- [Configuration](#configuration)
- [HTTP API](#http-api) — integrate the daemon from your own clients
- [Architecture](#architecture)
- [Update / uninstall](#update--uninstall)

---

## Why sunny

Three principles, in order:

1. **Data sovereignty.** Conversations, memory, and secrets live on your
   host. Never in someone else's cloud.
2. **Radical distribution.** One binary. `brew install` and go.
3. **Ecosystem-compatible.** Filesystem layout follows Claude Code
   conventions (`SKILL.md`, agent folders). Skills move freely between
   sunny and any Claude-family client.

Inspirations: **Plex** (self-hosted server, multi-client UI), **Ollama**
(daemon + thin clients), **Claude Code** (filesystem as source of truth).

---

## Install

### Homebrew (macOS · Linux)

```bash
brew tap noesrafa/tap
brew install sunny
```

Ships binaries for **macOS Apple Silicon** and **Linux x86_64**.

### Curl (Linux servers without brew)

```bash
VERSION=$(curl -s https://api.github.com/repos/noesrafa/sunny/releases/latest \
  | grep tag_name | cut -d'"' -f4)
curl -fsSL "https://github.com/noesrafa/sunny/releases/download/${VERSION}/sunny_${VERSION#v}_linux_amd64.tar.gz" \
  | sudo tar xz -C /usr/local/bin sunny
sudo chmod +x /usr/local/bin/sunny
```

### From source

```bash
git clone https://github.com/noesrafa/sunny
cd sunny
go build -o bin/sunny ./cmd/sunny
```

Requires Go 1.26+.

---

## Setup on a new machine

The full path from zero to a working sunny — daemon, AI backend,
optional multi-machine mesh. Everything is automated by `sunny setup`,
but here's what's happening underneath so you know why each piece is
there.

### 1. Get the daemon up

```bash
sunny start              # detached daemon on 127.0.0.1:7777
sunny status             # pid, addr, uptime, /healthz
sunny doctor             # one-screen checklist of providers + runtime
```

On the very first run, `sunny start` seeds `~/.sunny/` from defaults
embedded in the binary, writes `~/.sunny/token` (mode 0600), and
generates a mesh key for the federation. After that the directory is
yours; sunny never overwrites your edits.

> The daemon survives the TUI exiting — by design. Closing your
> terminal does not stop sunny. Only `sunny stop` (or a hard kill)
> takes it down.

### 2. Pick at least one AI backend

Sunny doesn't ship with a model. It ships with **four drivers** that
talk to whatever you've already configured. Pick one (or several) based
on what you want:

| Backend | What you give up | What you get | Setup |
|---|---|---|---|
| **claude-code** | Anthropic-only models | Easiest path: uses your existing `claude.ai` login. Brings the full Claude Code toolset (Read, Glob, Grep, Bash, …) for free. **Recommended starting point.** | `sunny setup claude-code` |
| **opencode** | Subprocess overhead | Access to opencode's 75+ providers (Anthropic, OpenAI, OpenRouter, Groq, OpenCode-Zen, …) with one auth flow. Inherits opencode's full toolset. | `sunny setup opencode` |
| **anthropic** | Bring your own key | Direct SDK calls with cache-control breakpoints. No CLI dependency, predictable wire shape, programmatic control. | `sunny setup anthropic` |
| **ollama** | Bring your own key | Cheap inference at scale on Ollama Cloud. Big models (gpt-oss-120B, deepseek-r1, qwen3-thinking) with thinking deltas. | `sunny setup ollama` |

You can install several and route per-agent — a `provider:` line in any
`agent.yaml` pins that agent to one backend. Without it, sunny falls
back through `claude-code → anthropic → ollama → opencode`.

#### claude-code (recommended)

> **Why:** zero-API-key path. Reuses the auth you already have on
> claude.ai. Brings every native Claude Code tool with it.

```bash
sunny setup claude-code
# under the hood:
#   brew install --cask claude-code   (macOS)
#   curl -fsSL https://claude.ai/install.sh | bash   (Linux)
# then:
claude login
sunny doctor   # → claude-code ✓
```

#### opencode

> **Why:** one CLI, dozens of model providers. If you want to
> A/B-test gpt-5 vs claude-opus vs deepseek-r1 from a single sunny
> install, opencode is the pragmatic shortcut.

```bash
sunny setup opencode
# under the hood:
#   brew install sst/tap/opencode    (macOS)
#   curl -fsSL https://opencode.ai/install | bash   (Linux)
# then:
opencode auth login
sunny doctor   # → opencode ✓
```

#### anthropic (direct API)

> **Why:** when you want predictable streaming, prompt cache control,
> or you don't want a separate CLI in the picture.

```bash
# Get a key: https://console.anthropic.com/settings/keys
sunny setup anthropic
# Or paste manually:
sunny secrets anthropic set api_key   # reads value from stdin
```

#### ollama (Ollama Cloud)

> **Why:** big models, thinking deltas, fixed-rate pricing. Useful for
> heavy reasoning runs you don't want to bill through Claude.

```bash
# Get a key: https://ollama.com/settings/keys
sunny setup ollama
# Or paste manually:
sunny secrets ollama set api_key
```

Self-hosted local Ollama works too — set `base_url` to your local
endpoint:

```bash
sunny secrets ollama set base_url   # e.g. http://localhost:11434
```

#### Verify

```bash
sunny doctor
# Providers
#   ✓ claude-code   v2.1.x on PATH
#   ✓ opencode      v1.14.x on PATH
#   ✓ anthropic     api_key configured
#   ✓ ollama        api_key configured
# ...
```

### 3. Optional: Tailscale for multi-machine

> **Why:** sunny is built around the idea of running daemons on every
> machine you own — laptop, VPS, home server — and having one TUI that
> drives all of them. Tailscale gives those machines a private mesh
> network without exposing anything to the public internet, and lets
> sunny use **same-tailscale-account = trusted-peer** for zero-config
> auth. No keys to share, no port forwarding, no TLS to set up.

#### macOS — install the CLI

The macOS Tailscale app puts its binary inside the app bundle. Easiest
path is the open-source CLI from brew:

```bash
brew install tailscale
sudo tailscaled install-system-daemon
sudo tailscale up                       # opens browser for login
tailscale status                        # should list every node on your tailnet
```

#### Linux (VPS, server)

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up                       # printable login URL
tailscale status
```

#### Activate the mesh

Once Tailscale is up on **every** machine, just:

```bash
sunny restart    # so the daemon binds to its tailnet IP, not just 127.0.0.1
```

Open `sunny` (TUI) on any of them — the header sidebar shows every other
sunny daemon in your tailscale account. `Ctrl+1..9` jumps between them.
No pair codes, no shared keys, no `peers.yaml` editing.

> **Sub-mesh override (advanced).** If you share your tailnet with
> other people but only want a private sub-mesh between some nodes:
> `sunny mesh export` on one, `sunny mesh import` on the others,
> `sunny restart`. Daemons with a matching key trust each other
> regardless of tailscale identity.

#### Off-tailnet hosts

For machines not on Tailscale, the manual pair flow still works:

```bash
# remote
sunny pair offer
→ Pair code: A4F7K2 (valid 5 min)

# local
sunny pair claim http://203.0.113.5:7777 A4F7K2
sunny doctor
```

---

## Daily use

```bash
sunny             # auto-start the daemon if needed, open the TUI
sunny update      # brew upgrade (or curl fallback) + restart the daemon
sunny restart     # stop + start in one shot
sunny doctor      # quick health check
```

### TUI shortcuts

The sidebar shows the three you'll use every minute. `Ctrl+/` opens
the full cheat sheet:

```
chat
  enter                send
  ctrl+j / alt+enter   newline
  ctrl+c               cancel current turn
  ctrl+l               reset chat (new conversation in same tab)

sessions
  ctrl+n               new chat
  ctrl+a               agents (create / edit / archive)
  ctrl+r               rename current tab
  ctrl+w               close current tab
  tab / shift+tab      next / prev tab
  ctrl+k               tab picker

peers (multi-machine)
  ctrl+1..9            jump to peer N (local, vps, …)

app
  ctrl+y               secrets / API keys
  ctrl+s               settings (theme)
  ctrl+d               git diff viewer
  ctrl+/               this dialog
  esc                  quit (default: cancel)

scroll
  pgup / pgdn          scroll chat
  home / end           top / bottom
```

### CLI

```bash
sunny start | stop | status | restart       # daemon lifecycle
sunny update                                  # in-place upgrade
sunny doctor                                  # health checklist
sunny setup [<provider>]                      # interactive provider setup
sunny token | sunny token rotate              # bearer credential
sunny secrets [<provider> [set|delete <field>]]
sunny peers [add|scan]                        # federation roster
sunny pair offer | pair claim <url> <code>    # cross-host pairing
sunny mesh export | import                    # shared mesh.key
```

---

## Configuration

### Filesystem layout

Everything sunny knows lives under `~/.sunny/`. Plain files —
editable, versionable, backupable, shareable.

```
~/.sunny/
├── token                      # bearer token (mode 0600)
├── secrets.yaml               # provider keys (mode 0600)
├── peers.yaml                 # remote daemons (mode 0600, optional)
├── tabs.json                  # daemon-side open tabs (multi-viewer sync)
├── mesh.key                   # optional shared key (mode 0600)
├── state.json                 # per-TUI prefs (theme, drafts)
├── run/                       # pid, log (managed by sunny)
├── agents/<slug>/
│   ├── agent.yaml             # name, description, model, effort, provider
│   ├── prompt.md              # system prompt
│   ├── knowledge/             # any *.md, walked recursively
│   ├── skills/<name>/SKILL.md # claude-code-compatible skill
│   └── conversations/<id>/
│       ├── meta.json          # title, timestamps, msg_count, model
│       └── events.jsonl       # append-only turn journal (chat is rebuilt from this)
└── .archive/                  # archived agents/convs (mv'd here, not deleted)
```

### `agent.yaml`

```yaml
name: Code Reviewer
description: Reviews PRs against our style guide
model: claude-opus-4-7        # or gemma4:31b, gpt-oss:120b, opencode/gpt-5-nano, etc.
effort: max                   # low | medium | high | xhigh | max
provider: anthropic           # anthropic | claude-code | ollama | opencode (optional)
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

> Sunny frames skills as **pre-loaded behavioural guidelines**, not as
> tools. The model reads them inline in the system prompt and applies
> them when relevant — there's no `Skill()` invocation. The filesystem
> layout stays Claude-Code-compatible so skills move freely between
> sunny and `claude` itself.

---

## HTTP API

The daemon's HTTP surface is fully usable from any client (TUI, mobile,
your own scripts). Auth: every endpoint except `/healthz` requires
`Authorization: Bearer $(sunny token)`.

### Chat — fire-and-forget + watch

Sunny v0.18+ separated **sending** from **receiving**: a turn is one
POST that returns 202 immediately, and any number of clients watch
the same conversation over a single SSE stream. Two TUIs viewing the
same chat see identical deltas in real time.

| Method + path | Purpose |
|---|---|
| `POST /agents/{slug}/conversations/{id}/turns` | Enqueue a turn (202 + `{conv_id, user_seq}`) |
| `GET  /agents/{slug}/conversations/{id}/watch?since=N` | SSE: every journal event with `seq > N` |
| `DELETE /agents/{slug}/conversations/{id}/turn` | Cancel the in-flight turn |

Each event on `/watch` is a single SSE frame:

```
data: {"seq":42,"kind":"text_delta","at":"2026-05-04T12:00:00Z","payload":{"text":"hello"}}
```

`kind` is one of `user`, `text_delta`, `thinking_delta`, `tool_use`,
`tool_result`, `done`, `error`, `cancelled`. Reconnect with
`?since=<lastSeq>` and you resume without gaps or duplicates.

### Tabs — server-side, multi-viewer-synced

Tabs (open chats) live on the daemon, not in each client. Clients
fetch the list at boot and reconcile from `tab.*` bus events.

| Method + path | Purpose |
|---|---|
| `GET    /tabs` | List every open tab |
| `POST   /tabs` | Open one (`{agent_slug, conv_id?, cwd?, title?}`) — daemon creates the conv if `conv_id` is omitted |
| `DELETE /tabs/{id}` | Close (the underlying conv stays in the journal) |
| `PATCH  /tabs/{id}` | Update title / cwd |

### Bus — one stream of metadata events

```
GET /events   →   SSE: agent.*, conversation.*, tab.*, secrets.*
```

Drives picker refreshes, sidebar reconciliation, multi-client tab sync.

### Agents · conversations · secrets · pairing · identity

| Method + path | Purpose |
|---|---|
| `GET /healthz` | Liveness (no auth) |
| `GET /sunny/identity` | `{app, version, mesh_fingerprint}` for federation handshakes |
| `GET    /agents` | List agent summaries |
| `POST   /agents` | Create (`{slug, name, model, ...}`) |
| `GET    /agents/{slug}` | Full detail incl. prompt + skills + knowledge |
| `PATCH  /agents/{slug}` | Partial update |
| `DELETE /agents/{slug}` | Archive |
| `GET /agents/{slug}/skills/{name}` | Frontmatter + body |
| `GET /agents/{slug}/knowledge/{file...}` | Raw markdown (path-traversal guarded) |
| `GET    /agents/{slug}/conversations` | Newest first |
| `POST   /agents/{slug}/conversations` | Create empty |
| `GET    /agents/{slug}/conversations/{id}` | Meta + journal replay |
| `DELETE /agents/{slug}/conversations/{id}` | Archive |
| `GET    /secrets` | Configured providers + field names (never values) |
| `PUT    /secrets/{provider}` | Replace fields |
| `DELETE /secrets/{provider}` | Remove |
| `POST /pairing/offer` | Generate a one-shot pair code (5-min TTL) |
| `POST /pairing/claim` | Exchange a code for a bearer token |

### Quick smoke test

```bash
TOK=$(sunny token)
CONV=$(curl -s -X POST -H "Authorization: Bearer $TOK" \
       -H "Content-Type: application/json" \
       -d '{"agent_slug":"sunny"}' \
       localhost:7777/tabs | jq -r .conv_id)

# subscribe (in another terminal)
curl -N -H "Authorization: Bearer $TOK" \
  "localhost:7777/agents/sunny/conversations/$CONV/watch?since=0"

# send
curl -X POST -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"hola"}]}' \
  "localhost:7777/agents/sunny/conversations/$CONV/turns"
```

---

## Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│                              sunny binary                          │
│                                                                    │
│  ┌──────────────────┐                  ┌─────────────────────────┐ │
│  │      ENGINE      │      HTTP        │         CLIENTS         │ │
│  │     (daemon)     │  ◄────────────►  │   TUI · mobile app      │ │
│  │                  │                  │   any HTTP client       │ │
│  │   sunny start    │                  │                         │ │
│  └──────────────────┘                  └─────────────────────────┘ │
└────────────────────────────────────────────────────────────────────┘
              ▲                                       ▲
              │                                       │
       ~/.sunny/                            multiple clients
       (your data)                          on the same daemon =
                                             real-time sync via
                                             /watch + bus events

  Multi-machine: each machine runs its own daemon. Tailscale-trusted
  daemons auto-discover each other; the TUI federates the roster
  and Ctrl+1..9 jumps between them.
```

The daemon is the **single owner** of state — every client view is
derived from the journal on disk plus a per-conversation pub/sub. Two
clients connected to the same daemon see the same chats, the same
tabs, the same deltas, with no replication logic in the clients
themselves.

Repo map:

```
sunny/
├── cmd/sunny/                 # CLI entrypoint, per-command files
├── internal/
│   ├── server/                # HTTP API
│   ├── engine/                # turn loop, tool round-trip, system prompt
│   ├── conv/                  # per-conversation pub/sub bus (sink + watch)
│   ├── conversation/          # journal persistence
│   ├── tabs/                  # daemon-side open-tab list (~/.sunny/tabs.json)
│   ├── provider/              # anthropic · claudecode · ollama · opencode
│   ├── tools/                 # view, ls, grep, glob (read-only)
│   ├── client/                # daemon HTTP client + Federation fan-out
│   ├── tui/                   # Bubble Tea client
│   ├── tsnet/                 # tailscale CLI shell-out
│   ├── mesh/                  # ~/.sunny/mesh.key (sub-mesh override)
│   ├── pairing/               # one-shot pair codes
│   ├── peers/                 # ~/.sunny/peers.yaml roster
│   ├── doctor/                # health probes
│   ├── secrets/               # ~/.sunny/secrets.yaml
│   └── state/                 # per-TUI prefs (theme + drafts)
└── defaults/                  # baked into the binary via go:embed
```

---

## Update / uninstall

```bash
sunny update                     # detects install method (brew or release tarball)
sunny update --no-restart        # skip the daemon restart at the end

# manual
brew upgrade sunny               # macOS / Linuxbrew
sudo sunny update                # if the binary lives where you need root to overwrite

# uninstall
brew uninstall sunny
brew untap noesrafa/tap          # optional
rm -rf ~/.sunny                  # optional: also wipe runtime data
```

---

## License

MIT — see [`LICENSE`](./LICENSE).
