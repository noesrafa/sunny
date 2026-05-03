// Package bootstrap seeds the runtime directory from the embedded defaults
// on first run. Once the directory exists, the user owns it.
package bootstrap

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/noesrafa/sunny/defaults"
)

// EnsureRuntime creates root and copies the embedded default tree into it
// if root does not yet exist. Returns true if a fresh seed happened.
// If root already exists (any contents), it is left untouched.
func EnsureRuntime(root string) (seeded bool, err error) {
	if _, err := os.Stat(root); err == nil {
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
