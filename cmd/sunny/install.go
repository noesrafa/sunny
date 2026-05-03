package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// installerPlan describes per-OS install candidates. The first
// candidate whose tool is on PATH wins; order each list by
// preference. Use this when you have multiple legitimate install
// paths (brew vs curl, npm vs apt) and want to defer to whatever
// the user already has set up.
type installerPlan struct {
	Mac   []candidate
	Linux []candidate
}

// candidate is one runnable install command. Name is the binary
// the candidate needs on PATH ("brew", "curl"); argv is what gets
// exec'd if it wins — argv[0] is the actual binary to run.
type candidate struct {
	name string
	argv []string
}

// pickInstaller returns the first candidate from the plan whose
// required binary is installed on the current OS. Returns nil when
// nothing matches — callers should fall back to printing manual
// instructions.
func pickInstaller(plan installerPlan) *candidate {
	var list []candidate
	switch runtime.GOOS {
	case "darwin":
		list = plan.Mac
	case "linux":
		list = plan.Linux
	}
	for _, c := range list {
		if _, err := exec.LookPath(c.name); err == nil {
			cp := c
			return &cp
		}
	}
	return nil
}

// runInstall confirms with the user and runs the install command,
// streaming its output to the terminal. With --print-only it shows
// the command and exits without running. On success it prints the
// post-install guidance lines verbatim — typically "to finish,
// run: <login command>".
func runInstall(name string, cmd *candidate, printOnly bool, postInstall []string) error {
	pretty := strings.Join(cmd.argv, " ")
	if printOnly {
		fmt.Printf("To install %s, run:\n  %s\n\n", name, pretty)
		printLines(postInstall)
		return nil
	}

	fmt.Printf("Will run:\n  %s\n", pretty)
	fmt.Print("Proceed? [Y/n]: ")
	if !confirm() {
		fmt.Println("aborted.")
		return nil
	}
	fmt.Println()

	c := exec.Command(cmd.argv[0], cmd.argv[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	fmt.Printf("\n✓ %s installed\n\n", name)
	printLines(postInstall)
	return nil
}

func printLines(lines []string) {
	for _, line := range lines {
		fmt.Println(line)
	}
}
