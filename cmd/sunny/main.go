// Command sunny is the unified CLI: TUI client, daemon (`serve`), and
// utility commands. The dispatch is kept thin — the heavy lifting
// lives in sibling files (tui.go, serve.go, lifecycle.go, token.go,
// secrets_cmd.go).
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// version is set by the linker at release time via -ldflags. For
// local `go build` it stays as "dev".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		// Bare `sunny` opens the TUI against the default daemon.
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
	case "restart":
		err = restart(args)
	case "update":
		err = update(args)
	case "status":
		err = status(args)
	case "serve":
		err = serve(args)
	case "token":
		err = token(args)
	case "secrets":
		err = secretsCmd(args)
	case "peers":
		err = peersCmd(args)
	case "pair":
		err = pairCmd(args)
	case "mesh":
		err = meshCmd(args)
	case "doctor":
		err = doctorCmd(args)
	case "setup":
		err = setupCmd(args)
	case "onboarding", "onboard":
		err = onboardingCmd(args)
	case "uninstall":
		err = uninstallCmd(args)
	case "app":
		err = appCmd(args)
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
  restart   Stop the running daemon (if any) and start a fresh one — handy after 'brew upgrade'.
  update    'brew upgrade noesrafa/tap/sunny' + restart the daemon. One command for the upgrade dance.
  status    Show whether the daemon is running, plus pid, addr, uptime.
  serve     Run the daemon in the foreground (advanced; prefer 'start').
  token     Print the daemon's bearer token. 'sunny token rotate' regenerates it.
  secrets   Manage provider keys. 'sunny secrets' lists, 'sunny secrets <p> set <field>' reads from stdin.
  peers     Manage federated daemons. 'sunny peers' lists, 'sunny peers add <name> <url>' adds (token from stdin).
  pair      Pair two daemons over a one-time code. 'sunny pair offer' on the remote, 'sunny pair claim <url> <code>' on the client.
  mesh      Manage shared tailnet mesh key. 'sunny mesh export' on one host, 'sunny mesh import <key>' on another → zero-config discovery between them.
  doctor    Print a checklist: providers (✓/⚠/✗), daemon, runtime. Run this first when something feels off.
  setup     Get a provider ready. 'sunny setup' walks you through it; 'sunny setup <p>' targets one.
  onboarding Interactive first-run flow: tailscale, brew, providers, first agent. Re-runnable as a manual doctor.
  uninstall Stop the daemon and clean sunny off the system. Asks before deleting ~/.sunny/.
  app       Print a QR for the iOS/Android app — one scan and the phone is paired.
  version   Print version.

common flags:
  --addr   HTTP listen address (default 127.0.0.1:7777)
  --root   sunny runtime directory (default ~/.sunny)`)
}

// defaultRoot returns ~/.sunny, falling back to ".sunny" in the cwd if
// the home dir can't be resolved (only fires in unusual environments
// like a container without HOME set).
func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sunny"
	}
	return filepath.Join(home, ".sunny")
}
