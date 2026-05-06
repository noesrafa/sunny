// Package server exposes the daemon's HTTP API: read-only metadata over
// the agent store, plus per-conversation chat turns over SSE.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/noesrafa/sunny/internal/conv"
	"github.com/noesrafa/sunny/internal/conversation"
	"github.com/noesrafa/sunny/internal/engine"
	"github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/mesh"
	"github.com/noesrafa/sunny/internal/pairing"
	"github.com/noesrafa/sunny/internal/monitors"
	"github.com/noesrafa/sunny/internal/runs"
	"github.com/noesrafa/sunny/internal/secrets"
	"github.com/noesrafa/sunny/internal/skill"
	"github.com/noesrafa/sunny/internal/store"
	"github.com/noesrafa/sunny/internal/tabs"
)

// Options bundles everything New needs. Grouping these keeps the
// constructor stable as we add subsystems.
//
// Engine is a pointer-to-pointer so the daemon can hot-swap it after
// secrets change without rebuilding the http.Server. The pointee is
// nil when no provider is configured; chat returns 503 in that case.
type Options struct {
	Store         *store.Store
	Conversations *conversation.Store
	// Sink is the per-conversation pub/sub bus. Required for chat:
	// the watch endpoint subscribes here, and the chat handler
	// publishes turn events through it. Constructed by the daemon
	// over the same underlying conversation.Store.
	Sink *conv.Sink
	// Tabs holds the daemon-side list of "open chat tabs". Multi-
	// viewer sync (the same conversation visible in multiple TUIs
	// at once) is built on top of this — every TUI fetches the same
	// list and listens for tab.* bus events.
	Tabs *tabs.Store
	// Runs is the file-backed store of background-service
	// definitions; Runtime supervises their live processes. Both
	// optional — if nil the /runs routes return 503.
	Runs    *runs.Store
	Runtime *runs.Runtime
	// Scheduler runs agent-authored monitor YAML files at their
	// configured intervals. Optional — if nil, /monitors routes
	// return 503.
	Scheduler *monitors.Scheduler
	Secrets   *secrets.Store
	Engine  *atomic.Pointer[engine.Engine]
	Log     *slog.Logger
	// Token is the bearer credential clients must send. Empty disables
	// auth (test/dev only).
	Token string
	// RebuildEngine is invoked after a successful PUT/DELETE on
	// /secrets so the daemon can re-instantiate provider drivers
	// against the new file. Optional — if nil, secret writes still
	// succeed but won't take effect until the daemon restarts.
	RebuildEngine func()
	// Pairs handles the `sunny pair offer` / `sunny pair claim`
	// dance. Optional: if nil, the pairing endpoints return 503.
	Pairs *pairing.Service
	// Hub publishes mutation events (agent created, conversation
	// turn appended, etc.) to subscribers of GET /events. Optional;
	// if nil, /events returns 503 and mutating handlers no-op the
	// publish step.
	Hub *events.Hub
	// MeshKey, when non-empty, enables tailnet-based auto-auth:
	// requests from a known tailnet IP carrying X-Sunny-Mesh that
	// matches this key bypass bearer auth. Empty disables the
	// shortcut (bearer remains the only path).
	MeshKey mesh.Key
	// Version is the linker-set version string (`vX.Y.Z`). Surfaced
	// at GET /sunny/identity for compat checks. Empty falls back to
	// "dev".
	Version string
	// InstanceID is a stable per-installation random ID, surfaced at
	// /sunny/identity. Lets the TUI distinguish "the same daemon
	// across reboots" from "a new daemon at the same address".
	InstanceID string
	// Root is the sunny runtime directory (defaults to ~/.sunny).
	// Forwarded by the lifecycle handlers when they spawn the
	// detached `sunny restart/update/stop` helper so the helper
	// targets this exact daemon (important when running multiple
	// daemons against different roots).
	Root string
	// StartedAt is the wall-clock time the daemon's HTTP handler
	// was constructed. Surfaced by GET /sunny/version so clients
	// can detect when a restart has actually happened (the value
	// changes only on a fresh boot).
	StartedAt time.Time
	// AutoTrustTailnet enables the zero-config auto-trust path: any
	// request from a tailnet IP belonging to the same tailscale
	// account as this daemon authenticates without any header. On
	// by default in v0.17 — turn off for tailnets you share with
	// other people whose machines you don't want talking to your
	// daemon (work tailnets, shared family tailnets).
	AutoTrustTailnet bool
}

// New builds the daemon's HTTP handler.
//
// Auth: every route requires `Authorization: Bearer <token>` except
// /healthz (so liveness probes work without credentials).
func New(opts Options) http.Handler {
	srv := &server{
		store:         opts.Store,
		conv:          opts.Conversations,
		sink:          opts.Sink,
		tabs:          opts.Tabs,
		runs:          opts.Runs,
		runtime:       opts.Runtime,
		scheduler:     opts.Scheduler,
		secrets:       opts.Secrets,
		engine:        opts.Engine,
		log:           opts.Log,
		rebuildEngine: opts.RebuildEngine,
		pairs:         opts.Pairs,
		hub:           opts.Hub,
		meshKey:       opts.MeshKey,
		version:       opts.Version,
		instanceID:    opts.InstanceID,
		root:          opts.Root,
		startedAt:     opts.StartedAt,
		activeTurns:   newActiveTurnsRegistry(),
	}
	if srv.startedAt.IsZero() {
		srv.startedAt = time.Now()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.health)
	mux.HandleFunc("GET /agents", srv.listAgents)
	mux.HandleFunc("POST /agents", srv.createAgent)
	mux.HandleFunc("GET /agents/{id}", srv.getAgent)
	mux.HandleFunc("PATCH /agents/{id}", srv.updateAgent)
	mux.HandleFunc("DELETE /agents/{id}", srv.deleteAgent)
	mux.HandleFunc("GET /agents/{id}/skills/{name}", srv.getSkill)
	mux.HandleFunc("GET /agents/{id}/knowledge/{file...}", srv.getKnowledge)
	mux.HandleFunc("GET /agents/{id}/avatar", srv.getAvatar)
	mux.HandleFunc("PUT /agents/{id}/avatar", srv.putAvatar)
	mux.HandleFunc("DELETE /agents/{id}/avatar", srv.deleteAvatar)
	mux.HandleFunc("GET /agents/{id}/conversations", srv.listConversations)
	mux.HandleFunc("POST /agents/{id}/conversations", srv.createConversation)
	mux.HandleFunc("GET /agents/{id}/conversations/{conv_id}", srv.getConversation)
	mux.HandleFunc("DELETE /agents/{id}/conversations/{conv_id}", srv.deleteConversation)
	mux.HandleFunc("POST /agents/{id}/conversations/{conv_id}/turns", srv.postTurns)
	mux.HandleFunc("DELETE /agents/{id}/conversations/{conv_id}/turn", srv.deleteTurn)
	mux.HandleFunc("GET /agents/{id}/conversations/{conv_id}/watch", srv.watchConversation)
	mux.HandleFunc("GET /secrets", srv.listSecrets)
	mux.HandleFunc("PUT /secrets/{provider}", srv.putSecrets)
	mux.HandleFunc("DELETE /secrets/{provider}", srv.deleteSecrets)
	mux.HandleFunc("POST /pairing/offer", srv.offerPairing)
	mux.HandleFunc("POST /pairing/claim", srv.claimPairing)
	mux.HandleFunc("GET /events", srv.streamEvents)
	mux.HandleFunc("GET /sunny/identity", srv.streamIdentity)
	mux.HandleFunc("GET /sunny/version", srv.getSunnyVersion)
	mux.HandleFunc("GET /sunny/version/check", srv.getSunnyVersionCheck)
	mux.HandleFunc("POST /sunny/restart", srv.postSunnyRestart)
	mux.HandleFunc("POST /sunny/update", srv.postSunnyUpdate)
	mux.HandleFunc("POST /sunny/stop", srv.postSunnyStop)
	mux.HandleFunc("GET /fs/list", srv.fsList)
	mux.HandleFunc("GET /tabs", srv.listTabs)
	mux.HandleFunc("POST /tabs", srv.openTab)
	mux.HandleFunc("DELETE /tabs/{id}", srv.closeTab)
	mux.HandleFunc("PATCH /tabs/{id}", srv.patchTab)
	mux.HandleFunc("POST /tabs/{id}/conversation", srv.rebindTabConv)
	mux.HandleFunc("GET /runs", srv.listRuns)
	mux.HandleFunc("POST /runs", srv.createRun)
	mux.HandleFunc("GET /runs/{id}", srv.getRun)
	mux.HandleFunc("PATCH /runs/{id}", srv.updateRun)
	mux.HandleFunc("DELETE /runs/{id}", srv.deleteRun)
	mux.HandleFunc("POST /runs/{id}/start", srv.startRun)
	mux.HandleFunc("POST /runs/{id}/stop", srv.stopRun)
	mux.HandleFunc("POST /runs/{id}/restart", srv.restartRun)
	mux.HandleFunc("GET /runs/{id}/logs", srv.getRunLogs)
	mux.HandleFunc("GET /runs/{id}/logs/watch", srv.watchRunLogs)
	mux.HandleFunc("GET /monitors", srv.listMonitors)
	mux.HandleFunc("PATCH /monitors/{name}", srv.patchMonitor)
	mux.HandleFunc("GET /monitors/{name}/history", srv.getMonitorHistory)
	mux.HandleFunc("GET /stats", srv.stats)
	mux.HandleFunc("GET /providers", srv.listProviders)

	// Compose middleware:
	//   logging → tailnetIdentity → meshAuth → requireBearer → mux
	//
	// tailnetIdentity is the zero-config auto-trust path: any
	// request from a tailnet IP belonging to the same tailscale
	// account as this daemon is marked authed. meshAuth is the
	// opt-in shared-key path for sub-meshes within a tailnet
	// (different tailscale users). requireBearer is the always-on
	// fallback (manual pair flow, off-tailnet hosts).
	tailnetCache := NewTailnetCache(5 * time.Minute)
	handler := requireBearer(opts.Token, mux)
	handler = MeshAuth(opts.MeshKey, tailnetCache.IPs, handler)
	handler = TailnetIdentityAuth(opts.AutoTrustTailnet, tailnetCache.SameUser, handler)
	return logging(opts.Log, handler)
}


// requireBearer enforces `Authorization: Bearer <token>` on every route
// except /healthz. Compares with subtle.ConstantTimeCompare to avoid
// timing leaks. If token is empty, auth is bypassed (test/dev only).
func requireBearer(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz must be reachable for liveness probes; pairing
		// /claim carries its own credential (the one-shot code) and
		// must reject the bearer header — a pairing client legitimately
		// has no token yet. /sunny/identity is intentionally public
		// so the mesh discovery flow can ask "is this daemon part of
		// my mesh?" before sending any credential.
		if r.URL.Path == "/healthz" || pairingExempt(r.URL.Path) || identityExempt(r.URL.Path) || avatarExempt(r.Method, r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		// Mesh middleware upstream may have already authenticated
		// this request via the tailnet + shared-key shortcut.
		if isMeshAuthed(r) {
			h.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

type server struct {
	store         *store.Store
	conv          *conversation.Store
	sink          *conv.Sink
	tabs          *tabs.Store
	runs          *runs.Store
	runtime       *runs.Runtime
	scheduler     *monitors.Scheduler
	secrets       *secrets.Store
	engine        *atomic.Pointer[engine.Engine]
	log           *slog.Logger
	rebuildEngine func()
	pairs         *pairing.Service
	hub           *events.Hub
	meshKey       mesh.Key
	version       string
	instanceID    string
	root          string
	startedAt     time.Time
	// activeTurns enforces "at most one in-flight turn per conv" so
	// POST /turns can return 409 on contention and DELETE /turn can
	// look up the cancel func by conv key.
	activeTurns *activeTurnsRegistry
}

func logging(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("request", "method", r.Method, "path", r.URL.Path)
		h.ServeHTTP(w, r)
	})
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type agentItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Effort      string `json:"effort,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Skills      int    `json:"skills"`
	Knowledge   int    `json:"knowledge"`
	HasAvatar   bool   `json:"has_avatar"`
}

func summarize(a *store.Agent) agentItem {
	return agentItem{
		ID:          a.ID,
		Name:        a.Config.Name,
		Description: a.Config.Description,
		Model:       a.Config.Model,
		Effort:      a.Config.Effort,
		Provider:    a.Config.Provider,
		Skills:      len(a.Skills),
		Knowledge:   len(a.Knowledge),
		HasAvatar:   a.HasAvatar,
	}
}

func (s *server) listAgents(w http.ResponseWriter, _ *http.Request) {
	agents := s.store.Agents()
	out := make([]agentItem, 0, len(agents))
	for _, a := range agents {
		out = append(out, summarize(a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Agent(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	type skillItem struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	type knowledgeItem struct {
		Name string `json:"name"`
	}
	out := struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Model       string          `json:"model"`
		Effort      string          `json:"effort,omitempty"`
		Provider    string          `json:"provider,omitempty"`
		Prompt      string          `json:"prompt,omitempty"`
		HasAvatar   bool            `json:"has_avatar"`
		Skills      []skillItem     `json:"skills"`
		Knowledge   []knowledgeItem `json:"knowledge"`
	}{
		ID:          a.ID,
		Name:        a.Config.Name,
		Description: a.Config.Description,
		Model:       a.Config.Model,
		Effort:      a.Config.Effort,
		Provider:    a.Config.Provider,
		Prompt:      a.Prompt,
		HasAvatar:   a.HasAvatar,
		Skills:      []skillItem{},
		Knowledge:   []knowledgeItem{},
	}
	for _, sk := range a.Skills {
		out.Skills = append(out.Skills, skillItem{Name: sk.Front.Name, Description: sk.Front.Description})
	}
	for _, k := range a.Knowledge {
		out.Knowledge = append(out.Knowledge, knowledgeItem{Name: k.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getSkill(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Agent(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	for _, sk := range a.Skills {
		if sk.Front.Name != name {
			continue
		}
		body, err := skill.LoadBody(sk.Dir)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Name         string   `json:"name"`
			Description  string   `json:"description"`
			Category     string   `json:"category,omitempty"`
			AllowedTools []string `json:"allowed_tools,omitempty"`
			Body         string   `json:"body"`
		}{
			Name:         sk.Front.Name,
			Description:  sk.Front.Description,
			Category:     sk.Category,
			AllowedTools: sk.Front.AllowedTools,
			Body:         body,
		})
		return
	}
	http.NotFound(w, r)
}

func (s *server) getKnowledge(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Agent(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(r.PathValue("file")))
	if cleaned == "." || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") || filepath.IsAbs(cleaned) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	for _, k := range a.Knowledge {
		if k.Name == cleaned {
			data, err := os.ReadFile(k.Path)
			if err != nil {
				http.Error(w, "read error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write(data)
			return
		}
	}
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
