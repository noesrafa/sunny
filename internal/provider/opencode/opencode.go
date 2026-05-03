// Package opencode implements provider.Provider on top of the
// `opencode` CLI binary, the same way internal/provider/claudecode
// implements it for `claude`.
//
// Sunny does not advertise its own tools to opencode (see
// engine.advertisedTools): opencode brings a complete native toolset
// — read/edit/bash/grep/glob/task/etc. — and runs the round-trip
// internally. Tool events the driver surfaces are informational; the
// engine never re-executes them. Pair this file with wire.go (event
// shape translation) and agent_sync.go (system prompt → opencode
// agent file).
package opencode

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

// New returns a driver. Returns an error if the `opencode` binary
// cannot be found on PATH so callers can fall back to another
// provider without crashing the daemon.
func New() (*Driver, error) {
	bin, err := exec.LookPath("opencode")
	if err != nil {
		return nil, fmt.Errorf("opencode: `opencode` not on PATH (try: sunny setup opencode)")
	}
	return &Driver{bin: bin}, nil
}

// Driver is the provider.Provider implementation for opencode. It
// holds only the resolved binary path; everything else is per-turn.
type Driver struct {
	bin string
}

func (d *Driver) Name() string { return "opencode" }

// Stream spawns one `opencode run` subprocess per turn:
//
//   - First turn (req.ProviderState empty): syncs the agent file at
//     ~/.config/opencode/agent/sunny-<slug>.md, spawns with
//     `--agent sunny-<slug>`, sends the user message as a positional
//     arg, drains nd-json events, returns the new sessionID in
//     Done.ProviderState.
//
//   - Subsequent turns (req.ProviderState set): spawns with
//     `--session <id>`; opencode rehydrates the conversation from its
//     sqlite store and continues. The agent file is re-synced (cheap
//     no-op when unchanged).
//
// One process per turn keeps the engine stateless. Cost is one spawn
// per turn (~200ms on M-series). Benefit: no map of long-lived
// processes to track, no leaks on crash, --session reconstructs
// context every time anyway.
func (d *Driver) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("opencode: messages required")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		return nil, fmt.Errorf("opencode: last message must be role=user")
	}
	if strings.TrimSpace(last.Content) == "" {
		return nil, fmt.Errorf("opencode: last user message has no content")
	}

	args, err := d.buildArgs(ctx, req, last)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, d.bin, args...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	} else if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode: stderr pipe: %w", err)
	}
	// Drain stderr so a chatty opencode (deprecation notices, provider
	// warnings) can't fill the pipe and deadlock the JSON stream.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode: start: %w", err)
	}

	out := make(chan provider.Event, 64)
	go d.pump(ctx, cmd, stdout, out)
	return out, nil
}

// buildArgs assembles the argv for one `opencode run`. Split out so
// Stream itself stays at one screen height.
func (d *Driver) buildArgs(ctx context.Context, req provider.Request, last provider.Message) ([]string, error) {
	args := []string{
		"run",
		"--format", "json",
		"--dangerously-skip-permissions",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if v := mapEffort(req.Effort); v != "" {
		args = append(args, "--variant", v)
	}
	if req.AdaptiveThinking {
		// Without this opencode swallows reasoning blocks instead of
		// emitting `reasoning` events, so the engine would never see
		// thinking deltas.
		args = append(args, "--thinking")
	}
	if req.ProviderState != "" {
		args = append(args, "--session", req.ProviderState)
	} else {
		slug := slugFromContext(ctx)
		if slug == "" {
			// No slug supplied — synthesize one from the prompt so
			// successive runs land on the same agent file.
			slug = "anon-" + fingerprint([]byte(flattenSystem(req.System)))[:8]
		}
		name, err := syncAgentFile(slug, req.System)
		if err != nil {
			return nil, err
		}
		args = append(args, "--agent", name)
	}
	// Positional message goes last; opencode joins multi-args with
	// spaces, so we pass the whole message as a single arg.
	args = append(args, last.Content)
	return args, nil
}

// pump reads JSON events from opencode and translates them into
// provider events on out. Owns the channel; closes it when the
// process exits.
func (d *Driver) pump(ctx context.Context, cmd *exec.Cmd, stdout io.Reader, out chan<- provider.Event) {
	defer close(out)
	var (
		sessionID  string
		stopReason string
		cost       float64
		lastTokens *tokens
		fatalErr   string
	)

	for ev := range decode(stdout) {
		if sessionID == "" && ev.SessionID != "" {
			sessionID = ev.SessionID
		}
		switch ev.Type {
		case "step_start":
			// Marker only.

		case "text":
			var p textPart
			if err := json.Unmarshal(ev.Part, &p); err != nil || p.Text == "" {
				continue
			}
			out <- provider.TextDelta{Text: p.Text}

		case "reasoning":
			var p textPart
			if err := json.Unmarshal(ev.Part, &p); err != nil || p.Text == "" {
				continue
			}
			out <- provider.ThinkingDelta{Text: p.Text}

		case "tool_use":
			var p toolPart
			if err := json.Unmarshal(ev.Part, &p); err != nil {
				continue
			}
			// opencode only emits this when status is completed or
			// error, so we synthesize both ToolUse and ToolResult
			// back-to-back for parity with the claude-code/anthropic
			// flow. The engine ignores the ToolUse for its round-trip
			// loop (advertisedTools is empty for opencode).
			toolID := p.CallID
			if toolID == "" {
				toolID = p.ID
			}
			inputJSON := string(p.State.Input)
			if inputJSON == "" {
				inputJSON = "{}"
			}
			out <- provider.ToolUse{ID: toolID, Name: p.Tool, Input: inputJSON}
			content, isErr := summarizeToolOutput(p.State)
			out <- provider.ToolResult{ToolUseID: toolID, Content: content, IsError: isErr}

		case "step_finish":
			var p stepFinishPart
			if err := json.Unmarshal(ev.Part, &p); err == nil {
				if p.Reason != "" {
					stopReason = p.Reason
				}
				cost += p.Cost
				if p.Tokens != nil {
					lastTokens = p.Tokens
				}
			}

		case "error":
			msg := decodeError(ev.Error)
			if msg == "" {
				msg = "unknown opencode error"
			}
			if fatalErr == "" {
				fatalErr = msg
			} else {
				fatalErr += "; " + msg
			}

		case "parse_error":
			out <- provider.Error{
				Err: fmt.Errorf("opencode: malformed event from CLI: %.200s", string(ev.Raw)),
			}
			return
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			out <- provider.Error{Err: ctx.Err()}
			return
		}
		if fatalErr != "" {
			out <- provider.Error{Err: fmt.Errorf("opencode: %s", fatalErr)}
			return
		}
		out <- provider.Error{Err: fmt.Errorf("opencode: process exited: %w", err)}
		return
	}

	if fatalErr != "" {
		out <- provider.Error{Err: fmt.Errorf("opencode: %s", fatalErr)}
		return
	}

	done := provider.Done{
		StopReason:    stopReason,
		ProviderState: sessionID,
		CostUSD:       cost,
	}
	if lastTokens != nil {
		done.InputTokens = lastTokens.Input
		done.OutputTokens = lastTokens.Output
		if lastTokens.Cache != nil {
			done.CacheReadTokens = lastTokens.Cache.Read
			done.CacheCreationTokens = lastTokens.Cache.Write
		}
	}
	out <- done
}

// agentSlugKey is the context key sunny uses to thread the active
// agent's slug through to the driver. Kept unexported; callers
// interact through WithAgentSlug.
type agentSlugKey struct{}

// WithAgentSlug returns ctx with the agent slug attached. The engine
// wraps every Turn() with this so the agent file lands at
// sunny-<slug>.md instead of an anon hash.
func WithAgentSlug(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, agentSlugKey{}, slug)
}

func slugFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(agentSlugKey{}).(string); ok {
		return v
	}
	return ""
}
