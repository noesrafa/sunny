package main

import (
	"flag"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/onboarding"
)

// uninstallCmd is `sunny uninstall`: a styled bubbletea flow that
// stops the daemon, asks before removing ~/.sunny/, and either runs
// `brew uninstall sunny` or `rm` of the binary depending on how
// sunny was installed.
//
// Defaults are conservative: user data (~/.sunny) is NEVER removed
// without an explicit Yes. Pass --yes to skip every prompt (useful
// in scripts/CI). --keep-data always preserves ~/.sunny.
func uninstallCmd(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	yes := fs.Bool("yes", false, "answer yes to all prompts (deletes data without asking)")
	keepData := fs.Bool("keep-data", false, "skip the prompt and keep ~/.sunny/ no matter what")
	if err := fs.Parse(args); err != nil {
		return err
	}

	model := onboarding.NewUninstallModel(onboarding.UninstallOptions{
		Root:     *root,
		Yes:      *yes,
		KeepData: *keepData,
	})
	prog := tea.NewProgram(model)
	_, err := prog.Run()
	return err
}
