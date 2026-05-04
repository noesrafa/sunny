package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
)

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

	cmd := exec.CommandContext(ctx, d.bin, args...)
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
		return nil, fmt.Errorf("claudecode: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: stdout pipe: %w", err)
	}
	// Drain stderr in the background so a chatty claude doesn't fill its
	// pipe and deadlock. We don't surface stderr today; v0.4 will fold
	// its lines into a debug log.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: stderr pipe: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	if err := cmd.Start(); err != nil {
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
		return nil, fmt.Errorf("claudecode: write user turn: %w", err)
	}
	// Closing stdin signals end-of-input. Claude finishes its turn (which
	// may include several tool_use round-trips it executes against its
	// own tools) and emits a `result` event when done, then exits.
	_ = stdin.Close()

	out := make(chan provider.Event, 64)
	go func() {
		defer close(out)
		var (
			sessionID  string
			stopReason string
			isError    bool
			cost       float64
			lastUsage  *usage
		)

		// Skill memo: the first `system` event carries session_id; on
		// streaming runs claude re-emits init at the start of each
		// resumed turn — first sighting wins so resumed sessions keep
		// their original ID.
		for ev := range decode(stdout) {
			switch ev.Type {
			case "system":
				if ev.Subtype == "init" && sessionID == "" {
					sessionID = ev.SessionID
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

		if err := cmd.Wait(); err != nil {
			// Distinguish: context cancellation vs claude exiting mid-turn.
			if ctx.Err() != nil {
				out <- provider.Error{Err: ctx.Err()}
				return
			}
			out <- provider.Error{Err: fmt.Errorf("claudecode: process exited: %w", err)}
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
