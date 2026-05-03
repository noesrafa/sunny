// Package ollama implements provider.Provider on top of Ollama Cloud's
// /api/chat endpoint (https://ollama.com).
//
// Auth is bearer-token; streaming responses are JSONL with one object
// per line, terminated by `{"done": true, ...}`. The driver maps that
// to the typed event stream sunny's engine expects.
//
// We deliberately implement the HTTP client by hand (not via Ollama's
// Go SDK) — the surface is tiny, the SDK adds a dep + version
// coupling, and our streaming/event mapping is provider-specific
// anyway.
//
// What's NOT here yet:
//   - Tool calling (`tools` request field, `message.tool_calls`
//     response field). Sketch in CLAUDE.md; lands when sunny gets a
//     real tool-execution layer.
//   - `format` (JSON schema enforcement) — pair feature with tools.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/secrets"
)

// randRead is a thin alias so synthesizeID is testable in isolation
// without exposing crypto/rand to the test surface.
var randRead = rand.Read

const (
	defaultBaseURL = "https://ollama.com"
	// defaultKeepAlive tells Ollama Cloud to keep the model warm for
	// 10 minutes after the turn. Cold-loads on cloud are seconds-to-
	// tens-of-seconds; this saves the second-turn cost in interactive
	// chats while not hoarding GPU.
	defaultKeepAlive = "10m"
)

// Driver streams Ollama Cloud completions. Holds the secrets store so
// api_key + base_url can be rotated without restart.
type Driver struct {
	secrets *secrets.Store
	hc      *http.Client
}

// New constructs a driver. Verifies a key is configured at build time
// so the auto-detect chain can fall through cleanly when ollama isn't
// set up.
func New(s *secrets.Store) (*Driver, error) {
	if s == nil {
		return nil, errors.New("ollama: secrets store required")
	}
	if probe := s.GetOrEnv("ollama", "api_key", "OLLAMA_API_KEY"); probe == "" {
		return nil, errors.New("ollama: api_key not configured (set via `sunny secrets ollama set api_key`)")
	}
	// Streaming has no body timeout (turns can be long), but tune the
	// transport so a wedged TCP connection doesn't hang forever. Cloud
	// occasionally stalls during model load; ResponseHeaderTimeout
	// catches that without affecting the streaming body.
	transport := &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Driver{secrets: s, hc: &http.Client{Transport: transport}}, nil
}

func (d *Driver) Name() string { return "ollama" }

// chatRequest is what /api/chat expects. We always set stream:true
// (engine consumes deltas) and pass keep_alive so cloud models stay
// warm between turns. Think tracks the engine's AdaptiveThinking
// flag — Ollama emits content into message.thinking when this is on
// for reasoning-capable models. Tools (when non-nil) advertise
// invokable functions; the model emits tool_calls in the final
// streamed message when it wants to invoke them.
type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
	KeepAlive string        `json:"keep_alive,omitempty"`
	Think     bool          `json:"think,omitempty"`
	Tools     []toolDef     `json:"tools,omitempty"`
}

// toolDef matches Ollama's tool advertisement shape (which mirrors
// OpenAI's). Function.Parameters carries our raw JSON Schema.
type toolDef struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatMessage covers user/assistant/system/tool + the optional
// thinking channel that reasoning models (gpt-oss, deepseek-r1,
// qwen3 thinking variants) emit on Ollama Cloud.
type chatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	// ToolName mirrors OpenAI's `name` field on tool messages —
	// Ollama is OpenAI-compatible here. Some servers also echo it,
	// some don't; we send it for safety.
	ToolName string `json:"name,omitempty"`
}

// toolCall is what the model emits when invoking a tool. ID groups
// the request with its eventual `role:"tool"` response.
type toolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function toolCallInvocation `json:"function"`
}

type toolCallInvocation struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// chatResponse is one JSONL line. For streamed turns, every line
// carries a partial delta in `message.content` (and optionally
// `message.thinking`) until `done: true`, where usage counters land.
type chatResponse struct {
	Model     string       `json:"model"`
	CreatedAt string       `json:"created_at"`
	Message   *chatMessage `json:"message,omitempty"`
	Done      bool         `json:"done"`
	// Usage counters appear on the final `done: true` line. Names
	// mirror Ollama's wire format; we map them onto provider.Done.
	PromptEvalCount int64  `json:"prompt_eval_count,omitempty"`
	EvalCount       int64  `json:"eval_count,omitempty"`
	DoneReason      string `json:"done_reason,omitempty"`
}

func (d *Driver) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	apiKey := d.secrets.GetOrEnv("ollama", "api_key", "OLLAMA_API_KEY")
	if apiKey == "" {
		return nil, errors.New("ollama: api_key missing")
	}
	baseURL := d.secrets.GetOrEnv("ollama", "base_url", "OLLAMA_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	model := req.Model
	if model == "" {
		return nil, errors.New("ollama: model required")
	}

	// Flatten system blocks into a single leading system message —
	// Ollama doesn't have a separate "system" param like Anthropic;
	// it expects role=system inside messages[]. CacheControl on the
	// SystemBlocks is ignored: cloud doesn't expose a cache surface.
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if sys := joinSystem(req.System); sys != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user", "assistant", "system":
			cm := chatMessage{Role: m.Role, Content: m.Content}
			for _, tc := range m.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, toolCall{
					ID:   tc.ID,
					Type: "function",
					Function: toolCallInvocation{
						Name:      tc.Name,
						Arguments: tc.Input,
					},
				})
			}
			msgs = append(msgs, cm)
		case "tool":
			msgs = append(msgs, chatMessage{
				Role:     "tool",
				Content:  m.Content,
				ToolName: m.ToolUseID, // older servers echo `name`; harmless extra
			})
		default:
			return nil, fmt.Errorf("ollama: unknown role %q", m.Role)
		}
	}

	body, err := json.Marshal(chatRequest{
		Model:     model,
		Messages:  msgs,
		Stream:    true,
		KeepAlive: defaultKeepAlive,
		Think:     req.AdaptiveThinking,
		Tools:     buildOllamaTools(req.Tools),
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "application/x-ndjson")

	resp, err := d.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ollama: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	out := make(chan provider.Event, 32)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		// Ollama lines can be large when models echo big contexts.
		scanner.Buffer(make([]byte, 1<<16), 1<<24)
		var lastUsage chatResponse
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev chatResponse
			if err := json.Unmarshal(line, &ev); err != nil {
				out <- provider.Error{Err: fmt.Errorf("ollama: bad JSONL: %w", err)}
				return
			}
			if ev.Message != nil {
				if ev.Message.Thinking != "" {
					out <- provider.ThinkingDelta{Text: ev.Message.Thinking}
				}
				if ev.Message.Content != "" {
					out <- provider.TextDelta{Text: ev.Message.Content}
				}
				// Tool calls arrive on a single line (Ollama doesn't
				// split arguments across deltas the way Anthropic
				// does). Emit one ToolUse per call, in order.
				for _, tc := range ev.Message.ToolCalls {
					id := tc.ID
					if id == "" {
						// Some servers omit IDs; synthesize one so the
						// engine's request/response pairing still works.
						id = synthesizeID(tc.Function.Name)
					}
					out <- provider.ToolUse{
						ID:    id,
						Name:  tc.Function.Name,
						Input: string(tc.Function.Arguments),
					}
				}
			}
			if ev.Done {
				lastUsage = ev
				break
			}
		}
		if err := scanner.Err(); err != nil {
			if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				out <- provider.Error{Err: fmt.Errorf("ollama: stream read: %w", err)}
				return
			}
			out <- provider.Error{Err: ctx.Err()}
			return
		}
		stop := lastUsage.DoneReason
		if stop == "" {
			stop = "end_turn"
		}
		out <- provider.Done{
			StopReason:   stop,
			InputTokens:  lastUsage.PromptEvalCount,
			OutputTokens: lastUsage.EvalCount,
		}
	}()
	return out, nil
}

// chatResponse needs ToolCalls on its embedded Message — extend.
// (We declare it on chatMessage above; this comment exists so the
// reader notices the Message struct is shared on both directions.)

// buildOllamaTools translates sunny's neutral ToolDef shape into
// what /api/chat accepts. nil-safe so callers don't have to branch.
func buildOllamaTools(in []provider.ToolDef) []toolDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]toolDef, len(in))
	for i, t := range in {
		out[i] = toolDef{
			Type: "function",
			Function: toolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return out
}

// synthesizeID generates a stable-enough id when the server omits
// one. Format: `<name>_<8hex>`. The engine pairs request→response by
// id, so collisions inside a turn would cross wires; rand suffixes
// make that vanishingly unlikely.
func synthesizeID(name string) string {
	var b [4]byte
	_, _ = randRead(b[:])
	return fmt.Sprintf("%s_%x", name, b)
}

// joinSystem flattens system blocks into one string. CacheControl on
// the blocks is ignored (Ollama has no equivalent). Blocks are joined
// with double-newline so headings stay separated.
func joinSystem(blocks []provider.SystemBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}
