package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/noesrafa/sunny/internal/provider"
)

// idleTimeout is how long claude can go without emitting any stream
// event before sunny declares the turn unresponsive and kills the
// process group. Picked to be long enough for legitimate long-
// running tools (a slow `npm install`, a heavy grep) but short
// enough that a hung bash sub-shell doesn't keep the TUI staring at
// "thinking" all afternoon.
const idleTimeout = 5 * time.Minute

// New returns a driver. Returns an error if the `claude` binary cannot be
// found on PATH — callers can use that signal to fall back to another
// provider.
func New() (*Driver, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claudecode: `claude` not on PATH (install Claude Code first)")
	}
	return &Driver{bin: bin}, nil
}

type Driver struct {
	bin string
}

func (d *Driver) Name() string { return "claude-code" }

// Stream spawns one `claude` subprocess for one turn:
//
//   - First turn (req.ProviderState empty): spawn fresh, prime with the
//     agent's system prompt via --append-system-prompt, send the new
//     user message via stream-json on stdin, drain events to result,
//     return the session_id in Done.ProviderState.
//
//   - Subsequent turns (req.ProviderState set): spawn with --resume
//     <session_id>; claude reloads the conversation from
//     ~/.claude/projects/<cwd>/<id>.jsonl and continues. The new user
//     message is the only thing we send on stdin.
//
// One process per turn keeps the engine stateless. Cost: one spawn per
// turn (~50–100 ms). Benefit: no map of long-lived processes to track,
// no leaks on crash, --resume reconstructs context every time anyway.
func (d *Driver) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("claudecode: messages required")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		return nil, fmt.Errorf("claudecode: last message must be role=user")
	}

	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		// Claude CLI's default is to ask for approval on every Bash / file
		// op. For sunny the engine IS the approval surface (or the user's
		// decision to run a particular agent), so we skip claude-side
		// prompts. Without this the CLI would hang waiting on stdin
		// approvals that the API caller has no way to deliver.
		"--dangerously-skip-permissions",
	}

	// On --dangerously-skip-permissions claude still sandboxes file/bash
	// to the session's cwd. Allowing $HOME lets agents touch their own
	// knowledge / skills under ~/.sunny/ without spawning per-cwd.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		args = append(args, "--add-dir", home)
	}

	if req.Model != "" {
		args = append(args, "--model", normalizeModel(req.Model))
	}
	if eff := req.Effort; eff != "" {
		args = append(args, "--effort", eff)
	}
	if req.ProviderState != "" {
		// Continue an existing claude session. With --resume, the CLI
		// rehydrates the conversation from ~/.claude/projects/... and
		// expects only the NEW user message on stdin.
		args = append(args, "--resume", req.ProviderState)
	} else if sys := flattenSystem(req.System); sys != "" {
		// First turn: prime claude with the agent's prompt + skills +
		// knowledge. --append-system-prompt is appended to claude's
		// built-in system prompt, so the agent inherits all of claude
		// code's tools (read/write/bash/glob/grep/web) AND gets sunny's
		// custom persona on top.
		args = append(args, "--append-system-prompt", sys)
	}

	// turnCtx wraps the caller's ctx so the inactivity watchdog can
	// cancel independently. Cleanup lives at the end of the consumer
	// goroutine (deferred turnCancel below) — failures during Start
	// path still call it explicitly to avoid leaking the watcher.
	turnCtx, turnCancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(turnCtx, d.bin, args...)
	// Isolate claude in its own process group so a Ctrl+C from the TUI
	// (which lands here as ctx cancellation) kills bash sub-shells too,
	// not just the claude binary. Without this, `claude` exits but its
	// running bash child keeps the stdout pipe open as an orphan, the
	// decoder's scanner.Scan() never sees EOF, and the turn hangs
	// forever even though the cancel signal arrived. WaitDelay is the
	// secondary safety: if any goroutine is still blocked on the pipe
	// 5s after the kill, Go force-closes it.
	configureProcessGroup(cmd)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	} else if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		turnCancel()
		return nil, fmt.Errorf("claudecode: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		turnCancel()
		return nil, fmt.Errorf("claudecode: stdout pipe: %w", err)
	}
	// Drain stderr in the background so a chatty claude doesn't fill its
	// pipe and deadlock. We don't surface stderr today; v0.4 will fold
	// its lines into a debug log.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		turnCancel()
		return nil, fmt.Errorf("claudecode: stderr pipe: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	if err := cmd.Start(); err != nil {
		turnCancel()
		return nil, fmt.Errorf("claudecode: start: %w", err)
	}

	// Send the new user message. Even on --resume, the CLI expects
	// exactly one user message on stdin per spawned turn.
	userPayload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": last.Content}},
		},
	}
	line, _ := json.Marshal(userPayload)
	line = append(line, '\n')
	if _, err := stdin.Write(line); err != nil {
		_ = cmd.Process.Kill()
		turnCancel()
		return nil, fmt.Errorf("claudecode: write user turn: %w", err)
	}
	// Closing stdin signals end-of-input. Claude finishes its turn (which
	// may include several tool_use round-trips it executes against its
	// own tools) and emits a `result` event when done, then exits.
	_ = stdin.Close()

	out := make(chan provider.Event, 64)
	go func() {
		defer close(out)
		defer turnCancel()
		var (
			sessionID  string
			stopReason string
			isError    bool
			cost       float64
			lastUsage  *usage
			idleKilled atomic.Bool
			sawResult  bool
		)

		// Inactivity watchdog: if no decoder event arrives for
		// idleTimeout, cancel turnCtx — that mass-kills the process
		// group via configureProcessGroup, stdout closes, the for-
		// range below unwinds. We surface this as a distinct error
		// (vs user-cancel) so the TUI can render the right message.
		activity := make(chan struct{}, 1)
		go runIdleWatchdog(turnCtx, activity, &idleKilled, turnCancel)

		// Run cmd.Wait concurrently. Without this we can only call
		// Wait after the decoder loop ends, but the loop won't end
		// when a backgrounded bash claude spawned (`run_in_background:
		// true`) inherits claude's stdout — bash typically detaches
		// via setsid into its own session but still holds the pipe's
		// write end, so EOF never arrives even though claude itself
		// has exited cleanly. Running Wait in parallel lets WaitDelay
		// force-close our parent pipe end as a safety net, and lets
		// the result-event handler below close stdout proactively
		// without losing the wait error.
		waitDone := make(chan error, 1)
		go func() { waitDone <- cmd.Wait() }()

		// Skill memo: the first `system` event carries session_id; on
		// streaming runs claude re-emits init at the start of each
		// resumed turn — first sighting wins so resumed sessions keep
		// their original ID.
		for ev := range decode(stdout) {
			// Non-blocking signal — if the watchdog hasn't drained the
			// previous tick yet, drop. The buffer of 1 means we never
			// queue more than one pending notification.
			select {
			case activity <- struct{}{}:
			default:
			}
			switch ev.Type {
			case "system":
				if ev.Subtype == "init" && sessionID == "" {
					sessionID = ev.SessionID
					// Surface the resume id immediately so the daemon
					// can persist it before the turn ends. Otherwise a
					// cancel/error before "result" would leave
					// meta.json without a session id, and the next
					// turn would spawn claude fresh ("no tengo
					// contexto previo").
					out <- provider.SessionState{State: sessionID}
				}
			case "assistant":
				if ev.Message == nil {
					continue
				}
				if ev.Message.Usage != nil {
					lastUsage = ev.Message.Usage
				}
				for _, blk := range ev.Message.Content {
					switch blk.Type {
					case "text":
						if blk.Text != "" {
							out <- provider.TextDelta{Text: blk.Text}
						}
					case "thinking":
						if blk.Thinking != "" {
							out <- provider.ThinkingDelta{Text: blk.Thinking}
						}
					case "tool_use":
						out <- provider.ToolUse{
							ID:    blk.ID,
							Name:  blk.Name,
							Input: string(blk.Input),
						}
					}
				}
			case "user":
				// Tool results from claude's own tool runner come back as
				// "user" messages whose content is tool_result blocks.
				// Surface them so the TUI can render the round-trip.
				if ev.Message == nil {
					continue
				}
				for _, blk := range ev.Message.Content {
					if blk.Type != "tool_result" {
						continue
					}
					out <- provider.ToolResult{
						ToolUseID: blk.ToolUseID,
						Content:   summarizeToolResult(blk.Content),
						IsError:   blk.IsError,
					}
				}
			case "result":
				stopReason = ev.StopReason
				isError = ev.IsError
				cost = ev.TotalCostUSD
				sawResult = true
				// Definitive turn boundary. Force EOF on the decoder
				// so we unwind even when a backgrounded bash claude
				// detached still owns the pipe's write end. Closing
				// our read end doesn't kill the bash — it just gives
				// it EPIPE on its next write, which it ignores. The
				// dev server (or whatever was started in background)
				// keeps running for the next turn to use, which is
				// the whole point of `run_in_background: true`.
				_ = stdout.Close()
			case "rate_limit_event":
				// Surfaced as a sidebar widget by future versions; for
				// now just informational. Drop silently.
			case "parse_error":
				out <- provider.Error{
					Err: fmt.Errorf("claudecode: malformed event from CLI: %.200s", ev.Result),
				}
				return
			}
		}

		// cmd.Wait was started in a goroutine when the loop began; read
		// its result now that the decoder has unwound. It will already
		// have completed in the common case (claude exits right after
		// emitting "result") or will complete within WaitDelay once
		// our stdout.Close above lets pipes drain.
		waitErr := <-waitDone

		// Distinguish four exit paths:
		//   - Idle watchdog fired: claude went silent for too long
		//   - Caller's ctx cancelled: user hit Ctrl+C / daemon shutdown
		//   - sawResult + waitErr: benign — closing stdout to unwind
		//     the decoder makes Wait surface "use of closed file"-style
		//     errors. The protocol-level turn already succeeded.
		//   - Anything else: real exit error from the CLI
		if idleKilled.Load() {
			out <- provider.Error{Err: fmt.Errorf("claudecode: unresponsive — no output for %s", idleTimeout)}
			return
		}
		if ctx.Err() != nil {
			out <- provider.Error{Err: ctx.Err()}
			return
		}
		if waitErr != nil && !sawResult {
			out <- provider.Error{Err: fmt.Errorf("claudecode: process exited: %w", waitErr)}
			return
		}

		if isError {
			out <- provider.Error{Err: fmt.Errorf("claudecode: turn failed: %s", stopReason)}
			return
		}

		done := provider.Done{
			StopReason:    stopReason,
			ProviderState: sessionID,
			CostUSD:       cost,
		}
		if lastUsage != nil {
			done.InputTokens = lastUsage.InputTokens
			done.OutputTokens = lastUsage.OutputTokens
			done.CacheReadTokens = lastUsage.CacheReadInputTokens
			done.CacheCreationTokens = lastUsage.CacheCreationInputTokens
		}
		out <- done
	}()
	return out, nil
}

// runIdleWatchdog cancels the turn context if no activity arrives
// on `activity` within idleTimeout. Sets killed=true before
// cancelling so the consumer can distinguish this from a user-
// initiated cancel. Exits cleanly when ctx is already done (turn
// finished normally) or when activity keeps arriving.
func runIdleWatchdog(ctx context.Context, activity <-chan struct{}, killed *atomic.Bool, cancel context.CancelFunc) {
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-activity:
			if !timer.Stop() {
				// Drain only if Stop returned false because the timer
				// already fired (the channel has a value). Don't drain
				// otherwise — Stop returning false on a timer that has
				// not fired yet is also possible after Reset, and that
				// channel would block.
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
		case <-timer.C:
			killed.Store(true)
			cancel()
			return
		}
	}
}

// flattenSystem joins the engine's SystemBlocks into a single string for
// --append-system-prompt. We drop the cache_control hints — claude CLI
// has its own caching against the Anthropic backend that we don't
// influence here.
func flattenSystem(blocks []provider.SystemBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, blk := range blocks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(blk.Text))
	}
	return b.String()
}

// normalizeModel maps sunny's full model IDs to the aliases the CLI
// accepts (`opus`, `sonnet`, `haiku`). If the value is not a recognized
// full ID, pass it through verbatim — the CLI accepts aliases directly.
func normalizeModel(m string) string {
	switch {
	case strings.HasPrefix(m, "claude-opus-"):
		return "opus"
	case strings.HasPrefix(m, "claude-sonnet-"):
		return "sonnet"
	case strings.HasPrefix(m, "claude-haiku-"):
		return "haiku"
	}
	return m
}

// summarizeToolResult flattens claude's tool_result content (which may be
// a JSON string, an array of content blocks, or arbitrary JSON) into a
// short string for transcript rendering. Falls back to the raw JSON when
// the shape is unfamiliar.
func summarizeToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Most common: plain string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Or: array of {"type":"text","text":...} blocks.
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, b := range arr {
			if t, ok := b["text"].(string); ok && t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(raw)
}
