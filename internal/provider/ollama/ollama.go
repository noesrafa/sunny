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
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/secrets"
)

const defaultBaseURL = "https://ollama.com"

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
	return &Driver{secrets: s, hc: &http.Client{}}, nil
}

func (d *Driver) Name() string { return "ollama" }

// chatRequest is what /api/chat expects. We send `stream: true` always
// (engine expects deltas).
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is one JSONL line. For streamed turns, every line carries
// a partial delta in `message.content` until `done: true`, where usage
// counters land.
type chatResponse struct {
	Model     string       `json:"model"`
	CreatedAt string       `json:"created_at"`
	Message   *chatMessage `json:"message,omitempty"`
	Done      bool         `json:"done"`
	// Usage counters appear on the final `done: true` line. Names mirror
	// Ollama's wire format; we map them onto provider.Done.
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
	// it expects role=system inside messages[].
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if sys := joinSystem(req.System); sys != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user", "assistant", "system":
			msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
		default:
			return nil, fmt.Errorf("ollama: unknown role %q", m.Role)
		}
	}

	body, err := json.Marshal(chatRequest{Model: model, Messages: msgs, Stream: true})
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
			if ev.Message != nil && ev.Message.Content != "" {
				out <- provider.TextDelta{Text: ev.Message.Content}
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
