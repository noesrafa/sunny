package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/noesrafa/sunny/internal/lifecycle"
)

// start spawns the daemon detached. `sunny` (no args) calls
// startDaemon directly via the auto-start path; this command is for
// users who want the daemon up before opening the TUI.
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
// ifNotRunning is true and the daemon is already alive at the
// recorded addr, it returns the existing state without spawning.
//
// Used by `sunny start` (ifNotRunning=false → errors on duplicate)
// and the no-arg auto-start flow (ifNotRunning=true → silently
// reuses).
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

	if err := waitHealthy(addr, cmd.Process.Pid, 3*time.Second); err != nil {
		// Daemon failed to come up. Reap and clean state so the next
		// invocation gets a fresh attempt instead of inheriting stale
		// state.json. Surface the tail of the log for diagnosis.
		if lifecycle.IsAlive(cmd.Process.Pid) {
			_ = cmd.Process.Kill()
		}
		_ = paths.ClearState()
		if tail := tailLog(paths.Log, 20); tail != "" {
			return nil, fmt.Errorf("%w\n--- last lines of %s ---\n%s", err, paths.Log, tail)
		}
		return nil, fmt.Errorf("%w (see %s)", err, paths.Log)
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

// update upgrades sunny via Homebrew and restarts the daemon so the
// new binary takes effect. Defaults: brew upgrade noesrafa/tap/
// sunny, then `sunny restart`. --no-restart skips the restart for
// users running it inside scripts that own daemon lifecycle.
//
// Today this assumes Homebrew. Curl/manual installations can run
// their own upgrade flow; a future version could detect install
// method and do the right thing.
func update(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	noRestart := fs.Bool("no-restart", false, "skip restarting the daemon after the upgrade")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("brew not on PATH — install Homebrew from https://brew.sh or upgrade sunny manually")
	}

	fmt.Printf("current: sunny %s\n", version)
	fmt.Println("upgrading via brew…")
	cmd := exec.Command("brew", "upgrade", "noesrafa/tap/sunny")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brew upgrade: %w", err)
	}

	// Print the version after upgrade by exec'ing the on-disk
	// binary — version is a linker-set string in the OLD binary
	// that's still running this process. The on-disk binary is the
	// new one (brew replaced it), so `sunny version` reflects
	// what the next launch will use.
	if binary, err := os.Executable(); err == nil {
		if out, err := exec.Command(binary, "version").Output(); err == nil {
			fmt.Printf("now:     %s", string(out))
		}
	}

	if *noRestart {
		return nil
	}
	fmt.Println()
	return restart(nil)
}

// restart stops the running daemon (if any) and starts a fresh one
// on the same address. The default --addr matches whatever the
// running daemon is bound to, so a bare `sunny restart` is the
// idiomatic "I just upgraded the binary, swap the daemon" command.
func restart(args []string) error {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	addr := fs.String("addr", "", "HTTP listen address (defaults to the running daemon's addr, or 127.0.0.1:7777)")
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	timeout := fs.Duration("timeout", 5*time.Second, "graceful shutdown wait before SIGKILL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths := lifecycle.PathsFor(*root)

	// Inherit the running daemon's addr by default. Falling back to
	// the documented default keeps `sunny restart` working on a
	// fresh install where no daemon is up yet.
	resolvedAddr := *addr
	if state, err := paths.LoadState(); err == nil {
		if resolvedAddr == "" {
			resolvedAddr = state.Addr
		}
		if lifecycle.IsAlive(state.PID) {
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
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if lifecycle.IsAlive(state.PID) {
				_ = proc.Signal(syscall.SIGKILL)
				fmt.Printf("stopped  pid=%d  (SIGKILL after %s)\n", state.PID, *timeout)
			} else {
				fmt.Printf("stopped  pid=%d\n", state.PID)
			}
		}
		_ = paths.ClearState()
	}

	if resolvedAddr == "" {
		resolvedAddr = "127.0.0.1:7777"
	}
	if _, err := startDaemon(resolvedAddr, *root, false); err != nil {
		return err
	}
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

// waitHealthy polls /healthz until it returns OK or until the spawned
// daemon process exits. The PID check shortcuts the timeout when the
// daemon crashes during boot — without it, callers would waste 3s
// waiting on a corpse.
func waitHealthy(addr string, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := pingHealth(addr, 200*time.Millisecond); err == nil {
			return nil
		}
		if !lifecycle.IsAlive(pid) {
			return fmt.Errorf("daemon process exited before becoming healthy")
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

// tailLog returns the last n lines of the file at path, or "" if the
// file can't be read. Best-effort — bounded by reading the whole file
// since sunny.log stays small in practice.
func tailLog(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
