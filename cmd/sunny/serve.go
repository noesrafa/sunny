package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/bootstrap"
	"github.com/noesrafa/sunny/internal/conversation"
	"github.com/noesrafa/sunny/internal/engine"
	"github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/mesh"
	"github.com/noesrafa/sunny/internal/pairing"
	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/provider/anthropic"
	"github.com/noesrafa/sunny/internal/provider/claudecode"
	"github.com/noesrafa/sunny/internal/provider/ollama"
	"github.com/noesrafa/sunny/internal/provider/opencode"
	"github.com/noesrafa/sunny/internal/secrets"
	"github.com/noesrafa/sunny/internal/server"
	"github.com/noesrafa/sunny/internal/store"
	"github.com/noesrafa/sunny/internal/tools"
	"github.com/noesrafa/sunny/internal/tsnet"
)

// serve runs the daemon in the foreground. Used by `start` (re-exec'd
// as a detached child) and directly when debugging.
//
// The engine is held behind atomic.Pointer so PUT /secrets can swap it
// in place — the http.Server keeps running with the same handler;
// only the routing-to-provider logic re-resolves.
func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "HTTP listen address")
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	seeded, err := bootstrap.EnsureRuntime(*root)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	if seeded {
		log.Info("seeded runtime from defaults", "root", *root)
	} else {
		log.Info("using existing runtime", "root", *root)
	}

	st, err := store.Load(*root)
	if err != nil {
		return fmt.Errorf("load store: %w", err)
	}
	log.Info("store loaded", "agents", len(st.Agents()))

	tok, err := auth.EnsureToken(*root)
	if err != nil {
		return fmt.Errorf("ensure token: %w", err)
	}
	log.Info("auth ready", "token_path", auth.Path(*root))

	secretsStore, err := secrets.New(*root)
	if err != nil {
		return fmt.Errorf("load secrets: %w", err)
	}
	log.Info("secrets ready", "path", secrets.Path(*root))

	var enginePtr atomic.Pointer[engine.Engine]
	enginePtr.Store(buildEngine(log, secretsStore))
	convs := conversation.NewStore(*root)

	rebuild := func() {
		log.Info("rebuilding engine after secrets change")
		enginePtr.Store(buildEngine(log, secretsStore))
	}

	pairs := pairing.NewService(tok)
	hub := events.New(log)

	// Mesh key: load if present, generate-and-save if absent so a
	// fresh install is mesh-ready by default. Failure to write is
	// non-fatal — daemon still serves bearer-only.
	meshKey, mErr := mesh.Load(*root)
	if mErr != nil && errors.Is(mErr, mesh.ErrAbsent) {
		generated, gErr := mesh.Generate()
		if gErr == nil {
			if sErr := mesh.Save(*root, generated); sErr == nil {
				meshKey = generated
				log.Info("mesh key generated", "fingerprint", meshKey.Fingerprint())
			}
		}
	} else if mErr == nil {
		log.Info("mesh key loaded", "fingerprint", meshKey.Fingerprint())
	}

	srv := &http.Server{
		Addr: *addr,
		Handler: server.New(server.Options{
			Store:         st,
			Conversations: convs,
			Secrets:       secretsStore,
			Engine:        &enginePtr,
			Log:           log,
			Token:         tok,
			RebuildEngine: rebuild,
			Pairs:         pairs,
			Hub:           hub,
			MeshKey:       meshKey,
			Version:       version,
			InstanceID:    *root, // not strictly stable across reinstalls; good enough for v0.16
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Resolve every interface we want to listen on. The primary
	// `--addr` (usually 127.0.0.1:7777) is non-negotiable; the
	// optional tailnet bind kicks in only when tailscale is
	// installed AND logged in. Bind failures on the tailnet are
	// non-fatal — the daemon stays up on its primary address with
	// a warning, so a misconfigured Tailscale doesn't kill chat.
	addrs := []string{*addr}
	if extra := tailnetBind(*addr, log); extra != "" && extra != *addr {
		addrs = append(addrs, extra)
	}

	listeners := make([]net.Listener, 0, len(addrs))
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			if a == *addr {
				return fmt.Errorf("listen %s: %w", a, err)
			}
			log.Warn("tailnet listener skipped", "addr", a, "err", err.Error())
			continue
		}
		listeners = append(listeners, ln)
	}

	errCh := make(chan error, len(listeners))
	for _, ln := range listeners {
		ln := ln
		go func() {
			log.Info("listening", "addr", ln.Addr().String())
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		return srv.Shutdown(shutdownCtx)
	}
}

// tailnetBind detects the tailnet IPv4 (when tailscale is installed
// and logged in) and returns "<ip>:<port>" using the same port as
// primary. Returns "" when there's nothing to do — caller skips the
// extra listener silently. Errors are logged at debug level so they
// don't spam the operator on a non-tailscale host.
func tailnetBind(primary string, log *slog.Logger) string {
	if !tsnet.Available() {
		return ""
	}
	ip, err := tsnet.LocalIP()
	if err != nil {
		log.Debug("tailscale ip lookup failed; skipping tailnet bind", "err", err.Error())
		return ""
	}
	// Reuse the port from --addr.
	_, port, err := net.SplitHostPort(primary)
	if err != nil || port == "" {
		log.Debug("could not split addr for tailnet bind", "addr", primary)
		return ""
	}
	return net.JoinHostPort(ip, port)
}

// buildEngine probes every supported provider and returns an engine
// indexing all the ones that succeeded. The default provider is the
// first one to come up in declaration order, or whatever
// SUNNY_PROVIDER pins.
//
// Order: claude-code → anthropic → ollama → opencode. claude-code
// wins by default because it inherits the user's claude.ai login (no
// separate API key) AND brings claude code's full toolset. anthropic
// and ollama depend on `secrets.yaml` (or env vars). opencode is
// last because it requires its own auth setup (`opencode auth login`)
// in a separate config tree.
//
// Returning a zero-engine (no providers) is OK — chat returns 503
// until at least one driver is configured.
func buildEngine(log *slog.Logger, s *secrets.Store) *engine.Engine {
	toolReg := tools.Default()
	choice := strings.ToLower(strings.TrimSpace(os.Getenv("SUNNY_PROVIDER")))
	if choice == "off" {
		log.Info("engine disabled", "reason", "SUNNY_PROVIDER=off")
		return engine.New(nil, "", toolReg)
	}

	registry := map[string]provider.Provider{}
	tryAdd := func(name string, drv provider.Provider, err error) {
		if err != nil {
			log.Debug("provider skipped", "name", name, "reason", err.Error())
			return
		}
		registry[name] = drv
		log.Info("provider ready", "name", name)
	}

	cc, ccErr := claudecode.New()
	tryAdd("claude-code", cc, ccErr)
	an, anErr := anthropic.New(s)
	tryAdd("anthropic", an, anErr)
	ol, olErr := ollama.New(s)
	tryAdd("ollama", ol, olErr)
	oc, ocErr := opencode.New()
	tryAdd("opencode", oc, ocErr)

	defaultName := pickDefaultProvider(choice, registry, log)
	if defaultName == "" {
		log.Warn("no providers available — configure one via `sunny secrets <provider> set api_key` or install claude code")
	}
	return engine.New(registry, defaultName, toolReg)
}

// pickDefaultProvider honors SUNNY_PROVIDER if it points at a
// registered driver, else falls back to the natural priority order.
func pickDefaultProvider(choice string, reg map[string]provider.Provider, log *slog.Logger) string {
	switch choice {
	case "claude-code", "claude_code", "claudecode":
		if _, ok := reg["claude-code"]; ok {
			return "claude-code"
		}
		log.Warn("SUNNY_PROVIDER=claude-code but claude CLI not available")
	case "anthropic":
		if _, ok := reg["anthropic"]; ok {
			return "anthropic"
		}
		log.Warn("SUNNY_PROVIDER=anthropic but api_key not configured")
	case "ollama":
		if _, ok := reg["ollama"]; ok {
			return "ollama"
		}
		log.Warn("SUNNY_PROVIDER=ollama but api_key not configured")
	case "opencode":
		if _, ok := reg["opencode"]; ok {
			return "opencode"
		}
		log.Warn("SUNNY_PROVIDER=opencode but opencode CLI not available")
	}
	for _, n := range []string{"claude-code", "anthropic", "ollama", "opencode"} {
		if _, ok := reg[n]; ok {
			return n
		}
	}
	return ""
}

// quiet compile-time check that drivers satisfy provider.Provider.
var (
	_ provider.Provider = (*anthropic.Driver)(nil)
	_ provider.Provider = (*claudecode.Driver)(nil)
	_ provider.Provider = (*ollama.Driver)(nil)
	_ provider.Provider = (*opencode.Driver)(nil)
)
