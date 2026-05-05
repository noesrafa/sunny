//go:build unix

package runs

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in a fresh process group so
// killGroup can take down the whole tree (sh + every descendant
// `bun dev`, vite, watcher, …) when the user stops the run.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup signals the entire process group rooted at pgid.
// Negative PID = process group on POSIX.
func killGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	return syscall.Kill(-pgid, sig)
}
