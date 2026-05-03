# Sunny

Self-hosted personal agent. One binary, your data, your rules.

> **Status:** v0.0 — bootstrapping. Designing the layout before writing the engine.

## Idea

Sunny is a Go daemon you install with `brew install sunny` and run anywhere — Mac, VPS, Raspberry Pi. On first launch it creates `~/.sunny/`, seeded from defaults baked into the binary. From there, every agent, skill, and piece of knowledge lives as plain files you can edit, version, share, and back up.

Three principles:

- **Data sovereignty.** Conversations, memory, skills live on your host. Never in someone else's cloud.
- **Radical distribution.** One binary. `brew install` and go. Your sibling installs it on a $35 Pi and has their own agent without a monthly bill.
- **Ecosystem-compatible.** Filesystem layout follows Claude Code conventions (`SKILL.md`, agent folders). Skills move freely between Sunny and any Claude-family client.

Inspirations: **Plex** (self-hosted server, multi-client UI), **Ollama** (daemon + thin clients), **Claude Code** (filesystem as source of truth for skills/agents).

## Repo layout

```
sunny/
├── cmd/sunny/          # the binary entrypoint (TBD)
├── internal/           # engine packages (TBD)
└── defaults/           # baked into the binary via go:embed
    └── agents/
        └── sunny/      # default agent shipped with the binary
            ├── agent.yaml
            ├── prompt.md
            ├── knowledge/
            └── skills/
```

`defaults/` is the seed. On first run the binary copies it to `~/.sunny/`, after which the user owns those files entirely — Sunny never overwrites them.

## Runtime layout (`~/.sunny/`)

```
~/.sunny/
└── agents/
    └── sunny/
        ├── agent.yaml          # identity: name, description, model
        ├── prompt.md           # system prompt (optional)
        ├── knowledge/          # any *.md, walked recursively
        │   └── about.md
        └── skills/
            ├── greet/
            │   └── SKILL.md    # YAML frontmatter (name, description) + body
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

A skill is a folder. `SKILL.md` is the contract; anything else in the folder (scripts, templates, references) is a resource the skill can use.

## Build

TBD — engine not written yet.

## License

MIT — see [`LICENSE`](./LICENSE).
