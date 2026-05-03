package claude

import (
	"os"
	"strings"
	"testing"
)

func TestDecode_EmptyInput(t *testing.T) {
	out := Decode(strings.NewReader(""))
	if _, ok := <-out; ok {
		t.Fatal("expected channel closed on empty input")
	}
}

func TestDecode_ParseError(t *testing.T) {
	out := Decode(strings.NewReader("not json\n"))
	ev, ok := <-out
	if !ok {
		t.Fatal("expected at least one event")
	}
	if ev.Type != "parse_error" {
		t.Fatalf("got type=%q want parse_error", ev.Type)
	}
	if ev.Result != "not json" {
		t.Fatalf("expected raw line in Result, got %q", ev.Result)
	}
}

func TestDecode_SkipsBlankLines(t *testing.T) {
	in := `{"type":"system","subtype":"init","session_id":"abc"}

{"type":"result","is_error":false,"duration_ms":10,"num_turns":1}
`
	out := Decode(strings.NewReader(in))
	var got []Event
	for ev := range out {
		got = append(got, ev)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Type != "system" || got[0].Subtype != "init" {
		t.Errorf("first event: %+v", got[0])
	}
	if got[1].Type != "result" || got[1].DurationMs != 10 {
		t.Errorf("second event: %+v", got[1])
	}
}

func TestDecode_StreamSampleFixture(t *testing.T) {
	f, err := os.Open("../../testdata/stream-sample.jsonl")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	defer f.Close()
	out := Decode(f)

	var types []string
	for ev := range out {
		types = append(types, ev.Type)
	}
	want := []string{"system", "rate_limit_event", "assistant", "result"}
	if len(types) != len(want) {
		t.Fatalf("event count: got %d (%v), want %d (%v)", len(types), types, len(want), want)
	}
	for i, w := range want {
		if types[i] != w {
			t.Errorf("event %d: got %q, want %q", i, types[i], w)
		}
	}
}

func TestDecode_AssistantMessageContent(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"cmd":"ls"}}]}}` + "\n"
	out := Decode(strings.NewReader(line))
	ev := <-out
	if ev.Message == nil || len(ev.Message.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %+v", ev.Message)
	}
	text := ev.Message.Content[0]
	if text.Type != "text" || text.Text != "hi" {
		t.Errorf("text block: %+v", text)
	}
	tu := ev.Message.Content[1]
	if tu.Type != "tool_use" || tu.Name != "Bash" || tu.ID != "tu_1" {
		t.Errorf("tool_use block: %+v", tu)
	}
	if !strings.Contains(string(tu.Input), `"cmd":"ls"`) {
		t.Errorf("tool input lost: %q", string(tu.Input))
	}
}
