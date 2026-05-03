package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/noesrafa/sunny/internal/doctor"
	"github.com/noesrafa/sunny/internal/secrets"
)

// setupCmd is the user-friendly entrypoint for getting a provider
// ready to chat:
//
//	sunny setup                → doctor + interactive picker
//	sunny setup <provider>     → flow for that provider
//	sunny setup … --print-only → never runs an installer or writes
//	                             a secret; only prints what would
//	                             happen. Useful in CI / over SSH.
//
// Per-provider flows live below. Dispatch is intentionally tiny —
// no plug-in registry — because there are four providers and that
// number changes maybe once a year.
func setupCmd(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	printOnly := fs.Bool("print-only", false, "print commands instead of running them")

	// Accept flags on either side of the provider name. Go's flag
	// package stops at the first positional, so we partition args
	// ourselves: first non-flag wins as the provider, everything else
	// goes to flag.Parse.
	var provider string
	var flagArgs []string
	for _, a := range args {
		if provider == "" && !strings.HasPrefix(a, "-") {
			provider = a
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if provider == "" {
		return setupInteractive(*root, *printOnly)
	}
	return dispatchSetup(provider, *root, *printOnly)
}

// dispatchSetup routes a provider name to its flow. Shared by the
// CLI form (sunny setup <p>) and the interactive picker.
func dispatchSetup(provider, root string, printOnly bool) error {
	switch provider {
	case "claude-code", "claude_code", "claudecode":
		return setupClaudeCode(printOnly)
	case "opencode":
		return setupOpencode(printOnly)
	case "anthropic":
		return setupAPIKey(root, "anthropic", "api_key", "https://console.anthropic.com/settings/keys", printOnly)
	case "ollama":
		return setupAPIKey(root, "ollama", "api_key", "https://ollama.com/settings/keys", printOnly)
	default:
		return fmt.Errorf("unknown provider %q (try: claude-code, opencode, anthropic, ollama)", provider)
	}
}

// setupInteractive runs the doctor first, then prompts the user to
// pick a provider that needs attention. Plays nice in non-tty
// environments by listing options and exiting.
func setupInteractive(root string, printOnly bool) error {
	report := doctor.Run(root)
	renderReport(os.Stdout, report)

	var pending []doctor.Result
	for _, p := range report.Providers {
		if p.Status != doctor.StatusOK {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		fmt.Println("\nAll providers ready. Nothing to do.")
		return nil
	}

	fmt.Println("\nPick a provider to configure:")
	for i, p := range pending {
		fmt.Printf("  %d) %s — %s\n", i+1, p.Name, p.Detail)
	}

	if !isTTY() {
		fmt.Println("\n(stdin is not a tty — pass a provider name explicitly: sunny setup <name>)")
		return nil
	}

	fmt.Print("\nNumber [1]: ")
	choice := readLine()
	if choice == "" {
		choice = "1"
	}
	idx := 0
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(pending) {
		return fmt.Errorf("invalid choice: %q", choice)
	}
	fmt.Println()
	return dispatchSetup(pending[idx-1].Name, root, printOnly)
}

// setupClaudeCode installs the `claude` CLI if missing, then prints
// the login command (which is interactive and we deliberately don't
// drive — opening browsers and pasting codes is the user's job).
func setupClaudeCode(printOnly bool) error {
	postInstall := []string{
		"To finish setup, log in:",
		"  claude /login",
	}
	if _, err := exec.LookPath("claude"); err == nil {
		printAlreadyInstalled("claude", postInstall)
		return nil
	}
	cmd := pickInstaller(installerPlan{
		Mac: []candidate{
			{name: "brew", argv: []string{"brew", "install", "--cask", "claude-code"}},
			{name: "curl", argv: []string{"sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash"}},
		},
		Linux: []candidate{
			{name: "curl", argv: []string{"sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash"}},
		},
	})
	if cmd == nil {
		return printManualInstall("claude-code", "https://docs.claude.com/en/docs/claude-code/quickstart")
	}
	return runInstall("claude-code", cmd, printOnly, postInstall)
}

// setupOpencode installs the `opencode` CLI if missing, then directs
// the user to its own auth flow.
func setupOpencode(printOnly bool) error {
	postInstall := []string{
		"To finish setup, log in to a provider:",
		"  opencode auth login",
	}
	if _, err := exec.LookPath("opencode"); err == nil {
		printAlreadyInstalled("opencode", postInstall)
		return nil
	}
	cmd := pickInstaller(installerPlan{
		Mac: []candidate{
			{name: "brew", argv: []string{"brew", "install", "sst/tap/opencode"}},
			{name: "curl", argv: []string{"sh", "-c", "curl -fsSL https://opencode.ai/install | bash"}},
		},
		Linux: []candidate{
			{name: "curl", argv: []string{"sh", "-c", "curl -fsSL https://opencode.ai/install | bash"}},
		},
	})
	if cmd == nil {
		return printManualInstall("opencode", "https://opencode.ai/docs/")
	}
	return runInstall("opencode", cmd, printOnly, postInstall)
}

// setupAPIKey is the shared flow for providers whose only setup is
// "save an API key in secrets.yaml" (anthropic, ollama, and any
// future addition with the same shape). Reminds the user to bounce
// the daemon afterwards: existing providers re-read on every Stream
// call, but a brand-new one needs the registry rebuilt.
func setupAPIKey(root, provider, field, hintURL string, printOnly bool) error {
	store, err := secrets.New(root)
	if err != nil {
		return fmt.Errorf("open secrets: %w", err)
	}

	if printOnly {
		fmt.Printf("Get an API key from: %s\n", hintURL)
		fmt.Printf("Then run: sunny secrets %s set %s\n", provider, field)
		return nil
	}

	fmt.Printf("Get your API key from:\n  %s\n\n", hintURL)
	fmt.Println("Paste it here (input is hidden when piped; otherwise echoes):")
	value, err := readSecretValue(provider + "." + field)
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("empty value — refusing to save")
	}
	if err := store.SetField(provider, field, value); err != nil {
		return fmt.Errorf("save: %w", err)
	}

	fmt.Printf("\n✓ saved %s.%s\n\n", provider, field)
	fmt.Println("If the daemon is running, restart it so the new provider")
	fmt.Println("enters the registry:")
	fmt.Println("  sunny stop && sunny start")
	return nil
}

func printAlreadyInstalled(bin string, postInstall []string) {
	fmt.Printf("✓ `%s` is already on PATH\n\n", bin)
	for _, line := range postInstall {
		fmt.Println(line)
	}
	fmt.Println("\nThen test from sunny:")
	fmt.Println("  sunny doctor")
}

func printManualInstall(name, docsURL string) error {
	fmt.Println("Could not find a supported installer (brew, curl) on this system.")
	fmt.Printf("Install %s manually:\n  %s\n", name, docsURL)
	return nil
}
