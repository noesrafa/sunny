package claude

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

type RunOnceOpts struct {
	Cwd       string
	SessionID string
	Stderr    io.Writer
}

// RunOnce spawns `claude -p <prompt> --output-format stream-json --verbose` and returns
// a channel of decoded events. The channel closes when the process exits.
func RunOnce(ctx context.Context, opts RunOnceOpts, prompt string) (<-chan Event, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Stderr = opts.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	decoded := Decode(stdout)
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		for ev := range decoded {
			out <- ev
		}
		_ = cmd.Wait()
	}()
	return out, nil
}
