package main

import (
	"flag"
	"fmt"
	"os"

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
// Same auto-start contract as `sunny tui`: if --no-auto-start is
// passed and the daemon isn't running, we error out cleanly rather
// than silently bypass.
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
	prog := tea.NewProgram(model)
	_, err = prog.Run()
	return err
}
