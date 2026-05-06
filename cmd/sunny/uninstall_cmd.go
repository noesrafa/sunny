package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// uninstallCmd is `sunny uninstall`: a guided cleanup that stops the
// daemon, optionally removes ~/.sunny/, and then either runs
// `brew uninstall sunny` (+ untap) or `rm` of the binary, depending
// on how sunny was installed.
//
// Defaults are conservative — the user data (~/.sunny) is NEVER
// removed without an explicit "yes" prompt, and the brew untap step
// asks too. Pass --yes to skip prompts (useful for scripts and CI).
func uninstallCmd(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	yes := fs.Bool("yes", false, "answer yes to all prompts (deletes data without asking)")
	keepData := fs.Bool("keep-data", false, "skip the prompt and keep ~/.sunny/ no matter what")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Println("sunny uninstall — let's clean things up.")
	fmt.Println()

	// 1. Stop the daemon if it's running. We don't ask; a daemon
	// running while we uninstall is just confusing.
	fmt.Println("→ stopping the daemon if it's running…")
	if err := stop(nil); err != nil {
		// Common case: daemon wasn't running. The stop helper
		// already prints a friendly message; carry on.
		fmt.Println("  (no daemon was running — ok)")
	}

	// 2. Decide whether to remove ~/.sunny/. This is the user's data;
	// we are extremely deliberate about removing it.
	rootPath := *root
	if exists(rootPath) && !*keepData {
		remove := *yes
		if !remove {
			fmt.Println()
			fmt.Printf("→ delete %s? this includes ALL agents, conversations, secrets,\n", rootPath)
			fmt.Println("  and runs/monitors. There is no undo.")
			remove = promptYesNo("  delete ~/.sunny/?", false)
		}
		if remove {
			if err := os.RemoveAll(rootPath); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ rm %s: %v\n", rootPath, err)
			} else {
				fmt.Printf("  ✓ removed %s\n", rootPath)
			}
		} else {
			fmt.Printf("  · keeping %s — your agents and conversations are safe.\n", rootPath)
		}
	}

	// 3. Detect how sunny was installed and remove the binary.
	binary := detectSunnyBinary()
	if binary == "" {
		fmt.Println()
		fmt.Println("→ sunny binary path not found on PATH (probably already gone).")
	} else if isBrewInstalled(binary) {
		fmt.Println()
		fmt.Println("→ detected brew install. Running `brew uninstall sunny`…")
		if err := runStreaming("brew", "uninstall", "sunny"); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ brew uninstall failed: %v\n", err)
		}
		// Tap removal — only ask if `brew tap` shows our tap.
		if hasBrewTap("noesrafa/sunny") {
			fmt.Println()
			untap := *yes
			if !untap {
				untap = promptYesNo("  remove the brew tap noesrafa/sunny too?", true)
			}
			if untap {
				if err := runStreaming("brew", "untap", "noesrafa/sunny"); err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ brew untap failed: %v\n", err)
				}
			}
		}
	} else {
		fmt.Println()
		fmt.Printf("→ standalone binary at %s.\n", binary)
		remove := *yes
		if !remove {
			remove = promptYesNo("  delete the binary?", true)
		}
		if remove {
			if err := os.Remove(binary); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ rm %s: %v\n", binary, err)
			} else {
				fmt.Printf("  ✓ removed %s\n", binary)
			}
		}
	}

	fmt.Println()
	fmt.Println("done. Thanks for trying sunny — feedback always welcome.")
	return nil
}

// detectSunnyBinary returns the path of the currently-running sunny
// binary, or "" if it can't be resolved. We prefer os.Executable()
// because it's the canonical reference; LookPath is a fallback for
// the rare case Executable() returns a temp shim.
func detectSunnyBinary() string {
	if path, err := os.Executable(); err == nil {
		// Resolve symlinks so /opt/homebrew/bin/sunny → cellar/.../sunny.
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return real
		}
		return path
	}
	if path, err := exec.LookPath("sunny"); err == nil {
		return path
	}
	return ""
}

// isBrewInstalled reports whether the binary path looks like a
// Homebrew install (lives under brew's prefix). Same heuristic
// `sunny update` uses internally.
func isBrewInstalled(binary string) bool {
	if binary == "" {
		return false
	}
	for _, prefix := range []string{"/opt/homebrew/", "/usr/local/Cellar/", "/home/linuxbrew/"} {
		if strings.HasPrefix(binary, prefix) {
			return true
		}
	}
	// Fallback: ask brew if it knows about us.
	if out, err := exec.Command("brew", "list", "sunny").Output(); err == nil && len(out) > 0 {
		return true
	}
	return false
}

// hasBrewTap reports whether `brew tap` lists the given tap.
func hasBrewTap(name string) bool {
	out, err := exec.Command("brew", "tap").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// runStreaming runs a command with stdout/stderr inherited so the
// user sees progress (brew is chatty during install/uninstall).
func runStreaming(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// promptYesNo asks one [y/N] question on stdin. defaultYes flips
// the default to capital Y for "press enter to confirm" UX.
func promptYesNo(prompt string, defaultYes bool) bool {
	suffix := " [y/N] "
	if defaultYes {
		suffix = " [Y/n] "
	}
	fmt.Print(prompt + suffix)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return defaultYes
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return defaultYes
	}
	return answer == "y" || answer == "yes"
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
