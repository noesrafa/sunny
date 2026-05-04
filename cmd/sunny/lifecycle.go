package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// update upgrades sunny in place and restarts the daemon so the new
// binary takes effect. Tries Homebrew first; on any brew failure
// (no brew, formula not installed via brew, network issue) it falls
// back to downloading the latest GitHub release tarball and atomic-
// renaming the binary into place. --no-restart skips the restart
// for callers that own daemon lifecycle.
//
// On VPS / Linux installs done with the curl one-liner, the
// fallback is the path that actually runs.
func update(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	noRestart := fs.Bool("no-restart", false, "skip restarting the daemon after the upgrade")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Printf("current: sunny %s\n", version)

	upgraded := false
	if _, err := exec.LookPath("brew"); err == nil {
		fmt.Println("upgrading via brew…")
		cmd := exec.Command("brew", "upgrade", "noesrafa/tap/sunny")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err == nil {
			upgraded = true
		} else {
			fmt.Fprintln(os.Stderr, "brew upgrade did not succeed — falling back to GitHub release download")
		}
	}
	if !upgraded {
		if err := updateViaRelease(); err != nil {
			return err
		}
	}

	// Re-exec on-disk binary to get the post-upgrade version.
	// This process still has the old linker-set `version` constant;
	// the new binary is what shows up to future invocations.
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

// updateViaRelease pulls the latest GitHub release matching the
// host's GOOS/GOARCH and replaces the running binary atomically.
// The atomic rename trick lets the in-flight process keep using its
// open inode while new invocations get the freshly-installed bits;
// the next `sunny restart` then picks up the new code.
func updateViaRelease() error {
	const apiURL = "https://api.github.com/repos/noesrafa/sunny/releases/latest"
	httpClient := &http.Client{Timeout: 30 * time.Second}

	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	type release struct {
		TagName string  `json:"tag_name"`
		Assets  []asset `json:"assets"`
	}

	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return fmt.Errorf("fetch latest release info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch latest release info: status %d", resp.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return fmt.Errorf("decode release info: %w", err)
	}

	suffix := "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	var dlURL, dlName string
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, suffix) {
			dlURL, dlName = a.URL, a.Name
			break
		}
	}
	if dlURL == "" {
		return fmt.Errorf("no asset matching %s/%s in release %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}

	fmt.Printf("downloading %s…\n", dlName)
	dlResp, err := httpClient.Get(dlURL)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download asset: status %d", dlResp.StatusCode)
	}

	binData, err := extractSunnyBinary(dlResp.Body)
	if err != nil {
		return err
	}

	// Resolve symlinks so we operate on the real file on disk
	// (e.g. /usr/local/bin/sunny might link to ~/.local/bin/sunny).
	cur, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(cur); err == nil {
		cur = real
	}
	dir := filepath.Dir(cur)
	tmpPath := filepath.Join(dir, ".sunny.update.tmp")
	if err := os.WriteFile(tmpPath, binData, 0o755); err != nil {
		return fmt.Errorf("write new binary to %s: %w (try sudo if permission denied)", tmpPath, err)
	}
	if err := os.Rename(tmpPath, cur); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install new binary at %s: %w (try sudo if permission denied)", cur, err)
	}
	fmt.Printf("installed %s at %s\n", rel.TagName, cur)
	return nil
}

// extractSunnyBinary streams a gzipped tar archive looking for the
// `sunny` executable entry and returns its bytes. Other files in
// the archive (README, LICENSE) are ignored.
func extractSunnyBinary(r io.Reader) ([]byte, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "sunny" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read sunny entry: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("`sunny` binary not found in tarball")
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
