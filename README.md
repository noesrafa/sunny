# Sunny

Self-hosted personal agent. One binary, your data, your rules.

> **Status:** v0.0.2 — substrate only. The daemon loads the filesystem, exposes a
> read-only HTTP API, and provides a minimal TUI client. No engine, no provider,
> no chat loop yet — those land in v0.1.

## Install

### macOS / Linux via Homebrew

```bash
brew tap noesrafa/tap
brew install sunny
```

Currently ships binaries for **macOS Apple Silicon** and **Linux x86_64**. Other
targets can be added by editing `.goreleaser.yaml`.

### Linux via tarball (no brew)

```bash
curl -L https://github.com/noesrafa/sunny/releases/latest/download/sunny_0.0.2_linux_amd64.tar.gz | tar xz
sudo mv sunny /usr/local/bin/
```

(Replace `0.0.2` with the latest tag from [releases](https://github.com/noesrafa/sunny/releases).)

### From source

```bash
git clone https://github.com/noesrafa/sunny
cd sunny
go build -o bin/sunny ./cmd/sunny
./bin/sunny version
```

Requires Go 1.26+.

## Update

```bash
brew upgrade sunny
```

Or, if installed from tarball, redo the `curl … | tar xz` step above with the
new version.

## Uninstall

```bash
brew uninstall sunny
brew untap noesrafa/tap         # optional: remove the tap entirely
rm -rf ~/.sunny                  # optional: also wipe your runtime data
```

## Quick start

```bash
sunny start    # daemon detached, listens on 127.0.0.1:7777
sunny status   # pid, addr, uptime, healthz
sunny          # open the TUI (alias: sunny tui)
sunny stop     # graceful shutdown
```

On the very first run, `sunny start` seeds `~/.sunny/` from the defaults baked
into the binary. After that the directory is yours — Sunny never overwrites it.

## Idea

A Go daemon you install with `brew install sunny` and run anywhere — Mac, VPS,
Raspberry Pi. Every agent, skill, and piece of knowledge lives as plain files
you can edit, version, share, and back up.

Three principles:

- **Data sovereignty.** Conversations, memory, skills live on your host. Never
  in someone else's cloud.
- **Radical distribution.** One binary. `brew install` and go. Your sibling
  installs it on a $35 Pi and has their own agent without a monthly bill.
- **Ecosystem-compatible.** Filesystem layout follows Claude Code conventions
  (`SKILL.md`, agent folders). Skills move freely between Sunny and any
  Claude-family client.

Inspirations: **Plex** (self-hosted server, multi-client UI), **Ollama**
(daemon + thin clients), **Claude Code** (filesystem as source of truth for
skills/agents).

## Repo layout

```
sunny/
├── cmd/sunny/                   # CLI entrypoint
├── internal/
│   ├── agent/                   # agent.yaml loader + validation
│   ├── skill/                   # SKILL.md frontmatter parser
│   ├── store/                   # walks ~/.sunny, builds in-memory index
│   ├── bootstrap/               # seeds ~/.sunny from embedded defaults
│   ├── lifecycle/               # pid/state/log files for start/stop/status
│   ├── server/                  # read-only HTTP API
│   ├── client/                  # tiny HTTP client used by the TUI
│   └── tui/                     # Bubble Tea client
└── defaults/                    # baked into the binary via go:embed
    └── agents/
        └── sunny/               # default agent shipped with the binary
            ├── agent.yaml
            ├── prompt.md
            ├── knowledge/
            └── skills/
```

## Runtime layout (`~/.sunny/`)

```
~/.sunny/
├── run/                          # pid, log, state.json (managed by sunny)
└── agents/
    └── sunny/
        ├── agent.yaml            # identity: name, description, model
        ├── prompt.md             # system prompt (optional)
        ├── knowledge/            # any *.md, walked recursively
        │   └── about.md
        └── skills/
            ├── greet/
            │   └── SKILL.md      # YAML frontmatter (name, description) + body
            └── summarize/
                └── SKILL.md
```

### `agent.yaml`

```yaml
name: sunny
description: Default agent shipped with Sunny.
model: claude-opus-4-7
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

A skill is a folder. `SKILL.md` is the contract; anything else in the folder
(scripts, templates, references) is a resource the skill can use.

## HTTP API

While the daemon is running:

| Endpoint | Returns |
|---|---|
| `GET /healthz` | `{"status":"ok"}` |
| `GET /agents` | List of agents with name, model, skill/knowledge counts |
| `GET /agents/{slug}` | Full agent detail incl. skill names + knowledge files |
| `GET /agents/{slug}/skills/{name}` | Skill frontmatter + body |
| `GET /agents/{slug}/knowledge/{file...}` | Raw markdown file |

```bash
curl -s localhost:7777/agents | jq
```

## License

MIT — see [`LICENSE`](./LICENSE).
