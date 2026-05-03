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

## Estado actual (v0.14.0)

### Lo que funciona end-to-end

- **Daemon detached** (`sunny start/stop/status/serve`), sobrevive
  cierre de TUI/terminal vía `Setsid`. Healthcheck + recuperación
  de boot fallido (mata huérfano, limpia state, surface tail del log).
- **Bootstrap** desde defaults embebidos en `~/.sunny/` la primera
  vez; agentes existentes nunca se sobreescriben.
- **HTTP API** completo (todas las rutas autenticadas con Bearer
  token salvo `/healthz`):
  - Agentes: `GET/POST/PATCH/DELETE /agents` + `GET /agents/{slug}/skills/{name}` + `GET /agents/{slug}/knowledge/{file}`
  - Conversaciones: `GET/POST /agents/{slug}/conversations`, `GET/DELETE /agents/{slug}/conversations/{id}`, `POST /agents/{slug}/conversations/{id}/turn` (SSE)
  - Secrets: `GET /secrets`, `PUT/DELETE /secrets/{provider}` (nunca devuelve valores)
- **Auth**: token de 32 bytes random en `~/.sunny/token` (mode 0600)
  generado al primer boot. Comandos `sunny token` / `sunny token rotate`.
- **Persistencia de conversaciones** en `agents/<slug>/conversations/<id>/{meta.json, events.jsonl}`.
  El journal es la verdad; meta es rollup. `provider_state` vive en
  meta para sobrevivir restart. Cancel mid-turn registra `cancelled`
  en el journal. Si la conv server-side fue archivada, la TUI crea
  una nueva transparentemente al siguiente send (no error rojo).
- **Multi-agente con CRUD** vía HTTP + TUI (`ctrl+a` picker; create
  con auto-slug desde el nombre, edit con prefill, archive). El
  archive mueve a `~/.sunny/.archive/` con timestamp; restaurar es
  manual (mover folder a `agents/`).
- **Cuatro providers**:
  - **claude-code**: subprocess wrapper, usa el login de claude.ai
    del usuario, trae todo el toolset nativo de claude code.
  - **anthropic**: SDK streaming con cache breakpoints, tool support
    completo.
  - **ollama**: Ollama Cloud `/api/chat`, JSONL streaming, thinking
    deltas para modelos de razonamiento (gpt-oss, deepseek-r1,
    qwen3-thinking), `keep_alive: 10m` default, tool support
    OpenAI-compatible.
  - **opencode**: subprocess wrapper de `opencode run --format
    json`, escribe per-agent file en `~/.config/opencode/agent/
    sunny-<slug>.md` (opencode no tiene `--append-system-prompt`),
    delega round-trip de tools a opencode mismo. Hereda los 75+
    providers de opencode + sus tools nativas (read/edit/bash/
    grep/glob/task/...) sin que sunny tenga que conocerlos.
  Routing por `agent.yaml.provider` con fallback al default del
  daemon. SUNNY_PROVIDER env var pinea uno explícitamente.
- **Secrets** centralizados en `~/.sunny/secrets.yaml` (mode 0600).
  Env vars override. CLI (`sunny secrets <p> set <field>` con
  stdin), TUI (`ctrl+y` paste form), HTTP. Mutators reload del
  disco antes de escribir para no pisar ediciones concurrentes
  CLI/daemon.
- **Tools read-only**: `view`, `ls`, `grep`, `glob` en
  `internal/tools/`. cwd-bounded con resolución de symlinks
  (catched bug en macOS de `/tmp` → `/private/tmp`). Engine corre
  el round-trip loop (cap 25 iteraciones), tool events surface a
  la SSE stream para que la TUI los renderice. claude-code no
  recibe estos tools (tiene los suyos nativos; evitar colisión).
- **TUI** restaura su estado al reabrir (sesiones, drafts, theme,
  agente activo, conv id). `ctrl+q` para salir (no `esc` —
  demasiado fácil de tirar por accidente). NewSession dialog
  reducido a agent + cwd; model + effort viven en `agent.yaml`.
- **Onboarding**: `sunny doctor` imprime checklist (✓/⚠/✗) de
  providers, daemon, runtime y peers; `sunny setup [provider]` instala
  el binario apropiado (brew/curl con confirmación) o pide la API
  key, según el provider. `--print-only` para flujos no-
  interactivos (CI, SSH).
- **Mesh (Fase 1 + 2a)**: `~/.sunny/peers.yaml` opcional con `name`,
  `url`, `token` por peer remoto; `local` es implícito. `client.
  Federation` fan-outs el listing de agentes en paralelo, errores
  per-peer aislados. La TUI muestra agentes con prefijo `host/slug`
  cuando hay >1 peer; conversaciones viven en el engine de origen
  (data por ubicación). CRUD de agentes (create/edit/archive) sólo
  funciona contra local en v0.13 — federation de escritura viene
  más adelante.

  **Pairing (Fase 2a)**: dos endpoints HTTP nuevos en el daemon —
  `POST /pairing/offer` (auth) emite un código alfanumérico de 6
  chars con TTL 5 min, `POST /pairing/claim` (no auth — el código
  ES el credential) lo intercambia por el bearer del daemon. CLI
  `sunny pair offer` (en remoto) y `sunny pair claim <url> <code>`
  (en cliente) automatiza todo, derivando un nombre de peer del
  hostname. Sin SSH ni copy-paste de tokens.

  **Tailscale (Fase 2b)**: `internal/tsnet` envuelve `tailscale ip`
  y `tailscale status --json` por shell-out. Si tailscale está
  instalado al boot, el daemon hace bind extra a la IP del tailnet
  además de 127.0.0.1 (mismo puerto). `sunny peers scan` lista los
  peers del tailnet con healthcheck a `:7777/healthz`, distinguiendo
  candidatos (reachable, no-paired), ya pareados, y "tailnet hosts
  sin sunny". Sunny doctor surface tailnet IP + hint a `peers scan`
  cuando el CLI está disponible. Sin tailscale, todo el código
  degrada silencioso (sin nags ni errores).
- **Release**: GoReleaser → linux/amd64 + darwin/arm64, Homebrew
  tap auto-actualizado por tag.

## Roadmap

### Lo que sigue (post-v0.14.0)

**Fase 3 del mesh — tiempo real cross-cliente**:

- [ ] **`GET /events` SSE general** en el daemon: emite eventos
      `agent_*` y `conversation_*` en cuanto suceden.
- [ ] **TUI subscribe a /events de cada peer** al boot; refresca
      sidebar y agent picker al recibir eventos remotos.
- [ ] **Optimistic local-first**: muestra el mensaje del usuario
      inmediatamente; el evento del propio daemon confirma cuando
      aparece en el journal.

**mDNS/Bonjour fallback** para LAN sin Tailscale — nice-to-have,
no bloqueante.

**Fase 3 del mesh — tiempo real cross-cliente**:

- [ ] **`GET /events` SSE general** en el daemon: emite `agent_*` y
      `conversation_*` para que clientes federados se sincronicen.
- [ ] **TUI subscribe a /events de cada peer** al boot; refresca
      sidebar y agent picker al recibir eventos remotos.

**Federation de escritura** (paralelo a Fase 2/3):
- [ ] **CRUD de agentes contra remoto** desde la TUI. Hoy solo
      local. El picker emite el host correcto pero el form de edit/
      create siempre va al daemon local.
- [ ] **`sunny peer scan-agents`** para hot-cache la lista de
      agentes remotos cuando el daemon remoto está caído.

**Después del mesh** (orden negociable):

- [ ] **Tools de write/exec (edit/write/bash) + permission flow**.
      Complemento natural del read-only quartet que ya está
      en `internal/tools/`. Necesita un protocolo nuevo
      daemon→TUI→user para pedir aprobación antes de tocar disco
      o ejecutar comandos. Sub-tasks:
      - `permissions.Service` con `Request(ctx, action) → granted bool`
      - SSE event nuevo `permission_request` (separado del flow del turn)
      - Dialog en TUI que escucha y responde (allow once / allow session / deny)
      - Cada tool write/exec gateway-ed con allowed-list de safe commands para bash

**Polish que ayudaría:**

- [ ] **Reload del journal en TUI**: los `Items` cacheados en
      `state.json` son la fuente de verdad para el render al reabrir.
      Falta reconciliar con `events.jsonl` cuando difieren (otro
      cliente escribió mientras estábamos cerrados). `GET
      /conversations/{id}` ya existe; falta wire en TUI.
- [ ] **Picker de conversaciones por agente** en la sidebar.
      Hoy se ven sesiones locales pero no las convs persistidas
      del agente. Útil para reabrir chats archivados.
- [ ] **Rename de agente**: HTTP no lo expone porque el slug es
      el directory name. Hoy: mover el folder a mano y reload.
- [ ] **launchd / systemd**: sobrevivir reboot del host. Comandos
      `sunny enable` / `sunny disable`. Anotado como deuda en CLAUDE.md.
- [ ] **Tests**: arrancando (state, tui, doctor, opencode hoy).
      Falta workflow de CI que corra `go vet ./... && go build ./...
      && go test ./...` en push, más un integration test del daemon
      (start, /healthz, /agents, POST /turn, stop).

**Capacidades del agente:**

- [ ] **Tools adicionales**: `download` (HTTP get), `fetch`
      (HTML→md), MCP bridge. Todos pueden venir gradualmente
      después del permission flow.
- [ ] **Subagentes**: que un agente pueda llamar a otro como tool.
      Útil para workflows multi-paso.

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
