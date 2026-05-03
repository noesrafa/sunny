package session

import (
	"encoding/json"
	"io"
	"testing"

	"charm.land/log/v2"

	"github.com/noesrafa/sunny/internal/claude"
)

// newTestSession builds a Session with a discard logger and no live stream.
// HandleEvent doesn't touch Stream, so this is enough for state-machine
// tests.
func newTestSession() *Session {
	return &Session{
		State:  StateIdle,
		logger: log.NewWithOptions(io.Discard, log.Options{}),
	}
}

func TestHandleEvent_SystemInitSetsRemote(t *testing.T) {
	s := newTestSession()
	s.HandleEvent(claude.Event{
		Type:      "system",
		Subtype:   "init",
		SessionID: "abc",
		Model:     "claude-opus-4-7",
	})
	if s.RemoteID != "abc" {
		t.Errorf("RemoteID = %q, want abc", s.RemoteID)
	}
	if s.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", s.Model)
	}
}

func TestHandleEvent_AssistantTextAddsItem(t *testing.T) {
	s := newTestSession()
	s.HandleEvent(claude.Event{
		Type: "assistant",
		Message: &claude.Message{
			Content: []claude.ContentBlock{{Type: "text", Text: "hello"}},
		},
	})
	if len(s.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(s.Items))
	}
	at, ok := s.Items[0].(AssistantTextItem)
	if !ok {
		t.Fatalf("got %T, want AssistantTextItem", s.Items[0])
	}
	if at.Text != "hello" {
		t.Errorf("Text = %q, want hello", at.Text)
	}
	if !s.turnHadOutput {
		t.Error("turnHadOutput should be true after assistant text")
	}
}

func TestHandleEvent_AssistantTextSkipsBlank(t *testing.T) {
	s := newTestSession()
	s.HandleEvent(claude.Event{
		Type: "assistant",
		Message: &claude.Message{
			Content: []claude.ContentBlock{{Type: "text", Text: "   \n  "}},
		},
	})
	if len(s.Items) != 0 {
		t.Errorf("blank text should be ignored, got %d items", len(s.Items))
	}
}

func TestHandleEvent_ToolUseLinkedByResult(t *testing.T) {
	s := newTestSession()
	s.HandleEvent(claude.Event{
		Type: "assistant",
		Message: &claude.Message{
			Content: []claude.ContentBlock{{
				Type:  "tool_use",
				ID:    "tu_1",
				Name:  "Bash",
				Input: json.RawMessage(`{"cmd":"ls"}`),
			}},
		},
	})
	if len(s.Items) != 1 {
		t.Fatalf("expected 1 item after tool_use, got %d", len(s.Items))
	}
	s.HandleEvent(claude.Event{
		Type: "user",
		Message: &claude.Message{
			Content: []claude.ContentBlock{{
				Type:      "tool_result",
				ToolUseID: "tu_1",
				Content:   json.RawMessage(`"ok"`),
			}},
		},
	})
	tu, ok := s.Items[0].(ToolUseItem)
	if !ok {
		t.Fatalf("expected ToolUseItem, got %T", s.Items[0])
	}
	if !tu.Done || tu.Result != "ok" {
		t.Errorf("tool_use not linked: %+v", tu)
	}
	if len(s.Items) != 1 {
		t.Errorf("orphan tool_result added, len=%d", len(s.Items))
	}
}

func TestHandleEvent_ResultGoesIdle(t *testing.T) {
	s := newTestSession()
	s.State = StateThinking
	s.HandleEvent(claude.Event{
		Type:         "result",
		IsError:      false,
		DurationMs:   42,
		NumTurns:     1,
		TotalCostUSD: 0.01,
	})
	if s.State != StateIdle {
		t.Errorf("State = %v, want StateIdle", s.State)
	}
	if s.Turns != 1 {
		t.Errorf("Turns = %d, want 1", s.Turns)
	}
	// Last item is ResultItem; first is the EmptyResponseItem (no output).
	if len(s.Items) != 2 {
		t.Fatalf("expected 2 items (empty + result), got %d", len(s.Items))
	}
	if _, ok := s.Items[0].(EmptyResponseItem); !ok {
		t.Errorf("first item: got %T, want EmptyResponseItem", s.Items[0])
	}
	if _, ok := s.Items[1].(ResultItem); !ok {
		t.Errorf("last item: got %T, want ResultItem", s.Items[1])
	}
}

func TestHandleEvent_ResultPreservesOutput(t *testing.T) {
	s := newTestSession()
	s.State = StateThinking
	s.HandleEvent(claude.Event{
		Type: "assistant",
		Message: &claude.Message{
			Content: []claude.ContentBlock{{Type: "text", Text: "answer"}},
		},
	})
	s.HandleEvent(claude.Event{Type: "result"})
	// No EmptyResponseItem when there was output.
	for _, it := range s.Items {
		if _, ok := it.(EmptyResponseItem); ok {
			t.Error("unexpected EmptyResponseItem when assistant produced text")
		}
	}
}

func TestHandleEvent_RateLimitStored(t *testing.T) {
	s := newTestSession()
	rl := &claude.RateLimitInfo{Status: "allowed", RateLimitType: "five_hour"}
	s.HandleEvent(claude.Event{Type: "rate_limit_event", RateLimitInfo: rl})
	if s.RateLimit == nil || s.RateLimit.Status != "allowed" {
		t.Errorf("RateLimit not stored: %+v", s.RateLimit)
	}
}

func TestExtractToolResult_Variants(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
	}{
		{"plain string", `"hello"`, "hello"},
		{"text-block array", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"empty", ``, ""},
		{"unknown shape falls through", `{"foo":"bar"}`, `{"foo":"bar"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractToolResult(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
