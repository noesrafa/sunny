//go:build !unix

package claudecode

import (
	"os/exec"
	"time"
)

// configureProcessGroup is a no-op on non-unix platforms. The release
// matrix only ships darwin + linux, so this stub exists purely to keep
// `go build ./...` working on Windows during development. WaitDelay
// still applies — it's portable.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
