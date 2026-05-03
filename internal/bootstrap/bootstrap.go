// Package bootstrap seeds the runtime directory from the embedded defaults
// on first run. Once the agents tree exists the user owns it.
package bootstrap

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/noesrafa/sunny/defaults"
)

// EnsureRuntime copies the embedded default tree into root the first time
// the daemon boots. Returns true if a fresh seed happened.
//
// We gate on the existence of root/agents, not root itself: `sunny start`
// creates root/run/ before spawning the daemon, so root is already present
// by the time serve calls EnsureRuntime. The agents/ subtree is the real
// "fresh install" marker.
func EnsureRuntime(root string) (seeded bool, err error) {
	agentsDir := filepath.Join(root, "agents")
	if _, err := os.Stat(agentsDir); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return false, err
	}
	err = fs.WalkDir(defaults.FS, "agents", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(root, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(defaults.FS, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
