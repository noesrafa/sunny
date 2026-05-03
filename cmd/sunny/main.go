package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/noesrafa/sunny/internal/bootstrap"
	"github.com/noesrafa/sunny/internal/engine"
	"github.com/noesrafa/sunny/internal/lifecycle"
	"github.com/noesrafa/sunny/internal/logger"
	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/provider/anthropic"
	"github.com/noesrafa/sunny/internal/provider/claudecode"
	"github.com/noesrafa/sunny/internal/server"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/store"
	"github.com/noesrafa/sunny/internal/tui"
)

// version is set by the linker at release time via -ldflags. For local
// `go build` it stays as "dev".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		// `sunny` with no arguments opens the TUI against the default daemon.
		if err := openTUI(nil); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "tui":
		err = openTUI(args)
	case "start":
		err = start(args)
	case "stop":
		err = stop(args)
	case "status":
		err = status(args)
	case "serve":
		err = serve(args)
	case "version", "-v", "--version":
		fmt.Println("sunny", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: sunny [command] [args]

commands:
  (none)    Open the TUI (alias for 'tui').
  tui       Open the TUI client. --addr selects which daemon to connect to.
  start     Run the daemon detached. Logs to <root>/run/sunny.log.
  stop      Stop the running daemon.
  status    Show whether the daemon is running, plus pid, addr, uptime.
  serve     Run the daemon in the foreground (advanced; prefer 'start').
  version   Print version.

common flags:
  --addr   HTTP listen address (default 127.0.0.1:7777)
  --root   sunny runtime directory (default ~/.sunny)`)
}

func openTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "daemon address to connect to")
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	noAutoStart := fs.Bool("no-auto-start", false, "skip auto-starting the daemon if it isn't running")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Auto-start the daemon when it isn't already running. The TUI is
	// useless without a daemon; making the user remember `sunny start`
	// before `sunny` is the wrong default. --no-auto-start opts out for
	// users who want the legacy behavior or who manage the daemon
	// out-of-band (systemd, launchd, etc.).
	if !*noAutoStart {
		paths := lifecycle.PathsFor(*root)
		alive := false
		if s, err := paths.LoadState(); err == nil && lifecycle.IsAlive(s.PID) {
			alive = true
			*addr = s.Addr // honor whatever addr the running daemon is on
		}
		if !alive {
			fmt.Fprintln(os.Stderr, "starting daemon…")
			s, err := startDaemon(*addr, *root, true)
			if err != nil {
				return fmt.Errorf("auto-start: %w", err)
			}
			*addr = s.Addr
		}
	}
	_ = *addr // TODO: pass to TUI options once chat is wired to the daemon

	cwd, _ := os.Getwd()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lg, closer := logger.Setup("sunny")
	defer closer.Close()
	lg.Info("tui starting", "cwd", cwd, "log", logger.LogPath())

	mgr := session.NewManager()
	first, err := session.New(ctx, cwd, session.Options{
		Logger: lg,
		Title:  "sunny",
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	mgr.Add(first)

	model := tui.NewModel(ctx, mgr, cwd, tui.Options{
		Logger:                   lg,
		DefaultModel:             first.Model,
		DefaultEffort:            first.Effort,
		DangerousSkipPermissions: true,
	})
	return model.Run(ctx)
}

func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sunny"
	}
	return filepath.Join(home, ".sunny")
}

// buildEngine picks a provider with this precedence:
//
//  1. SUNNY_PROVIDER env var if set ("claude-code" | "anthropic" | "off"),
//  2. claude-code if the `claude` CLI is on PATH,
//  3. anthropic API if ANTHROPIC_API_KEY is set,
//  4. nothing — daemon serves read-only endpoints; chat returns 503.
//
// Order matters: claude-code wins by default because it inherits the
// user's claude.ai login (no separate API key) AND brings claude code's
// full toolset. The Anthropic API path needs a key but is the right
// choice for headless / cheaper runs.
func buildEngine(log *slog.Logger) *engine.Engine {
	choice := strings.ToLower(strings.TrimSpace(os.Getenv("SUNNY_PROVIDER")))
	switch choice {
	case "off":
		log.Info("engine disabled", "reason", "SUNNY_PROVIDER=off")
		return nil
	case "claude-code", "claude_code", "claudecode":
		drv, err := claudecode.New()
		if err != nil {
			log.Warn("engine disabled", "reason", err.Error(), "wanted", choice)
			return nil
		}
		log.Info("engine ready", "provider", drv.Name(), "source", "SUNNY_PROVIDER")
		return engine.New(drv)
	case "anthropic":
		drv, err := anthropic.New("")
		if err != nil {
			log.Warn("engine disabled", "reason", err.Error(), "wanted", choice)
			return nil
		}
		log.Info("engine ready", "provider", drv.Name(), "source", "SUNNY_PROVIDER")
		return engine.New(drv)
	}

	// Auto: prefer claude-code, then anthropic.
	if drv, err := claudecode.New(); err == nil {
		log.Info("engine ready", "provider", drv.Name(), "source", "auto-detected")
		return engine.New(drv)
	} else {
		log.Debug("claude-code unavailable", "reason", err.Error())
	}
	if drv, err := anthropic.New(""); err == nil {
		log.Info("engine ready", "provider", drv.Name(), "source", "auto-detected")
		return engine.New(drv)
	} else {
		log.Warn("engine disabled",
			"reason", "no provider available — install Claude Code or set ANTHROPIC_API_KEY")
		log.Debug("anthropic unavailable", "reason", err.Error())
	}
	return nil
}

// quiet compile-time check that both drivers satisfy provider.Provider.
var (
	_ provider.Provider = (*anthropic.Driver)(nil)
	_ provider.Provider = (*claudecode.Driver)(nil)
)

// serve runs the daemon in the foreground. Used by `start` (re-exec'd as a
// detached child) and directly when debugging.
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

	// Engine is optional: the daemon still boots and serves read-only
	// endpoints when no provider can be initialized; chat returns 503
	// until one is available.
	eng := buildEngine(log)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(st, eng, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

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

func start(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "HTTP listen address")
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := startDaemon(*addr, *root, false); err != nil {
		return err
	}
	return nil
}

// startDaemon spawns the daemon detached and waits for healthz. When
// ifNotRunning is true and the daemon is already alive at the recorded
// addr, it returns the existing state without spawning. Used by `sunny
// start` (ifNotRunning=false → errors on duplicate) and the no-arg
// auto-start flow (ifNotRunning=true → silently reuses).
func startDaemon(addr, root string, ifNotRunning bool) (*lifecycle.State, error) {
	paths := lifecycle.PathsFor(root)

	if s, err := paths.LoadState(); err == nil {
		if lifecycle.IsAlive(s.PID) {
			if ifNotRunning {
				return s, nil
			}
			return nil, fmt.Errorf("already running (pid=%d, addr=%s) — `sunny stop` first", s.PID, s.Addr)
		}
		_ = paths.ClearState()
	}

	if err := os.MkdirAll(paths.Run, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}

	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()

	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve self: %w", err)
	}

	cmd := exec.Command(binary, "serve", "--addr", addr, "--root", root)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn: %w", err)
	}

	state := &lifecycle.State{
		PID:       cmd.Process.Pid,
		Addr:      addr,
		StartedAt: time.Now(),
		Binary:    binary,
	}
	if err := paths.SaveState(state); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("save state: %w", err)
	}

	if err := waitHealthy(addr, 3*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "warning: started but not yet healthy: %v\n", err)
		fmt.Fprintf(os.Stderr, "         tail %s for details\n", paths.Log)
	}

	fmt.Printf("started  pid=%d  addr=%s  log=%s\n", state.PID, addr, paths.Log)
	return state, nil
}

func stop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	timeout := fs.Duration("timeout", 5*time.Second, "graceful shutdown wait before SIGKILL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths := lifecycle.PathsFor(*root)
	state, err := paths.LoadState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("not running")
			return nil
		}
		return err
	}

	if !lifecycle.IsAlive(state.PID) {
		_ = paths.ClearState()
		fmt.Printf("stale state cleaned up (pid=%d was not alive)\n", state.PID)
		return nil
	}

	proc, err := os.FindProcess(state.PID)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal: %w", err)
	}

	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if !lifecycle.IsAlive(state.PID) {
			_ = paths.ClearState()
			fmt.Printf("stopped  pid=%d\n", state.PID)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	_ = proc.Signal(syscall.SIGKILL)
	_ = paths.ClearState()
	fmt.Printf("stopped  pid=%d  (SIGKILL after %s)\n", state.PID, *timeout)
	return nil
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths := lifecycle.PathsFor(*root)
	state, err := paths.LoadState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("not running")
			return nil
		}
		return err
	}

	if !lifecycle.IsAlive(state.PID) {
		fmt.Printf("stale     pid=%d not alive — run `sunny stop` to clean up\n", state.PID)
		return nil
	}

	uptime := time.Since(state.StartedAt).Round(time.Second)
	fmt.Printf("status:   running\n")
	fmt.Printf("pid:      %d\n", state.PID)
	fmt.Printf("addr:     %s\n", state.Addr)
	fmt.Printf("uptime:   %s\n", uptime)
	fmt.Printf("root:     %s\n", paths.Root)
	fmt.Printf("log:      %s\n", paths.Log)

	if err := pingHealth(state.Addr, 1*time.Second); err == nil {
		fmt.Printf("healthz:  ok\n")
	} else {
		fmt.Printf("healthz:  %v\n", err)
	}
	return nil
}

func waitHealthy(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := pingHealth(addr, 200*time.Millisecond); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("not healthy after %s", timeout)
}

func pingHealth(addr string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
