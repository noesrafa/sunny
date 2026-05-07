package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	charmlog "charm.land/log/v2"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/lifecycle"
	"github.com/noesrafa/sunny/internal/logger"
	"github.com/noesrafa/sunny/internal/mesh"
	"github.com/noesrafa/sunny/internal/peers"
	"github.com/noesrafa/sunny/internal/session"
	uistate "github.com/noesrafa/sunny/internal/state"
	"github.com/noesrafa/sunny/internal/tui"
)

// openTUI is the entrypoint for `sunny` and `sunny tui`.
//
// Responsibilities, in order:
//  1. Auto-start the daemon if it isn't running (unless --no-auto-start).
//  2. Load the bearer token from the file the daemon wrote.
//  3. Restore saved sessions from ~/.sunny/state.json (or boot a fresh one).
//  4. Construct the bubbletea model and hand off control.
//
// Failures in (1) or (2) abort with an error before the TUI ever paints
// — better to show "daemon failed to start" synchronously than to open
// onto a frozen chat view.
func openTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "daemon address to connect to")
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	noAutoStart := fs.Bool("no-auto-start", false, "skip auto-starting the daemon if it isn't running")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Auto-start the daemon when it isn't already running. The TUI is
	// useless without one; making the user remember `sunny start`
	// before `sunny` is the wrong default. --no-auto-start opts out.
	if !*noAutoStart {
		paths := lifecycle.PathsFor(*root)
		alive := false
		if s, err := paths.LoadState(); err == nil && lifecycle.IsAlive(s.PID) {
			alive = true
			*addr = s.Addr // honor whatever addr the running daemon is on
		}
		if !alive {
			fmt.Fprintf(os.Stderr, "sunny: daemon not running — starting it on %s…\n", *addr)
			s, err := startDaemon(*addr, *root, true)
			if err != nil {
				return fmt.Errorf("auto-start: %w", err)
			}
			*addr = s.Addr
		}
	}

	// Token must exist at this point: the daemon writes it on first
	// boot, and we only get here after we've verified the daemon is
	// alive.
	tok, err := auth.LoadToken(*root)
	if err != nil {
		return fmt.Errorf("load token: %w (daemon may not have written it yet)", err)
	}

	// Plumb the linker-set version into the logo before the model is
	// constructed. Strip the leading "v" so the logo's "v" prefix
	// doesn't double up.
	tui.Version = strings.TrimPrefix(version, "v")

	// Load the peer roster (local + everything in ~/.sunny/peers.yaml)
	// so the TUI can fan out agent listings across the federation.
	// Failure here is non-fatal: degrade to a single-peer roster so
	// the TUI still opens against the local daemon.
	roster, perr := peers.Load(*root, *addr, tok)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "sunny: peers.yaml: %v (continuing with local only)\n", perr)
		roster = peers.Roster{Local: peers.Peer{Name: peers.LocalName, URL: "http://" + *addr, Token: tok}}
	}
	fed := client.NewFederation(roster)

	cwd, _ := os.Getwd()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lg, closer := logger.Setup("sunny")
	defer closer.Close()
	lg.Info("tui starting", "cwd", cwd, "log", logger.LogPath())

	// Federation discovery is owned by the daemon now (GET
	// /federation/peers). We kick off one async pass at boot to
	// populate the federation quickly; the periodic refresh runs
	// from inside the bubbletea loop (federationDiscoverTickCmd).
	// Failures are silent — peers.yaml entries already work either
	// way, and the next tick retries.
	go discoverViaDaemon(ctx, fed, lg)

	prefs := loadPrefs(lg)

	// Build one session.Manager per peer currently in the
	// federation. Local is always present; tailnet peers join async
	// (discoverTailnetPeers above + the TUI's peerSyncTickCmd
	// reconciliation tick).
	peerManagers := map[string]*session.Manager{}
	peerOrder := []string{peers.LocalName}
	peerManagers[peers.LocalName] = session.NewManager()
	for _, name := range fed.Names() {
		if _, ok := peerManagers[name]; ok {
			continue
		}
		peerManagers[name] = session.NewManager()
		peerOrder = append(peerOrder, name)
	}

	// Hydrate per-peer tabs from each daemon. Sessions track their
	// owning tab id so close → DELETE /tabs/{id} works. Per-peer
	// fetches run in parallel with a short budget so a slow remote
	// doesn't block boot.
	hydrateTabsParallel(ctx, fed, peerManagers, lg)

	// First launch (no saved tabs anywhere): open one default
	// "sunny" tab on local so the user lands in a usable chat
	// instead of an empty welcome screen.
	if peerManagers[peers.LocalName].Len() == 0 {
		if err := bootstrapDefaultTab(ctx, fed.Local(), peerManagers[peers.LocalName], cwd, lg); err != nil {
			return fmt.Errorf("bootstrap default tab: %w", err)
		}
	}

	// Restore drafts + active tab idx from per-TUI prefs. These
	// are local to this device — different machines naturally
	// have different drafts.
	applyPrefs(prefs, peerManagers, peerOrder)

	first := peerManagers[peers.LocalName].Sessions[0]
	mk, _ := mesh.Load(*root) // empty when no mesh.key — mesh peers just won't auto-add
	model := tui.NewModel(ctx, peerManagers, peerOrder, cwd, tui.Options{
		Logger:                   lg,
		DefaultModel:             first.Model,
		DefaultEffort:            first.Effort,
		DangerousSkipPermissions: true,
		DaemonAddr:               *addr,
		DaemonToken:              tok,
		DefaultAgent:             "sunny",
		InitialTheme:             prefs.Theme,
		Federation:               fed,
		MeshKey:                  string(mk),
	})
	return model.Run(ctx)
}

// hydrateTabsParallel calls GET /tabs on every peer concurrently
// and turns each tab into a session.Session inside the matching
// peerManager. Per-peer failures are logged and skipped — a slow or
// down peer can't block boot.
func hydrateTabsParallel(ctx context.Context, fed *client.Federation, peerManagers map[string]*session.Manager, lg *charmlog.Logger) {
	type peerJob struct {
		name string
		mgr  *session.Manager
	}
	jobs := []peerJob{}
	for name, mgr := range peerManagers {
		jobs = append(jobs, peerJob{name: name, mgr: mgr})
	}
	tCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j peerJob) {
			defer wg.Done()
			c := fed.For(j.name)
			if c == nil {
				return
			}
			tabs, err := c.ListTabs(tCtx)
			if err != nil {
				lg.Warn("list tabs", "peer", j.name, "err", err)
				return
			}
			for _, t := range tabs {
				s, err := session.New(ctx, fallbackCwd(t.Cwd), session.Options{
					Logger:  lg,
					Title:   t.Title,
					AgentID: t.AgentID,
					Host:    j.name,
					TabID:   t.ID,
					ConvID:  t.ConvID,
				})
				if err != nil {
					lg.Warn("session from tab", "peer", j.name, "tab", t.ID, "err", err)
					continue
				}
				j.mgr.Add(s)
			}
		}(j)
	}
	wg.Wait()
}

// bootstrapDefaultTab opens a "sunny" tab on the local daemon so
// fresh installs (or first runs after wiping ~/.sunny/tabs.json)
// land in an actual chat instead of an empty TUI.
func bootstrapDefaultTab(ctx context.Context, c *client.Client, mgr *session.Manager, cwd string, lg *charmlog.Logger) error {
	if c == nil {
		return fmt.Errorf("local client unavailable")
	}
	tab, err := c.OpenTab(ctx, client.OpenTabRequest{
		AgentID: "sunny",
		Cwd:     cwd,
	})
	if err != nil {
		return err
	}
	s, err := session.New(ctx, cwd, session.Options{
		Logger:  lg,
		Title:   tab.Title,
		AgentID: tab.AgentID,
		Host:    peers.LocalName,
		TabID:   tab.ID,
		ConvID:  tab.ConvID,
	})
	if err != nil {
		return err
	}
	mgr.Add(s)
	return nil
}

// applyPrefs walks the saved per-peer prefs and applies drafts +
// active tab id to the freshly-hydrated managers. Drafts are keyed
// by tab id; if a saved draft refers to a tab that no longer exists
// on the daemon it's silently dropped.
func applyPrefs(prefs *uistate.State, peerManagers map[string]*session.Manager, peerOrder []string) {
	if prefs == nil || prefs.PeerState == nil {
		return
	}
	for _, name := range peerOrder {
		mgr := peerManagers[name]
		ps := prefs.PeerState[name]
		if mgr == nil || ps == nil {
			continue
		}
		for _, s := range mgr.Sessions {
			if d, ok := ps.Drafts[s.TabID]; ok {
				s.Draft = d
			}
		}
		if ps.ActiveTabID != "" {
			for i, s := range mgr.Sessions {
				if s.TabID == ps.ActiveTabID {
					mgr.Active = i
					break
				}
			}
		}
	}
}

func fallbackCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

// discoverViaDaemon asks the LOCAL daemon for its federation list
// (GET /federation/peers — server-side cached tailnet sweep) and
// applies each entry to fed. The daemon owns the tailscale CLI
// shell-out + per-peer identity probe; this side just consumes the
// classified result.
//
// Mesh trust adds need our local mesh.key — we read it here rather
// than have the daemon expose it (the key is a secret, never
// returned over HTTP). Empty mesh key just skips mesh entries.
func discoverViaDaemon(ctx context.Context, fed *client.Federation, lg *charmlog.Logger) {
	local := fed.Local()
	if local == nil {
		return
	}
	peers, err := local.FetchFederationPeers(ctx)
	if err != nil {
		lg.Debug("federation discovery", "err", err.Error())
		return
	}
	mk, _ := mesh.Load(rootForFed())
	for _, p := range peers {
		switch p.Trust {
		case "tailnet":
			fed.AddTailnetPeer(p.Name, p.URL)
			lg.Info("peer discovered (same tailscale account)", "name", p.Name, "url", p.URL)
		case "mesh":
			if mk == "" {
				continue
			}
			fed.AddMeshPeer(p.Name, p.URL, string(mk))
			lg.Info("peer discovered (shared mesh key)", "name", p.Name, "url", p.URL)
		}
	}
}

// rootForFed is the sunny runtime dir we read mesh.key from when
// discoverViaDaemon classifies "mesh" entries. The TUI binary always
// lives next to the user's runtime dir, so defaultRoot is correct.
func rootForFed() string { return defaultRoot() }

// loadPrefs reads ~/.sunny/state.json (v8 — UI-local prefs only,
// no session/tab content; that lives on the daemon). Failures
// degrade to an empty prefs object so the TUI still boots cleanly.
func loadPrefs(lg *charmlog.Logger) *uistate.State {
	st, err := uistate.Load()
	if err != nil {
		lg.Warn("load prefs failed; starting fresh", "err", err)
		return uistate.Empty()
	}
	return st
}

