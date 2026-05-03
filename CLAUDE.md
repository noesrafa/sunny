# sunny — conventions for contributors and AI assistants

This file is the contract for how we evolve `sunny`. Read it before
proposing changes; update it when conventions change.

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
