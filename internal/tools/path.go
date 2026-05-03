package tools

import (
	"errors"
	"path/filepath"
	"strings"
)

// errOutsideCwd is returned for any read attempt that resolves
// outside the session's cwd. Read-only tools hard-deny instead of
// asking — there's no permission UI yet, and the model rarely needs
// arbitrary filesystem access for the read use cases.
var errOutsideCwd = errors.New("path is outside the session cwd")

// resolveInside returns the absolute path of `relOrAbs` joined to
// cwd, validating that the result stays inside cwd (no `..`
// escapes, no symlinks pointing outside). Comparison is done after
// resolving symlinks on BOTH sides — important on macOS where /tmp
// is a symlink to /private/tmp; if we only resolved one side, every
// /tmp access from a /tmp cwd would falsely look outside-cwd.
func resolveInside(cwd, relOrAbs string) (string, error) {
	if cwd == "" {
		return "", errors.New("cwd is required")
	}
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	// Resolve symlinks on the cwd if possible. EvalSymlinks fails
	// for non-existent paths; for cwd that's a programming error
	// upstream — fall back to the lexical path in that case rather
	// than blocking the call.
	if resolved, err := filepath.EvalSymlinks(cwdAbs); err == nil {
		cwdAbs = resolved
	}

	target := relOrAbs
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwdAbs, target)
	}
	target = filepath.Clean(target)
	// Resolve symlinks on the target when it exists, then compare.
	// Non-existent paths skip the resolve step but still need the
	// lexical check below.
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		if !insideRoot(cwdAbs, resolved) {
			return "", errOutsideCwd
		}
		return resolved, nil
	}
	if !insideRoot(cwdAbs, target) {
		return "", errOutsideCwd
	}
	return target, nil
}

// insideRoot returns true iff `target` is `root` or a descendant.
// Both inputs must be absolute and lexically clean.
func insideRoot(root, target string) bool {
	if target == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}
