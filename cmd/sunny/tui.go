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
	"github.com/noesrafa/sunny/internal/tsnet"
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

	// Tailnet auto-discovery: zero-config when tailscale is up.
	// Two paths, in priority order:
	//
	//   1. Identity: any sunny daemon on the tailnet whose
	//      tailscale UserID matches ours (= our own machine, no
	//      shared keys needed).
	//   2. Mesh-key: sunny daemons NOT in our tailscale account
	//      that hold a shared mesh.key (sub-mesh override).
	//
	// peers.yaml entries take precedence over both — operator
	// config wins.
	//
	// Runs ASYNC in a goroutine so a slow / hung discovery never
	// blocks TUI startup. Discovered peers join the federation as
	// they're found; the agent picker re-queries on open so they
	// surface naturally.
	if tsnet.Available() {
		go discoverTailnetPeers(ctx, fed, *root, lg)
	}

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

// discoverTailnetPeers is the zero-config discovery flow. Walks
// the tailnet, GETs /sunny/identity on each candidate, and adds
// peers in priority:
//
//	1. Same tailscale UserID as us → AddTailnetPeer (no creds).
//	2. Our local mesh.key matches their fingerprint → AddMeshPeer.
//	3. Otherwise skip — daemon belongs to someone else's mesh.
//
// Hard 5-second budget for the whole pass. Per-peer probes run in
// parallel; a tailnet of dozens completes well within budget.
// When the budget expires we add whatever responded and walk away
// — better to show the federation we have than to block forever
// on one stuck peer.
func discoverTailnetPeers(ctx context.Context, fed *client.Federation, root string, lg *charmlog.Logger) {
	// Total wall-clock budget. Belt-and-suspenders: per-peer
	// FetchIdentity already has its own 3s timeout, but if every
	// peer in a 50-node tailnet stalled at the SYN, summing those
	// timeouts could still take longer than anybody's patience.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	st, err := tsnet.FetchStatus()
	if err != nil {
		lg.Debug("tailnet discovery: status", "err", err.Error())
		return
	}
	mk, _ := mesh.Load(root) // optional — empty when no mesh.key
	wantFP := ""
	if mk != "" {
		wantFP = mk.Fingerprint()
	}
	port := "7777"

	type result struct {
		name string
		base string
		path string // "identity" | "mesh" | ""
	}
	resCh := make(chan result, len(st.Peers))

	// Count what we ACTUALLY launch, not what's in st.Peers — a
	// tailnet with offline / IP-less peers (iPhone in a pocket,
	// retired node, etc.) makes len(st.Peers) > the number of
	// goroutines that send on resCh. Waiting for len(st.Peers)
	// recvs deadlocks the loop and, when discovery is synchronous,
	// freezes TUI boot. The launched counter is the bug fix.
	launched := 0
	for _, p := range st.Peers {
		if !p.Online || p.IP == "" {
			continue
		}
		launched++
		go func(p tsnet.Peer) {
			base := "http://" + p.IP + ":" + port
			id, err := client.FetchIdentity(ctx, base)
			if err != nil {
				resCh <- result{name: p.HostName, base: base}
				return
			}
			r := result{name: peerNameFromTailscaleHost(p.HostName), base: base}
			switch {
			case p.UserID != 0 && p.UserID == st.Self.UserID:
				r.path = "identity"
			case wantFP != "" && id.Mesh == wantFP:
				r.path = "mesh"
			}
			resCh <- r
		}(p)
	}

	added := 0
	for i := 0; i < launched; i++ {
		select {
		case r := <-resCh:
			switch r.path {
			case "identity":
				fed.AddTailnetPeer(r.name, r.base)
				added++
				lg.Info("tailnet peer discovered (same tailscale account)", "name", r.name, "url", r.base)
			case "mesh":
				fed.AddMeshPeer(r.name, r.base, string(mk))
				added++
				lg.Info("tailnet peer discovered (shared mesh key)", "name", r.name, "url", r.base)
			}
		case <-ctx.Done():
			lg.Debug("tailnet discovery: budget exceeded", "completed", i, "expected", launched)
			return
		}
	}
	if added > 0 {
		lg.Info("tailnet discovery complete", "added", added)
	}
}

// peerNameFromTailscaleHost makes a slug out of a tailnet hostname.
// tailscale's HostName is already short and dash-friendly (e.g.
// "mac-rafael", "vps-1") but may have uppercase or trailing dots
// from the magicDNS suffix.
func peerNameFromTailscaleHost(h string) string {
	h = strings.ToLower(h)
	if i := strings.Index(h, "."); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "remote"
	}
	return h
}

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

