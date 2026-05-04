//go:build unix

package claudecode

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup puts claude in a fresh process group and
// rewires cmd.Cancel so context cancellation kills the whole group,
// not just claude itself. This is what stops a Ctrl+C from leaving an
// orphaned `bash` child holding the stdout pipe open.
//
// WaitDelay bounds how long Wait() will block after the kill before
// it force-closes the inherited pipes. 5s is generous — a healthy
// process group dies in milliseconds; the delay only matters when
// something (a stuck syscall, a child of a child) refuses to release
// fds.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative PID = process group. Setpgid above made the leader
		// PID the group id, so this kills claude + every descendant.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
