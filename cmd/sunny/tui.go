package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

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
	if tsnet.Available() {
		discoverTailnetPeers(ctx, fed, *root, lg)
	}

	mgr := session.NewManager()
	saved, themeID, activeIdx := loadSavedState(lg)
	for _, ss := range saved {
		restored, err := restoreSession(ctx, lg, ss)
		if err != nil {
			lg.Warn("restore session failed; skipping", "title", ss.Title, "err", err)
			continue
		}
		mgr.Add(restored)
	}
	if mgr.Len() == 0 {
		// First launch (or every restore failed) — bootstrap one fresh
		// session bound to the default agent.
		first, err := session.New(ctx, cwd, session.Options{
			Logger:    lg,
			Title:     "sunny",
			AgentSlug: "sunny",
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		mgr.Add(first)
	} else if activeIdx >= 0 && activeIdx < mgr.Len() {
		mgr.Active = activeIdx
	}

	first := mgr.Sessions[0]
	model := tui.NewModel(ctx, mgr, cwd, tui.Options{
		Logger:                   lg,
		DefaultModel:             first.Model,
		DefaultEffort:            first.Effort,
		DangerousSkipPermissions: true,
		DaemonAddr:               *addr,
		DaemonToken:              tok,
		DefaultAgent:             "sunny",
		InitialTheme:             themeID,
		Federation:               fed,
	})
	return model.Run(ctx)
}

// discoverTailnetPeers is the zero-config discovery flow. Walks
// the tailnet, GETs /sunny/identity on each candidate, and adds
// peers in priority:
//
//	1. Same tailscale UserID as us → AddTailnetPeer (no creds).
//	2. Our local mesh.key matches their fingerprint → AddMeshPeer.
//	3. Otherwise skip — daemon belongs to someone else's mesh.
//
// Bounded by tsnet.FetchStatus (5s) + per-peer FetchIdentity (3s
// each, parallel), so a tailnet of dozens completes in seconds.
func discoverTailnetPeers(ctx context.Context, fed *client.Federation, root string, lg *charmlog.Logger) {
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
		name        string
		base        string
		path        string // "identity" | "mesh" | ""
	}
	resCh := make(chan result, len(st.Peers))
	for _, p := range st.Peers {
		if !p.Online || p.IP == "" {
			continue
		}
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
	for range st.Peers {
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

// loadSavedState reads ~/.sunny/state.json. Failures degrade to a fresh
// launch — the TUI bootstraps a new session in that case.
func loadSavedState(lg *charmlog.Logger) ([]uistate.SavedSession, string, int) {
	st, err := uistate.Load()
	if err != nil {
		lg.Warn("load state failed; starting fresh", "err", err)
		return nil, "", 0
	}
	return st.Sessions, st.Theme, st.ActiveIdx
}

// restoreSession rebuilds a Session from its saved form. The transcript
// items are decoded from the cached blob; ConvID + AgentSlug are
// preserved so the next send hits the same server-side conversation.
// AgentSlug falls back to "sunny" for legacy state files.
func restoreSession(ctx context.Context, lg *charmlog.Logger, ss uistate.SavedSession) (*session.Session, error) {
	items, err := session.UnmarshalItems(ss.Items)
	if err != nil {
		return nil, fmt.Errorf("decode items: %w", err)
	}
	slug := ss.AgentSlug
	if slug == "" {
		slug = "sunny"
	}
	host := ss.Host
	if host == "" {
		host = peers.LocalName
	}
	return session.New(ctx, ss.Cwd, session.Options{
		Logger:    lg,
		Title:     ss.Title,
		Model:     ss.Model,
		Effort:    ss.Effort,
		Draft:     ss.Draft,
		AgentSlug: slug,
		Host:      host,
		ConvID:    ss.ConvID,
		Items:     items,
		TotalCost: ss.TotalCost,
		Turns:     ss.Turns,
	})
}
