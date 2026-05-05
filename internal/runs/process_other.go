//go:build !unix

package runs

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup is a no-op on non-unix. The release matrix
// only ships darwin + linux; this keeps `go build ./...` working on
// Windows during development.
func configureProcessGroup(cmd *exec.Cmd) {}

// killGroup is a no-op on non-unix. Callers that need cross-platform
// kills should fall back to cmd.Process.Kill().
func killGroup(pgid int, sig syscall.Signal) error { return nil }
