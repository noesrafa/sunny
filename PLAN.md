# Sunny — Plan

> Este documento es la fuente de verdad del proyecto. Si abres una sesión nueva
> de Claude, léelo primero. Después: `README.md`, `cmd/sunny/main.go`,
> `internal/server/server.go`.

## Visión

**Sunny es un agente personal autohosteado, distribuible como un binario único.**
Vive donde tú decidas (Mac, VPS, Raspberry Pi). Cada instancia es soberana: tu
data nunca sale del host donde corre.

Inspiraciones: **Plex** (servidor en casa, multi-cliente), **Ollama** (daemon
+ cliente delgado), **Claude Code** (filesystem como fuente de verdad).

## Arquitectura

Sunny tiene **dos roles bien separados**:

```
┌──────────────────────────────────────────────────────────┐
│                       sunny binario                       │
│                                                           │
│  ┌─────────────────┐         ┌─────────────────────────┐ │
│  │     ENGINE      │         │         CLIENT          │ │
│  │   (daemon)      │   HTTP  │          (TUI)          │ │
│  │                 │ ◄─────► │                         │ │
│  │  sunny start    │         │      sunny / sunny tui  │ │
│  └─────────────────┘         └─────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

### Engine (daemon)

`sunny start` lanza un proceso largo que:

- Lee/escribe `~/.sunny/` (agentes, skills, knowledge, conversaciones)
- Habla con providers de IA (Anthropic API, OpenAI-compatible, eventualmente Ollama local)
- Expone una API HTTP en `127.0.0.1:7777` y, **si Tailscale está corriendo**,
  también en la IP del tailnet (auto-detectado)
- Único dueño del estado mutable (conversaciones, journal, locks)

**Lo que el engine NO hace:**
- No tiene UI. Solo HTTP.
- No spawnea procesos cliente. Tú lanzas el TUI cuando quieras.

### Client (TUI)

`sunny` (sin args) abre un cliente Bubble Tea que:

- Conecta a uno o más daemons sunny por HTTP
- Renderiza chat, sidebar, input — toda la presentación
- **No conoce providers, no sabe de Anthropic API, no spawnea claude CLI**
- Federa: si hay varios sunny en el tailnet, los muestra unificados

**Lo que el TUI NO hace:**
- No persiste conversaciones (eso es del engine)
- No habla con providers (eso es del engine)
- No conoce el filesystem layout (lo lee vía API)

## Modelo de mesh con Tailscale

Cada host corre su propio `sunny start`. Cada engine es soberano de SU data.
La unificación pasa **en el cliente**.

```
┌──────────────┐         ┌──────────────┐
│  sunny-mac   │         │  sunny-vps   │
│  daemon      │         │  daemon      │
│  :7777       │         │  :7777       │
└──────┬───────┘         └──────┬───────┘
       │                        │
       └────────────┬───────────┘
              tailnet HTTP
                    │
            ┌───────┴────────┐
            │   TUI client   │
            │  fan-out + UI  │
            └────────────────┘
```

### Cómo se "arma solita" la red

El daemon, al iniciar:
1. Siempre bind a `127.0.0.1:7777`
2. Si detecta `tailscale` corriendo (`tailscale ip` con éxito), bind extra a la
   IP del tailnet
3. Si detecta mDNS/Bonjour en LAN, anuncia
4. Sin Tailscale ni LAN: nodo aislado, todo local

El TUI, al iniciar:
1. Lee `~/.sunny/peers.yaml` (manual) si existe
2. Lee `tailscale status --json` y filtra hosts que respondan en `:7777`
3. Hace fan-out a todos los engines conocidos al renderizar

**Sin Tailscale: cero config, sigue funcionando como single-node.**

### Identidad y auth entre peers

- Local (`127.0.0.1`): token en `~/.sunny/token` (Bearer auth)
- Tailscale peer (source IP en el tailnet): trust automático — la red ya autenticó
- Cualquier otra IP: ni siquiera escucha (no bindea ahí)

Los agentes en mesh se identifican como `host/slug`: `mac/zoro`, `vps/sunny`.
Sin colisiones, fácil de filtrar en la UI.

## Filesystem como fuente de verdad

Todo lo que define un agente vive en archivos planos, editables, versionables,
backupeable:

```
~/.sunny/
├── agents/<slug>/
│   ├── agent.yaml          # identidad: name, description, model
│   ├── prompt.md           # system prompt
│   ├── knowledge/          # markdown libre, walked recursively
│   └── skills/<slug>/SKILL.md   # frontmatter (name, description) + body
└── run/
    ├── state.json          # pid, addr, started_at del daemon
    └── sunny.log
```

Convención de **skills 100% compatible con Claude Code**: `SKILL.md` con
frontmatter `name` + `description`. Skills se intercambian entre sunny, Claude
Code, y cualquier cliente de la familia sin conversión.

`defaults/` baked en el binario via `go:embed`. Al primer `sunny start` se
copia a `~/.sunny/`. A partir de ahí el usuario es dueño — sunny nunca
sobrescribe.

## Estado actual (v0.8.0)

### Lo que funciona end-to-end

- **Daemon detached** (`sunny start/stop/status/serve`), sobrevive
  cierre de TUI/terminal vía `Setsid`.
- **Bootstrap** desde defaults embebidos en `~/.sunny/` la primera vez;
  agentes existentes nunca se sobreescriben.
- **HTTP API** completo:
  - Lectura: `/healthz`, `/agents`, `/agents/{slug}`, `.../skills/{name}`,
    `.../knowledge/{file}`
  - Escritura: `POST/PATCH/DELETE /agents`, `POST/GET/DELETE /agents/{slug}/conversations`,
    `POST /agents/{slug}/conversations/{id}/turn` (SSE), `PUT/DELETE /secrets/{provider}`
- **Bearer auth** en todas las rutas excepto `/healthz`. Token
  generado al primer boot en `~/.sunny/token` (mode 0600). Comandos
  `sunny token`, `sunny token rotate`.
- **Persistencia de conversaciones** en `agents/<slug>/conversations/<id>/{meta.json, events.jsonl}`.
  El journal es la verdad; meta es rollup. `provider_state`
  (claude-code session id) vive en meta para sobrevivir restarts.
- **Multi-agente con CRUD** via HTTP + TUI (`ctrl+a` picker; create
  con auto-slug, edit, archive). Borrado mueve a `~/.sunny/.archive/`.
- **Tres providers**: claude-code (CLI subprocess), anthropic (SDK),
  ollama (Ollama Cloud /api/chat). Routing por `agent.yaml.provider`
  con fallback al default del daemon.
- **Secrets** centralizados en `~/.sunny/secrets.yaml` (mode 0600,
  estructurado por proveedor). Env vars override. CLI (`sunny
  secrets <p> set <field>` con stdin), TUI (`ctrl+y` paste form),
  HTTP. Nunca se devuelve un valor por la API.
- **TUI** restaura su estado al reabrir (sesiones, drafts, theme,
  agente activo, conv id). Ctrl+c cancela el turno en vuelo y queda
  registrado como `cancelled` en el journal.
- **Release**: GoReleaser → linux/amd64 + darwin/arm64, Homebrew tap
  auto-actualizado por tag.

## Roadmap

### Lo que sigue (post-v0.8.0)

- [ ] **Reload del journal en TUI**: hoy los `Items` cacheados en
      `state.json` son la fuente de verdad para el render al reabrir.
      Falta reconciliar con `events.jsonl` cuando difieren (otro
      cliente escribió mientras estábamos cerrados).
- [ ] **Picker de conversaciones por agente**: la sidebar lista
      sesiones locales pero no las conversaciones persistidas de un
      agente. Útil para reabrir un chat archivado.
- [ ] **launchd / systemd**: sobrevivir reboot del host. Comandos
      `sunny enable / disable`. Anotado como deuda en CLAUDE.md.
- [ ] **Tools ejecutables**: skills declaradas en system prompt como
      texto pero no invocables. MCP nativo o formato propio.
- [ ] **Rename de agente**: HTTP no lo expone. Hoy: mover el folder a
      mano y reload.
- [ ] **Tests**: cero hoy (intencional). CI mínimo + integration
      suite del daemon como red de seguridad antes del primer
      refactor mayor.

---

### Histórico (entregado)

#### v0.2.0 — TUI tonta (refactor "rip multiplexor")

Mover toda la lógica de provider/spawn fuera del cliente. El TUI queda
limitado a presentación + HTTP a engines.

- [ ] Borrar `internal/claude/` (subprocess wrapper)
- [ ] Borrar `internal/runs/`, `internal/terminal/`, `internal/favs/`
- [ ] Reescribir `internal/session/session.go`: quitar `Stream *claude.Stream`,
      reemplazar por items que vienen del engine vía HTTP/SSE
- [ ] Reescribir `internal/session/transcript.go`: quitar refs a `claude.Event`,
      definir tipos de evento propios o usar JSON del daemon
- [ ] Adaptar `internal/tui/model.go` y handlers (`model_appmsg.go`,
      `model_keys.go`) para que `Send` haga POST al daemon en vez de
      `stream.SendBlocks`
- [ ] Eliminar dialogs de runs/panes (`dialog_runs.go`, `dialog_runlogs.go`,
      `dialog_runedit.go`, `dialog_newpane.go`)
- [ ] Eliminar sidebar sections de runs/terminals
- [ ] Cambiar paths de estado de `~/.sunnytui/` a `~/.sunny/` (`internal/state/`,
      `internal/logger/`)

**Resultado:** binario más chico, sin código muerto. TUI todavía no chatea
porque el engine no tiene `/chat` aún — pero su scope queda perfectamente
delimitado.

### v0.3.0 — Engine de chat

El daemon empieza a hablarle a un provider real.

- [ ] Provider abstracto (`internal/provider/`) con primer driver: Anthropic API
- [ ] Token API en `~/.sunny/.env` (plain) — encriptación viene después
- [ ] `POST /agents/{slug}/conversations` — crea conversación
- [ ] `POST /agents/{slug}/conversations/{id}/turns` — manda mensaje
- [ ] `GET /agents/{slug}/conversations/{id}/events` — SSE de eventos
      (assistant tokens, tool_use, tool_result, end_turn, error)
- [ ] Persistencia: JSONL append-only en `~/.sunny/agents/<slug>/conversations/<id>.jsonl`
- [ ] TUI consume SSE y renderiza streaming
- [ ] Cancelación: `DELETE /agents/{slug}/conversations/{id}/turns/current`

**Hito:** primer chat real end-to-end con UN agente UN host.

### v0.4.0 — Auth + tokens

- [ ] `~/.sunny/token` (32 bytes random, chmod 600, gen al primer arranque)
- [ ] `Authorization: Bearer <token>` validado con `subtle.ConstantTimeCompare`
- [ ] TUI lee el token al inicio
- [ ] CLI helper: `sunny token` (imprime token actual)

### v0.5.0 — Mesh + Tailscale

- [ ] Detectar `tailscale ip` al iniciar el daemon, bindear a la IP del tailnet
- [ ] `~/.sunny/peers.yaml` para peers manuales
- [ ] `tailscale status --json` para auto-discovery
- [ ] TUI fan-out: lista agents desde TODOS los peers, prefijo `host/slug`
- [ ] Trust por source IP (Tailscale peer = trusted, no token)

**Hito:** abro `sunny` en mi Mac, veo agentes de mi VPS y mi Pi en una sola
sidebar.

### v0.6.0 — Polish + paridad con franky

- [ ] MCP tools (igual que franky usa)
- [ ] Hot reload de `~/.sunny/agents/` (fsnotify) — editas un SKILL.md, el daemon
      lo recoge sin reiniciar
- [ ] Subagentes
- [ ] Cron / scheduled runs
- [ ] Telegram bridge (opcional, plugin)

### v1.0 — Producción

- [ ] Tests de integración
- [ ] Reproducibilidad de builds
- [ ] Notarización macOS
- [ ] Documentación completa de API
- [ ] Migración guidelines desde franky

## Decisiones tomadas (no cambiar sin discutir)

| Tema | Decisión | Razón |
|---|---|---|
| Lenguaje | Go ≥ 1.26 | Binario único, cross-compile trivial, ARM friendly |
| Nombre del binario | `sunny` | Engine + TUI bajo el mismo nombre |
| Carpeta de data | `~/.sunny/` | Auto-creada al primer run con defaults embebidos |
| Filesystem layout | **Compatible con Claude Code** | Skills bidireccionales sin conversión |
| Distribución | Homebrew tap + GoReleaser | Linux/macOS, ARM/x86, un solo `brew install` |
| TUI base | bubbletea v2 + bubbles + lipgloss + glamour | Lifted desde sunnytui |
| Daemon vs lib | **Daemon HTTP** | Una fuente de verdad de estado, multi-cliente trivial |
| Multi-host | Daemons separados por host, descubrimiento Tailscale | Soberanía de data por host |
| Memoria del agente | Markdown + JSONL en filesystem, git para sync | Portable, auditable, backupeable |

## Decisiones pendientes

| Tema | Default | Alternativas |
|---|---|---|
| HTTP router | `net/http` puro (Go 1.22+ ServeMux) | `chi`, `gin`, `echo` |
| Streaming | Server-Sent Events (SSE) | WebSocket, gRPC streaming |
| Logging | `log/slog` stdlib | `charmbracelet/log` (lo trae sunnytui) |
| API keys | `~/.sunny/.env` plain en v0.3 | OS keyring, vault encriptado |
| Provider en v0.3 | Anthropic primero | OpenAI, Ollama Cloud, local Ollama |
| MCP | Cliente nativo en v0.6 | Plugin separado |

## Coexistencia con franky

Franky (el sistema TS actual del autor en `/home/rafa/franky/`) sigue siendo el
sistema de producción. Sunny **no reemplaza franky** en v0.x — itera en
paralelo. Cuando sunny tenga paridad funcional (≥v0.6), franky se va a
deprecar.

Mientras tanto, los agentes pueden migrarse uno por uno: copiar el folder de
agente desde franky-core/knowledge/agents/{slug}/ a ~/.sunny/agents/{slug}/.
La estructura es compatible (Claude Code-style en ambos lados).
