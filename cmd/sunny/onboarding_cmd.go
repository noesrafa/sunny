package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/lifecycle"
	"github.com/noesrafa/sunny/internal/onboarding"
)

// onboardingCmd is the entrypoint for `sunny onboarding`. It auto-
// starts the daemon (the onboarding model needs the daemon to PUT
// secrets and PATCH the default agent) and then hands off control to
// the bubbletea program.
//
// Flow contract:
//   - --no-auto-start: error out if the daemon isn't running.
//   - SIGINT (ctrl+c) cancels the bubbletea context, ensuring the
//     program exits even if a subprocess install is in flight (the
//     subprocess goroutine is detached at that point — its result
//     is just discarded).
//   - If the user presses enter on the Done step, the model sets
//     ShouldLaunchTUI=true and we exec `sunny tui` so they land in
//     a usable chat instead of an empty terminal.
func onboardingCmd(args []string) error {
	fs := flag.NewFlagSet("onboarding", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "daemon address to connect to")
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	noAutoStart := fs.Bool("no-auto-start", false, "skip auto-starting the daemon if it isn't running")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*noAutoStart {
		paths := lifecycle.PathsFor(*root)
		alive := false
		if s, err := paths.LoadState(); err == nil && lifecycle.IsAlive(s.PID) {
			alive = true
			*addr = s.Addr
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

	tok, err := auth.LoadToken(*root)
	if err != nil {
		return fmt.Errorf("load token: %w", err)
	}

	model, err := onboarding.New(*root, *addr, tok)
	if err != nil {
		return err
	}

	// Signal-cancellable context so ctrl+c immediately tears the
	// bubbletea program down, not just the in-flight install
	// subprocess. Without this, a slow `brew install` could keep the
	// program "alive" in the user's perception even after they hit
	// ctrl+c.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	prog := tea.NewProgram(model, tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil {
		return err
	}

	// If the user finished cleanly (enter on Done), chain into the
	// TUI. Replacing the process via syscall.Exec keeps the same
	// pid + tty, which is the cleanest handoff. Falls back to
	// exec.Command if Exec isn't supported (Windows would be the
	// edge — sunny doesn't ship there yet).
	if m, ok := final.(*onboarding.Model); ok && m.ShouldLaunchTUI() {
		return launchTUI()
	}
	return nil
}

// launchTUI replaces the current process with `sunny tui`. After the
// onboarding bubbletea program has restored the terminal, syscall.Exec
// hands the tty cleanly to the TUI without an intermediate flicker.
func launchTUI() error {
	bin, err := os.Executable()
	if err != nil {
		// Fall back to PATH lookup; harmless if `sunny` is on the
		// user's path (Homebrew install path).
		if p, lerr := exec.LookPath("sunny"); lerr == nil {
			bin = p
		} else {
			return fmt.Errorf("locate sunny binary: %w", err)
		}
	}
	args := []string{bin, "tui"}
	env := os.Environ()
	return syscall.Exec(bin, args, env)
}
